// In-process fake Speech-to-Speech session. Used ONLY by integration tests and
// offline development (PALABRA_WS_ENDPOINT=fake://) — never in production.
// It emulates the contract the worker relies on: Start blocks until "ready",
// SendAudio never blocks, Audio() emits translated frames at roughly
// real-time pace, Events() carries usage and a final Close.
package palabra

import (
        "context"
        "errors"
        "math"
        "sync/atomic"
        "time"

        "github.com/palabra/palabra-group-translator/internal/audio"
)

// ErrFakeSessionClosed mirrors ErrSessionClosed for the fake.
var ErrFakeSessionClosed = errors.New("palabra: fake session closed")

type fakeSession struct {
        cfg    SessionConfig
        out    chan audio.AudioFrame
        events chan SessionEvent

        ctx     context.Context
        cancel  context.CancelFunc
        in      chan audio.AudioFrame
        started atomic.Bool
        closed  atomic.Bool
        seq     atomic.Uint64
        phase   float64 // sine phase carried across chunks for continuity
}

// NewFakeSession builds an un-started fake session.
func NewFakeSession(cfg SessionConfig) SpeechToSpeechSession {
        return &fakeSession{
                cfg:    cfg,
                out:    make(chan audio.AudioFrame, 150),
                events: make(chan SessionEvent, 16),
                in:     make(chan audio.AudioFrame, 150),
        }
}

func (f *fakeSession) Start(ctx context.Context, cfg SessionConfig) error {
        if f.started.Swap(true) {
                return errors.New("palabra: fake session already started")
        }
        f.cfg = cfg
        f.ctx, f.cancel = context.WithCancel(context.WithoutCancel(ctx))
        go f.loop()
        f.emit(SessionEvent{Kind: EventSessionReady, SessionID: cfg.SessionID})
        return nil
}

// loop consumes input frames and emits a 440 Hz sine at ~real-time pace,
// preserving the output duration ratio (input 1 s -> output ~0.8 s).
func (f *fakeSession) loop() {
        defer close(f.out)
        defer close(f.events)

        rate := f.cfg.OutputSampleRate
        if rate <= 0 {
                rate = audio.SampleRate16k
        }
        chunkSamples := rate / 50 // 20 ms chunks
        tick := time.NewTicker(20 * time.Millisecond)
        defer tick.Stop()

        // Usage ticker: one metered event every ~10 s of wall time.
        usage := time.NewTicker(10 * time.Second)
        defer usage.Stop()

        for {
                select {
                case <-f.ctx.Done():
                        f.emit(SessionEvent{Kind: EventClosed, SessionID: f.cfg.SessionID})
                        return
                case <-usage.C:
                        f.emit(SessionEvent{Kind: EventUsage, SessionID: f.cfg.SessionID,
                                Usage: InUsage{SessionSeconds: 10, TranslatedMinutes: 10.0 / 60, AudioSecondsIn: 10, AudioSecondsOut: 8}})
                case <-tick.C:
                        frames, ok := f.drainInput()
                        if !ok {
                                continue
                        }
                        inputSamples := 0
                        for _, fr := range frames {
                                inputSamples += fr.Samples
                        }
                        // Emit ~80% of the input duration as sine audio so the worker's
                        // pacing logic sees realistic proportions.
                        outSamples := inputSamples * 4 / 5
                        if outSamples < chunkSamples/4 && len(frames) > 0 {
                                outSamples = chunkSamples / 4
                        }
                        for n := 0; n < outSamples; n += chunkSamples {
                                end := n + chunkSamples
                                if end > outSamples {
                                        end = outSamples
                                }
                                fr := f.sineFrame(n, end, rate)
                                select {
                                case f.out <- fr:
                                case <-f.ctx.Done():
                                        f.emit(SessionEvent{Kind: EventClosed, SessionID: f.cfg.SessionID})
                                        return
                                }
                        }
                }
        }
}

// drainInput non-blockingly collects any queued input frames.
func (f *fakeSession) drainInput() []audio.AudioFrame {
        var frames []audio.AudioFrame
        for {
                select {
                case fr := <-f.in:
                        frames = append(frames, fr)
                        continue
                default:
                }
                return frames
        }
}

func (f *fakeSession) sineFrame(from, to, rate int) audio.AudioFrame {
        samples := make([]int16, to-from)
        for i := range samples {
                samples[i] = int16(9000 * math.Sin(f.phase))
                f.phase += 2 * math.Pi * 440 / float64(rate)
                if f.phase > 2*math.Pi {
                        f.phase -= 2 * math.Pi
                }
        }
        return audio.NewPCM16Frame(samples, rate, time.Now().UnixMilli(), f.seq.Add(1))
}

func (f *fakeSession) SendAudio(ctx context.Context, frame audio.AudioFrame) error {
        if f.closed.Load() {
                return ErrFakeSessionClosed
        }
        select {
        case f.in <- frame:
                return nil
        default:
                return nil // drop-oldest semantics emulated by dropping newest
        }
}

func (f *fakeSession) Audio() <-chan audio.AudioFrame { return f.out }

func (f *fakeSession) Events() <-chan SessionEvent { return f.events }

func (f *fakeSession) Close() error {
        if f.closed.Swap(true) {
                return nil
        }
        if f.cancel != nil {
                f.cancel()
        }
        return nil
}

func (f *fakeSession) emit(ev SessionEvent) {
        select {
        case f.events <- ev:
        default:
        }
}
