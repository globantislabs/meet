package audio

import "math"

// Resampler is a stateful streaming linear-interpolation resampler for mono
// PCM16. It keeps interpolation state (read position + buffered tail samples)
// across Process calls, so arbitrary chunk boundaries produce a continuous
// output stream — required for LiveKit RTP frames (variable chunking) feeding
// a fixed-rate Palabra session.
//
// NOTE ON QUALITY: linear interpolation is deliberately simple (dependency
// free, good enough for speech at 48k<->16k where aliasing above 8 kHz is
// muted). PRODUCTION should replace it with a windowed-sinc (e.g. polyphase)
// resampler after profiling — swap the implementation behind NewResampler,
// the call sites only rely on Process/Flush.
//
// A Resampler is NOT safe for concurrent use; each pipeline stage owns one.
type Resampler struct {
	srcRate int
	dstRate int
	// step is the number of input samples to advance per output sample.
	step float64
	// buf holds unconsumed input samples (a small rolling window).
	buf []int16
	// pos is the read position within buf in input-sample units. The next
	// output sample interpolates buf[floor(pos)] and buf[floor(pos)+1].
	pos float64
}

// NewResampler creates a resampler converting srcRate to dstRate. Equal rates
// make the resampler a pass-through (Process returns a copy of its input).
// Non-positive rates fall back to pass-through as well.
func NewResampler(srcRate, dstRate int) *Resampler {
	if srcRate <= 0 || dstRate <= 0 || srcRate == dstRate {
		return &Resampler{srcRate: srcRate, dstRate: dstRate, step: 1}
	}
	return &Resampler{
		srcRate: srcRate,
		dstRate: dstRate,
		step:    float64(srcRate) / float64(dstRate),
		buf:     make([]int16, 0, 2048),
	}
}

// SourceRate returns the configured input rate.
func (r *Resampler) SourceRate() int { return r.srcRate }

// TargetRate returns the configured output rate.
func (r *Resampler) TargetRate() int { return r.dstRate }

// Process consumes input samples and returns as many output samples as are
// fully determinable from the buffered input (the last input sample is always
// retained for interpolation across the next call). The 48000->16000 and
// 16000->48000 cases (and any other ratio) are handled by the same code path.
func (r *Resampler) Process(in []int16) []int16 {
	if r == nil || r.step == 1 {
		return append([]int16(nil), in...)
	}
	if len(in) > 0 {
		r.buf = append(r.buf, in...)
	}

	outCap := 0
	if r.step < 1 {
		outCap = int(float64(len(in))/r.step) + 4
	} else {
		outCap = len(in)/int(r.step) + 4
	}
	out := make([]int16, 0, outCap)

	for {
		i := int(r.pos) // floor
		// Interpolation needs the pair (i, i+1) to exist.
		if i+1 >= len(r.buf) {
			break
		}
		frac := r.pos - float64(i)
		s0 := float64(r.buf[i])
		s1 := float64(r.buf[i+1])
		out = append(out, int16(math.Round(s0+(s1-s0)*frac)))
		r.pos += r.step
	}

	// Trim fully consumed samples while keeping buf[floor(pos)] as the
	// interpolation anchor for the next call.
	keep := int(r.pos)
	if keep >= len(r.buf) {
		keep = len(r.buf) - 1
	}
	if keep > 0 {
		remaining := len(r.buf) - keep
		copy(r.buf, r.buf[keep:])
		r.buf = r.buf[:remaining]
		r.pos -= float64(keep)
	}
	return out
}

// Flush terminates the stream: it returns the final held input sample as the
// last output sample and resets internal state. The flushed tail is an
// approximation (the pending interpolation interval is collapsed onto the last
// known sample); for speech this is inaudible. Call once at end of stream.
func (r *Resampler) Flush() []int16 {
	if r == nil || len(r.buf) == 0 {
		return nil
	}
	last := r.buf[len(r.buf)-1]
	r.buf = r.buf[:0]
	r.pos = 0
	return []int16{last}
}
