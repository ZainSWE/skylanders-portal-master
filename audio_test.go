package main

import (
	"math"
	"testing"
)

func TestDecimator48k(t *testing.T) {
	d := Decimator{InRate: 48000}
	var out [][2]float32
	// 1 second of a 440 Hz tone at 48 kHz
	for i := 0; i < 48000; i++ {
		v := float32(math.Sin(2 * math.Pi * 440 * float64(i) / 48000))
		d.Push(v, -v, func(l, r float32) { out = append(out, [2]float32{l, r}) })
	}
	if len(out) != 8000 {
		t.Fatalf("want 8000 samples, got %d", len(out))
	}
	// tone should survive (440 Hz is well under the 4 kHz limit)
	var peak float32
	for _, f := range out {
		if f[0] > peak {
			peak = f[0]
		}
		if f[0] != -f[1] {
			t.Fatal("channels mixed up")
		}
	}
	if peak < 0.9 {
		t.Fatalf("tone too quiet after decimation: %v", peak)
	}
}

func TestDecimator44k(t *testing.T) {
	d := Decimator{InRate: 44100}
	n := 0
	for i := 0; i < 44100*3; i++ {
		d.Push(0, 0, func(l, r float32) { n++ })
	}
	if n < 23999 || n > 24001 {
		t.Fatalf("want ~24000, got %d", n)
	}
}

func TestEncodeAndPCM(t *testing.T) {
	b := EncodeSamples([]int16{0x1234, -2}, false)
	if b[0] != 0x34 || b[1] != 0x12 || b[2] != 0xFE || b[3] != 0xFF {
		t.Fatalf("% x", b)
	}
	b = EncodeSamples([]int16{0x1234}, true)
	if b[0] != 0x12 || b[1] != 0x34 {
		t.Fatalf("swap % x", b)
	}
	f := PCMToFrames([]byte{0x00, 0x40, 0x00, 0xC0})
	if f[0][0] != 0.5 || f[0][1] != -0.5 {
		t.Fatalf("%v", f)
	}
	s := ToSpeaker([][2]float32{{1, 1}, {-1, -1}, {0.5, 0}}, 1, nil)
	if s[0] != 32767 || s[1] != -32767 || s[2] != 8192 {
		t.Fatalf("%v", s)
	}
}

func TestChimeAndMeter(t *testing.T) {
	c := Chime()
	if len(c) < AudioRate || len(c) > 3*AudioRate {
		t.Fatalf("chime length %d", len(c))
	}
	for _, f := range c {
		if math.Abs(float64(f[0])) > 1 {
			t.Fatal("chime clips")
		}
	}
	var m Meter
	for i := 0; i < 20; i++ {
		m.Feed(c[1000:1032])
	}
	if m.Level[0] < 0.5 {
		t.Fatalf("meter too low: %v", m.Level)
	}
	m.Decay(60)
	if m.Level[0] > 0.01 {
		t.Fatalf("meter didn't decay: %v", m.Level)
	}
}
