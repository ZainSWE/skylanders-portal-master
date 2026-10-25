//go:build !windows && !js

package main

// A demo build for testing the window without portals:
//   go run . [connected|needsdriver|noportal] [number of portals]

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

type demo struct {
	mu      sync.Mutex
	s       Settings
	start   time.Time
	state   string
	startup bool
	audio   APIAudio
	audioAt time.Time
	portals []APIPortal
	lights  map[string]Settings
}

func (d *demo) State() APIState {
	d.mu.Lock()
	defer d.mu.Unlock()
	t := time.Since(d.start).Seconds()
	lvl := [2]float64{}
	a := d.audio
	if a.Source != "" {
		a.Pos = time.Since(d.audioAt).Seconds()
		if a.Dur > 0 && a.Pos > a.Dur {
			d.audio = APIAudio{}
			a = d.audio
		}
	}
	if a.Source != "" || d.s.Effect == "music" {
		lvl = [2]float64{0.5 + 0.5*sin(t*7), 0.5 + 0.5*sin(t*5.3+1)}
	}
	a.Level = lvl
	st := APIStatus{State: d.state}
	var portals []APIPortal
	switch d.state {
	case "connected":
		for _, p := range d.portals {
			l := d.s
			if x, ok := d.lights[p.Key]; ok {
				l = x
			}
			p.Lights = stateSettings(l)
			fr := makeFrame(l, t, lvl)
			p.Frame = apiFrame(fr)
			if len(portals) == 0 {
				p.Figures = d.demoFigures(t)
			}
			portals = append(portals, p)
		}
		st.Text, st.Device = fmt.Sprintf("Connected to %d portals.", len(portals)), "Spyro Portal"
	case "needsdriver":
		st.Text = "Found Spyro Portal, but Windows doesn't have a driver that lets this app control it yet."
		st.Device, st.DriverNeeded = "Spyro Portal", []string{"Spyro Portal"}
	case "noportal":
		st.Text = "Plug a portal into a USB port."
	}
	var routes []APIRoute
	for _, p := range portals {
		if p.Type == "trapteam" {
			routes = []APIRoute{{"xbox-g726", "Xbox 360 speaker format"}, {"main-le", "Plain sound"}, {"main-be", "Plain sound, swapped bytes"}}
			break
		}
	}
	var arrival APIArrival
	if len(portals) > 0 {
		for _, f := range portals[0].Figures {
			if f.Arrival > arrival.Seq {
				arrival = APIArrival{Seq: f.Arrival, Portal: portals[0].Key, PortalType: portals[0].Type, Figure: f}
			}
		}
	}
	return APIState{
		Arrival:    arrival,
		Settings:   stateSettings(d.s),
		Status:     st,
		Devices:    []APIDevice{{Label: "Spyro Portal  (USB ID 1430:1F17, driver: WinUSB)", Ready: true}},
		DeviceInfo: "Spyro Portal (1430:1F17)\nInterface 0 (class FF/5D/01)\n  endpoint 0x81 interrupt IN, 32 bytes\n  endpoint 0x02 interrupt OUT, 32 bytes",
		Portals:    portals,
		Routes:     routes,
		Frame:      apiFrame(makeFrame(d.s, t, lvl)),
		Audio: func() APIAudio {
			a.Outputs = []APIOutput{{ID: "{0.0.0.00000000}.{a1}", Name: "Speakers (Realtek(R) Audio)", Default: false},
				{ID: "{0.0.0.00000000}.{b2}", Name: "Headphones (HyperX Cloud II)", Default: true}}
			if d.s.SoundMode == "both" {
				a.Hearing = "Headphones (HyperX Cloud II)"
			}
			return a
		}(),
		Startup: d.startup,
		LogPath: "%APPDATA%\\PortalLights\\log.txt",
	}
}

// demoFigures: Spyro arrives after 2 s, a Water trap after 7 s (its villain is read 1.5 s later).
func (d *demo) demoFigures(t float64) []FigureInfo {
	if v, err := strconv.ParseFloat(os.Getenv("DEMO_DELAY"), 64); err == nil { // for screenshots without figures
		t -= v
	}
	var out []FigureInfo
	if t > 2 {
		f := Identify(demoHeader(16, 0))
		f.Arrival = 1
		out = append(out, f)
	}
	if t > 7 {
		f := Identify(demoHeader(211, 12290))
		f.Slot, f.Arrival, f.Reading = 1, 2, t < 8.5
		if !f.Reading {
			f.Villain = "Gulper"
		}
		out = append(out, f)
	}
	return out
}

func demoHeader(id, variant uint16) []byte {
	b := make([]byte, 32)
	b[0x10], b[0x11], b[0x1C], b[0x1D] = byte(id), byte(id>>8), byte(variant), byte(variant>>8)
	return b
}

func (d *demo) Update(p map[string]json.RawMessage) {
	d.mu.Lock()
	d.s = ApplyPatch(d.s, p)
	d.mu.Unlock()
}
func (d *demo) SetStartup(on bool) error { d.startup = on; return nil }
func (d *demo) UpdatePortal(key string, patch map[string]json.RawMessage) {
	d.mu.Lock()
	l, ok := d.lights[key]
	if !ok {
		l = d.s
	}
	d.lights[key] = ApplyPatch(l, patch)
	d.mu.Unlock()
}
func (d *demo) SetPortalType(key, typ string) {
	d.mu.Lock()
	for i := range d.portals {
		if d.portals[i].Key == key {
			d.portals[i].Type = typ
		}
	}
	d.mu.Unlock()
}
func (d *demo) hasSpeaker() bool {
	for _, p := range d.portals {
		if p.Type == "trapteam" {
			return true
		}
	}
	return false
}
func (d *demo) PlayPCM(name string, pcm []byte) error {
	if !d.hasSpeaker() {
		return errors.New("no Trap Team portal is connected")
	}
	d.mu.Lock()
	d.audio = APIAudio{Source: "file", Name: name, Dur: float64(len(pcm)) / 4 / AudioRate, Speaker: true}
	d.audioAt = time.Now()
	d.mu.Unlock()
	return nil
}
func (d *demo) PlayTest(v int) error {
	if !d.hasSpeaker() {
		return errors.New("no Trap Team portal is connected")
	}
	d.mu.Lock()
	d.audio = APIAudio{Source: "test", Name: "Test chime", Dur: 1.6, Speaker: true}
	d.audioAt = time.Now()
	d.mu.Unlock()
	return nil
}
func (d *demo) PlayFX(SoundEvent) {}
func (d *demo) StopAudio()        { d.mu.Lock(); d.audio = APIAudio{}; d.mu.Unlock() }
func (d *demo) Open(what string)  { fmt.Println("open", what) }
func (d *demo) DataDir() string {
	dir := "/tmp/pm-data"
	if v := os.Getenv("DEMO_DATA"); v != "" {
		dir = v
	}
	os.MkdirAll(dir, 0o755)
	return dir
}
func (d *demo) Quit() { fmt.Println("quit"); os.Exit(0) }

func main() {
	d := &demo{s: defaults(), start: time.Now(), state: "connected", startup: true, lights: map[string]Settings{}}
	n := 2
	if len(os.Args) > 1 {
		d.state = os.Args[1]
	}
	if len(os.Args) > 2 {
		n, _ = strconv.Atoi(os.Args[2])
	}
	all := []APIPortal{
		{Key: `usb\vid_1430&pid_1f17\5&1`, Name: "Spyro Portal", USBID: "1430:1F17", Type: "trapteam", Detected: "trapteam"},
		{Key: `usb\vid_1430&pid_1f17\5&2`, Name: "Spyro Portal", USBID: "1430:1F17", Type: "classic"},
	}
	d.portals = all[:min(n, len(all))]
	srv, err := StartServer(d)
	if err != nil {
		panic(err)
	}
	fmt.Println(srv.URL())
	select {}
}
