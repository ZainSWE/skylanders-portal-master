//go:build js && wasm

package main

// The web version's engine. The page talks to portals over WebUSB; everything else (effects,
// figure detection, sounds, the speaker's audio format, settings) is the same Go code the
// Windows app uses. The page calls in through window.PortalCore:
//
//	init(settingsText, profilesJSON)     restore saved settings
//	addPortal(key, name, outSize, pid)   a portal was opened
//	removePortal(key)
//	message(key, bytes)                  something arrived from a portal
//	tick()                               -> {packets: [{key, data}], sounds: [{style, kind}], save}
//	request(path, query, body)           -> JSON, the same API the Windows app serves
//	sound(style, kind)                   -> Float32Array at 22050 Hz, to play on this device
//	save()                               -> {settings, profiles}

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"syscall/js"
	"time"
)

type webPortal struct {
	key, name, pid string
	outSize        int
	typ, detected  string
	opened         time.Time
	greetStep      int
	mReply         bool
	ready          bool
	watcher        *FigureWatcher
	frame          Frame
	last           Frame
	lastTrap       RGB
	lastSent       [4]time.Time
	fresh          bool
	flashAt        time.Time
	flashCol       RGB
	flashSeq       int
	speaker        bool
	speakerIdle    time.Time
	xs             XboxSpeaker
	out            [][]byte
}

type webEngine struct {
	mu       sync.Mutex
	cfg      Settings
	profiles map[string]Profile
	portals  map[string]*webPortal
	start    time.Time
	seq      int
	arrival  APIArrival
	local    []SoundEvent // sounds to play on this device
	dirty    bool         // settings changed since the last save

	// the Trap Team speaker
	clip       *clip
	fx         *clip
	soundUntil time.Time
	audioStart time.Time
	produced   int
	problem    string
}

var web = &webEngine{cfg: defaults(), profiles: map[string]Profile{}, portals: map[string]*webPortal{}, start: time.Now()}

// send queues a command for a portal, framed like the Windows app does.
func (e *webEngine) send(p *webPortal, cmd []byte) {
	var pkt []byte
	if e.cfg.XboxHeader {
		pkt = make([]byte, 32)
		copy(pkt, append([]byte{0x0B, 0x14}, cmd...))
	} else {
		pkt = make([]byte, max(p.outSize, 32))
		copy(pkt, cmd)
	}
	if len(cmd) > 0 && cmd[0] == 'M' {
		p.xs.Reset()
	}
	p.out = append(p.out, pkt)
}

func (e *webEngine) lightsFor(key string) Settings {
	if p, ok := e.profiles[key]; ok {
		return withLights(e.cfg, p.Lights)
	}
	return e.cfg
}

func (e *webEngine) profile(key string) Profile {
	p, ok := e.profiles[key]
	if !ok {
		p = Profile{Lights: e.cfg}
	}
	return p
}

func (e *webEngine) addPortal(key, name string, outSize int, pid string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p := &webPortal{key: key, name: name, outSize: outSize, pid: strings.ToLower(pid), opened: time.Now(), fresh: true}
	e.portals[key] = p
	e.send(p, []byte{'R'})
}

func (e *webEngine) removePortal(key string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p := e.portals[key]; p != nil && p.watcher != nil {
		p.watcher.Stop()
	}
	delete(e.portals, key)
}

func (e *webEngine) message(key string, data []byte) {
	if len(data) > 2 && data[0] == 0x0B && data[1] == 0x14 {
		data = data[2:]
	}
	e.mu.Lock()
	p := e.portals[key]
	if p == nil {
		e.mu.Unlock()
		return
	}
	if len(data) >= 2 && data[0] == 'M' && data[1] == 0 {
		p.mReply = true
	}
	w := p.watcher
	e.mu.Unlock()
	if w != nil {
		w.Handle(data)
	}
}

// greet wakes a portal and works out whether it's a Trap Team portal, without blocking:
// R, then A 01 after 120 ms, then M 00 after another 60 ms; only Trap Team portals answer M.
func (e *webEngine) greet(p *webPortal) {
	t := time.Since(p.opened)
	switch {
	case p.greetStep == 0 && t > 120*time.Millisecond:
		e.send(p, []byte{'A', 0x01})
		p.greetStep = 1
	case p.greetStep == 1 && t > 180*time.Millisecond:
		e.send(p, []byte{'M', 0x00})
		p.greetStep = 2
	case p.greetStep == 2 && (p.mReply || t > 900*time.Millisecond):
		if p.mReply || (p.pid == "1f17" && p.outSize >= 64) {
			p.detected = "trapteam"
		}
		prof, known := e.profiles[p.key]
		if !known {
			e.profiles[p.key] = Profile{Lights: e.cfg}
			e.dirty = true
		}
		p.typ = nonEmptyStr(prof.Type, nonEmptyStr(p.detected, "classic"))
		key := p.key
		p.watcher = NewFigureWatcher(func(cmd []byte) error {
			e.mu.Lock()
			defer e.mu.Unlock()
			if q := e.portals[key]; q != nil {
				e.send(q, cmd)
			}
			return nil
		}, func() int { e.mu.Lock(); defer e.mu.Unlock(); e.seq++; return e.seq },
			func(f FigureInfo) { e.placed(key, f) }, func(f FigureInfo) { e.identified(key, f) })
		go p.watcher.Run()
		p.ready, p.greetStep, p.fresh = true, 3, true
		e.playLocked(SoundEvent{Style: p.typ, Kind: "on"})
	}
}

func nonEmptyStr(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}

func (e *webEngine) placed(key string, f FigureInfo) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p := e.portals[key]
	if p == nil {
		return
	}
	if e.arrival.Seq <= f.Arrival {
		e.arrival = APIArrival{Seq: f.Arrival, Portal: key, PortalType: p.typ, Figure: f}
	}
	e.playLocked(SoundEvent{Style: p.typ, Kind: "place"})
}

func (e *webEngine) identified(key string, f FigureInfo) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p := e.portals[key]
	if p == nil || !e.cfg.FigureFlash || p.flashSeq == f.Arrival {
		return
	}
	col, ok := elementColours[f.Element]
	if !ok {
		col = RGB{255, 255, 255}
	}
	p.flashAt, p.flashCol, p.flashSeq, p.fresh = time.Now(), col, f.Arrival, true
}

func (e *webEngine) hasSpeaker() bool {
	for _, p := range e.portals {
		if p.ready && p.typ == "trapteam" {
			return true
		}
	}
	return false
}

// playLocked plays a portal sound wherever the sound mode says (same rules as the Windows app).
func (e *webEngine) playLocked(ev SoundEvent) {
	s := e.cfg
	place := ev.Kind == "place" || ev.Kind == "trap"
	if !ev.Force && ((place && !s.SummonSound) || (!place && !s.PowerSounds)) {
		return
	}
	if place && !ev.Force && time.Now().Before(e.soundUntil) {
		return
	}
	pc := PortalSound(ev.Style, ev.Kind, 22050)
	e.soundUntil = time.Now().Add(time.Duration(float64(len(pc)) / 22050 * float64(time.Second)))
	if s.SoundMode != "portal" || !e.hasSpeaker() {
		e.local = append(e.local, ev)
	}
	if s.SoundMode != "pc" && e.hasSpeaker() {
		e.fx = &clip{kind: "fx", name: "Portal sound", frames: monoFrames(PortalSound(ev.Style, ev.Kind, AudioRate))}
	}
}

const lightGap = 50 * time.Millisecond

// tick runs every 20 ms: greetings, lights and speaker audio.
func (e *webEngine) tick() (packets map[string][][]byte, sounds []SoundEvent, save bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Since(e.start).Seconds()
	keys := make([]string, 0, len(e.portals))
	for k := range e.portals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p := e.portals[k]
		if !p.ready {
			e.greet(p)
			continue
		}
		lt := e.lightsFor(k)
		f := makeFrame(lt, now, [2]float64{})
		if !p.flashAt.IsZero() {
			if t := time.Since(p.flashAt).Seconds(); t <= 3 {
				f = flashFrame(f, p.flashCol, t)
			}
		}
		trapCol := RGB{f.Trap, f.Trap, f.Trap} // the trap light only shines white
		p.frame = f
		fresh := p.fresh
		p.fresh = false
		gap := time.Duration(0)
		if p.typ == "trapteam" {
			gap = lightGap
		}
		sendIf := func(slot int, changed bool, cmd []byte, done func()) {
			since := time.Since(p.lastSent[slot])
			if fresh || (changed && since >= gap) || since > time.Second {
				e.send(p, cmd)
				p.lastSent[slot] = time.Now()
				done()
			}
		}
		if p.typ == "trapteam" {
			fade := 75
			if f.Fade > 0 && f.Fade < fade {
				fade = f.Fade
			}
			side := func(id byte, c RGB) []byte {
				return []byte{'J', id, byte(c[0]), byte(c[1]), byte(c[2]), byte(fade & 0xFF), byte(fade >> 8)}
			}
			sendIf(0, f.Right != p.last.Right, side(0x00, f.Right), func() { p.last.Right = f.Right })
			sendIf(1, f.Left != p.last.Left, side(0x02, f.Left), func() { p.last.Left = f.Left })
			sendIf(2, trapCol != p.lastTrap, []byte{'L', 0x01, byte(trapCol[0]), byte(trapCol[1]), byte(trapCol[2])}, func() { p.lastTrap = trapCol })
		} else {
			c := f.Single
			sendIf(3, c != p.last.Single, []byte{'C', byte(c[0]), byte(c[1]), byte(c[2])}, func() { p.last.Single = c })
		}
	}
	e.audio()
	packets = map[string][][]byte{}
	for _, k := range keys {
		if p := e.portals[k]; len(p.out) > 0 {
			packets[k], p.out = p.out, nil
		}
	}
	sounds, e.local = e.local, nil
	save, e.dirty = e.dirty, false
	return
}

// audio feeds the Trap Team speaker, keeping about 80 ms ahead of real time.
func (e *webEngine) audio() {
	busy := e.clip != nil || e.fx != nil
	var speakers []*webPortal
	for _, p := range e.portals {
		if p.ready && p.typ == "trapteam" {
			speakers = append(speakers, p)
		}
	}
	for _, p := range speakers {
		switch {
		case busy && !p.speaker:
			e.send(p, []byte{'M', 0x01})
			p.speaker = true
		case busy:
			p.speakerIdle = time.Time{}
		case p.speaker && p.speakerIdle.IsZero():
			p.speakerIdle = time.Now()
		case p.speaker && time.Since(p.speakerIdle) > 1500*time.Millisecond:
			e.send(p, []byte{'M', 0x00})
			p.speaker = false
		}
	}
	if !busy || len(speakers) == 0 {
		e.audioStart = time.Time{}
		if len(speakers) == 0 {
			e.clip, e.fx = nil, nil
		}
		return
	}
	if e.audioStart.IsZero() {
		e.audioStart, e.produced = time.Now(), 0
	}
	due := int((time.Since(e.audioStart).Seconds()+0.08)*AudioRate) - e.produced
	if due <= 0 {
		return
	}
	due = min(due, AudioRate/4)
	frames := make([][2]float32, due)
	if c := e.clip; c != nil {
		n := copy(frames, c.frames[c.pos:])
		c.pos += n
		if c.pos >= len(c.frames) {
			e.clip = nil
		}
	}
	if f := e.fx; f != nil {
		for i := 0; i < due && f.pos < len(f.frames); i++ {
			frames[i][0] = frames[i][0]*0.6 + f.frames[f.pos][0]
			frames[i][1] = frames[i][1]*0.6 + f.frames[f.pos][1]
			f.pos++
		}
		if f.pos >= len(f.frames) {
			e.fx = nil
		}
	}
	e.produced += due
	samples := ToSpeaker(frames, e.cfg.Volume*e.cfg.Volume, nil)
	for _, p := range speakers {
		p.xs.Add(samples, func(fr []byte) { p.out = append(p.out, fr) })
	}
}

// ---- the API the page uses (the same one the Windows app serves) ----

func (e *webEngine) state() APIState {
	var portals []APIPortal
	keys := make([]string, 0, len(e.portals))
	for k := range e.portals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p := e.portals[k]
		if !p.ready {
			continue
		}
		var figs []FigureInfo
		if p.watcher != nil {
			figs = p.watcher.Figures()
		}
		portals = append(portals, APIPortal{Key: k, Name: p.name, USBID: "1430:" + strings.ToUpper(p.pid), Type: p.typ, Detected: p.detected,
			Lights: stateSettings(e.lightsFor(k)), Frame: apiFrame(p.frame), Figures: figs})
	}
	st := APIStatus{State: "noportal", Text: "Plug in your Xbox 360 portal, then click Connect portal and choose it."}
	if len(portals) > 0 {
		st = APIStatus{State: "connected", Text: "Connected."}
	} else if len(e.portals) > 0 {
		st = APIStatus{State: "searching", Text: "Waking the portal…"}
	}
	a := APIAudio{}
	if e.clip != nil {
		a.Source, a.Name = e.clip.kind, e.clip.name
		a.Pos, a.Dur = float64(e.clip.pos)/AudioRate, float64(len(e.clip.frames))/AudioRate
	}
	a.Problem = e.problem
	var routes []APIRoute
	if e.hasSpeaker() {
		routes = []APIRoute{{"xbox-g726", "Xbox 360 speaker format"}}
	}
	return APIState{Settings: stateSettings(e.cfg), Status: st, Portals: portals, Arrival: e.arrival, Routes: routes,
		Frame: apiFrame(makeFrame(e.cfg, time.Since(e.start).Seconds(), [2]float64{})), Audio: a,
		Features: &APIFeatures{Connect: true}}
}

func (e *webEngine) request(path, query string, body []byte) (any, error) {
	q, _ := url.ParseQuery(query)
	e.mu.Lock()
	defer e.mu.Unlock()
	switch path {
	case "/api/state":
	case "/api/settings":
		var patch map[string]json.RawMessage
		if err := json.Unmarshal(body, &patch); err != nil {
			return nil, fmt.Errorf("bad settings")
		}
		lights, global := splitPatch(patch)
		var key string
		if raw, ok := patch["portal"]; ok && json.Unmarshal(raw, &key) == nil && e.portals[key] != nil && len(lights) > 0 {
			p := e.profile(key)
			p.Lights = ApplyPatch(withLights(e.cfg, p.Lights), lights)
			e.profiles[key] = p
			e.cfg = withLights(e.cfg, p.Lights)
			if wp := e.portals[key]; wp != nil {
				wp.fresh = true
			}
			patch = global
		}
		if len(patch) > 0 {
			header := e.cfg.XboxHeader
			e.cfg = ApplyPatch(e.cfg, patch)
			if e.cfg.XboxHeader != header {
				for _, p := range e.portals {
					p.fresh = true
				}
			}
		}
		e.dirty = true
	case "/api/portal":
		var b struct{ Key, Type string }
		json.Unmarshal(body, &b)
		if (b.Type == "classic" || b.Type == "trapteam") && e.portals[b.Key] != nil {
			p := e.profile(b.Key)
			p.Type = b.Type
			e.profiles[b.Key] = p
			if wp := e.portals[b.Key]; wp != nil && wp.typ != b.Type {
				e.send(wp, []byte{'C', 0, 0, 0})
				wp.typ, wp.fresh, wp.speaker = b.Type, true, false
			}
			e.dirty = true
		}
	case "/api/audio/fx":
		style, kind := q.Get("style"), q.Get("kind")
		if style != "trapteam" {
			style = "classic"
		}
		if kind != "on" && kind != "off" {
			kind = "place"
		}
		e.playLocked(SoundEvent{Style: style, Kind: kind, Force: q.Get("force") == "1"})
	case "/api/audio/test":
		if !e.hasSpeaker() {
			return nil, fmt.Errorf("connect a Trap Team portal first; only Trap Team portals have a speaker")
		}
		e.clip = &clip{kind: "test", name: "Test chime", frames: Chime()}
	case "/api/audio/file":
		if !e.hasSpeaker() {
			return nil, fmt.Errorf("connect a Trap Team portal first; only Trap Team portals have a speaker")
		}
		e.clip = &clip{kind: "file", name: q.Get("name"), frames: PCMToFrames(body)}
	case "/api/audio/stop":
		e.clip = nil
	default:
		return nil, fmt.Errorf("not available in the web version")
	}
	return e.state(), nil
}

// ---- the bridge to JavaScript ----

func bytesFrom(v js.Value) []byte {
	if v.IsUndefined() || v.IsNull() {
		return nil
	}
	b := make([]byte, v.Get("byteLength").Int())
	js.CopyBytesToGo(b, js.Global().Get("Uint8Array").New(v.Get("buffer"), v.Get("byteOffset"), v.Get("byteLength")))
	return b
}

func bytesTo(b []byte) js.Value {
	u := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(u, b)
	return u
}

func main() {
	core := map[string]any{
		"init": js.FuncOf(func(_ js.Value, a []js.Value) any {
			web.mu.Lock()
			defer web.mu.Unlock()
			if t := a[0].String(); t != "" {
				web.cfg = parseSettings(t)
			}
			web.cfg.Remote = false
			if a[1].String() != "" {
				web.profiles = decodeProfiles([]byte(a[1].String()), web.cfg)
			}
			return nil
		}),
		"addPortal": js.FuncOf(func(_ js.Value, a []js.Value) any {
			web.addPortal(a[0].String(), a[1].String(), a[2].Int(), a[3].String())
			return nil
		}),
		"removePortal": js.FuncOf(func(_ js.Value, a []js.Value) any { web.removePortal(a[0].String()); return nil }),
		"message": js.FuncOf(func(_ js.Value, a []js.Value) any {
			web.message(a[0].String(), bytesFrom(a[1]))
			return nil
		}),
		"tick": js.FuncOf(func(_ js.Value, _ []js.Value) any {
			packets, sounds, save := web.tick()
			out := js.Global().Get("Array").New()
			for k, list := range packets {
				for _, pkt := range list {
					out.Call("push", map[string]any{"key": k, "data": bytesTo(pkt)})
				}
			}
			snd := js.Global().Get("Array").New()
			for _, s := range sounds {
				snd.Call("push", map[string]any{"style": s.Style, "kind": s.Kind})
			}
			return map[string]any{"packets": out, "sounds": snd, "save": save}
		}),
		"request": js.FuncOf(func(_ js.Value, a []js.Value) any {
			res, err := web.request(a[0].String(), a[1].String(), bytesFrom(a[2]))
			if err != nil {
				return map[string]any{"error": err.Error()}
			}
			b, _ := json.Marshal(res)
			return map[string]any{"json": string(b)}
		}),
		"sound": js.FuncOf(func(_ js.Value, a []js.Value) any {
			m := PortalSound(a[0].String(), a[1].String(), 22050)
			arr := js.Global().Get("Float32Array").New(len(m))
			for i, v := range m {
				arr.SetIndex(i, v)
			}
			return arr
		}),
		"save": js.FuncOf(func(_ js.Value, _ []js.Value) any {
			web.mu.Lock()
			defer web.mu.Unlock()
			return map[string]any{"settings": web.cfg.String(), "profiles": string(encodeProfiles(web.profiles))}
		}),
	}
	js.Global().Set("PortalCore", js.ValueOf(core))
	js.Global().Call("dispatchEvent", js.Global().Get("Event").New("portalcore"))
	select {}
}
