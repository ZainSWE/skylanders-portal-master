package main

// G.726 ADPCM at 32 kbit/s (4-bit codes, 8 kHz). The Xbox 360 Traptanium
// Portal expects its speaker audio in this format: frames with the header
// 0B 17 carrying 30 bytes = 60 codes, first code in the low nibble.
//
// The decoder is a Go port of the one in skylanders-portal (src/portal/g726.cpp,
// MIT License, Copyright (c) 2026 TheBiemGamer), which follows Sun Microsystems'
// public g72x reference. The encoder adds the reference's quantizer on top of
// the same predictor and adaptation code.

var g726Power2 = [15]int{1, 2, 4, 8, 0x10, 0x20, 0x40, 0x80, 0x100, 0x200, 0x400, 0x800, 0x1000, 0x2000, 0x4000}

func g726Quan(value int, table []int) int {
	i := 0
	for i < len(table) && value >= table[i] {
		i++
	}
	return i
}

func g726FMult(an, srn int) int {
	anmag := an
	if an <= 0 {
		anmag = (-an) & 0x1FFF
	}
	anexp := g726Quan(anmag, g726Power2[:]) - 6
	var anmant int
	switch {
	case anmag == 0:
		anmant = 32
	case anexp >= 0:
		anmant = anmag >> anexp
	default:
		anmant = anmag << -anexp
	}
	wanexp := anexp + ((srn >> 6) & 0xF) - 13
	wanmant := (anmant*(srn&0o77) + 0x30) >> 4
	var retval int
	if wanexp >= 0 {
		retval = (wanmant << wanexp) & 0x7FFF
	} else {
		retval = wanmant >> -wanexp
	}
	if (an ^ srn) < 0 {
		return -retval
	}
	return retval
}

func g726ToFloat(mag int) int {
	exp := g726Quan(mag, g726Power2[:])
	return (exp << 6) + ((mag << 6) >> exp)
}

var (
	g726Dqln = [16]int{-2048, 4, 135, 213, 273, 323, 373, 425, 425, 373, 323, 273, 213, 135, 4, -2048}
	g726Wi   = [16]int{-12, 18, 41, 64, 112, 198, 355, 1122, 1122, 355, 198, 112, 64, 41, 18, -12}
	g726Fi   = [16]int{0, 0, 0, 0x200, 0x200, 0x200, 0x600, 0xE00, 0xE00, 0x600, 0x200, 0x200, 0x200, 0, 0, 0}
	g726Qtab = []int{-124, 80, 178, 246, 300, 349, 400}
)

func g726Reconstruct(sign bool, dqln, y int) int {
	dql := dqln + (y >> 2)
	if dql < 0 {
		if sign {
			return -0x8000
		}
		return 0
	}
	dex := (dql >> 7) & 15
	dqt := 128 + (dql & 127)
	dq := (dqt << 7) >> (14 - dex)
	if sign {
		return dq - 0x8000
	}
	return dq
}

// G726 holds the codec state shared by the encoder and decoder.
type G726 struct {
	yl, yu, dms, dml, ap int32
	a                    [2]int32
	b                    [6]int32
	pk                   [2]int32
	dq                   [6]int16
	sr                   [2]int32
	td                   bool
}

func NewG726() *G726 {
	g := &G726{}
	g.Reset()
	return g
}

func (g *G726) Reset() {
	*g = G726{yl: 34816, yu: 544}
	for i := range g.dq {
		g.dq[i] = 32
	}
	g.sr = [2]int32{32, 32}
}

func (g *G726) stepSize() int {
	if g.ap >= 256 {
		return int(g.yu)
	}
	y := int(g.yl >> 6)
	dif := int(g.yu) - y
	al := int(g.ap >> 2)
	if dif > 0 {
		y += (dif * al) >> 6
	} else if dif < 0 {
		y += (dif*al + 0x3F) >> 6
	}
	return y
}

func (g *G726) predictorZero() int {
	s := 0
	for i := range g.b {
		s += g726FMult(int(g.b[i]>>2), int(g.dq[i]))
	}
	return s
}

func (g *G726) predictorPole() int {
	return g726FMult(int(g.a[1]>>2), int(g.sr[1])) + g726FMult(int(g.a[0]>>2), int(g.sr[0]))
}

func (g *G726) update(y, wi, fi, dq, sr, dqsez int) {
	pk0 := int32(0)
	if dqsez < 0 {
		pk0 = 1
	}
	mag := dq & 0x7FFF

	ylint := g.yl >> 15
	ylfrac := (g.yl >> 10) & 0x1F
	thr1 := (32 + ylfrac) << ylint
	thr2 := thr1
	if ylint > 9 {
		thr2 = 31 << 10
	}
	dqthr := (thr2 + (thr2 >> 1)) >> 1
	tr := g.td && int32(mag) > dqthr

	yu := y + ((wi - y) >> 5)
	if yu < 544 {
		yu = 544
	}
	if yu > 5120 {
		yu = 5120
	}
	g.yu = int32(yu)
	g.yl += int32(yu) + ((-g.yl) >> 6)

	a2p := int32(0)
	if tr {
		g.a = [2]int32{}
		g.b = [6]int32{}
	} else {
		pks1 := pk0 ^ g.pk[0]
		a2p = g.a[1] - (g.a[1] >> 7)
		if dqsez != 0 {
			fa1 := -g.a[0]
			if pks1 != 0 {
				fa1 = g.a[0]
			}
			switch {
			case fa1 < -8191:
				a2p -= 0x100
			case fa1 > 8191:
				a2p += 0xFF
			default:
				a2p += fa1 >> 5
			}
			if pk0^g.pk[1] != 0 {
				switch {
				case a2p <= -12160:
					a2p = -12288
				case a2p >= 12416:
					a2p = 12288
				default:
					a2p -= 0x80
				}
			} else {
				switch {
				case a2p <= -12416:
					a2p = -12288
				case a2p >= 12160:
					a2p = 12288
				default:
					a2p += 0x80
				}
			}
		}
		g.a[1] = a2p

		a1 := g.a[0] - (g.a[0] >> 8)
		if dqsez != 0 {
			if pks1 == 0 {
				a1 += 192
			} else {
				a1 -= 192
			}
		}
		a1ul := 15360 - a2p
		if a1 < -a1ul {
			a1 = -a1ul
		}
		if a1 > a1ul {
			a1 = a1ul
		}
		g.a[0] = a1

		for i := range g.b {
			bi := g.b[i] - (g.b[i] >> 8)
			if mag != 0 {
				if (int32(dq) ^ int32(g.dq[i])) >= 0 {
					bi += 128
				} else {
					bi -= 128
				}
			}
			g.b[i] = bi
		}
	}

	for i := len(g.dq) - 1; i > 0; i-- {
		g.dq[i] = g.dq[i-1]
	}
	if mag == 0 {
		if dq >= 0 {
			g.dq[0] = 0x20
		} else {
			g.dq[0] = int16(-992) // 0xFC20
		}
	} else if dq >= 0 {
		g.dq[0] = int16(g726ToFloat(mag))
	} else {
		g.dq[0] = int16(g726ToFloat(mag) - 0x400)
	}
	g.sr[1] = g.sr[0]
	switch {
	case sr == 0:
		g.sr[0] = 0x20
	case sr > 0:
		g.sr[0] = int32(g726ToFloat(sr))
	case sr > -32768:
		g.sr[0] = int32(g726ToFloat(-sr) - 0x400)
	default:
		g.sr[0] = 0xFC20
	}
	g.pk[1] = g.pk[0]
	g.pk[0] = pk0

	g.td = !tr && a2p < -11776

	g.dms += (int32(fi) - g.dms) >> 5
	g.dml += ((int32(fi) << 2) - g.dml) >> 7
	diff := (g.dms << 2) - g.dml
	if diff < 0 {
		diff = -diff
	}
	switch {
	case tr:
		g.ap = 256
	case y < 1536 || g.td || diff >= (g.dml>>3):
		g.ap += (0x200 - g.ap) >> 4
	default:
		g.ap += (-g.ap) >> 4
	}
}

// step runs one sample through the shared predictor given its code.
func (g *G726) step(i, se, sez, y int) int {
	dq := g726Reconstruct(i&0x08 != 0, g726Dqln[i], y)
	var sr int
	if dq < 0 {
		sr = se - (dq & 0x3FFF)
	} else {
		sr = se + dq
	}
	dqsez := sr - se + sez
	g.update(y, g726Wi[i]<<5, g726Fi[i], dq, sr, dqsez)
	return sr
}

// Decode turns one 4-bit code into a 16-bit sample.
func (g *G726) Decode(code byte) int16 {
	i := int(code & 0x0F)
	sezi := g.predictorZero()
	sez := sezi >> 1
	se := (sezi + g.predictorPole()) >> 1
	y := g.stepSize()
	sr := g.step(i, se, sez, y) << 2
	if sr > 32767 {
		sr = 32767
	} else if sr < -32768 {
		sr = -32768
	}
	return int16(sr)
}

// Encode turns one 16-bit sample into a 4-bit code.
func (g *G726) Encode(sample int16) byte {
	sl := int(sample) >> 2 // the codec works on 14-bit samples
	sezi := g.predictorZero()
	sez := sezi >> 1
	se := (sezi + g.predictorPole()) >> 1
	d := sl - se
	y := g.stepSize()

	// Quantize d using the scale factor y (Sun reference quantize()).
	dqm := d
	if dqm < 0 {
		dqm = -dqm
	}
	exp := g726Quan(dqm>>1, g726Power2[:])
	mant := ((dqm << 7) >> exp) & 0x7F
	dl := (exp << 7) + mant
	dln := dl - (y >> 2)
	i := g726Quan(dln, g726Qtab)
	switch {
	case d < 0:
		i = (len(g726Qtab) << 1) + 1 - i
	case i == 0:
		i = (len(g726Qtab) << 1) + 1
	}
	g.step(i, se, sez, y)
	return byte(i)
}

// XboxSpeaker packs 8 kHz samples into Xbox 360 portal speaker frames:
// 0B 17 followed by 30 bytes of G.726 codes (60 samples, low nibble first).
type XboxSpeaker struct {
	codec   *G726
	pending []int16
}

const xboxSpeakerSamples = 60

// Reset starts a new stream (the portal resets its decoder when the speaker is switched on).
func (x *XboxSpeaker) Reset() {
	x.codec = nil
	x.pending = x.pending[:0]
}

func (x *XboxSpeaker) Add(samples []int16, emit func(frame []byte)) {
	if x.codec == nil {
		x.codec = NewG726()
	}
	x.pending = append(x.pending, samples...)
	for len(x.pending) >= xboxSpeakerSamples {
		frame := make([]byte, 32)
		frame[0], frame[1] = 0x0B, 0x17
		for i := 0; i < xboxSpeakerSamples; i += 2 {
			lo := x.codec.Encode(x.pending[i])
			hi := x.codec.Encode(x.pending[i+1])
			frame[2+i/2] = lo | hi<<4
		}
		x.pending = x.pending[xboxSpeakerSamples:]
		emit(frame)
	}
}
