//go:build windows

package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

var (
	procWUAssoc    = winusb.NewProc("WinUsb_GetAssociatedInterface")
	procWUQueryIfc = winusb.NewProc("WinUsb_QueryInterfaceSettings")
	procWUOvResult = winusb.NewProc("WinUsb_GetOverlappedResult")
	procWUAbort    = winusb.NewProc("WinUsb_AbortPipe")
	pCancelIoEx    = syscall.NewLazyDLL("kernel32.dll").NewProc("CancelIoEx")
	pCreateEventW  = syscall.NewLazyDLL("kernel32.dll").NewProc("CreateEventW")
)

// Route is one way of sending speaker audio to a portal.
type Route struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	iface int    // index into the portal's interface handles (0 = main)
	pipe  uint8  // OUT endpoint
	size  int    // bytes per USB packet
	rate  int    // 8000 or 16000
	swap  bool   // big-endian samples
	full  bool   // pad every write to a full 64-byte audio packet
	xbox  bool   // Xbox 360 speaker frames: 0B 17 + G.726
}

// Portal is an open connection to one portal.
type Portal interface {
	Send(cmd []byte) error                        // a command of up to 32 bytes, like 'C' r g b
	SendAudio(samples []int16, route Route) error // 8 kHz mono speaker audio
	Routes() []Route                              // ways this portal might accept audio
	Replies() [][]byte                            // recent messages from the portal (oldest first)
	Describe() string                             // USB layout, for troubleshooting
	HasSpeakerHint() bool                         // the USB layout says this is a Trap Team portal
	SetHandler(func(msg []byte))                  // called with each message from the portal (header removed)
	Close()
}

// msgHandler delivers portal messages to whoever is listening (the figure watcher).
type msgHandler struct {
	mu sync.Mutex
	fn func([]byte)
}

func (h *msgHandler) SetHandler(fn func([]byte)) {
	h.mu.Lock()
	h.fn = fn
	h.mu.Unlock()
}

func (h *msgHandler) deliver(msg []byte) {
	h.mu.Lock()
	fn := h.fn
	h.mu.Unlock()
	if fn != nil {
		if len(msg) > 2 && msg[0] == 0x0B && msg[1] == 0x14 {
			msg = msg[2:]
		}
		fn(append([]byte(nil), msg...))
	}
}

// Speaker audio on the main channel always goes out as full 64-byte packets:
// the portal tells sound apart from commands (32 bytes or less) by size, so
// audio can't be mistaken for a command such as writing to a figure.
const audioPacketBytes = 64

// replyLog keeps the last few messages a portal sent us.
type replyLog struct {
	mu   sync.Mutex
	msgs [][]byte
}

func (r *replyLog) add(b []byte) {
	if len(b) == 0 {
		return
	}
	c := append([]byte(nil), b...)
	r.mu.Lock()
	r.msgs = append(r.msgs, c)
	if len(r.msgs) > 48 {
		r.msgs = r.msgs[len(r.msgs)-48:]
	}
	r.mu.Unlock()
}

func (r *replyLog) all() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte(nil), r.msgs...)
}

// ---------------------------------------------------------------------------
// Portals using the WinUSB driver (Xbox 360 portals after the Zadig fix)

type ifaceInfo struct {
	handle  uintptr
	number  int
	class   [3]byte
	outs    []pipeInfo
	ins     []pipeInfo
	isoOuts int
}

type winusbPortal struct {
	file    syscall.Handle
	ifaces  []ifaceInfo // [0] is the main interface
	out, in uint8
	size    int
	header  bool
	mu      sync.Mutex
	closed  atomic.Bool
	drained chan struct{}
	info    string
	replies replyLog
	pid     string
	speaker XboxSpeaker
	msgHandler

	// Writes go through a queue to one writer that keeps several USB transfers
	// in flight, so the portal's channel never sits idle (the speaker needs
	// over half of its capacity).
	qmu   sync.Mutex
	queue chan usbWrite
	werr  atomic.Value // first write error (the portal was unplugged, etc.)
	wdone chan struct{}
}

type usbWrite struct {
	iface int
	pipe  uint8
	data  []byte
}

const writesInFlight = 8

// HasSpeakerHint: the Xbox 360 Trap Team portal has a 64-byte command channel;
// the older Xbox 360 portals have a 32-byte one.
func (p *winusbPortal) HasSpeakerHint() bool { return p.pid == "1f17" && p.size >= 64 }

var errPortalClosed = errors.New("portal closed")

// write queues a packet for the portal. It only blocks if the queue is full.
func (p *winusbPortal) write(iface int, pipe uint8, pkt []byte) error {
	if e, _ := p.werr.Load().(error); e != nil {
		return e
	}
	p.qmu.Lock()
	defer p.qmu.Unlock()
	if p.closed.Load() {
		return errPortalClosed
	}
	select {
	case p.queue <- usbWrite{iface, pipe, pkt}:
		return nil
	case <-time.After(time.Second):
		return errors.New("the portal stopped accepting data")
	}
}

type writeSlot struct {
	ov    syscall.Overlapped
	iface int
	data  []byte // kept referenced until the transfer finishes
	busy  bool
}

func (p *winusbPortal) writer() {
	defer close(p.wdone)
	slots := make([]*writeSlot, writesInFlight)
	for i := range slots {
		ev, _, _ := pCreateEventW.Call(0, 1, 0, 0) // manual-reset event for this transfer slot
		slots[i] = &writeSlot{ov: syscall.Overlapped{HEvent: syscall.Handle(ev)}}
	}
	finish := func(s *writeSlot) {
		var n uint32
		ok, _, err := procWUOvResult.Call(p.ifaces[s.iface].handle, uintptr(unsafe.Pointer(&s.ov)), uintptr(unsafe.Pointer(&n)), 1)
		s.busy, s.data = false, nil
		if ok == 0 && p.werr.Load() == nil && !p.closed.Load() {
			p.werr.Store(err)
		}
	}
	next := 0
	for w := range p.queue {
		s := slots[next]
		next = (next + 1) % len(slots)
		if s.busy {
			finish(s) // wait for the oldest transfer to make room
		}
		if p.werr.Load() != nil {
			continue // portal gone: just empty the queue
		}
		ev := s.ov.HEvent
		s.ov = syscall.Overlapped{HEvent: ev}
		s.iface, s.data = w.iface, w.data
		ok, _, err := procWUWrite.Call(p.ifaces[w.iface].handle, uintptr(w.pipe), uintptr(unsafe.Pointer(&s.data[0])),
			uintptr(len(s.data)), 0, uintptr(unsafe.Pointer(&s.ov)))
		if ok == 0 && err != syscall.ERROR_IO_PENDING {
			s.data = nil
			p.werr.Store(err)
			continue
		}
		s.busy = true
	}
	for _, s := range slots {
		if s.busy {
			finish(s)
		}
		syscall.CloseHandle(s.ov.HEvent)
	}
}

func (p *winusbPortal) Send(cmd []byte) error {
	var pkt []byte
	if p.header {
		pkt = make([]byte, 32) // Xbox 360 frames are 32 bytes: 0B 14 + command
		copy(pkt, append([]byte{0x0B, 0x14}, cmd...))
	} else {
		pkt = make([]byte, p.size)
		copy(pkt, cmd)
	}
	if len(cmd) > 0 && cmd[0] == 'M' {
		p.mu.Lock()
		p.speaker.Reset() // the portal starts a fresh audio stream when the speaker is switched
		p.mu.Unlock()
	}
	return p.write(0, p.out, pkt)
}

func (p *winusbPortal) SendAudio(samples []int16, r Route) error {
	if r.xbox {
		var frames [][]byte
		p.mu.Lock()
		p.speaker.Add(samples, func(f []byte) { frames = append(frames, f) })
		p.mu.Unlock()
		for _, f := range frames {
			if err := p.write(0, p.out, f); err != nil {
				return err
			}
		}
		return nil
	}
	if r.rate == 16000 {
		samples = upsample2(samples)
	}
	data := EncodeSamples(samples, r.swap)
	chunk := r.size
	if r.full {
		chunk = audioPacketBytes
	}
	for len(data) > 0 {
		n := min(chunk, len(data))
		pkt := make([]byte, chunk)
		copy(pkt, data[:n])
		if r.iface == 0 {
			for i := 0; i < len(pkt); i += max(p.size, 8) { // if the endpoint splits it, guard each piece
				safePacket(pkt[i:])
			}
		}
		if err := p.write(r.iface, r.pipe, pkt); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

func (p *winusbPortal) Routes() []Route {
	var routes []Route
	if p.header {
		routes = append(routes, Route{ID: "xbox-g726", Label: "Xbox 360 speaker format", iface: 0, pipe: p.out, size: 32, rate: 8000, xbox: true})
	}
	return append(routes,
		Route{ID: "main-le", Label: "Plain sound", iface: 0, pipe: p.out, size: p.size, rate: 8000, full: true},
		Route{ID: "main-be", Label: "Plain sound, swapped bytes", iface: 0, pipe: p.out, size: p.size, rate: 8000, swap: true, full: true})
}

func (p *winusbPortal) Replies() [][]byte { return p.replies.all() }

// drain keeps reading the portal's replies so its outgoing queue never fills up.
func (p *winusbPortal) drain() {
	defer close(p.drained)
	if p.in == 0 {
		return
	}
	buf := make([]byte, 64)
	for !p.closed.Load() {
		var n uint32
		if ok, _, _ := procWURead.Call(p.ifaces[0].handle, uintptr(p.in), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&n)), 0); ok != 0 && n > 0 {
			p.replies.add(buf[:n])
			p.deliver(buf[:n])
		}
	}
}

func (p *winusbPortal) Describe() string { return p.info }

func (p *winusbPortal) Close() {
	// Let queued writes (like "lights off") go out first, briefly.
	deadline := time.Now().Add(300 * time.Millisecond)
	for len(p.queue) > 0 && time.Now().Before(deadline) && p.werr.Load() == nil {
		time.Sleep(5 * time.Millisecond)
	}
	p.qmu.Lock()
	if p.closed.Swap(true) {
		p.qmu.Unlock()
		return
	}
	close(p.queue)
	p.qmu.Unlock()
	if p.werr.Load() != nil {
		procWUAbort.Call(p.ifaces[0].handle, uintptr(p.out))
	}
	select {
	case <-p.wdone:
	case <-time.After(2 * time.Second):
		procWUAbort.Call(p.ifaces[0].handle, uintptr(p.out))
		<-p.wdone
	}
	<-p.drained
	p.mu.Lock()
	for i := len(p.ifaces) - 1; i >= 0; i-- {
		procWUFree.Call(p.ifaces[i].handle)
	}
	syscall.CloseHandle(p.file)
	p.mu.Unlock()
}

func readIface(h uintptr) ifaceInfo {
	f := ifaceInfo{handle: h}
	var desc [9]byte
	if ok, _, _ := procWUQueryIfc.Call(h, 0, uintptr(unsafe.Pointer(&desc[0]))); ok != 0 {
		f.number = int(desc[2])
		f.class = [3]byte{desc[5], desc[6], desc[7]}
	}
	for i := uintptr(0); i < 16; i++ {
		var pi pipeInfo
		if ok, _, _ := procWUQueryPipe.Call(h, 0, i, uintptr(unsafe.Pointer(&pi))); ok == 0 {
			break
		}
		switch {
		case pi.PipeType == 1: // isochronous: WinUsb_WritePipe can't use these
			if pi.PipeID&0x80 == 0 {
				f.isoOuts++
			}
		case pi.PipeID&0x80 != 0:
			f.ins = append(f.ins, pi)
		default:
			f.outs = append(f.outs, pi)
		}
	}
	return f
}

func openWinUSB(path, pid string, header bool) (Portal, error) {
	if err := procWUInit.Find(); err != nil {
		return nil, errors.New("winusb.dll is missing")
	}
	pp, _ := syscall.UTF16PtrFromString(path)
	f, err := syscall.CreateFile(pp, syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, err
	}
	var u uintptr
	if ok, _, e := procWUInit.Call(uintptr(f), uintptr(unsafe.Pointer(&u))); ok == 0 {
		syscall.CloseHandle(f)
		return nil, e
	}
	p := &winusbPortal{file: f, size: 32, header: header, drained: make(chan struct{}), pid: pid,
		queue: make(chan usbWrite, 64), wdone: make(chan struct{})}
	p.ifaces = append(p.ifaces, readIface(u))
	for i := uintptr(0); i < 8; i++ {
		var h uintptr
		if ok, _, _ := procWUAssoc.Call(u, i, uintptr(unsafe.Pointer(&h))); ok == 0 {
			break
		}
		p.ifaces = append(p.ifaces, readIface(h))
	}
	main := p.ifaces[0]
	if len(main.outs) > 0 {
		p.out = main.outs[0].PipeID
		if main.outs[0].MaxPacket > 0 {
			p.size = int(main.outs[0].MaxPacket)
		}
	}
	if len(main.ins) > 0 {
		p.in = main.ins[0].PipeID
	}

	// Record the portal's USB layout for the troubleshooting panel and log.
	var sb strings.Builder
	cfg := make([]byte, 1024)
	var n uint32
	if ok, _, _ := procWUGetDesc.Call(u, 0x02, 0, 0, uintptr(unsafe.Pointer(&cfg[0])), uintptr(len(cfg)), uintptr(unsafe.Pointer(&n))); ok != 0 && n > 0 {
		sb.WriteString(describeConfig(cfg[:n]))
	}
	fmt.Fprintf(&sb, "\nWinUSB opened %d interface(s):", len(p.ifaces))
	for _, f := range p.ifaces {
		fmt.Fprintf(&sb, " #%d (%02X/%02X/%02X, %d out, %d in, %d isochronous out)", f.number, f.class[0], f.class[1], f.class[2], len(f.outs), len(f.ins), f.isoOuts)
	}
	p.info = strings.TrimSpace(sb.String())

	if p.out == 0 {
		close(p.drained)
		p.closed.Store(true)
		for i := len(p.ifaces) - 1; i >= 0; i-- {
			procWUFree.Call(p.ifaces[i].handle)
		}
		syscall.CloseHandle(f)
		return nil, errors.New("the portal has no output endpoint")
	}
	writeTO, readTO := uint32(500), uint32(50)
	for _, f := range p.ifaces {
		for _, o := range f.outs {
			procWUSetPolicy.Call(f.handle, uintptr(o.PipeID), pipeTimeout, 4, uintptr(unsafe.Pointer(&writeTO)))
		}
	}
	if p.in != 0 {
		procWUSetPolicy.Call(u, uintptr(p.in), pipeTimeout, 4, uintptr(unsafe.Pointer(&readTO)))
	}
	go p.drain()
	go p.writer()
	return p, nil
}

// ---------------------------------------------------------------------------
// PS3 / Wii / Wii U / PC portals (Windows' built-in HID driver)

type hidPortal struct {
	file    syscall.Handle
	size    int // output report length including the report ID byte
	inLen   int
	mu      sync.Mutex
	info    string
	closed  atomic.Bool
	drained chan struct{}
	replies replyLog
	msgHandler
}

func (p *hidPortal) Send(cmd []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	pkt := make([]byte, p.size) // pkt[0] = report ID 0
	copy(pkt[1:], cmd)
	ok, _, err := procHidSetOutput.Call(uintptr(p.file), uintptr(unsafe.Pointer(&pkt[0])), uintptr(len(pkt)))
	if ok == 0 {
		return err
	}
	return nil
}

var errHIDSpeaker = errors.New("this portal's current driver can't carry sound. Switch it to WinUSB with Zadig (see Troubleshooting) to use the speaker")

func (p *hidPortal) SendAudio(samples []int16, r Route) error {
	if p.size-1 < audioPacketBytes {
		return errHIDSpeaker // smaller reports would look like commands to the portal
	}
	data := EncodeSamples(samples, r.swap)
	p.mu.Lock()
	defer p.mu.Unlock()
	for len(data) > 0 {
		n := min(audioPacketBytes, len(data))
		pkt := make([]byte, p.size)
		copy(pkt[1:], data[:n])
		var done uint32
		if err := syscall.WriteFile(p.file, pkt, &done, nil); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

func (p *hidPortal) Routes() []Route {
	return []Route{
		{ID: "main-le", Label: "Plain sound", rate: 8000, full: true},
		{ID: "main-be", Label: "Plain sound, swapped bytes", rate: 8000, swap: true, full: true},
	}
}

func (p *hidPortal) Replies() [][]byte    { return p.replies.all() }
func (p *hidPortal) HasSpeakerHint() bool { return false }
func (p *hidPortal) Describe() string     { return p.info }

func (p *hidPortal) drain() {
	defer close(p.drained)
	if p.inLen < 2 {
		return
	}
	buf := make([]byte, p.inLen)
	for !p.closed.Load() {
		var n uint32
		if err := syscall.ReadFile(p.file, buf, &n, nil); err != nil {
			return
		}
		if n > 1 {
			p.replies.add(buf[1:n]) // skip the report ID
			p.deliver(buf[1:n])
		}
	}
}

func (p *hidPortal) Close() {
	if p.closed.Swap(true) {
		return
	}
	pCancelIoEx.Call(uintptr(p.file), 0)
	syscall.CloseHandle(p.file)
}

func openHID(path string) (Portal, error) {
	pp, _ := syscall.UTF16PtrFromString(path)
	f, err := syscall.CreateFile(pp, syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, err
	}
	size, inLen := 33, 33
	var pre uintptr
	if ok, _, _ := procHidGetPreparse.Call(uintptr(f), uintptr(unsafe.Pointer(&pre))); ok != 0 {
		var caps [64]uint16
		if st, _, _ := procHidGetCaps.Call(pre, uintptr(unsafe.Pointer(&caps[0]))); st == 0x00110000 && caps[3] > 1 {
			size, inLen = int(caps[3]), int(caps[2])
		}
		procHidFreePrepare.Call(pre)
	}
	p := &hidPortal{file: f, size: size, inLen: inLen, drained: make(chan struct{}),
		info: fmt.Sprintf("HID portal: output reports %d bytes, input reports %d bytes", size, inLen)}
	go p.drain()
	return p, nil
}

// ---------------------------------------------------------------------------

// Candidate is one portal we could open.
type Candidate struct {
	Key  string // stable per plugged-in portal
	Dev  Detected
	path string
	hid  bool
}

// candidates lists every portal that has a usable driver.
func candidates(devs []Detected) []Candidate {
	var out []Candidate
	var usbPaths []string
	gotUSBPaths := false
	for _, d := range devs {
		if !d.IsWinUSB() {
			continue
		}
		if !gotUSBPaths {
			usbPaths = interfacePaths(&guidUSBDevice)
			gotUSBPaths = true
		}
		paths := append([]string(nil), usbPaths...)
		for _, g := range d.guids {
			g := g
			paths = append(paths, interfacePaths(&g)...)
		}
		for _, p := range paths {
			if strings.Contains(strings.ToLower(p), d.pathKey()) {
				out = append(out, Candidate{Key: strings.ToLower(nonEmpty(d.Instance, p)), Dev: d, path: p})
				break
			}
		}
	}
	// HID portals: one per HID interface path.
	var hidDev *Detected
	for i := range devs {
		if devs[i].IsHID() {
			hidDev = &devs[i]
			break
		}
	}
	if hidDev != nil {
		for _, p := range interfacePaths(&guidHID) {
			lp := strings.ToLower(p)
			if strings.Contains(lp, "vid_1430&") {
				d := *hidDev
				for _, x := range devs {
					if x.IsHID() && strings.Contains(lp, x.key) {
						d = x
					}
				}
				out = append(out, Candidate{Key: lp, Dev: d, path: p, hid: true})
			}
		}
	}
	return out
}

func (c Candidate) Open(xboxHeader bool) (Portal, error) {
	if c.hid {
		return openHID(c.path)
	}
	return openWinUSB(c.path, c.Dev.PID, xboxHeader && c.Dev.PID != "0150")
}
