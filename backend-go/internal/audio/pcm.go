package audio

import (
	"encoding/binary"
	"math"
)

// This file holds pure PCM16 helper functions. All of them are safe for
// concurrent use (no shared state) and never allocate more than their output.

// BytesToSamples decodes little-endian PCM16 bytes into samples. A trailing
// odd byte (malformed frame) is dropped rather than causing an error — audio
// pipelines prefer resync over stall.
func BytesToSamples(b []byte) []int16 {
	n := len(b) / 2
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		out[i] = int16(binary.LittleEndian.Uint16(b[i*2 : i*2+2]))
	}
	return out
}

// SamplesToBytes encodes samples as little-endian PCM16.
func SamplesToBytes(s []int16) []byte {
	out := make([]byte, len(s)*2)
	for i, v := range s {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(v))
	}
	return out
}

// Mix sums two sample buffers with hard clipping to the int16 range. Buffers of
// different lengths are mixed over the longer length; the shorter buffer is
// treated as silence past its end. Used if two sources ever need to share a
// route (e.g. future stereo handling); the current planner never mixes.
func Mix(a, b []int16) []int16 {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		var sum int
		if i < len(a) {
			sum += int(a[i])
		}
		if i < len(b) {
			sum += int(b[i])
		}
		out[i] = clampI16(sum)
	}
	return out
}

// RMS returns the root-mean-square amplitude of the samples in [0, 1.0]
// relative to full scale (32768). An empty buffer returns 0.
func RMS(s []int16) float64 {
	if len(s) == 0 {
		return 0
	}
	var sum float64
	for _, v := range s {
		f := float64(v) / 32768.0
		sum += f * f
	}
	return math.Sqrt(sum / float64(len(s)))
}

// DownmixStereoToMono averages L/R sample pairs into a single mono channel.
// Input must be interleaved stereo PCM16; an odd trailing sample is ignored.
func DownmixStereoToMono(stereo []int16) []int16 {
	n := len(stereo) / 2
	out := make([]int16, n)
	for i := 0; i < n; i++ {
		out[i] = clampI16((int(stereo[i*2]) + int(stereo[i*2+1])) / 2)
	}
	return out
}

// clampI16 saturates v into the int16 range (no wraparound distortion).
func clampI16(v int) int16 {
	if v > math.MaxInt16 {
		return math.MaxInt16
	}
	if v < math.MinInt16 {
		return math.MinInt16
	}
	return int16(v)
}
