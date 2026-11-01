// Package audio contains pure PCM16 audio primitives shared by the Palabra
// sessions and the LiveKit media worker: frames, sample conversion, mixing and
// a streaming resampler.
package audio

import "time"

// Canonical rates used by the pipeline.
const (
	// SampleRate48k is the LiveKit/browser-native rate we publish at.
	SampleRate48k = 48000
	// SampleRate16k is the Palabra S2S input/output rate (verify against the
	// current provider docs; configurable in SessionConfig).
	SampleRate16k = 16000
	// ChannelsMono is the only channel layout used end-to-end.
	ChannelsMono = 1
	// BytesPerSample is the size of one PCM16 sample in little-endian bytes.
	BytesPerSample = 2
)

// AudioFrame is one contiguous chunk of PCM16 little-endian mono audio flowing
// through the pipeline. Frames are value types; Data may be reused by the
// producer only until the frame is handed off (consumers must not mutate it).
type AudioFrame struct {
	// Data holds PCM16 LE interleaved samples (Channels * Samples * 2 bytes).
	Data []byte
	// SampleRate is the rate of the samples in Data (Hz).
	SampleRate int
	// Channels is the channel count (always ChannelsMono today).
	Channels int
	// Samples is the number of samples per channel in Data.
	Samples int
	// TimestampUnixMs is the worker-side wall-clock stamp used for latency
	// accounting (T1/T3 stage markers). It is NOT an RTP timestamp.
	TimestampUnixMs int64
	// Seq is a monotonically increasing per-stream sequence number.
	Seq uint64
}

// Duration returns the playback duration of the frame. Zero-rate frames
// (defensive) return 0 rather than a nonsensical value.
func (f AudioFrame) Duration() time.Duration {
	if f.SampleRate <= 0 {
		return 0
	}
	return time.Duration(f.Samples) * time.Second / time.Duration(f.SampleRate)
}

// NewPCM16Frame builds an AudioFrame from interleaved PCM16 samples.
func NewPCM16Frame(samples []int16, sampleRate int, timestampUnixMs int64, seq uint64) AudioFrame {
	return AudioFrame{
		Data:            SamplesToBytes(samples),
		SampleRate:      sampleRate,
		Channels:        ChannelsMono,
		Samples:         len(samples),
		TimestampUnixMs: timestampUnixMs,
		Seq:             seq,
	}
}
