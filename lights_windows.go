//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	stSearching = iota
	stNoPortal
	stNeedsDriver
	stCantOpen
	stConnected
)

var stateNames = map[int]string{stSearching: "searching", stNoPortal: "noportal", stNeedsDriver: "needsdriver",
	stCantOpen: "cantopen", stConnected: "connected"}

type Status struct {
	State        int
	Text         string
	Device       string
	Devices      []Detected
	DriverNeeded []string // names of portals that still need the Zadig fix
}

// conn is one connected portal.
type conn struct {
	key         string
	dev         Detected
	p           Portal
	typ         string // "classic" or "trapteam"
	detected    string
	info        string
	routes      []Route
	last        Frame
	frame       Frame // what this portal shows right now
	lastSent    [4]time.Time
	fresh       bool
	speaker     bool
	watcher     *FigureWatcher
	flashAt     time.Time // when a figure was last placed (for the colour burst)
	flashCol    RGB
	flashSeq    int
	lastTrap    RGB // what the trap light was last sent
	connectedAt time.Time
}

func (c *conn) name() string { return nonEmpty(c.dev.Name, "Portal") }

// Engine owns the portal connections and drives the lights on all of them.
type Engine struct {
	OnFigure  func(f FigureInfo, portalType string, alreadyOn bool) // a figure was placed; alreadyOn: it was on the portal when it connected
	OnConnect func(portalType string)                               // called when a portal is connected
	mu        sync.Mutex
	cfg       Settings
	fresh     bool
	status    Status
	frame     Frame
	level     [2]float64
	profiles  map[string]Profile // each portal's type and lights, by portal key
	onSave    func(map[string]Profile)
	stop      chan struct{}
	done      chan struct{}

	pmu   sync.Mutex // guards conns
	conns map[string]*conn

	arrivalSeq int
	arrival    APIArrival
}

func (e *Engine) nextSeq() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.arrivalSeq++
	return e.arrivalSeq
}

// Arrival is the most recently placed figure.
func (e *Engine) Arrival() APIArrival {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.arrival
}

// placed is called as soon as a figure is put on a portal, before it has been read.
func (e *Engine) placed(c *conn, f FigureInfo) {
	e.pmu.Lock()
	typ := c.typ
	e.pmu.Unlock()
	e.mu.Lock()
	if e.arrival.Seq <= f.Arrival {
		e.arrival = APIArrival{Seq: f.Arrival, Portal: c.key, PortalType: typ, Figure: f}
	}
	e.mu.Unlock()
	log.Printf("figure placed on %s (slot %d)", c.name(), f.Slot)
	if e.OnFigure != nil {
		go e.OnFigure(f, typ, time.Since(c.connectedAt) < 2*time.Second)
	}
}

// identified is called once a placed figure has been read: the portal glows in its element colour.
func (e *Engine) identified(c *conn, f FigureInfo) {
	log.Printf("figure on %s: %s (%s %s, id %d/%d)", c.name(), f.Name, f.Element, f.Kind, f.ID, f.Variant)
	e.mu.Lock()
	flash := e.cfg.FigureFlash
	e.mu.Unlock()
	e.pmu.Lock()
	defer e.pmu.Unlock()
	if flash && c.flashSeq != f.Arrival {
		col, ok := elementColours[f.Element]
		if !ok {
			col = RGB{255, 255, 255} // couldn't tell: a white glow
		}
		c.flashAt, c.flashCol, c.flashSeq, c.fresh = time.Now(), col, f.Arrival, true
	}
}

var errNoSpeaker = errors.New("no Trap Team portal is connected")

func NewEngine(cfg Settings, profiles map[string]Profile, onSave func(map[string]Profile)) *Engine {
	if profiles == nil {
		profiles = map[string]Profile{}
	}
	return &Engine{cfg: cfg.Normalize(), fresh: true, stop: make(chan struct{}), done: make(chan struct{}),
		profiles: profiles, onSave: onSave, conns: map[string]*conn{},
		status: Status{State: stSearching, Text: "Looking for your portals..."}}
}

func (e *Engine) Settings() Settings {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg
}

func (e *Engine) SetSettings(s Settings) {
	e.mu.Lock()
	e.cfg, e.fresh = s.Normalize(), true
	e.mu.Unlock()
}

func (e *Engine) SetLevel(l [2]float64) {
	e.mu.Lock()
	e.level = l
	e.mu.Unlock()
}

func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status
}

func (e *Engine) Frame() Frame {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.frame
}

// ConnectedTypes lists the type of each connected portal.
func (e *Engine) ConnectedTypes() []string {
	var out []string
	for _, c := range e.list() {
		e.pmu.Lock()
		out = append(out, c.typ)
		e.pmu.Unlock()
	}
	return out
}

func (e *Engine) list() []*conn {
	e.pmu.Lock()
	defer e.pmu.Unlock()
	out := make([]*conn, 0, len(e.conns))
	for _, c := range e.conns {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

func (e *Engine) Portals() []APIPortal {
	var out []APIPortal
	for _, c := range e.list() {
		e.pmu.Lock()
		typ, frame := c.typ, c.frame
		e.pmu.Unlock()
		lights := e.lightsFor(c.key)
		var figs []FigureInfo
		if c.watcher != nil {
			figs = c.watcher.Figures()
		}
		out = append(out, APIPortal{Key: c.key, Name: c.name(), USBID: strings.ToUpper(c.dev.VID + ":" + c.dev.PID),
			Type: typ, Detected: c.detected, Info: c.info, Lights: stateSettings(lights), Frame: apiFrame(frame), Figures: figs})
	}
	return out
}

// lightsFor returns a portal's own light settings (the app defaults if it has none yet).
func (e *Engine) lightsFor(key string) Settings {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p, ok := e.profiles[key]; ok {
		return withLights(e.cfg, p.Lights)
	}
	return e.cfg
}

// profile returns (creating if needed) a portal's profile. Call with e.mu held.
func (e *Engine) profile(key string) Profile {
	p, ok := e.profiles[key]
	if !ok {
		p = Profile{Lights: e.cfg}
	}
	return p
}

func (e *Engine) saveProfiles() {
	e.mu.Lock()
	m := make(map[string]Profile, len(e.profiles))
	for k, v := range e.profiles {
		m[k] = v
	}
	e.mu.Unlock()
	if e.onSave != nil {
		e.onSave(m)
	}
}

// SetPortalType records what kind of portal this is (the user knows best).
// knownPortal reports whether key is a connected portal or one with saved settings, so
// requests can't fill the settings file with made-up portals.
func (e *Engine) knownPortal(key string) bool {
	e.pmu.Lock()
	_, connected := e.conns[key]
	e.pmu.Unlock()
	e.mu.Lock()
	_, saved := e.profiles[key]
	e.mu.Unlock()
	return connected || saved
}

func (e *Engine) SetPortalType(key, typ string) {
	if !e.knownPortal(key) {
		return
	}
	e.mu.Lock()
	p := e.profile(key)
	p.Type = typ
	e.profiles[key] = p
	e.mu.Unlock()
	e.pmu.Lock()
	if c := e.conns[key]; c != nil && c.typ != typ {
		darken(c)
		c.typ, c.fresh, c.speaker = typ, true, false
	}
	e.pmu.Unlock()
	e.saveProfiles()
}

// SetPortalLights changes one portal's lights without touching the others.
func (e *Engine) SetPortalLights(key string, patch map[string]json.RawMessage) {
	if !e.knownPortal(key) {
		return
	}
	e.mu.Lock()
	p := e.profile(key)
	p.Lights = ApplyPatch(withLights(e.cfg, p.Lights), patch)
	e.profiles[key] = p
	e.cfg = withLights(e.cfg, p.Lights) // new portals start from the most recent look
	e.mu.Unlock()
	e.pmu.Lock()
	if c := e.conns[key]; c != nil {
		c.fresh = true
	}
	e.pmu.Unlock()
	e.saveProfiles()
}

func (e *Engine) speakers() []*conn {
	var out []*conn
	for _, c := range e.list() {
		e.pmu.Lock()
		trap := c.typ == "trapteam"
		e.pmu.Unlock()
		if trap {
			out = append(out, c)
		}
	}
	return out
}

func (e *Engine) HasSpeaker() bool { return len(e.speakers()) > 0 }

// Routes lists the ways audio might reach the (first) Trap Team speaker.
func (e *Engine) Routes() []APIRoute {
	var out []APIRoute
	sp := e.speakers()
	if len(sp) == 0 {
		return nil
	}
	for _, r := range sp[0].routes {
		out = append(out, APIRoute{ID: r.ID, Label: r.Label})
	}
	return out
}

// SetSpeaker turns the speaker on every Trap Team portal on or off.
func (e *Engine) SetSpeaker(on bool) {
	for _, c := range e.speakers() {
		e.pmu.Lock()
		need := c.speaker != on
		e.pmu.Unlock()
		if !need {
			continue
		}
		v := byte(0)
		if on {
			v = 1
		}
		if err := c.p.Send([]byte{'M', v}); err == nil {
			e.pmu.Lock()
			c.speaker = on
			e.pmu.Unlock()
		}
	}
}

// SendAudio sends speaker samples to every Trap Team portal.
func (e *Engine) SendAudio(samples []int16) error {
	routeID := e.Settings().AudioRoute
	sp := e.speakers()
	if len(sp) == 0 {
		return errNoSpeaker
	}
	var firstErr error
	sent := 0
	for _, c := range sp {
		r := c.routes[0] // "auto": the first route is the best guess for this portal
		for _, x := range c.routes {
			if x.ID == routeID {
				r = x
			}
		}
		if err := c.p.SendAudio(samples, r); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		} else {
			sent++
		}
	}
	if sent == 0 {
		return firstErr
	}
	return nil
}

func (e *Engine) setStatus(s Status) {
	e.mu.Lock()
	changed := e.status.Text != s.Text
	e.status = s
	e.mu.Unlock()
	if changed {
		log.Println("status:", s.Text)
	}
}

// Stop turns the lights off and waits for the loop to finish.
func (e *Engine) Stop() {
	select {
	case <-e.stop:
	default:
		close(e.stop)
	}
	select {
	case <-e.done:
	case <-time.After(3 * time.Second):
	}
}

func nonEmpty(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}

const (
	sideRight = 0x00
	sideTrap  = 0x01
	sideLeft  = 0x02
	fadeMS    = 75 // the Trap Team portal fades between updates by itself
	// Trap Team portals share their USB channel with the speaker, so their lights are updated
	// at most this often. The fade is a little longer than the gap, so each fade runs into the
	// next and the light never sits still between steps.
	trapLightGap = 50 * time.Millisecond
)

func darken(c *conn) {
	c.p.Send([]byte{'C', 0, 0, 0})
	if c.typ == "trapteam" {
		c.p.Send([]byte{'J', sideRight, 0, 0, 0, 0, 0})
		c.p.Send([]byte{'J', sideLeft, 0, 0, 0, 0, 0})
		c.p.Send([]byte{'L', sideTrap, 0, 0, 0})
	}
}

// greet wakes a newly opened portal and works out whether it's a Trap Team portal.
func greet(p Portal) (detected string, notes string) {
	p.Send([]byte{'R'})
	time.Sleep(120 * time.Millisecond)
	p.Send([]byte{'A', 0x01})
	time.Sleep(60 * time.Millisecond)
	p.Send([]byte{'M', 0x00}) // only Trap Team portals (which have a speaker) answer this
	deadline := time.Now().Add(700 * time.Millisecond)
	var mReply []byte
	for time.Now().Before(deadline) && mReply == nil {
		time.Sleep(50 * time.Millisecond)
		mReply = findReply(p.Replies(), func(b []byte) bool { return len(b) >= 2 && b[0] == 'M' && b[1] == 0x00 })
	}
	msgs := p.Replies()
	var sb strings.Builder
	if r := findReply(msgs, func(b []byte) bool { return len(b) >= 3 && b[0] == 'R' && b[1] < 0x10 }); r != nil {
		fmt.Fprintf(&sb, "Reply to R: % X\n", r)
	}
	if mReply != nil || p.HasSpeakerHint() {
		detected = "trapteam"
		if mReply != nil {
			fmt.Fprintf(&sb, "Reply to M: % X (has a speaker)\n", mReply)
		} else {
			sb.WriteString("64-byte channel: Trap Team portal\n")
		}
	} else {
		sb.WriteString("No reply to M (no speaker found)\n")
	}
	seen := map[string]bool{}
	n := 0
	for _, m := range msgs {
		k := fmt.Sprintf("% X", m[:min(len(m), 6)])
		if seen[k] || n >= 4 {
			continue
		}
		seen[k] = true
		n++
		fmt.Fprintf(&sb, "Message: % X\n", m[:min(len(m), 32)])
	}
	return detected, strings.TrimSpace(sb.String())
}

func (e *Engine) Run() {
	defer close(e.done)
	var lastScan time.Time
	begin := time.Now()
	header := e.Settings().XboxHeader
	tick := time.NewTicker(20 * time.Millisecond) // 50 updates a second keeps fades smooth
	defer tick.Stop()

	drop := func(c *conn, why error) {
		log.Printf("portal disconnected (%s): %v", c.dev.Label(), why)
		e.pmu.Lock()
		delete(e.conns, c.key)
		e.pmu.Unlock()
		if c.watcher != nil {
			c.watcher.Stop()
		}
		c.p.Close()
		lastScan = time.Time{}
	}

	for {
		select {
		case <-e.stop:
			for _, c := range e.list() {
				darken(c)
				if c.speaker {
					c.p.Send([]byte{'M', 0})
				}
				if c.watcher != nil {
					c.watcher.Stop()
				}
				c.p.Close()
			}
			return
		case <-tick.C:
		}

		now := time.Since(begin).Seconds()
		e.mu.Lock()
		cfg, fresh, level := e.cfg, e.fresh, e.level
		e.fresh = false
		e.frame = makeFrame(cfg, now, level) // preview while no portal is connected
		e.mu.Unlock()

		if cfg.XboxHeader != header { // reconnect everything so the new setting takes effect
			for _, c := range e.list() {
				drop(c, errors.New("command format changed"))
			}
			header = cfg.XboxHeader
		}

		if time.Since(lastScan) > 2*time.Second {
			lastScan = time.Now()
			e.scan(cfg)
		}

		for _, c := range e.list() {
			lt := e.lightsFor(c.key)
			f := makeFrame(lt, now, level)
			e.pmu.Lock()
			trapCol := RGB{f.Trap, f.Trap, f.Trap} // the trap light only shines white
			if !c.flashAt.IsZero() {
				if t := time.Since(c.flashAt).Seconds(); t <= 3 {
					f = flashFrame(f, c.flashCol, t)
				}
			}
			typ, cfresh := c.typ, c.fresh || fresh
			c.fresh = false
			c.frame = f
			e.pmu.Unlock()
			var err error
			gap := time.Duration(0)
			if typ == "trapteam" {
				gap = trapLightGap
			}
			// c.last holds what was last sent, so a change held back by the gap still goes out next time.
			send := func(slot int, changed bool, cmd []byte, done func()) {
				since := time.Since(c.lastSent[slot])
				if err != nil || !(cfresh || (changed && since >= gap) || since > time.Second) {
					return
				}
				if err = c.p.Send(cmd); err == nil {
					c.lastSent[slot] = time.Now()
					done()
				}
			}
			if typ == "trapteam" {
				fade := fadeMS
				if f.Fade > 0 && f.Fade < fadeMS {
					fade = f.Fade
				}
				side := func(id byte, col RGB) []byte {
					return []byte{'J', id, byte(col[0]), byte(col[1]), byte(col[2]), byte(fade & 0xFF), byte(fade >> 8)}
				}
				send(0, f.Right != c.last.Right, side(sideRight, f.Right), func() { c.last.Right = f.Right })
				send(1, f.Left != c.last.Left, side(sideLeft, f.Left), func() { c.last.Left = f.Left })
				send(2, trapCol != c.lastTrap, []byte{'L', sideTrap, byte(trapCol[0]), byte(trapCol[1]), byte(trapCol[2])}, func() { c.lastTrap = trapCol })
			} else {
				col := f.Single
				send(3, col != c.last.Single, []byte{'C', byte(col[0]), byte(col[1]), byte(col[2])}, func() { c.last.Single = col })
			}
			if err != nil {
				drop(c, err)
				continue
			}
		}
	}
}

// scan looks for newly plugged-in portals and updates the status.
func (e *Engine) scan(cfg Settings) {
	devs := scanDevices()
	var lastErr error
	for _, cand := range candidates(devs) {
		e.pmu.Lock()
		_, open := e.conns[cand.Key]
		e.pmu.Unlock()
		if open {
			continue
		}
		p, err := cand.Open(cfg.XboxHeader)
		if err != nil {
			lastErr = err
			continue
		}
		detected, notes := greet(p)
		e.mu.Lock()
		prof, known := e.profiles[cand.Key]
		if !known {
			prof = Profile{Lights: e.cfg} // a new portal starts with the current look
			e.profiles[cand.Key] = prof
		}
		e.mu.Unlock()
		if !known {
			e.saveProfiles()
		}
		typ := nonEmpty(prof.Type, nonEmpty(detected, "classic"))
		c := &conn{key: cand.Key, dev: cand.Dev, p: p, typ: typ, detected: detected, routes: p.Routes(), fresh: true, connectedAt: time.Now(),
			info: strings.TrimSpace(p.Describe() + "\n" + notes)}
		if typ != "trapteam" && detected == "" {
			darken(c) // clear anything a previous Trap Team setting left on
		}
		log.Printf("connected: %s as %s (detected %q)\n%s", cand.Dev.Label(), typ, detected, c.info)
		c.watcher = NewFigureWatcher(p.Send, e.nextSeq, func(f FigureInfo) { e.placed(c, f) }, func(f FigureInfo) { e.identified(c, f) })
		p.SetHandler(c.watcher.Handle)
		go c.watcher.Run()
		e.pmu.Lock()
		e.conns[cand.Key] = c
		e.pmu.Unlock()
		if e.OnConnect != nil {
			go e.OnConnect(typ)
		}
	}

	conns := e.list()
	var needDriver []string
	for _, d := range devs {
		if !d.IsWinUSB() && !d.IsHID() && !d.IsCompositeParent() {
			needDriver = append(needDriver, nonEmpty(d.Name, "Portal"))
		}
	}
	st := Status{Devices: devs, DriverNeeded: needDriver}
	switch {
	case len(conns) == 1:
		st.State, st.Device = stConnected, conns[0].name()
		st.Text = "Connected to " + conns[0].name() + "."
	case len(conns) > 1:
		st.State, st.Device = stConnected, fmt.Sprintf("%d portals", len(conns))
		st.Text = fmt.Sprintf("Connected to %d portals.", len(conns))
	case len(needDriver) > 0:
		st.State, st.Device = stNeedsDriver, needDriver[0]
		st.Text = "Found " + needDriver[0] + ", but Windows doesn't have a driver that lets this app control it yet."
	case lastErr != nil:
		st.State = stCantOpen
		st.Text = "Found your portal but couldn't open it (" + strings.TrimSpace(lastErr.Error()) +
			"). Close any other program using it, like Dolphin or RPCS3, then unplug it and plug it back in."
	case len(devs) > 0:
		st.State = stCantOpen
		st.Text = "Found a portal-like device but couldn't use it. Unplug it and plug it back in."
	default:
		st.State = stNoPortal
		st.Text = "Plug a portal into a USB port. A wireless portal also needs its USB dongle plugged in and batteries in the portal."
	}
	e.setStatus(st)
}
