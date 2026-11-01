package livekit

// media.go — EVERY LiveKit media-plane touchpoint (track subscribe, Opus
// decode, Opus encode, track publish) is isolated in this file. If the
// server-sdk-go/pion APIs drift between versions, fix THIS FILE ONLY.
//
// Audio path (blueprint §6, §7):
//
//	LiveKit remote mic track (Opus 48k)  ──decode──▶  PCM16 48k mono
//	  ──resample 16k──▶ Palabra session (input)
//	Palabra output PCM16 16k ──resample 48k──▶ encode Opus ──publish──▶ LiveKit
//
// Opus codec: github.com/hraban/opus (cgo). CGO_ENABLED=1 is required for the
// WORKER binary; the api/scheduler binaries remain pure Go. If a pure-Go
// decoder is preferred, benchmark first and swap inside this file.

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/hraban/opus"
	"github.com/livekit/protocol/livekit"
	lk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v3"
)

// ── receive side ────────────────────────────────────────────────────────────

// PCMHandler consumes decoded PCM16 mono 48 kHz frames from a remote track.
type PCMHandler func(pcm []int16, timestamp time.Time)

// SubscribeAudioTrack subscribes a remote Opus track and continuously decodes
// it into PCM16 mono 48 kHz frames, invoking handler for each frame. It blocks
// until ctx is cancelled or the track ends. Timestamps are wall-clock receive
// times (T1 latency marker), NOT RTP timestamps.
func SubscribeAudioTrack(ctx context.Context, track *webrtc.TrackRemote, handler PCMHandler) error {
	dec, err := opus.NewDecoder(48000, 1)
	if err != nil {
		return fmt.Errorf("livekit: opus decoder: %w", err)
	}

	sb := newSampleBuilder(track)
	pcmBuf := make([]byte, 48000*2) // 1s worst case at 48k mono

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		pkt, err := sb.nextPacket()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("livekit: track read ended: %w", err)
		}
		if pkt == nil {
			continue
		}
		n, err := dec.Decode(pkt.data, pcmBuf)
		if err != nil {
			// A corrupt packet must not kill the stream; drop and continue.
			continue
		}
		samples := pcmToInt16(pcmBuf[:n])
		handler(samples, time.Now())
	}
}

// opusPacket pairs a depacketized payload with its arrival order.
type opusPacket struct {
	data []byte
}

// newSampleBuilder wraps pion's samplebuilder for Opus payloads. It is kept
// behind a tiny interface so the packetization strategy is swappable.
type sampleBuilder struct {
	ch     chan *rtp.Packet
	dep    *codecs.OpusPacket
	head   *rtp.Packet
	closed chan struct{}
}

func newSampleBuilder(track *webrtc.TrackRemote) *sampleBuilder {
	sb := &sampleBuilder{
		ch:     make(chan *rtp.Packet, 512),
		dep:    &codecs.OpusPacket{},
		closed: make(chan struct{}),
	}
	go func() {
		defer close(sb.ch)
		for {
			pkt, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			select {
			case sb.ch <- pkt:
			case <-sb.closed:
				return
			}
		}
	}()
	return sb
}

// nextPacket returns the next depacketized Opus payload, or nil on a gap.
func (sb *sampleBuilder) nextPacket() (*opusPacket, error) {
	select {
	case pkt, ok := <-sb.ch:
		if !ok {
			return nil, io.EOF
		}
		if payload, _, err := sb.dep.Unmarshal(pkt.Payload); err == nil {
			return &opusPacket{data: payload}, nil
		}
		return nil, nil
	case <-sb.closed:
		return nil, io.EOF
	}
}

// Close stops the reader goroutine.
func (sb *sampleBuilder) Close() { close(sb.closed) }

func pcmToInt16(b []byte) []int16 {
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(b[2*i]) | int16(b[2*i+1])<<8
	}
	return out
}

// ── publish side ────────────────────────────────────────────────────────────

// OutputTrack is one published translated-audio track. It accepts PCM16 mono
// 48 kHz samples and publishes them as Opus into the LiveKit room.
type OutputTrack struct {
	mu     sync.Mutex
	track  *lk.LocalSampleTrack
	enc    *opus.Encoder
	work   []byte
	closed bool
}

// NewOutputTrack publishes a new audio track named trackName from the local
// participant and returns a writer for translated PCM. 20 ms frames are
// assumed at the call site (blueprint §7.3 framing discipline).
func NewOutputTrack(ctx context.Context, room *lk.Room, trackName string) (*OutputTrack, error) {
	enc, err := opus.NewEncoder(48000, 1, opus.App(2048))
	if err != nil {
		return nil, fmt.Errorf("livekit: opus encoder: %w", err)
	}
	local, err := lk.NewLocalSampleTrack(lk.SampleTrackOptions{
		CodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, Channels: 2},
	})
	if err != nil {
		return nil, fmt.Errorf("livekit: create local sample track: %w", err)
	}
	if _, err = room.LocalParticipant.PublishTrack(local, &lk.TrackPublicationOptions{
		Name: trackName,
	}); err != nil {
		return nil, fmt.Errorf("livekit: publish track %s: %w", trackName, err)
	}
	return &OutputTrack{track: local, enc: enc, work: make([]byte, 4096)}, nil
}

// WritePCM16 accepts one chunk of PCM16 mono 48 kHz audio. Callers feed small
// chunks (e.g. 20 ms = 960 samples); this method buffers to Opus frame size
// internally. Concurrency: one writer goroutine per track (worker contract).
func (t *OutputTrack) WritePCM16(pcm []int16) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return fmt.Errorf("livekit: output track closed")
	}
	if len(pcm) == 0 {
		return nil
	}
	bytes := make([]byte, len(pcm)*2)
	for i, s := range pcm {
		bytes[2*i] = byte(s)
		bytes[2*i+1] = byte(s >> 8)
	}
	// hraban/opus Encode wants the full frame; feed in 960-sample (20 ms)
	// slices, padding the tail of a burst with silence.
	const frameSamples = 960
	for off := 0; off < len(pcm); off += frameSamples {
		end := off + frameSamples
		var chunk []byte
		if end <= len(pcm) {
			chunk = bytes[2*off : 2*end]
		} else {
			tail := make([]byte, 2*frameSamples)
			copy(tail, bytes[2*off:])
			chunk = tail
		}
		n, err := t.enc.Encode(chunk, t.work)
		if err != nil {
			return fmt.Errorf("livekit: opus encode: %w", err)
		}
		if err := t.track.WriteSample(mediaSample(t.work[:n], 20*time.Millisecond)); err != nil {
			return fmt.Errorf("livekit: write sample: %w", err)
		}
	}
	return nil
}

// Close unpublishes and releases the track.
func (t *OutputTrack) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	// Unpublish is best-effort; LiveKit cleans up when the room ends.
	_ = t.track.Close()
}

// ParticipantInfo aliases the protocol type for callers that need it.
type ParticipantInfo = livekit.ParticipantInfo
