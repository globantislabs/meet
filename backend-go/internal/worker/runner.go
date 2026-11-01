// Runner: the media worker process supervisor. It discovers which rooms it
// should run (assigned by the scheduler), starts/stops RoomRuntimes, renews
// leases, refreshes its heartbeat and dispatches Redis room events.
//
// Assignment model:
//   - scheduler: SADD worker:{id}:rooms {roomID} + publishes plan_changed
//   - worker:    claims room:worker:{roomID} via SET NX (lease), starts the
//     runtime, renews the lease on every assignment tick
//   - room end:  runtime torn down, membership dropped from the set
package worker

import (
        "context"
        "sync"
        "time"

        lksdk "github.com/livekit/server-sdk-go/v2"
        "github.com/jackc/pgx/v5/pgxpool"
        "github.com/redis/go-redis/v9"
        "github.com/rs/zerolog"

        "github.com/palabra/palabra-group-translator/internal/observability"
        "github.com/palabra/palabra-group-translator/internal/store"
)

// connectRoom wraps the lksdk entry point so the exact connect call (and its
// version-sensitive signature) lives in ONE place.
func connectRoom(host, token string, cb lksdk.RoomCallback) (*lksdk.Room, error) {
        // VERIFY against the pinned server-sdk-go version: ConnectToRoomWithToken
        // is the standard v2 entry point.
        return lksdk.ConnectToRoomWithToken(host, token, cb)
}

// UsageWriter persists Palabra metering events into the usage table.
type UsageWriter struct {
        pool *pgxpool.Pool
}

// NewUsageWriter builds the usage writer.
func NewUsageWriter(pool *pgxpool.Pool) *UsageWriter { return &UsageWriter{pool: pool} }

// Record inserts one metering row.
func (u *UsageWriter) Record(ctx context.Context, rec UsageRecord) error {
        if u == nil || u.pool == nil {
                return nil
        }
        _, err := u.pool.Exec(ctx, `
                INSERT INTO usage (room_id, session_id, source_lang, target_lang,
                        session_seconds, translated_minutes)
                VALUES ($1::uuid, $2, $3, $4, $5, $6)`,
                rec.RoomID, rec.SessionID, rec.SourceLang, rec.TargetLang,
                rec.SessionSeconds, rec.TranslatedMinutes)
        return err
}

// RunnerConfig is the runner's static configuration.
type RunnerConfig struct {
        WorkerID       string
        WorkerIdentity string // LiveKit identity prefix for this worker
        LKHost         string
        LKAPIKey       string
        LKAPISecret    string
        LKTokenTTL     time.Duration
        HeartbeatTTL   time.Duration
        AssignPoll     time.Duration
}

// Runner supervises all room runtimes of this worker process.
type Runner struct {
        cfg     RunnerConfig
        deps    RuntimeDeps
        rdb     *redis.Client
        metrics *observability.Metrics
        log     zerolog.Logger

        mu      sync.Mutex
        runtimes map[string]*RoomRuntime
        rootCtx context.Context
}

// NewRunner builds the runner.
func NewRunner(cfg RunnerConfig, deps RuntimeDeps, rdb *redis.Client,
        metrics *observability.Metrics, log zerolog.Logger) *Runner {
        if cfg.AssignPoll <= 0 {
                cfg.AssignPoll = 3 * time.Second
        }
        return &Runner{
                cfg:      cfg,
                deps:     deps,
                rdb:      rdb,
                metrics:  metrics,
                log:      log.With().Str("worker", cfg.WorkerID).Logger(),
                runtimes: make(map[string]*RoomRuntime),
        }
}

// Run blocks until ctx is done: assignment polling, event dispatch, heartbeat.
func (r *Runner) Run(ctx context.Context) error {
        r.rootCtx = ctx
        // 1. Live event dispatch.
        pubsub := r.rdb.Subscribe(ctx, store.ChanRoomEvents)
        defer pubsub.Close()
        msgs := pubsub.Channel()

        // 2. Heartbeat: proves this worker is alive to the scheduler.
        hb := time.NewTicker(r.cfg.HeartbeatTTL / 2)
        defer hb.Stop()
        _ = r.rdb.Set(ctx, hbKey(r.cfg.WorkerID), "ok", r.cfg.HeartbeatTTL, nil).Err()

        // 3. Assignment polling.
        poll := time.NewTicker(r.cfg.AssignPoll)
        defer poll.Stop()

        for {
                select {
                case <-ctx.Done():
                        r.stopAll()
                        return nil

                case msg, ok := <-msgs:
                        if !ok {
                                r.log.Warn().Msg("redis event channel closed")
                                r.stopAll()
                                return nil
                        }
                        r.dispatch(ctx, msg.Payload)

                case <-hb.C:
                        _ = r.rdb.Set(ctx, hbKey(r.cfg.WorkerID), "ok", r.cfg.HeartbeatTTL, nil).Err()
                        r.updateGauges(ctx)

                case <-poll.C:
                        r.reconcile(ctx)
                }
        }
}

// dispatch applies a Redis room event.
func (r *Runner) dispatch(ctx context.Context, payload string) {
        ev, err := store.ParseRoomEvent(payload)
        if err != nil {
                r.log.Warn().Err(err).Msg("skip malformed room event")
                return
        }
        switch ev.Type {
        case store.EventPlanChanged:
                if rt := r.runtime(ev.RoomID); rt != nil {
                        rt.HandlePlanChanged()
                }
        case store.EventRoomEnded:
                r.stopRoom(ev.RoomID)
                _ = r.rdb.SRem(ctx, roomsSetKey(r.cfg.WorkerID), ev.RoomID).Err()
        }
}

// reconcile matches running runtimes to the assigned room set.
func (r *Runner) reconcile(ctx context.Context) {
        want, err := r.rdb.SMembers(ctx, roomsSetKey(r.cfg.WorkerID)).Result()
        if err != nil {
                r.log.Warn().Err(err).Msg("read assigned rooms failed")
                return
        }
        wantSet := make(map[string]struct{}, len(want))
        for _, roomID := range want {
                wantSet[roomID] = struct{}{}
                // One runtime per room; missing ones are started below.
                if _, running := r.runtimes[roomID]; !running {
                        if err := r.startRoom(ctx, roomID); err != nil {
                                r.log.Error().Err(err).Str("room", roomID).Msg("start room runtime failed")
                        }
                }
        }
        // Stop runtimes no longer assigned to us.
        for roomID := range r.runtimes {
                if _, ok := wantSet[roomID]; !ok {
                        r.stopRoom(roomID)
                }
        }
}

func (r *Runner) startRoom(ctx context.Context, roomID string) error {
        // Claim the lease (SET NX): prevents two workers running the same room.
        leaseKey := store.KeyRoomWorker + roomID
        ok, err := r.rdb.SetNX(ctx, leaseKey, r.cfg.WorkerID, r.cfg.HeartbeatTTL).Result()
        if err != nil {
                return err
        }
        if !ok {
                // Someone else owns it (or stale assignment): drop from our set.
                _ = r.rdb.SRem(ctx, roomsSetKey(r.cfg.WorkerID), roomID).Err()
                return nil
        }

        // Resolve the LiveKit room name from the DB.
        // The scheduler only assigns active rooms; fetch livekit_room via the
        // participants store's pool is not available here — the runtime needs the
        // name, so the runner reads it through the room store passed via deps.
        name, err := r.deps.LiveKitRoomName(ctx, roomID)
        if err != nil {
                _ = r.rdb.Del(ctx, leaseKey).Err()
                return err
        }

        cfg := RuntimeConfig{
                RoomID:          roomID,
                LiveKitRoomName: name,
                WorkerID:        r.cfg.WorkerID,
                WorkerIdentity:  r.cfg.WorkerIdentity,
                LKHost:          r.cfg.LKHost,
                LKAPIKey:        r.cfg.LKAPIKey,
                LKAPISecret:     r.cfg.LKAPISecret,
                LKTokenTTL:      r.cfg.LKTokenTTL,
        }
        rt := NewRoomRuntime(cfg, r.deps)
        r.mu.Lock()
        r.runtimes[roomID] = rt
        r.mu.Unlock()

        go func() {
                runCtx, cancel := context.WithCancel(context.Background())
                defer cancel()
                go func() {
                        <-r.rootCtx.Done() // worker shutting down
                        cancel()
                }()
                if err := rt.Run(runCtx); err != nil {
                        r.log.Error().Err(err).Str("room", roomID).Msg("room runtime exited with error")
                }
                // Runtime ended (room ended, LiveKit disconnect): release the lease
                // and drop the assignment so the scheduler can reassign.
                rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
                defer rcancel()
                _ = r.rdb.Del(rctx, leaseKey).Err()
                _ = r.rdb.SRem(rctx, roomsSetKey(r.cfg.WorkerID), roomID).Err()
                r.mu.Lock()
                delete(r.runtimes, roomID)
                r.mu.Unlock()
                r.metrics.WorkerRoomCount.Set(float64(len(r.runtimes)))
        }()

        r.log.Info().Str("room", roomID).Msg("room runtime starting")
        r.metrics.WorkerRoomCount.Set(float64(len(r.runtimes)))
        return nil
}

func (r *Runner) stopRoom(roomID string) {
        r.mu.Lock()
        rt := r.runtimes[roomID]
        delete(r.runtimes, roomID)
        r.mu.Unlock()
        if rt != nil {
                // Runtime teardown happens inside its own goroutine via cancel.
                rt.CancelFromOutside()
        }
        r.metrics.WorkerRoomCount.Set(float64(len(r.runtimes)))
}

func (r *Runner) stopAll() {
        r.mu.Lock()
        rooms := make([]string, 0, len(r.runtimes))
        for id := range r.runtimes {
                rooms = append(rooms, id)
        }
        r.mu.Unlock()
        for _, id := range rooms {
                r.stopRoom(id)
        }
}

func (r *Runner) runtime(roomID string) *RoomRuntime {
        r.mu.Lock()
        defer r.mu.Unlock()
        return r.runtimes[roomID]
}

func (r *Runner) updateGauges(ctx context.Context) {
        n, err := r.rdb.SCard(ctx, roomsSetKey(r.cfg.WorkerID)).Result()
        if err == nil {
                r.metrics.WorkerRoomCount.Set(float64(n))
        }
        r.metrics.ActivePalabraSess.Set(float64(r.deps.Palabra.ActiveSessions()))
}

func (r *Runner) addRuntime(roomID string, rt *RoomRuntime) {
        r.mu.Lock()
        r.runtimes[roomID] = rt
        r.mu.Unlock()
}

func (r *Runner) addRuntime(roomID string, rt *RoomRuntime) {
        r.mu.Lock()
        r.runtimes[roomID] = rt
        r.mu.Unlock()
}

func hbKey(workerID string) string { return "worker:" + workerID + ":hb" }

func roomsSetKey(workerID string) string { return "worker:" + workerID + ":rooms" }
