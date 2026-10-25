package main

import (
	"errors"
	"sort"
	"sync"
	"time"
)

// FigureWatcher follows one portal's status messages and notices every figure placed on it,
// then reads which figure it is. It only ever sends the read ('Q') command, so it can't
// change a figure.
//
// A placement is reported straight from the status bits, before the figure is read, so a
// figure the portal struggles to read still counts. Each status report also says which slots
// just had a figure added; those flags are collected between reports, so a figure that is
// swapped quickly for another is still noticed.
type FigureWatcher struct {
	send         func([]byte) error
	onPlaced     func(FigureInfo) // a figure was put down (not read yet)
	onIdentified func(FigureInfo) // ...and this is what it turned out to be
	nextSeq      func() int       // numbers placements across all portals
	wake         chan struct{}
	replies      chan []byte // 'Q' replies
	stop         chan struct{}
	stopOnce     sync.Once
	readMu       sync.Mutex // one read at a time: the replies are shared

	mu      sync.Mutex
	present uint32 // latest: one bit per slot
	added   uint32 // slots reported as just added since Run last looked
	seen    bool   // a status report has arrived
	figs    map[int]FigureInfo
	at      map[int]time.Time // when each figure was noticed
}

func NewFigureWatcher(send func([]byte) error, nextSeq func() int, onPlaced, onIdentified func(FigureInfo)) *FigureWatcher {
	return &FigureWatcher{send: send, nextSeq: nextSeq, onPlaced: onPlaced, onIdentified: onIdentified,
		wake: make(chan struct{}, 1), replies: make(chan []byte, 16), stop: make(chan struct{}), figs: map[int]FigureInfo{}, at: map[int]time.Time{}}
}

// slotBits splits a status report's slot field (two bits per slot: 01 present, 11 just
// added, 10 just removed) into the slots holding a figure and those just added.
func slotBits(bits uint32) (present, added uint32) {
	for slot := 0; slot < 16; slot++ {
		switch (bits >> (2 * slot)) & 3 {
		case 1:
			present |= 1 << slot
		case 3:
			present |= 1 << slot
			added |= 1 << slot
		}
	}
	return
}

// Handle takes one message from the portal (without any Xbox header).
func (w *FigureWatcher) Handle(msg []byte) {
	if len(msg) == 0 {
		return
	}
	switch msg[0] {
	case 'S':
		if len(msg) < 5 {
			return
		}
		present, added := slotBits(uint32(msg[1]) | uint32(msg[2])<<8 | uint32(msg[3])<<16 | uint32(msg[4])<<24)
		w.mu.Lock()
		changed := !w.seen || present != w.present || added != 0
		w.present, w.seen = present, true
		w.added |= added
		w.mu.Unlock()
		if changed {
			select {
			case w.wake <- struct{}{}:
			default:
			}
		}
	case 'Q':
		select {
		case w.replies <- msg:
		default:
		}
	}
}

func (w *FigureWatcher) Stop() { w.stopOnce.Do(func() { close(w.stop) }) }

// Figures lists what's on the portal, by slot.
func (w *FigureWatcher) Figures() []FigureInfo {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]FigureInfo, 0, len(w.figs))
	for _, f := range w.figs {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slot < out[j].Slot })
	return out
}

var errReadFailed = errors.New("couldn't read the figure")

// query reads one block, trying several times: a figure that's just been put down, or sits
// near the edge, often fails the first read.
func (w *FigureWatcher) query(slot int, block uint8) ([]byte, error) {
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			select {
			case <-w.stop:
				return nil, errReadFailed
			case <-time.After(time.Duration(40*attempt) * time.Millisecond):
			}
		}
		for len(w.replies) > 0 { // drop stale replies
			<-w.replies
		}
		if err := w.send([]byte{'Q', 0x10 | byte(slot), block}); err != nil {
			return nil, err
		}
		deadline := time.After(350 * time.Millisecond)
	wait:
		for {
			select {
			case <-w.stop:
				return nil, errReadFailed
			case <-deadline:
				break wait
			case r := <-w.replies:
				if len(r) < 19 || r[2] != block || int(r[1]&0x0F) != slot {
					continue
				}
				if r[1]&0x10 == 0 {
					break wait // the portal couldn't read it this time
				}
				return append([]byte(nil), r[3:19]...), nil
			}
		}
	}
	return nil, errReadFailed
}

// read works out which figure is in a slot and reports it, if it's still the same placement.
func (w *FigureWatcher) read(slot, seq int) {
	w.readMu.Lock()
	defer w.readMu.Unlock()
	still := func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		f, ok := w.figs[slot]
		return ok && f.Arrival == seq
	}
	if !still() {
		return
	}
	b0, err := w.query(slot, 0)
	var info FigureInfo
	if err == nil {
		var b1 []byte
		if b1, err = w.query(slot, 1); err == nil {
			info = Identify(append(b0, b1...))
		}
	}
	if err != nil {
		info = FigureInfo{Name: "Unknown figure", Unknown: true}
	}
	info.Slot, info.Arrival = slot, seq
	w.mu.Lock()
	if f, ok := w.figs[slot]; !ok || f.Arrival != seq {
		w.mu.Unlock()
		return // taken off again meanwhile
	}
	w.figs[slot] = info
	w.mu.Unlock()
	w.onIdentified(info)
}

// Run watches the portal until Stop.
func (w *FigureWatcher) Run() {
	for {
		select {
		case <-w.stop:
			return
		case <-w.wake:
		}
		// Work out which slots changed, then number the new figures without holding w.mu:
		// nextSeq takes the engine's lock, and the engine reads Figures() while holding it.
		w.mu.Lock()
		present, added := w.present, w.added
		w.added = 0
		var newSlots []int
		for slot := 0; slot < 16; slot++ {
			bit := uint32(1) << slot
			_, known := w.figs[slot]
			switch {
			// "just added" on a slot we already know means it was swapped for another figure;
			// ignored for a moment after a placement in case the portal repeats the flag.
			case present&bit != 0 && (!known || (added&bit != 0 && time.Since(w.at[slot]) > 800*time.Millisecond)):
				newSlots = append(newSlots, slot)
				w.at[slot] = time.Now()
			case present&bit == 0 && known:
				delete(w.figs, slot)
				delete(w.at, slot)
			}
		}
		w.mu.Unlock()
		var placed []FigureInfo
		for _, slot := range newSlots {
			f := FigureInfo{Slot: slot, Arrival: w.nextSeq(), Name: "Figure", Unknown: true, Reading: true}
			w.mu.Lock()
			if w.present&(1<<slot) != 0 { // still there
				w.figs[slot] = f
				placed = append(placed, f)
			}
			w.mu.Unlock()
		}
		for _, f := range placed {
			w.onPlaced(f)
			go w.read(f.Slot, f.Arrival)
		}
	}
}
