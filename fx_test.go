package main

import (
	"math"
	"testing"
)

func TestPortalSounds(t *testing.T) {
	for _, rate := range []int{8000, 22050} {
		for _, style := range SoundStyles {
			for _, kind := range []string{"place", "trap", "on", "off"} {
				s := PortalSound(style, kind, rate)
				if len(s) < rate || len(s) > 3*rate {
					t.Fatalf("%s %s: %d samples", style, kind, len(s))
				}
				peak := 0.0
				for _, v := range s {
					if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
						t.Fatalf("%s %s: bad sample", style, kind)
					}
					peak = math.Max(peak, math.Abs(float64(v)))
				}
				if peak < 0.79 || peak > 0.86 {
					t.Fatalf("%s %s: peak %v", style, kind, peak)
				}
			}
		}
	}
}

func TestWavRoundTripAndResample(t *testing.T) {
	in := make([]float32, 22050)
	for i := range in {
		in[i] = float32(0.5 * math.Sin(2*math.Pi*440*float64(i)/22050))
	}
	got, rate, err := ReadWav(wavBytes(in, 22050, 1))
	if err != nil || rate != 22050 || len(got) != len(in) || math.Abs(float64(got[100]-in[100])) > 1e-3 {
		t.Fatalf("%v %d %d", err, rate, len(got))
	}
	down := Resample(got, 22050, 8000)
	if len(down) != 8000 {
		t.Fatalf("resampled to %d", len(down))
	}
	peak := 0.0
	for _, v := range down[100:] {
		peak = math.Max(peak, math.Abs(float64(v)))
	}
	if peak < 0.4 || peak > 0.55 { // a 440 Hz tone passes the filter
		t.Fatalf("peak after resampling %v", peak)
	}
	m := MixAt([]float32{1, 1}, []float32{1, 1, 1}, 1)
	if len(m) != 4 || m[1] != 2 || m[3] != 1 {
		t.Fatalf("%v", m)
	}
	if _, _, err := ReadWav([]byte("nope")); err == nil {
		t.Fatal("accepted a non-WAV")
	}
	w := wavBytes([]float32{0, 1, -1}, 22050, 1)
	if string(w[:4]) != "RIFF" || len(w) != 50 || w[46] != 0xff || w[47] != 0x7f {
		t.Fatalf("% x", w)
	}
}
