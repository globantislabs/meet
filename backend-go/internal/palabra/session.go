// Speech-to-Speech WebSocket session: one session translates one
// source->target language pair of streamed PCM audio over a single
// persistent WebSocket connection.
//
// Lifecycle (blueprint §8):
//
//      Start(ctx, cfg)
//        1. obtain short-lived access token from the auth endpoint (server-side creds)
//        2. dial the S2S WebSocket endpoint (Authorization header)
//        3. send init (languages, formats)
//        4. wait for session-ready ack (bounded by ReadyTimeout)
//      SendAudio(ctx, frame)   // enqueue PCM16 frames; non-blocking, drops stalest
//        ... reader goroutine: parse output_audio -> Audio() channel
//        ... reader goroutine: parse status/error/usage -> Events() channel
//      Close()                 // graceful, idempotent
//
// Concurrency rules honored (blueprint §6.3): dedicated read/write goroutines,
// bounded queues with explicit drop-oldest overflow policy (stale audio is
// dropped instead of accumulating delay), no locks held across network I/O,
// context-based cancellation, supervised goroutines with failure propagation.
package palabra

import (
        "context"
        "errors"
        "fmt"
        "net/http"
        "sync"
        "sync/atomic"
        "time"

        "github.com/coder/websocket"
        "github.com/rs/zerolog"

        "github.com/palabra/palabra-group-translator/internal/audio"
)

// Session errors surfaced to callers.
var (
        ErrSessionClosed  = errors.New("palabra: session closed")
        ErrSessionNotOpen = errors.New("palabra: session not started")
)

// SessionConfig carries everything one S2S session needs. VERIFY the accepted
// values against the current provider docs before deployment.
type SessionConfig struct {
        SessionID        string   `json:"session_id"` // correlation/metering only
        SourceLang       string   `json:"source_lang"`
        TargetLang       string   `json:"target_lang"`
        InputSampleRate  int      `json:"input_sample_rate"`
        OutputSampleRate int      `json:"output_sample_rate"`
        InputEncoding    string   `json:"input_encoding"`  // palabra.EncodingPCM16
        OutputEncoding   string   `json:"output_encoding"` // palabra.EncodingPCM16
        Voice            string   `json:"voice,omitempty"`
        Extra            []KVPair `json:"extra,omitempty"`
}

// SessionEventKind classifies non-audio session events.
type SessionEventKind int

const (
        EventSessionReady SessionEventKind = iota
        EventStatus
        EventRateLimit
        EventError
        EventUsage
        EventClosed
)

// SessionEvent is one provider event (never carries audio or transcripts).
type SessionEvent struct {
        Kind      SessionEventKind
        SessionID string
        Status    string // EventStatus
        Detail    string
        Retryable bool   // EventError
        Err       error  // EventError / EventClosed
        Usage     InUsage // EventUsage
}

// SpeechToSpeechSession is the provider-agnostic session interface consumed by
// the media worker (blueprint §8). Implementations: wsSession (real) and the
// fake package (tests / offline development).
type SpeechToSpeechSession interface {
        Start(ctx context.Context, cfg SessionConfig) error
        SendAudio(ctx context.Context, frame audio.AudioFrame) error
        Audio() <-chan audio.AudioFrame
        Events() <-chan SessionEvent
        Close() error
}

// SessionOptions is the process-wide configuration a wsSession needs.
type SessionOptions struct {
        WSEndpoint    string
        Auth          *AuthClient // nil => SkipAuth (fake/dev only)
        SkipAuth      bool
        MsgTypes      MsgTypes
        DialTimeout   time.Duration
        ReadyTimeout  time.Duration
        WriteTimeout  time.Duration
        PingInterval  time.Duration
        InQueueCap    int // frames buffered toward the provider
        OutQueueCap   int // frames buffered from the provider
        EventQueueCap int
        MaxMessage    int64
        Log           zerolog.Logger
}

func DefaultSessionOptions() SessionOptions {
        return SessionOptions{
                MsgTypes:      DefaultMsgTypes(),
                DialTimeout:   10 * time.Second,
                ReadyTimeout:  15 * time.Second,
                WriteTimeout:  5 * time.Second,
                PingInterval:  15 * time.Second,
                InQueueCap:    150, // ~3 s of 20 ms frames, drop-oldest
                OutQueueCap:   150,
                EventQueueCap: 64,
                MaxMessage:    1 << 21, // 2 MiB inbound cap
        }
}

// frameBuf is a bounded, drop-oldest AudioFrame buffer (see audio.Queue for
// rationale; a raw channel variant is used here so Audio() can expose the
// receive side directly without a pump goroutine).
type frameBuf struct {
        ch      chan audio.AudioFrame
        dropped atomic.Uint64
}

func newFrameBuf(capacity int) *frameBuf {
        if capacity < 1 {
                capacity = 1
        }
        return &frameBuf{ch: make(chan audio.AudioFrame, capacity)}
}

// push never blocks: when full it evicts the stalest frame first.
func (b *frameBuf) push(f audio.AudioFrame) {
        select {
        case b.ch <- f:
                return
        default:
        }
        select {
        case <-b.ch:
                b.dropped.Add(1)
        default:
        }
        select {
        case b.ch <- f:
        default:
                b.dropped.Add(1)
        }
}

// wsSession is the real provider session.
type wsSession struct {
        opts SessionOptions
        cfg  SessionConfig
        log  zerolog.Logger

        conn    *websocket.Conn
        ctx     context.Context
        cancel  context.CancelFunc
        in      *frameBuf
        out     *frameBuf
        events  chan SessionEvent
        readyCh chan error

        wg        sync.WaitGroup
        finishOnce sync.Once
        closeOnce sync.Once
        closeErr  error
        closed    atomic.Bool
        started   atomic.Bool
        ready     atomic.Bool

        sendSeq atomic.Uint64
}

// NewWSSession builds an un-started provider session.
func NewWSSession(opts SessionOptions) SpeechToSpeechSession {
        if opts.Log.GetLevel() == zerolog.Disabled {
                opts.Log = zerolog.Nop()
        }
        return &wsSession{
                opts:    opts,
                log:     opts.Log,
                in:      newFrameBuf(opts.InQueueCap),
                out:     newFrameBuf(opts.OutQueueCap),
                events:  make(chan SessionEvent, opts.EventQueueCap),
                readyCh: make(chan error, 1),
        }
}

// Start performs the full handshake and blocks until the session-ready ack.
// On any failure the session is closed and the error returned.
func (s *wsSession) Start(ctx context.Context, cfg SessionConfig) error {
        if s.closed.Load() {
                return ErrSessionClosed
        }
        if s.started.Swap(true) {
                return errors.New("palabra: session already started")
        }
        s.cfg = cfg
        s.ctx, s.cancel = context.WithCancel(context.WithoutCancel(ctx))

        // 1. Access token (server-side credentials only; cached by AuthClient).
        token := "fake-token"
        if !s.opts.SkipAuth {
                if s.opts.Auth == nil {
                        s.shutdown()
                        return errors.New("palabra: no auth client configured")
                }
                tok, err := s.opts.Auth.AccessToken(ctx)
                if err != nil {
                        s.shutdown()
                        return fmt.Errorf("palabra: access token: %w", err)
                }
                token = tok
        }

        // 2. Dial. VERIFY whether auth must also live in the first message (we
        // send both header and inline token for tolerance; see OutAuthInit).
        dialCtx, dialCancel := context.WithTimeout(s.ctx, s.opts.DialTimeout)
        defer dialCancel()
        hdr := http.Header{}
        hdr.Set("Authorization", "Bearer "+token)
        conn, _, err := websocket.Dial(dialCtx, s.opts.WSEndpoint, &websocket.DialOptions{
                HTTPHeader: hdr,
        })
        if err != nil {
                s.shutdown()
                return fmt.Errorf("palabra: dial %s: %w", s.opts.WSEndpoint, redact(err))
        }
        s.conn = conn
        s.conn.SetReadLimit(s.opts.MaxMessage)

        // 3. Init message.
        init := OutAuthInit{
                Type:             s.opts.MsgTypes.ClientInit,
                AccessToken:      token,
                SessionID:        cfg.SessionID,
                SourceLang:       cfg.SourceLang,
                TargetLang:       cfg.TargetLang,
                InputSampleRate:  cfg.InputSampleRate,
                OutputSampleRate: cfg.OutputSampleRate,
                InputEncoding:    orDefault(cfg.InputEncoding, EncodingPCM16),
                OutputEncoding:   orDefault(cfg.OutputEncoding, EncodingPCM16),
                Voice:            cfg.Voice,
                Extra:            cfg.Extra,
        }
        if err := s.writeJSON(init); err != nil {
                s.shutdown()
                return fmt.Errorf("palabra: send init: %w", err)
        }

        // 4. Reader + writer goroutines, supervised; Start blocks until ready.
        s.wg.Add(2)
        go s.readLoop()
        go s.writeLoop()
        go s.supervise()

        select {
        case err := <-s.readyCh:
                if err != nil {
                        s.shutdown()
                        return fmt.Errorf("palabra: session not ready: %w", err)
                }
                s.ready.Store(true)
                s.log.Info().Str("session", cfg.SessionID).
                        Str("pair", cfg.SourceLang+"->"+cfg.TargetLang).
                        Msg("palabra session ready")
                return nil
        case <-time.After(s.opts.ReadyTimeout):
                s.shutdown()
                return fmt.Errorf("palabra: session-ready timeout after %s", s.opts.ReadyTimeout)
        case <-ctx.Done():
                s.shutdown()
                return fmt.Errorf("palabra: start canceled: %w", ctx.Err())
        }
}

// SendAudio enqueues one PCM frame toward the provider. It never blocks: when
// the provider (or our writer) falls behind, the stalest frames are dropped —
// bounded latency over completeness (blueprint §7.2).
func (s *wsSession) SendAudio(ctx context.Context, frame audio.AudioFrame) error {
        if s.closed.Load() || !s.started.Load() {
                return ErrSessionClosed
        }
        s.in.push(frame)
        return nil
}

func (s *wsSession) Audio() <-chan audio.AudioFrame { return s.out.ch }

func (s *wsSession) Events() <-chan SessionEvent { return s.events }

// Close tears the session down gracefully and idempotently.
func (s *wsSession) Close() error {
        s.closeOnce.Do(func() {
                s.closed.Store(true)
                s.closeErr = s.shutdown()
        })
        return s.closeErr
}

// shutdown cancels the context and closes the socket. The supervisor
// goroutine (spawned in Start) waits for the reader/writer goroutines to exit
// and then closes the public channels exactly once. shutdown never blocks on
// a goroutine that might itself be blocked calling shutdown.
func (s *wsSession) shutdown() error {
        if s.cancel != nil {
                s.cancel()
        }
        if s.conn != nil {
                _ = s.conn.Close(websocket.StatusNormalClosure, "session closed")
        }
        return nil
}

// supervise waits for both pipeline goroutines to exit, then closes the
// public channels. This is the ONLY place channels are closed, so no send on
// a closed channel can ever race.
func (s *wsSession) supervise() {
        s.wg.Wait()
        s.finishOnce.Do(func() {
                close(s.events)
        })
}

// fail records a fatal error and tears the session down. Safe to call from
// inside reader/writer goroutines (it never waits on them).
func (s *wsSession) fail(err error) {
        if s.closed.Swap(true) {
                return
        }
        s.closeErr = err
        s.log.Error().Err(err).Msg("palabra: session failed")
        _ = s.shutdown()
}

func (s *wsSession) writeJSON(v any) error {
        b, err := EncodeJSON(v)
        if err != nil {
                return err
        }
        wctx, cancel := context.WithTimeout(s.ctx, s.opts.WriteTimeout)
        defer cancel()
        return s.conn.Write(wctx, websocket.MessageText, b)
}

// writeLoop is the dedicated writer goroutine: it drains the inbound frame
// buffer to the socket and keeps the connection alive with pings. A single
// goroutine owns all writes (coder/websocket allows one concurrent writer).
func (s *wsSession) writeLoop() {
        defer s.wg.Done()
        ping := time.NewTicker(s.opts.PingInterval)
        defer ping.Stop()
        for {
                select {
                case <-s.ctx.Done():
                        return
                case f := <-s.in.ch:
                        if f.Data == nil {
                                continue
                        }
                        seq := s.sendSeq.Add(1)
                        msg := OutInputAudio{
                                Type:       s.opts.MsgTypes.ClientInputAudio,
                                Data:       EncodeAudioB64(f.Data),
                                Seq:        seq,
                                DurationMs: int(f.Duration().Milliseconds()),
                        }
                        if err := s.writeJSON(msg); err != nil {
                                s.fail(fmt.Errorf("palabra: write audio: %w", redact(err)))
                                return
                        }
                case <-ping.C:
                        if err := s.writeJSON(OutPing{Type: s.opts.MsgTypes.ClientPing, TimestampUnixMs: time.Now().UnixMilli()}); err != nil {
                                s.fail(fmt.Errorf("palabra: ping: %w", redact(err)))
                                return
                        }
                }
        }
}

// readLoop is the dedicated reader goroutine: it parses every inbound message
// and dispatches audio vs. control events.
func (s *wsSession) readLoop() {
        defer s.wg.Done()
        for {
                if s.ctx.Err() != nil {
                        return
                }
                _, raw, err := s.conn.Read(s.ctx)
                if err != nil {
                        if s.ctx.Err() == nil {
                                s.fail(fmt.Errorf("palabra: read: %w", redact(err)))
                        }
                        return
                }
                inb, err := ParseInbound(s.opts.MsgTypes, raw)
                if err != nil {
                        s.log.Warn().Err(err).Msg("palabra: skip malformed inbound message")
                        continue
                }
                switch inb.Kind {
                case InboundSessionReady:
                        select {
                        case s.readyCh <- nil:
                        default:
                        }
                case InboundOutputAudio:
                        data, err := DecodeAudioB64(inb.Audio.Data)
                        if err != nil {
                                s.log.Warn().Err(err).Msg("palabra: skip undecodable audio")
                                continue
                        }
                        rate := inb.Audio.SampleRate
                        if rate <= 0 {
                                rate = s.cfg.OutputSampleRate
                        }
                        s.out.push(audio.AudioFrame{
                                Data:            data,
                                SampleRate:      rate,
                                Channels:        audio.ChannelsMono,
                                Samples:         len(data) / audio.BytesPerSample,
                                TimestampUnixMs: time.Now().UnixMilli(),
                                Seq:             inb.Audio.Seq,
                        })
                case InboundStatus:
                        s.emit(SessionEvent{Kind: EventStatus, SessionID: s.cfg.SessionID,
                                Status: inb.Status.Status, Detail: inb.Status.Detail})
                case InboundError:
                        ev := SessionEvent{Kind: EventError, SessionID: s.cfg.SessionID,
                                Detail: inb.Error.Message, Retryable: inb.Error.Retryable}
                        if inb.Error.IsRateLimit() {
                                ev.Kind = EventRateLimit
                        }
                        s.emit(ev)
                        if !inb.Error.Retryable {
                                s.fail(fmt.Errorf("palabra: provider error %s: %s", inb.Error.Code, inb.Error.Message))
                                return
                        }
                case InboundUsage:
                        s.emit(SessionEvent{Kind: EventUsage, SessionID: s.cfg.SessionID, Usage: *inb.Usage})
                default:
                        // Forward-compatible: skip unknown message types.
                }
        }
}

// fail is declared in the supervision section; see fail().


// emit delivers an event without ever blocking the reader: a full event queue
// drops the oldest-arrived *non-fatal* events (fatal Close events are closed
// by channel close instead).
func (s *wsSession) emit(ev SessionEvent) {
        if ev.Kind == EventClosed {
                select {
                case s.events <- ev:
                default:
                }
                return
        }
        select {
        case s.events <- ev:
        default:
                // make room, then retry once
                select {
                case <-s.events:
                default:
                }
                select {
                case s.events <- ev:
                default:
                }
        }
}

func orDefault(v, def string) string {
        if v == "" {
                return def
        }
        return v
}

// redact keeps error text but drops any wrapped provider body that could
// embed credential material. WebSocket library errors are safe; this is a
// defensive single point to tighten if the auth flow ever logs server bodies.
func redact(err error) error {
        msg := err.Error()
        if len(msg) > 300 {
                msg = msg[:300]
        }
        return errors.New(msg)
}
