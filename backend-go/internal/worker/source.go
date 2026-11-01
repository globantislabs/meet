// SourcePump: consumes one participant's LiveKit microphone track, decodes
// Opus -> PCM, resamples 48 kHz -> 16 kHz mono and fans fixed 20 ms frames out
// to every translation pipeline that needs this speaker.
//
// Pacing note: LiveKit delivers RTP in real time, so frames arrive at
// real-time pace — no artificial pacing is added here. The fan-out queues are
// bounded with drop-oldest semantics (blueprint §7.2: drop stale audio rather
// than build a backlog).
package worker

import (
        "context"
        "sync/atomic"
        "time"

        "github.com/hraban/opus"
        "github.com/pion/rtp"
        "github.com/pion/rtp/codecs"
        "github.com/pion/samplebuilder"
        "github.com/pion/webrtc/v3"
        "github.com/rs/zerolog"

        "github.com/palabra/palabra-group-translator/internal/audio"
)

// SourcePump is one subscribed microphone track's decode + fan-out loop.
type SourcePump struct {
        roomID  string
        speakerID string
        track   *webrtc.TrackRemote
        fanout  func(speakerID string, frame audio.AudioFrame)
        onStop  func(p *SourcePump)
        log     zerolog.Logger

        decoder *opus.Decoder
        resamp  *audio.Resampler

        acc      []int16  // pending resampled samples toward 20 ms frames
        seq      uint64
        pumpSeq  atomic.Uint64
        outSeq   atomic.Uint64

        ctx    context.Context
        cancel context.CancelFunc
        done   chan struct{}
}

// NewSourcePump builds a pump for one subscribed track. fanout is called for
// every decoded 20 ms frame (it must never block: it enqueues into bounded
// queues). onStop is invoked once when the pump terminates.
func NewSourcePump(roomID, speakerID string, track *webrtc.TrackRemote,
        fanout func(speakerID string, frame audio.AudioFrame),
        onStop func(p *SourcePump), log zerolog.Logger) *SourcePump {
        ctx, cancel := context.WithCancel(context.Background())
        dec, err := opus.NewDecoder(audio.SampleRate48k, audio.ChannelsMono)
        if err != nil {
                log.Error().Err(err).Msg("source pump: opus decoder init failed")
        }
        return &SourcePump{
                roomID:    roomID,
                speakerID: speakerID,
                track:     track,
                fanout:    fanout,
                onStop:    onStop,
                decoder:   dec,
                resamp:    audio.NewResampler(audio.SampleRate48k, audio.SampleRate16k),
                acc:       make([]int16, 0, 4096),
                ctx:       ctx,
                cancel:    cancel,
                done:      make(chan struct{}),
                log:       log,
        }
}

// Run blocks until the track ends or Stop is called.
func (p *SourcePump) Run() {
        defer close(p.done)
        defer func() {
                if p.onStop != nil {
                        p.onStop(p)
                }
        }()

        if p.decoder == nil {
                return
        }
        // Sample builder reorders RTP packets into contiguous Opus frames.
        sb := samplebuilder.New(200, &codecs.OpusDepacketizer{}, audio.SampleRate48k)

        pcm48 := make([]int16, 5760) // up to 120 ms @48k per decode

        for {
                var pkt rtp.Packet
                select {
                case <-p.ctx.Done():
                        return
                default:
                }
                if err := p.track.ReadRTP(&pkt); err != nil {
                        p.log.Info().Str("speaker", p.speakerID).Msg("source track ended")
                        return
                }
                p.pumpSeq.Add(1)
                sb.Push(&pkt)
                for {
                        opusFrame := sb.Pop()
                        if opusFrame == nil {
                                break
                        }
                        n, err := p.decoder.Decode(opusFrame.Data, pcm48)
                        if err != nil || n <= 0 {
                                continue
                        }
                        p.emit(time.Now().UnixMilli(), pcm48[:n])
                }
        }
}

// emit resamples the decoded chunk to 16 kHz and fans out complete 20 ms
// frames. stampMs is the worker receive time (latency stage T1).
func (p *SourcePump) emit(stampMs int64, chunk []int16) {
        p.acc = append(p.acc, p.resamp.Process(chunk)...)
        const frameSamples = audio.SampleRate16k / 50 // 20 ms
        for len(p.acc) >= frameSamples {
                frame := audio.NewPCM16Frame(p.acc[:frameSamples:frameSamples],
                        audio.SampleRate16k, stampMs, p.outSeq.Add(1))
                p.acc = p.acc[frameSamples:]
                if p.fanout != nil {
                        p.fanout(p.speakerID, frame)
                }
        }
}

// Stop terminates the pump loop.
func (p *SourcePump) Stop() { p.cancel() }

// Done exposes the termination signal for cleanup synchronization.
func (p *SourcePump) Done() <-chan struct{} { return p.done }
