package main

import "math"

// AudioRate is the Trap Team speaker's sample rate (mono, 16-bit).
const AudioRate = 8000

// SamplesPerPacket is how many 16-bit samples fit in one 64-byte audio packet.
const SamplesPerPacket = 32

// Decimator turns audio at any rate into AudioRate by averaging every input
// sample that falls inside each output sample's time slot (a simple box filter,
// which is plenty for a tiny 8 kHz speaker).
type Decimator struct {
	InRate   float64
	phase    float64
	sl, sr   float64
	n        int
	lastL    float32
	lastR    float32
	haveLast bool
}

func (d *Decimator) Push(l, r float32, out func(l, r float32)) {
	if d.InRate <= AudioRate {
		// Upsample (rare): repeat samples.
		d.phase += AudioRate
		for d.phase >= d.InRate {
			d.phase -= d.InRate
			out(l, r)
		}
		return
	}
	d.sl += float64(l)
	d.sr += float64(r)
	d.n++
	d.phase += AudioRate
	if d.phase >= d.InRate {
		d.phase -= d.InRate
		out(float32(d.sl/float64(d.n)), float32(d.sr/float64(d.n)))
		d.sl, d.sr, d.n = 0, 0, 0
	}
}

// Meter tracks a smoothed sound level per channel, 0..1.
type Meter struct{ Level [2]float64 }

func (m *Meter) Feed(frames [][2]float32) {
	if len(frames) == 0 {
		m.Decay(1)
		return
	}
	var sum [2]float64
	for _, f := range frames {
		sum[0] += float64(f[0]) * float64(f[0])
		sum[1] += float64(f[1]) * float64(f[1])
	}
	for c := 0; c < 2; c++ {
		rms := math.Sqrt(sum[c] / float64(len(frames)))
		// Map roughly -45 dB..-3 dB onto 0..1 so quiet music still moves the lights.
		v := 0.0
		if rms > 0 {
			v = clampf((20*math.Log10(rms)+45)/42, 0, 1)
		}
		if v > m.Level[c] {
			m.Level[c] += (v - m.Level[c]) * 0.6 // fast attack
		} else {
			m.Level[c] += (v - m.Level[c]) * 0.12 // slow release
		}
	}
}

func (m *Meter) Decay(steps int) {
	for i := 0; i < steps; i++ {
		m.Level[0] *= 0.9
		m.Level[1] *= 0.9
	}
}

// ToSpeaker mixes stereo frames down to mono 16-bit samples at the given volume.
func ToSpeaker(frames [][2]float32, volume float64, out []int16) []int16 {
	out = out[:0]
	for _, f := range frames {
		v := (float64(f[0]) + float64(f[1])) / 2 * volume
		v = clampf(v, -1, 1)
		out = append(out, int16(math.Round(v*32767)))
	}
	return out
}

// EncodeSamples turns samples into bytes, little-endian unless swap is set.
func EncodeSamples(s []int16, swap bool) []byte {
	b := make([]byte, len(s)*2)
	for i, v := range s {
		lo, hi := byte(uint16(v)), byte(uint16(v)>>8)
		if swap {
			lo, hi = hi, lo
		}
		b[2*i], b[2*i+1] = lo, hi
	}
	return b
}

// Chime is a short rising "portal" arpeggio used by the speaker test.
func Chime() [][2]float32 {
	notes := []float64{523.25, 659.25, 783.99, 1046.50, 1318.51} // C5 E5 G5 C6 E6
	const step = 0.14
	total := int((step*float64(len(notes)) + 0.9) * AudioRate)
	out := make([][2]float32, total)
	for i, f := range notes {
		start := int(float64(i) * step * AudioRate)
		for j := 0; start+j < total; j++ {
			t := float64(j) / AudioRate
			env := math.Exp(-t*3.2) * math.Min(1, t*200)
			v := 0.22 * env * (math.Sin(2*math.Pi*f*t) + 0.3*math.Sin(4*math.Pi*f*t))
			out[start+j][0] += float32(v)
			out[start+j][1] += float32(v)
		}
	}
	return out
}

// PCMToFrames converts interleaved stereo 16-bit little-endian PCM to frames.
func PCMToFrames(b []byte) [][2]float32 {
	n := len(b) / 4
	out := make([][2]float32, n)
	for i := 0; i < n; i++ {
		l := int16(uint16(b[4*i]) | uint16(b[4*i+1])<<8)
		r := int16(uint16(b[4*i+2]) | uint16(b[4*i+3])<<8)
		out[i] = [2]float32{float32(l) / 32768, float32(r) / 32768}
	}
	return out
}
