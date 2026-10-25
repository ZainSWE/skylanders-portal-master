package main

import (
	"strings"
	"testing"
)

func TestFindReply(t *testing.T) {
	isM := func(b []byte) bool { return len(b) >= 2 && b[0] == 'M' && b[1] == 0 }
	// what the Xbox 360 Trap Team portal actually replied to M 00
	if findReply([][]byte{{0x0B, 0x14, 'S', 0x55, 1}, {0x0B, 0x14, 0x4D, 0x00, 0x00, 0x00}}, isM) == nil {
		t.Fatal("should find M reply after an Xbox header")
	}
	if findReply([][]byte{{'M', 0x00, 0x00, 0x19}}, isM) == nil {
		t.Fatal("should find a plain M reply")
	}
	// status messages that happen to contain 4D 00 further in must not count
	if findReply([][]byte{{0x0B, 0x14, 'S', 'M', 0x00, 0}, {'S', 'M', 0, 5}}, isM) != nil {
		t.Fatal("false match")
	}
}

func TestSafePacketAndUpsample(t *testing.T) {
	p := []byte{0x0B, 0x14, 1}
	safePacket(p)
	if p[0] == 0x0B && p[1] == 0x14 {
		t.Fatal("header not broken up")
	}
	u := upsample2([]int16{0, 100, 200})
	if len(u) != 6 || u[1] != 50 || u[3] != 150 || u[5] != 200 {
		t.Fatalf("%v", u)
	}
}

func TestDescribeConfig(t *testing.T) {
	cfg := []byte{9, 2, 39, 0, 2, 1, 0, 0x80, 0xFA,
		9, 4, 0, 0, 2, 0xFF, 0x5D, 0x01, 0,
		7, 5, 0x81, 3, 32, 0, 1,
		7, 5, 0x02, 3, 32, 0, 1,
		9, 4, 1, 0, 1, 0xFF, 0x5D, 0x03, 0,
		7, 5, 0x03, 3, 64, 0, 2}
	s := describeConfig(cfg)
	for _, want := range []string{"Interface 0 alt 0 (class FF/5D/01)", "0x81 interrupt IN, 32 bytes", "Interface 1", "0x03 interrupt OUT, 64 bytes"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in\n%s", want, s)
		}
	}
}
