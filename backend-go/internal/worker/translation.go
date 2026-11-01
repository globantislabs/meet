// RoutePipeline: one translation route's full pipeline
//
//      source frames (bounded queue) -> Palabra S2S session -> output publisher
//
// It owns the Palabra session via the shared Manager (reference-counted),
// tracks the route's real state (starting / ready / error) for the UI, feeds
// usage metering, and reports lifecycle transitions to the room runtime.
package worker

import (
        "context"
        "sync"
        "sync/atomic"
        "time"

        "github.com/rs/zerolog"

        "github.com/palabra/palabra-group-translator/internal/audio"
        "github.com/palabra/palabra-group-translator/internal/observability"
        "github.com/palabra/palabra-group-translator/internal/palabra"
        "github.com/palabra/palabra-group-translator/internal/routing"
        "github.com/palabra/palabra-group-translator/internal/store"
)

// FrameWithRoute pairs a translated frame with its route key (internal type
// used between the pipeline and the output manager).
type FrameWithRoute struct {
        routeKey string
        audio.AudioFrame
}

// RouteKey exposes the route key of the frame.
func (f FrameWithRoute) RouteKey() string { return f.routeKey }

// RouteState is the real-time state of one translation route.
type RouteState int32

const (
        RouteStarting RouteState = iota
        RouteReady
        RouteError
)

// String returns the wire value written to Redis (consumed by the API/UI).
func (s RouteState) String() string {
        switch s {
        case RouteReady:
                return store.RouteStateReady
        case RouteError:
                return store.RouteStateError
        default:
                return store.RouteStateStarting
        }
}

// PipelineDeps carries the pipeline's collaborators.
type PipelineDeps struct {
        RoomID     string
        Route      routing.Route
        Palabra    *palabra.Manager
        Outputs    *OutputTrackManager
        Metrics    *observability.Metrics
        Log        zerolog.Logger
        UsageCh    chan<- UsageRecord
        OnState    func(routeKey, state string)
        OnFatal    func(routeKey string) // pipeline died -> runtime replans
}

// RoutePipeline is one route's runtime.
type RoutePipeline struct {
        deps PipelineDeps
        key  string

        in *audio.Queue

        session palabra.SpeechToSpeechSession

        state      atomic.Int32
        stopping   atomic.Bool
        stateMu    sync.Mutex
        firstSend  atomic.Int64 // unix ms of first frame sent to provider (T2)
        firstOut   atomic.Bool

        ctx    context.Context
        cancel context.CancelFunc
        done   chan struct{}
}

// NewRoutePipeline builds (but does not start) the pipeline.
func NewRoutePipeline(deps PipelineDeps) *RoutePipeline {
        ctx, cancel := context.WithCancel(context.Background())
        return &RoutePipeline{
                deps:  deps,
                key:   deps.Route.Key(),
                in:    audio.NewQueue(150, audio.PolicyDropOldest), // ~3 s of 20 ms frames
                ctx:   ctx,
                cancel: cancel,
                done:  make(chan struct{}),
        }
}

// Enqueue accepts one source frame for this speaker (never blocks).
func (p *RoutePipeline) Enqueue(frame audio.AudioFrame) {
        _ = p.in.Push(frame)
}

// Key returns the stable route key.
func (p *RoutePipeline) Key() string { return p.key }

// State returns the current route state.
func (p *RoutePipeline) State() RouteState { return RouteState(p.state.Load()) }

// Start dials the Palabra session and launches the pipeline goroutines. The
// output track is published ONLY after the session is ready (blueprint §6.4:
// never route audio to a language whose session is not ready).
func (p *RoutePipeline) Start(parent context.Context) error {
        go p.run(parent)
        return nil
}

func (p *RoutePipeline) run(parent context.Context) {
        defer close(p.done)
        // Fatal-notification guard: an INTENTIONAL stop must not trigger a replan.
        defer func() {
                if !p.stopping.Load() && p.deps.OnFatal != nil {
                        p.deps.OnFatal(p.key)
                }
        }()

        r := p.deps.Route
        cfg := palabra.SessionConfig{
                SessionID:        p.deps.RoomID + ":" + p.key,
                SourceLang:       r.SourceLang,
                TargetLang:       r.TargetLang,
                InputSampleRate:  audio.SampleRate16k,
                OutputSampleRate: audio.SampleRate16k,
                InputEncoding:    palabra.EncodingPCM16,
                OutputEncoding:   palabra.EncodingPCM16,
        }
        started := time.Now()

        sess, err := p.deps.Palabra.GetOrCreate(parent, p.deps.RoomID, r.SpeakerID, cfg)
        if err != nil {
                p.setState(RouteError)
                p.deps.Log.Error().Err(err).Str("route", p.key).Msg("palabra session start failed")
                p.deps.Metrics.PalabraErrors.WithLabelValues("start").Inc()
                return
        }
        p.session = sess
        p.deps.Metrics.ActivePalabraSess.Add(1)
        defer p.deps.Metrics.ActivePalabraSess.Add(-1)

        initDur := time.Since(started)
        p.deps.Metrics.SessionInitLatency.Observe(initDur.Seconds())

        // Reader: translated audio from the provider -> output publisher.
        go p.drainAudio()

        // Events: status/errors/usage.
        go p.drainEvents()

        // Writer: source frames -> provider.
        for {
                frame, err := p.in.Pop(p.ctx)
                if err != nil {
                        p.setState(RouteError)
                        return
                }
                if p.firstSend.Load() == 0 {
                        p.firstSend.Store(time.Now().UnixMilli())
                }
                // T1->T2 worker forwarding latency.
                if frame.TimestampUnixMs > 0 {
                        p.deps.Metrics.StageLatency.WithLabelValues("T1_to_T2").
                                Observe(float64(time.Now().UnixMilli()-frame.TimestampUnixMs) / 1000)
                }
                if err := sess.SendAudio(p.ctx, frame); err != nil {
                        p.setState(RouteError)
                        p.deps.Metrics.PalabraErrors.WithLabelValues("send").Inc()
                        return
                }
        }
}

// drainAudio forwards provider output to the output publisher and records
// first-translated-audio latency (T2->T3).
func (p *RoutePipeline) drainAudio() {
        firstSent := p.firstSend
        for frame := range p.session.Audio() {
                if p.firstOut.CompareAndSwap(false, true) {
                        sent := firstSent.Load()
                        if sent > 0 && frame.TimestampUnixMs > sent {
                                p.deps.Metrics.FirstTranslatedAudio.
                                        Observe(float64(frame.TimestampUnixMs-sent) / 1000)
                        }
                        // Session is usable: publish the output track now.
                        if err := p.deps.Outputs.EnsureTrack(p.deps.Route); err != nil {
                                p.deps.Log.Error().Err(err).Str("route", p.key).Msg("publish output track failed")
                                p.setState(RouteError)
                        }
                        p.setState(RouteReady)
                }
                p.deps.Outputs.Enqueue(p.key, FrameWithRoute{routeKey: p.key, AudioFrame: frame})
        }
}

// drainEvents consumes provider events.
func (p *RoutePipeline) drainEvents() {
        for ev := range p.session.Events() {
                switch ev.Kind {
                case palabra.EventSessionReady:
                        // Start() already waited for this; state flips on first audio.
                case palabra.EventStatus:
                        p.deps.Log.Debug().Str("route", p.key).Str("status", ev.Status).Msg("palabra status")
                case palabra.EventRateLimit:
                        p.deps.Metrics.PalabraErrors.WithLabelValues("rate_limit").Inc()
                        p.setState(RouteError)
                case palabra.EventError:
                        p.deps.Metrics.PalabraErrors.WithLabelValues("error").Inc()
                        if ev.Retryable {
                                p.deps.Log.Warn().Str("route", p.key).Str("detail", ev.Detail).Msg("palabra retryable error")
                        } else {
                                p.setState(RouteError)
                        }
                case palabra.EventUsage:
                        if p.deps.UsageCh != nil {
                                select {
                                case p.deps.UsageCh <- UsageRecord{
                                        RoomID: p.deps.RoomID, SessionID: ev.SessionID,
                                        SourceLang: p.deps.Route.SourceLang, TargetLang: p.deps.Route.TargetLang,
                                        SessionSeconds: ev.Usage.SessionSeconds,
                                        TranslatedMinutes: ev.Usage.TranslatedMinutes,
                                }:
                                default: // never block the pipeline on metering
                                }
                        }
                case palabra.EventClosed:
                        p.setState(RouteError)
                        return
                }
        }
}

func (p *RoutePipeline) setState(s RouteState) {
        prev := RouteState(p.state.Swap(int32(s)))
        if prev == s {
                return
        }
        p.stateMu.Lock()
        if p.deps.OnState != nil {
                p.deps.OnState(p.key, s.String())
        }
        p.stateMu.Unlock()
        if s == RouteError {
                p.deps.Log.Warn().Str("route", p.key).Msg("route entered error state")
        }
}

// markStopping marks the pipeline as intentionally stopping so its fatal
// callback does not trigger a replan.
func (p *RoutePipeline) markStopping() { p.stopping.Store(true) }

// Stop releases the session and stops the pipeline. Safe to call twice.
func (p *RoutePipeline) Stop() {
        p.markStopping()
        p.cancel()
        p.in.Close()
        <-p.done
        if p.session != nil {
                r := p.deps.Route
                p.deps.Palabra.Release(p.deps.RoomID, r.SpeakerID, r.SourceLang, r.TargetLang)
        }
}

// UsageRecord is one metering event to persist.
type UsageRecord struct {
        RoomID            string
        SessionID         string
        SourceLang        string
        TargetLang        string
        SessionSeconds    float64
        TranslatedMinutes float64
}
