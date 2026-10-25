package main

import (
	"math"
	"testing"
)

// Reference vectors from skylanders-portal tests/g726_test.cpp: 90 random code
// bytes and what Sun's public G.726 reference decoder makes of them.
var g726Codes = []byte{
	0x86, 0xb6, 0xd1, 0x26, 0xcb, 0x4b, 0x2b, 0xf6, 0x14, 0x16, 0x17, 0x8b,
	0x58, 0xf8, 0xbf, 0xd1, 0xe6, 0xc1, 0x33, 0x33, 0xa0, 0xf8, 0xee, 0x4e,
	0x23, 0x22, 0xf6, 0x52, 0x2b, 0x53, 0x49, 0x40, 0x67, 0x14, 0xf5, 0xf6,
	0x74, 0x9d, 0x5d, 0x24, 0x9b, 0x09, 0xa2, 0x9c, 0xa6, 0x16, 0x5b, 0xd9,
	0x65, 0xb7, 0xfc, 0x82, 0xed, 0xdd, 0x9d, 0x9c, 0x19, 0x6b, 0x07, 0xf0,
	0xb1, 0xd6, 0x79, 0x51, 0x0c, 0x06, 0x66, 0x78, 0x03, 0x9c, 0x57, 0xc5,
	0x4d, 0x91, 0xb6, 0x4d, 0x63, 0xb1, 0xbd, 0xa2, 0x99, 0x33, 0xa5, 0x2b,
	0xeb, 0x1f, 0xda, 0xab, 0x2b, 0x05,
}
var g726Expected = []int16{
	60, -92, 96, -52, 8, -20, 72, 16, -48, -36, -56, 64,
	-76, 40, 136, -12, 116, 12, 216, 24, 432, 168, -680, -1648,
	-6864, 14932, -29100, 2560, -2936, -10944, 2948, -6140, 16376, -4780, 4348, -6408,
	6808, 2224, 3900, 3424, 164, -4348, -9704, 12, -4736, -2080, -2616, 8020,
	5512, 4716, 3960, 3392, 8192, -240, 3400, 5492, -4508, 2764, 1368, 5744,
	-7400, 6836, -1192, 6012, 9680, 31828, 22000, 13016, 26236, 6024, 28008, 2296,
	21220, 30512, 1400, -17828, -10984, 14296, 13792, 11060, -8116, -19260, -32768, -10512,
	-2780, -17852, -14048, -28184, 19948, -26436, 31924, -232, -6096, 17900, -27876, -3912,
	13256, 28236, 32767, -7628, -5244, -3256, 6752, -20000, -11572, -9864, -8356, -7944,
	-9776, -18632, -14192, -24728, -32768, -11640, -26132, 13376, 32767, 12680, 12116, 4824,
	9120, 36, 14164, -2172, -11640, 22776, 6248, 29076, -4308, 3808, 22500, 7432,
	29192, 32767, -24928, 32767, 19376, 19216, 1124, -20216, 32767, 32296, 32767, -284,
	-4068, 18844, 14196, -10000, 19172, -14680, -6124, 10460, 10588, 27872, 9404, -9772,
	-6072, -10676, 5204, -10152, -20920, -31848, -28, 10124, 18492, -16348, -23488, -68,
	-7404, -2192, -4772, -1532, -8604, -8064, -9656, -10748, -11004, -2632, 5416, 1024,
}

func TestG726DecoderMatchesReference(t *testing.T) {
	g := NewG726()
	var out []int16
	for _, b := range g726Codes {
		out = append(out, g.Decode(b&0x0F), g.Decode(b>>4))
	}
	bad := 0
	for i := range out {
		if out[i] != g726Expected[i] {
			if bad < 5 {
				t.Errorf("sample %d: got %d want %d", i, out[i], g726Expected[i])
			}
			bad++
		}
	}
	if bad > 0 {
		t.Fatalf("%d mismatches", bad)
	}
}

func TestG726EncoderRoundTrip(t *testing.T) {
	enc, dec := NewG726(), NewG726()
	var sig, noise float64
	n := 8000
	for i := 0; i < n; i++ {
		x := 9000*math.Sin(2*math.Pi*440*float64(i)/8000) + 4000*math.Sin(2*math.Pi*1250*float64(i)/8000)
		s := int16(x)
		y := dec.Decode(enc.Encode(s))
		if i > 400 { // let the adaptation settle
			sig += float64(s) * float64(s)
			noise += (float64(s) - float64(y)) * (float64(s) - float64(y))
		}
	}
	snr := 10 * math.Log10(sig/noise)
	if snr < 18 {
		t.Fatalf("round trip too noisy: %.1f dB", snr)
	}
	t.Logf("G.726 round trip SNR %.1f dB", snr)

	// Silence stays (nearly) silent.
	enc.Reset()
	dec.Reset()
	peak := 0
	for i := 0; i < 2000; i++ {
		v := int(dec.Decode(enc.Encode(0)))
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	if peak > 64 {
		t.Fatalf("silence came back with peak %d", peak)
	}
}

func TestXboxSpeakerFrames(t *testing.T) {
	var f XboxSpeaker
	var frames [][]byte
	samples := make([]int16, 150)
	f.Add(samples, func(fr []byte) { frames = append(frames, append([]byte(nil), fr...)) })
	if len(frames) != 2 { // 120 of 150 samples make two frames
		t.Fatalf("got %d frames", len(frames))
	}
	for _, fr := range frames {
		if len(fr) != 32 || fr[0] != 0x0B || fr[1] != 0x17 {
			t.Fatalf("bad frame % X", fr)
		}
	}
	f.Add(make([]int16, 30), func(fr []byte) { frames = append(frames, fr) })
	if len(frames) != 3 {
		t.Fatal("leftover samples weren't kept")
	}
}
