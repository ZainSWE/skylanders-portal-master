package main

import (
	"encoding/binary"
	"errors"
	"math"
	"math/rand"
)

// Portal sounds, synthesised here (no game audio). Two styles, one for each kind of portal:
//   "classic"  - the Spyro's Adventure / Giants portal: airy, harp-like and magical.
//   "trapteam" - the Trap Team portal: crystal chimes, electric charge and a deep hit.
// Kinds: "place" (a figure is put on), "trap" (a trap goes in), "on" and "off".

var SoundKinds = []string{"place", "on", "off"}
var SoundStyles = []string{"classic", "trapteam"}

// SoundEvent is something the portal makes a sound for.
type SoundEvent struct {
	Style string // "classic" or "trapteam": which portal's turning on/off sounds
	Kind  string // "place", "on" or "off"
	Force bool   // a preview from the window: play even if that sound is switched off
}

// clip is a sound being played on the portal.
type clip struct {
	kind   string // "file" or "test"
	name   string
	frames [][2]float32
	pos    int
}

type synth struct {
	out []float32
	sr  float64
	rng *rand.Rand
	low float64 // lowest frequency worth playing (the portal's little speaker has no bass)
}

func newSynth(rate int, secs float64) *synth {
	low := 40.0
	if rate <= 8000 {
		low = 160
	}
	return &synth{out: make([]float32, int(secs*float64(rate))), sr: float64(rate), rng: rand.New(rand.NewSource(7)), low: low}
}

func (s *synth) span(start, dur float64, f func(i int, t float64)) {
	for i := max(0, int(start*s.sr)); i < len(s.out) && float64(i) < (start+dur)*s.sr; i++ {
		f(i, float64(i)/s.sr-start)
	}
}

// partial adds one decaying sine; anything the sample rate can't carry is skipped.
func (s *synth) partial(start, dur, hz, amp, attack, decay float64) {
	if hz >= s.sr*0.45 || hz <= 0 {
		return
	}
	s.span(start, dur, func(i int, t float64) {
		env := math.Min(t/attack, 1) * math.Exp(-t*decay)
		s.out[i] += float32(math.Sin(2*math.Pi*hz*t) * amp * env)
	})
}

// pluck: a harp-like note.
func (s *synth) pluck(start, hz, amp float64) {
	for k, a := range []float64{1, 0.45, 0.22, 0.1} {
		s.partial(start, 1.6, hz*float64(k+1), amp*a, 0.004, 3.2+float64(k)*1.6)
	}
}

// bell: a crystal chime with slightly out-of-tune overtones.
func (s *synth) bell(start, hz, amp, decay float64) {
	for k, r := range []float64{1, 2.01, 2.76, 4.07, 5.43} {
		a := []float64{1, 0.55, 0.42, 0.25, 0.16}[k]
		s.partial(start, 2.5, hz*r, amp*a, 0.003, decay*(1+float64(k)*0.5))
	}
}

// sweep: a tone gliding from f0 to f1. saw gives a buzzier, electric sound.
func (s *synth) sweep(start, dur, f0, f1, amp float64, saw bool, env func(x float64) float64) {
	f0, f1 = math.Max(f0, s.low), math.Max(f1, s.low)
	ph := 0.0
	s.span(start, dur, func(i int, t float64) {
		x := t / dur
		hz := f0 * math.Pow(f1/f0, x)
		ph += hz / s.sr
		v := math.Sin(2 * math.Pi * ph)
		if saw {
			p := math.Mod(ph, 1)
			v = 0
			for k := 1.0; k <= 6 && hz*k < s.sr*0.45; k++ { // band-limited saw
				v += math.Sin(2*math.Pi*p*k) / k
			}
			v *= 0.6
		}
		s.out[i] += float32(v * amp * env(x))
	})
}

// whoosh: noise through a band-pass filter sweeping from f0 to f1.
func (s *synth) whoosh(start, dur, f0, f1, amp float64, env func(x float64) float64) {
	var ic1, ic2 float64
	top := s.sr * 0.42
	s.span(start, dur, func(i int, t float64) {
		x := t / dur
		fc := math.Min(f0*math.Pow(f1/f0, x), top)
		g, k := math.Tan(math.Pi*fc/s.sr), 1/1.4
		a1 := 1 / (1 + g*(g+k))
		a2, in := g*a1, s.rng.Float64()*2-1
		v3 := in - ic2
		band := a1*ic1 + a2*v3
		v2 := ic2 + a2*ic1 + g*a2*v3
		ic1, ic2 = 2*band-ic1, 2*v2-ic2
		s.out[i] += float32(band * amp * env(x))
	})
}

// crackle: sparse electric clicks.
func (s *synth) crackle(start, dur, amp, density float64) {
	s.span(start, dur, func(i int, t float64) {
		if s.rng.Float64() < density/s.sr {
			n := int(s.sr * 0.004)
			for j := 0; j < n && i+j < len(s.out); j++ {
				s.out[i+j] += float32((s.rng.Float64()*2 - 1) * amp * math.Exp(-float64(j)/float64(n)*4))
			}
		}
	})
}

// hit: a deep thump (higher on the portal's speaker so it can be heard).
func (s *synth) hit(start, amp float64) {
	from, to := 140.0, 45.0
	if s.low > 100 {
		from, to = 330, 160
	}
	ph := 0.0
	s.span(start, 0.5, func(i int, t float64) {
		hz := from * math.Pow(to/from, math.Min(t/0.35, 1))
		ph += hz / s.sr
		s.out[i] += float32(math.Sin(2*math.Pi*ph) * amp * math.Exp(-t*9))
	})
}

func swell(x float64) float64 { return math.Sin(math.Pi * math.Min(x, 1)) }
func rise(x float64) float64  { return x * x }
func fall(x float64) float64  { return (1 - x) * (1 - x) }

// PortalSound makes one of the portal sounds at the given sample rate.
func PortalSound(style, kind string, rate int) []float32 {
	if kind == "place" || kind == "trap" {
		return classicSound("place", rate) // the same sound for every figure
	}
	if style == "trapteam" {
		return trapTeamSound(kind, rate)
	}
	return classicSound(kind, rate)
}

// Spyro's Adventure / Giants portal: harp, shimmer and a soft magical bloom.
func classicSound(kind string, rate int) []float32 {
	switch kind {
	case "on": // the portal wakes: a swell that blooms into a bright chord
		s := newSynth(rate, 2.3)
		s.whoosh(0, 1.15, 200, 2600, 0.55, rise)
		s.sweep(0, 1.1, 110, 260, 0.3, false, rise)
		for k, hz := range []float64{392, 493.9, 587.3, 784} {
			s.bell(1.0+float64(k)*0.05, hz, 0.2, 1.7)
		}
		s.whoosh(1.0, 1.2, 3000, 6000, 0.12, fall)
		s.hit(1.0, 0.45)
		return normalize(s.out, 0.85)
	case "off": // the magic drains away: falling harp and a closing whoosh
		s := newSynth(rate, 1.9)
		for k, hz := range []float64{1174.7, 987.8, 784, 587.3, 493.9, 392} {
			s.pluck(float64(k)*0.07, hz, 0.25)
		}
		s.whoosh(0.1, 1.3, 3200, 220, 0.45, fall)
		s.sweep(0.2, 1.2, 330, 110, 0.25, false, fall)
		return normalize(s.out, 0.8)
	default: // "place" and "trap": a rising whoosh, an upward harp run and a shimmer
		s := newSynth(rate, 2.0)
		s.whoosh(0, 0.9, 250, 3400, 0.6, swell)
		notes := []float64{523.3, 659.3, 784, 1046.5, 1318.5, 1568}
		for k, hz := range notes {
			s.pluck(0.35+float64(k)*0.055, hz, 0.24)
		}
		s.bell(0.72, 2093, 0.12, 2.4)
		s.whoosh(0.7, 1.0, 4500, 7000, 0.1, fall)
		s.hit(0.7, 0.45)
		return normalize(s.out, 0.8)
	}
}

// Trap Team portal: electric charge, crystal chimes and a heavy lock.
func trapTeamSound(kind string, rate int) []float32 {
	switch kind {
	case "on": // power surging up, a crystal ring and a deep hit
		s := newSynth(rate, 2.2)
		s.sweep(0, 1.0, 70, 420, 0.32, true, rise)
		s.crackle(0.3, 0.75, 0.35, 30)
		s.whoosh(0, 1.0, 300, 4000, 0.4, rise)
		s.hit(1.0, 0.8)
		for k, hz := range []float64{880, 1318.5, 1760} {
			s.bell(1.0+float64(k)*0.03, hz, 0.2, 1.5)
		}
		return normalize(s.out, 0.85)
	case "off": // powering down: a falling electric whine, crackle, a last click
		s := newSynth(rate, 1.6)
		s.sweep(0, 1.1, 520, 60, 0.38, true, fall)
		s.crackle(0, 0.8, 0.3, 24)
		s.bell(0, 1318.5, 0.14, 4)
		s.partial(1.1, 0.05, 2200, 0.3, 0.001, 70) // click
		return normalize(s.out, 0.8)
	case "trap": // a trap locks in: a metal clank and falling crystal notes
		s := newSynth(rate, 1.8)
		s.whoosh(0, 0.5, 600, 4000, 0.4, swell)
		for _, hz := range []float64{1210, 1733, 2357, 3110} {
			s.partial(0.35, 0.5, hz, 0.14, 0.001, 9)
		}
		s.hit(0.35, 0.7)
		for k, hz := range []float64{1760, 1318.5, 1046.5, 880} {
			s.bell(0.5+float64(k)*0.09, hz, 0.15, 2.6)
		}
		return normalize(s.out, 0.8)
	default: // "place": an electric swell into a cluster of crystal chimes and a hit
		s := newSynth(rate, 2.0)
		s.sweep(0, 0.65, 160, 900, 0.22, true, rise)
		s.whoosh(0, 0.7, 400, 5000, 0.5, rise)
		s.crackle(0.35, 0.35, 0.25, 40)
		s.hit(0.65, 0.75)
		for k, hz := range []float64{1046.5, 1568, 2093, 1318.5, 2637} {
			s.bell(0.65+float64(k)*0.035, hz, 0.14, 2.2)
		}
		return normalize(s.out, 0.8)
	}
}

// Announcer: a little slapback echo and room so a spoken name sounds bigger.
func Announcer(m []float32, rate int) []float32 {
	d1, d2 := int(0.09*float64(rate)), int(0.17*float64(rate))
	out := make([]float32, len(m)+d2*3)
	copy(out, m)
	for i := range out {
		if i >= d1 {
			out[i] += 0.32 * out[i-d1]
		}
		if i >= d2 {
			out[i] += 0.18 * out[i-d2]
		}
	}
	return normalize(out, 0.85)
}

// MixAt adds b into a starting at sample `at`, growing a as needed.
func MixAt(a, b []float32, at int) []float32 {
	if at < 0 {
		at = 0
	}
	if need := at + len(b); need > len(a) {
		a = append(a, make([]float32, need-len(a))...)
	}
	for i, v := range b {
		a[at+i] += v
	}
	return a
}

func normalize(m []float32, peak float64) []float32 {
	p := 0.0
	for _, v := range m {
		p = math.Max(p, math.Abs(float64(v)))
	}
	if p > 0 {
		g := float32(peak / p)
		for i := range m {
			m[i] *= g
		}
	}
	return m
}

// Resample converts mono samples between rates, filtering first when going down so
// the result doesn't alias.
func Resample(m []float32, from, to int) []float32 {
	if from == to || len(m) == 0 {
		return append([]float32(nil), m...)
	}
	src := m
	if to < from { // simple low-pass: two passes of a one-pole filter at about 0.4 * the new rate
		fc := 0.4 * float64(to)
		a := 1 - math.Exp(-2*math.Pi*fc/float64(from))
		src = make([]float32, len(m))
		var y1, y2 float64
		for i, v := range m {
			y1 += a * (float64(v) - y1)
			y2 += a * (y1 - y2)
			src[i] = float32(y2)
		}
	}
	n := int(int64(len(src)) * int64(to) / int64(from))
	out := make([]float32, n)
	step := float64(from) / float64(to)
	for i := range out {
		x := float64(i) * step
		j := int(x)
		f := float32(x - float64(j))
		a := src[min(j, len(src)-1)]
		b := src[min(j+1, len(src)-1)]
		out[i] = a + (b-a)*f
	}
	return out
}

// monoFrames turns mono samples into the stereo frames the player uses.
func monoFrames(m []float32) [][2]float32 {
	f := make([][2]float32, len(m))
	for i, v := range m {
		f[i] = [2]float32{v, v}
	}
	return f
}

// wavBytes wraps mono samples in a 16-bit WAV file.
func wavBytes(m []float32, rate int, gain float64) []byte {
	data := len(m) * 2
	b := make([]byte, 44+data)
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+data))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1) // PCM
	binary.LittleEndian.PutUint16(b[22:], 1) // mono
	binary.LittleEndian.PutUint32(b[24:], uint32(rate))
	binary.LittleEndian.PutUint32(b[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(data))
	for i, v := range m {
		x := math.Max(-1, math.Min(1, float64(v)*gain))
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(int16(x*32767)))
	}
	return b
}

// ReadWav reads a 16-bit PCM WAV (any rate, mono or stereo) as mono samples.
func ReadWav(b []byte) (samples []float32, rate int, err error) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, 0, errors.New("not a WAV file")
	}
	var channels, bits int
	for p := 12; p+8 <= len(b); {
		id, size := string(b[p:p+4]), int(binary.LittleEndian.Uint32(b[p+4:]))
		body := b[p+8 : min(len(b), p+8+size)]
		switch id {
		case "fmt ":
			if len(body) < 16 || binary.LittleEndian.Uint16(body) != 1 {
				return nil, 0, errors.New("only plain (PCM) WAV files are supported")
			}
			channels = int(binary.LittleEndian.Uint16(body[2:]))
			rate = int(binary.LittleEndian.Uint32(body[4:]))
			bits = int(binary.LittleEndian.Uint16(body[14:]))
		case "data":
			if bits != 16 || channels < 1 || rate < 4000 {
				return nil, 0, errors.New("only 16-bit WAV files are supported")
			}
			n := len(body) / (2 * channels)
			samples = make([]float32, n)
			for i := 0; i < n; i++ {
				sum := 0.0
				for c := 0; c < channels; c++ {
					sum += float64(int16(binary.LittleEndian.Uint16(body[2*(i*channels+c):])))
				}
				samples[i] = float32(sum / float64(channels) / 32768)
			}
			return samples, rate, nil
		}
		p += 8 + size + size%2
	}
	return nil, 0, errors.New("the WAV file has no sound data")
}
