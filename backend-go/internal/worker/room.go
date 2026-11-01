// RoomRuntime: the media worker's per-room actor. It joins the LiveKit room
// as a server-side participant, subscribes to participant microphones, keeps
// the translation routing plan in sync with membership and language
// preferences, publishes translated output tracks and enforces recipient
// permissions.
//
// Event sources that trigger a replan (blueprint §6.4):
//   - LiveKit participant connected / disconnected
//   - Redis plan_changed (API-side join / leave / language change)
//   - output track published (permission refresh)
//   - route fatal failure (clean restart)
//
// LOCKING RULE (blueprint §6.3): rt.mu guards the routing maps and is NEVER
// held across network I/O or callbacks that can re-enter (refreshPermissions
// takes rt.mu itself, so callers must not hold it).
package worker

import (
        "context"
        "sync"
        "sync/atomic"
        "time"

        lksdk "github.com/livekit/server-sdk-go/v2"
        "github.com/pion/webrtc/v3"
        "github.com/redis/go-redis/v9"
        "github.com/rs/zerolog"

        "github.com/palabra/palabra-group-translator/internal/audio"
        "github.com/palabra/palabra-group-translator/internal/livekit"
        "github.com/palabra/palabra-group-translator/internal/observability"
        "github.com/palabra/palabra-group-translator/internal/palabra"
        "github.com/palabra/palabra-group-translator/internal/participants"
        "github.com/palabra/palabra-group-translator/internal/routing"
        "github.com/palabra/palabra-group-translator/internal/store"
)

// RuntimeConfig is one room runtime's static configuration.
type RuntimeConfig struct {
        RoomID          string
        LiveKitRoomName string
        WorkerID        string
        WorkerIdentity  string
        LKHost          string
        LKAPIKey        string
        LKAPISecret     string
        LKTokenTTL      time.Duration
}

// RuntimeDeps carries the shared collaborators.
type RuntimeDeps struct {
        Parts   *participants.Store
        Palabra *palabra.Manager
        RDB     *redis.Client
        Metrics *observability.Metrics
        Usage   UsageWriter
        Log     zerolog.Logger
        // LiveKitRoomName resolves a room ID to its LiveKit room name (runner).
        LiveKitRoomName func(ctx context.Context, roomID string) (string, error)
}

// RoomRuntime manages one room's media plane.
type RoomRuntime struct {
        cfg     RuntimeConfig
        deps    RuntimeDeps
        log     zerolog.Logger
        room    *lksdk.Room
        outputs *OutputTrackManager

        ctx    context.Context
        cancel context.CancelFunc

        mu        sync.Mutex
        pumps     map[string]*SourcePump     // participant ID -> source pump
        pipelines map[string]*RoutePipeline  // route key -> pipeline
        identity  map[string]string          // participant ID -> livekit identity
        rows      []participants.Participant // latest connected rows (routing input)
        plan      routing.Plan

        roomEnded   atomic.Bool
        lastReplan  atomic.Int64
        usageCh     chan UsageRecord
        loopsWG     sync.WaitGroup
}

// NewRoomRuntime builds the runtime (not connected).
func NewRoomRuntime(cfg RuntimeConfig, deps RuntimeDeps) *RoomRuntime {
        ctx, cancel := context.WithCancel(context.Background())
        return &RoomRuntime{
                cfg:       cfg,
                deps:      deps,
                log:       deps.Log.With().Str("room", cfg.RoomID).Logger(),
                pumps:     make(map[string]*SourcePump),
                pipelines: make(map[string]*RoutePipeline),
                identity:  make(map[string]string),
                ctx:       ctx,
                cancel:    cancel,
                usageCh:   make(chan UsageRecord, 128),
        }
}

// Run connects to LiveKit, wires callbacks and blocks until the context is
// canceled or the room ends. Always tears down before returning.
func (rt *RoomRuntime) Run(parent context.Context) error {
        defer rt.teardown()

        token, err := livekit.IssueWorkerToken(rt.cfg.LKAPIKey, rt.cfg.LKAPISecret,
                rt.cfg.LiveKitRoomName, rt.cfg.WorkerIdentity, rt.cfg.LKTokenTTL)
        if err != nil {
                return err
        }

        // NOTE: RoomCallback field names/signatures must be checked against the
        // pinned lksdk version at deploy time (connectRoom in runner.go).
        rt.room, err = connectRoom(rt.cfg.LKHost, token, lksdk.RoomCallback{
                ParticipantConnectedCallback:    rt.onParticipantConnected,
                ParticipantDisconnectedCallback: rt.onParticipantDisconnected,
                TrackSubscribedCallback:         rt.onTrackSubscribed,
                TrackUnsubscribedCallback:       rt.onTrackUnsubscribed,
                DisconnectedCallback:            rt.onRoomDisconnected,
        })
        if err != nil {
                return err
        }
        rt.log.Info().Msg("media worker joined livekit room")

        rt.outputs = NewOutputTrackManager(rt.cfg.RoomID, rt.room, rt.log)
        rt.outputs.SetCallbacks(
                func(route routing.Route, sid string) {
                        rt.recordTrackSID(route, sid)
                        rt.refreshPermissions()
                },
                func(route routing.Route) { rt.refreshPermissions() },
        )

        // Initial plan from participants already in the room.
        if err := rt.replan("startup"); err != nil {
                return err
        }

        rt.loopsWG.Add(2)
        go rt.routeStateLoop()
        go rt.usageLoop()

        <-parent.Done()
        return nil
}

// ── LiveKit callbacks ────────────────────────────────────────────────────────

func (rt *RoomRuntime) onParticipantConnected(rp *lksdk.RemoteParticipant) {
        ctx, cancel := context.WithTimeout(rt.ctx, 5*time.Second)
        defer cancel()
        identity := rp.Identity()
        if p, err := rt.deps.Parts.GetByIdentity(ctx, rt.cfg.RoomID, identity); err == nil {
                _ = rt.deps.Parts.MarkConnectionState(ctx, rt.cfg.RoomID, p.ID, "connected")
                rt.deps.Metrics.ActiveParticipants.Inc()
        }
        _ = rt.replan("participant_connected")
}

func (rt *RoomRuntime) onParticipantDisconnected(rp *lksdk.RemoteParticipant) {
        ctx, cancel := context.WithTimeout(rt.ctx, 5*time.Second)
        defer cancel()
        identity := rp.Identity()
        if p, err := rt.deps.Parts.GetByIdentity(ctx, rt.cfg.RoomID, identity); err == nil {
                _ = rt.deps.Parts.MarkLeft(ctx, rt.cfg.RoomID, p.ID)
                rt.deps.Metrics.ActiveParticipants.Dec()
        }
        rt.stopPumpByIdentity(identity)
        _ = rt.replan("participant_disconnected")
}

func (rt *RoomRuntime) onTrackSubscribed(track *webrtc.TrackRemote,
        pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
        if track.Kind() != webrtc.RTPCodecTypeAudio {
                return
        }
        identity := rp.Identity()
        pid := rt.participantIDByIdentity(identity)
        if pid == "" {
                rt.log.Warn().Str("identity", identity).
                        Msg("subscribed track for unknown participant; skipping")
                return
        }

        rt.mu.Lock()
        if _, ok := rt.pumps[pid]; ok {
                rt.mu.Unlock()
                return // one pump per participant
        }
        p := NewSourcePump(rt.cfg.RoomID, pid, track, rt.fanoutFrame, func(sp *SourcePump) {
                rt.mu.Lock()
                delete(rt.pumps, sp.speakerID)
                rt.mu.Unlock()
        }, rt.log)
        rt.pumps[pid] = p
        rt.mu.Unlock()

        rt.deps.Metrics.ActiveSourceTracks.Inc()
        go p.Run()
        rt.log.Info().Str("participant", pid).Str("track", track.ID()).Msg("subscribed source track")
}

func (rt *RoomRuntime) onTrackUnsubscribed(pub *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
        rt.stopPumpByIdentity(rp.Identity())
}

func (rt *RoomRuntime) onRoomDisconnected() {
        rt.log.Info().Msg("livekit room disconnected")
        rt.cancel()
}

// ── Planning ─────────────────────────────────────────────────────────────────

// replan recomputes the routing plan from connected participants and applies
// the diff: start missing routes, stop unused ones, refresh permissions.
func (rt *RoomRuntime) replan(reason string) error {
        if rt.roomEnded.Load() || rt.room == nil {
                return nil
        }
        ctx, cancel := context.WithTimeout(rt.ctx, 10*time.Second)
        defer cancel()

        rows, err := rt.deps.Parts.ListActive(ctx, rt.cfg.RoomID)
        if err != nil {
                return err
        }
        live := map[string]bool{}
        for _, rp := range rt.room.GetRemoteParticipants() {
                live[rp.Identity()] = true
        }

        routingParts := make([]routing.Participant, 0, len(rows))
        identity := make(map[string]string, len(rows))
        var keptRows []participants.Participant
        for _, p := range rows {
                if !live[p.LiveKitIdentity] {
                        continue // DB row exists but participant not connected yet
                }
                keptRows = append(keptRows, p)
                identity[p.ID] = p.LiveKitIdentity
                routingParts = append(routingParts, routing.Participant{
                        ID: p.ID, Name: p.DisplayName, SourceLang: p.SourceLang, ListenLang: p.ListenLang,
                })
        }
        newPlan := routing.PlanRoutes(routingParts)

        // Phase 1 (locked): mutate maps, collect work.
        rt.mu.Lock()
        oldPlan := rt.plan
        started, stopped := routing.PlanDiff(oldPlan, newPlan)
        rt.plan = newPlan
        rt.rows = keptRows
        rt.identity = identity

        var newPipelines []*RoutePipeline
        var toUnpublish []string
        for _, r := range stopped {
                key := r.Key()
                if p := rt.pipelines[key]; p != nil {
                        p.markStopping()
                        delete(rt.pipelines, key)
                        go p.Stop()
                }
                if rt.outputs != nil && rt.outputs.TrackSID(key) != "" {
                        toUnpublish = append(toUnpublish, key)
                }
        }
        for _, r := range started {
                p := NewRoutePipeline(PipelineDeps{
                        RoomID:  rt.cfg.RoomID,
                        Route:   r,
                        Palabra: rt.deps.Palabra,
                        Outputs: rt.outputs,
                        Metrics: rt.deps.Metrics,
                        Log:     rt.log,
                        UsageCh: rt.usageCh,
                        OnState: rt.onRouteState,
                        OnFatal: rt.onRouteFatal,
                })
                rt.pipelines[r.Key()] = p
                newPipelines = append(newPipelines, p)
        }
        count := len(rt.pipelines)
        snapshot := snapshotPipelines(rt.pipelines)
        rt.mu.Unlock()

        // Phase 2 (unlocked): network I/O and side effects.
        if rt.outputs != nil {
                for _, key := range toUnpublish {
                        rt.outputs.UnpublishTrack(key)
                }
        }
        for _, p := range newPipelines {
                if err := p.Start(rt.ctx); err != nil {
                        rt.log.Error().Err(err).Str("route", p.Key()).Msg("pipeline start failed")
                } else {
                        rt.log.Info().Str("route", p.Key()).Msg("translation route started")
                }
        }
        if routing.PlanChanged(oldPlan, newPlan) || len(started) > 0 || len(stopped) > 0 {
                rt.refreshPermissions()
        }

        rt.deps.Metrics.TranslationRoutes.WithLabelValues(rt.cfg.RoomID).Set(float64(count))
        rt.publishStates(snapshot)
        rt.log.Info().Str("reason", reason).
                Int("participants", len(routingParts)).
                Int("routes", count).
                Int("sessions", newPlan.SessionCount()).
                Msg("routing plan applied")
        return nil
}

func snapshotPipelines(m map[string]*RoutePipeline) map[string]RouteState {
        out := make(map[string]RouteState, len(m))
        for k, p := range m {
                out[k] = p.State()
        }
        return out
}

// fanoutFrame delivers one source frame to every pipeline of that speaker.
// Called at frame rate; the mutex is held only for map access.
func (rt *RoomRuntime) fanoutFrame(speakerID string, frame audio.AudioFrame) {
        rt.mu.Lock()
        for _, p := range rt.pipelines {
                if p.deps.Route.SpeakerID == speakerID {
                        p.Enqueue(frame)
                }
        }
        rt.mu.Unlock()
}

// refreshPermissions recomputes publisher-side subscription permissions:
// "trackSID:identity" pairs for every (published route, recipient) pair.
func (rt *RoomRuntime) refreshPermissions() {
        if rt.outputs == nil {
                return
        }
        rt.mu.Lock()
        var pairs []string
        for key, p := range rt.pipelines {
                sid := rt.outputs.TrackSID(key)
                if sid == "" {
                        continue // not published yet; refreshed again on publish callback
                }
                for _, rec := range p.deps.Route.RecipientIDs {
                        if id, ok := rt.identity[rec]; ok {
                                pairs = append(pairs, sid+":"+id)
                        }
                }
        }
        rt.mu.Unlock()
        rt.outputs.ApplyPermissions(pairs)
}

// ── Route state reporting (source of truth for the UI) ──────────────────────

func (rt *RoomRuntime) onRouteState(routeKey, state string) {
        rt.mu.Lock()
        snapshot := snapshotPipelines(rt.pipelines)
        rt.mu.Unlock()
        rt.publishStates(snapshot)
}

func (rt *RoomRuntime) onRouteFatal(routeKey string) {
        if rt.roomEnded.Load() {
                return
        }
        rt.debounceReplan("route_fatal")
}

func (rt *RoomRuntime) recordTrackSID(route routing.Route, sid string) {
        rt.log.Info().Str("route", route.Key()).Str("sid", sid).Msg("output track live")
}

// debounceReplan coalesces rapid replan triggers (max 1 per 500 ms).
func (rt *RoomRuntime) debounceReplan(reason string) {
        now := time.Now().UnixMilli()
        last := rt.lastReplan.Load()
        if now-last < 500 {
                return
        }
        if rt.lastReplan.CompareAndSwap(last, now) {
                go func() { _ = rt.replan(reason) }()
        }
}

// routeStateLoop periodically publishes route states to Redis (the API reads
// them for the UI) — states expire 15 s after the worker stops refreshing.
func (rt *RoomRuntime) routeStateLoop() {
        defer rt.loopsWG.Done()
        t := time.NewTicker(5 * time.Second)
        defer t.Stop()
        for {
                select {
                case <-rt.ctx.Done():
                        return
                case <-t.C:
                        rt.mu.Lock()
                        snapshot := snapshotPipelines(rt.pipelines)
                        rt.mu.Unlock()
                        rt.publishStates(snapshot)
                }
        }
}

func (rt *RoomRuntime) publishStates(states map[string]RouteState) {
        if rt.deps.RDB == nil || len(states) == 0 {
                return
        }
        wire := make(map[string]string, len(states))
        for k, s := range states {
                wire[k] = s.String()
        }
        ctx, cancel := context.WithTimeout(rt.ctx, 3*time.Second)
        defer cancel()
        if err := store.SetRouteStates(ctx, rt.deps.RDB, rt.cfg.RoomID, wire, 15*time.Second); err != nil {
                rt.log.Warn().Err(err).Msg("publish route states failed")
        }
}

// usageLoop persists metering events (Palabra usage -> usage table).
func (rt *RoomRuntime) usageLoop() {
        defer rt.loopsWG.Done()
        for {
                select {
                case <-rt.ctx.Done():
                        return
                case rec := <-rt.usageCh:
                        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
                        if err := rt.deps.Usage.Record(ctx, rec); err != nil {
                                rt.log.Warn().Err(err).Msg("usage record failed")
                        }
                        cancel()
                }
        }
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func (rt *RoomRuntime) participantIDByIdentity(identity string) string {
        rt.mu.Lock()
        defer rt.mu.Unlock()
        for pid, id := range rt.identity {
                if id == identity {
                        return pid
                }
        }
        return ""
}

func (rt *RoomRuntime) stopPumpByIdentity(identity string) {
        pid := rt.participantIDByIdentity(identity)
        if pid == "" {
                return
        }
        rt.mu.Lock()
        p := rt.pumps[pid]
        delete(rt.pumps, pid)
        rt.mu.Unlock()
        if p != nil {
                p.Stop()
                rt.deps.Metrics.ActiveSourceTracks.Dec()
        }
}

// HandlePlanChanged is the Redis event entry point from the runner.
func (rt *RoomRuntime) HandlePlanChanged() { _ = rt.replan("plan_changed") }

// RoomID returns the room this runtime serves.
func (rt *RoomRuntime) RoomID() string { return rt.cfg.RoomID }

// CancelFromOutside asks the runtime to shut down (teardown runs in Run).
func (rt *RoomRuntime) CancelFromOutside() { rt.cancel() }

func (rt *RoomRuntime) teardown() {
        if rt.roomEnded.Swap(true) {
                return
        }
        rt.cancel()
        rt.mu.Lock()
        pipelines := rt.pipelines
        pumps := rt.pumps
        rt.pipelines = map[string]*RoutePipeline{}
        rt.pumps = map[string]*SourcePump{}
        rt.mu.Unlock()

        for _, p := range pumps {
                p.Stop()
                rt.deps.Metrics.ActiveSourceTracks.Dec()
        }
        for _, p := range pipelines {
                p.Stop()
        }
        if rt.outputs != nil {
                rt.outputs.StopAll()
        }
        rt.deps.Palabra.CloseRoom(rt.cfg.RoomID)
        rt.deps.Metrics.TranslationRoutes.DeleteLabelValues(rt.cfg.RoomID)
        if rt.room != nil {
                rt.room.Disconnect()
        }
        rt.loopsWG.Wait()
        rt.log.Info().Msg("room runtime torn down")
}
