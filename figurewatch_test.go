package main

import (
	"sync"
	"testing"
	"time"
)

// fakePortal answers 'Q' reads from figures in slots, like a real portal.
type fakePortal struct {
	mu    sync.Mutex
	slots map[int]map[uint8][]byte
	w     *FigureWatcher
	sent  [][]byte
	fail  map[int]bool // slots whose reads always fail
}

func (f *fakePortal) send(cmd []byte) error {
	f.mu.Lock()
	f.sent = append(f.sent, append([]byte(nil), cmd...))
	fig := f.slots[int(cmd[1]&0x0F)]
	fails := f.fail[int(cmd[1]&0x0F)]
	f.mu.Unlock()
	if cmd[0] != 'Q' {
		return nil
	}
	reply := make([]byte, 32)
	reply[0], reply[1], reply[2] = 'Q', cmd[1]&0x0F, cmd[2]
	if fails {
		// leave the success bit clear
	} else if data, ok := fig[cmd[2]]; ok {
		reply[1] |= 0x10
		copy(reply[3:], data)
	} else if fig != nil {
		reply[1] |= 0x10 // blank block
	}
	go f.w.Handle(reply)
	return nil
}

func (f *fakePortal) status() {
	var bits uint32
	f.mu.Lock()
	for s := range f.slots {
		bits |= 1 << (2 * s)
	}
	f.mu.Unlock()
	f.w.Handle([]byte{'S', byte(bits), byte(bits >> 8), byte(bits >> 16), byte(bits >> 24), 0, 1})
}

func TestFigureWatcher(t *testing.T) {
	spyro := figureHeader(16, 0)
	fp := &fakePortal{slots: map[int]map[uint8][]byte{}}
	var mu sync.Mutex
	var placed, identified []FigureInfo
	seq := 0
	fp.w = NewFigureWatcher(fp.send, func() int { seq++; return seq },
		func(f FigureInfo) { mu.Lock(); placed = append(placed, f); mu.Unlock() },
		func(f FigureInfo) { mu.Lock(); identified = append(identified, f); mu.Unlock() })
	go fp.w.Run()
	defer fp.w.Stop()

	fp.mu.Lock()
	fp.slots[0] = map[uint8][]byte{0: spyro[:16], 1: spyro[16:]}
	fp.slots[2] = buildTrap(211, 9)
	fp.mu.Unlock()
	fp.status()

	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
			mu.Lock()
			done := ok()
			mu.Unlock()
			if done {
				return
			}
		}
		t.Fatalf("timed out waiting for %s: placed %+v identified %+v", what, placed, identified)
	}
	waitFor("two placements and reads", func() bool { return len(placed) == 2 && len(identified) == 2 })
	figs := fp.w.Figures()
	if len(figs) != 2 || figs[0].Name != "Spyro" || figs[0].Slot != 0 || !figs[1].IsTrap {
		t.Fatalf("figures: %+v", figs)
	}
	// the same status again changes nothing
	fp.status()
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if len(placed) != 2 {
		t.Fatalf("placed again: %+v", placed)
	}
	mu.Unlock()

	// a figure the portal can't read still counts as placed
	fp.mu.Lock()
	fp.slots[5] = map[uint8][]byte{}
	fp.fail = map[int]bool{5: true}
	fp.mu.Unlock()
	fp.status()
	waitFor("the unreadable figure", func() bool { return len(placed) == 3 && len(identified) == 3 })
	mu.Lock()
	if placed[2].Slot != 5 || !identified[2].Unknown {
		t.Fatalf("unreadable: %+v / %+v", placed[2], identified[2])
	}
	mu.Unlock()

	// a quick swap in slot 0: the portal reports it as "just added" (11) without a gap
	time.Sleep(850 * time.Millisecond)
	fp.w.Handle([]byte{'S', 0x13, 0x04, 0, 0, 0, 1}) // slot 0: 11, slot 2: 01, slot 5: 01
	waitFor("the swapped figure", func() bool { return len(placed) == 4 })

	// only reads, never writes
	fp.mu.Lock()
	for _, c := range fp.sent {
		if c[0] != 'Q' {
			t.Fatalf("sent a non-read command: % X", c)
		}
	}
	fp.mu.Unlock()
	// removing Spyro
	fp.mu.Lock()
	delete(fp.slots, 0)
	fp.mu.Unlock()
	fp.status()
	time.Sleep(80 * time.Millisecond)
	if figs := fp.w.Figures(); len(figs) != 2 || figs[0].Slot != 2 {
		t.Fatalf("after removal: %+v", figs)
	}
}

func TestSlotBits(t *testing.T) {
	p, a := slotBits(0b10_11_01) // slot 0 present, slot 1 added, slot 2 just removed
	if p != 0b011 || a != 0b010 {
		t.Fatalf("%b %b", p, a)
	}
}
