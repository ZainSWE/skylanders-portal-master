package main

import (
	"fmt"
	"strings"
)

// findReply looks for a reply the match function accepts, either at the start
// of a message or after the Xbox 360 frame header 0B 14.
func findReply(msgs [][]byte, match func(b []byte) bool) []byte {
	for _, m := range msgs {
		if match(m) || (len(m) > 2 && m[0] == 0x0B && m[1] == 0x14 && match(m[2:])) {
			return m
		}
	}
	return nil
}

// safePacket makes sure an audio packet can't start with the Xbox 360 command
// header by nudging the first byte (an inaudible change).
func safePacket(pkt []byte) {
	if len(pkt) >= 2 && pkt[0] == 0x0B && pkt[1] == 0x14 {
		pkt[0] = 0x0A
	}
}

func upsample2(s []int16) []int16 {
	out := make([]int16, 0, len(s)*2)
	for i, v := range s {
		next := v
		if i+1 < len(s) {
			next = s[i+1]
		}
		out = append(out, v, int16((int32(v)+int32(next))/2))
	}
	return out
}

func describeConfig(b []byte) string {
	var sb strings.Builder
	types := []string{"control", "isochronous", "bulk", "interrupt"}
	for i := 0; i+2 <= len(b); {
		l, t := int(b[i]), b[i+1]
		if l < 2 || i+l > len(b) {
			break
		}
		d := b[i : i+l]
		switch {
		case t == 4 && l >= 9:
			fmt.Fprintf(&sb, "Interface %d alt %d (class %02X/%02X/%02X), %d endpoints\n", d[2], d[3], d[5], d[6], d[7], d[4])
		case t == 5 && l >= 7:
			dir := "OUT"
			if d[2]&0x80 != 0 {
				dir = "IN"
			}
			fmt.Fprintf(&sb, "  endpoint 0x%02X %s %s, %d bytes, every %d\n", d[2], types[d[3]&3], dir, int(d[4])|int(d[5])<<8, d[6])
		case t == 2:
		default:
			fmt.Fprintf(&sb, "  descriptor type 0x%02X: % X\n", t, d)
		}
		i += l
	}
	return strings.TrimSpace(sb.String())
}
