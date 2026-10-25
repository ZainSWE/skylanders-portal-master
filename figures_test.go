package main

import "testing"

func figureHeader(id, variant uint16) []byte {
	b := make([]byte, 32)
	b[0], b[1], b[2], b[3] = 0x12, 0x34, 0x56, 0x78 // serial
	b[0x10], b[0x11] = byte(id), byte(id>>8)
	b[0x1C], b[0x1D] = byte(variant), byte(variant>>8)
	return b
}

func TestIdentify(t *testing.T) {
	cases := []struct {
		id, variant         uint16
		name, element, kind string
	}{
		{16, 0, "Spyro", "Magic", "Skylander"},
		{466, 12288, "Blastermind", "Magic", "Trap Master"},
		{482, 12288, "Knight Light", "Light", "Trap Master"},
		{215, 12299, "", "Fire", "Trap"},
		{300, 0, "Dragon's Peak", "", "Magic Item"},
	}
	for _, c := range cases {
		f := Identify(figureHeader(c.id, c.variant))
		if f.Unknown || f.Element != c.element || f.Kind != c.kind || (c.name != "" && f.Name != c.name) {
			t.Errorf("%d/%d: got %+v", c.id, c.variant, f)
		}
	}
	for _, c := range []struct{ id, variant uint16 }{{30, 0x1801}, {30, 0}} { // Legendary/plain Chop Chop
		if f := Identify(figureHeader(c.id, c.variant)); f.Character != "Chop Chop" {
			t.Errorf("character of %+v: %q", f, f.Character)
		}
	}
	if f := Identify(figureHeader(9999, 0)); !f.Unknown {
		t.Errorf("unknown id should be unknown: %+v", f)
	}
	// a variant that isn't in the catalogue falls back to the base figure
	if f := Identify(figureHeader(16, 0x7777)); f.Name != "Spyro" {
		t.Errorf("fallback: %+v", f)
	}
}

// buildTrap makes the blocks of a trap holding villain id (the way the game saves it).
func buildTrap(trapID uint16, villain uint8) map[uint8][]byte {
	first := figureHeader(trapID, 12290)
	blocks := map[uint8][]byte{0: first[:16], 1: first[16:]}
	area := make([]byte, 336)
	area[1], area[9], area[16] = 1, 1, villain
	put := func(at int, v uint16) { area[at], area[at+1] = byte(v), byte(v>>8) }
	put(10, figureCRC16(area[64:]))
	put(12, figureCRC16(area[16:64]))
	head := append([]byte{}, area[:16]...)
	head[14], head[15] = 0x05, 0
	put(14, figureCRC16(head))
	for i, idx := range trapAreaBlocks(0) {
		blocks[idx] = encryptBlock(first, idx, area[i*16:i*16+16])
	}
	for _, idx := range trapAreaBlocks(1) {
		blocks[idx] = make([]byte, 16)
	}
	return blocks
}

func TestTrapVillain(t *testing.T) {
	name, ok := ReadTrapVillain(211, buildTrap(211, 9))
	if !ok || name != "Gulper" {
		t.Fatalf("got %q %v", name, ok)
	}
	empty := buildTrap(215, 0)
	if name, ok := ReadTrapVillain(215, empty); !ok || name != "" {
		t.Fatalf("empty trap: %q %v", name, ok)
	}
	broken := buildTrap(211, 9)
	broken[9][0] ^= 0xFF
	if _, ok := ReadTrapVillain(211, broken); ok {
		t.Fatal("corrupt save area should not read as valid")
	}
	if n := len(TrapBlocksToRead()); n != 44 {
		t.Fatalf("blocks to read: %d", n)
	}
}

func TestDecryptRoundTrip(t *testing.T) {
	first := figureHeader(16, 0)
	plain := []byte("sixteen byte blk")
	if got := decryptBlock(first, 8, encryptBlock(first, 8, plain)); string(got) != string(plain) {
		t.Fatalf("%q", got)
	}
}
