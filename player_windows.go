//go:build windows

package main

import (
	"errors"
	"log"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	winmm            = syscall.NewLazyDLL("winmm.dll")
	pTimeBeginPeriod = winmm.NewProc("timeBeginPeriod")
	pPlaySound       = winmm.NewProc("PlaySoundW")
)

// pcWav holds the WAVs playing on the PC (this one and the last), so they stay in memory while they play.
var pcWav [2][]byte

// Player feeds the Trap Team speaker and measures sound levels for the
// Music light effect.
type Player struct {
	engine *Engine

	mu         sync.Mutex
	clip       *clip
	fx         *clip // a portal sound mixed on top of whatever else is playing
	capDev     string
	hearing    string
	outputs    []APIOutput
	ring       [][2]float32 // captured PC sound waiting to be played
	capStop    chan struct{}
	capErr     string
	capOK      bool
	problem    string
	speaker    bool
	meter      Meter
	stopping   bool
	soundUntil time.Time // a portal sound is playing until then
	lead       bool      // a new sound started: send a little ahead so the portal has a buffer
	filling    bool      // PC sound: waiting for the buffer to fill before playing
}

const (
	pcBufferStart = AudioRate / 10       // start playing PC sound once 100 ms is buffered
	pcBufferMax   = AudioRate * 3 / 10   // more than 300 ms behind: catch up
	pcBufferTrim  = AudioRate * 15 / 100 // ...back to 150 ms
)

func NewPlayer(e *Engine) *Player { return &Player{engine: e} }

func (p *Player) PlayFrames(kind, name string, frames [][2]float32) error {
	if !p.engine.HasSpeaker() {
		return errors.New("no Trap Team portal is connected. Only Trap Team portals have a speaker; if one of yours is, set its type to Trap Team under Your portals")
	}
	p.mu.Lock()
	p.clip = &clip{kind: kind, name: name, frames: frames}
	p.problem = ""
	p.lead = true
	p.mu.Unlock()
	log.Printf("playing %s %q (%.1fs)", kind, name, float64(len(frames))/AudioRate)
	return nil
}

func (p *Player) Stop() {
	p.mu.Lock()
	p.clip = nil
	p.problem = ""
	p.mu.Unlock()
}

func (p *Player) Shutdown() {
	p.mu.Lock()
	p.stopping = true
	if p.capStop != nil {
		close(p.capStop)
		p.capStop = nil
	}
	p.mu.Unlock()
	p.engine.SetSpeaker(false)
}

func (p *Player) State() APIAudio {
	p.mu.Lock()
	defer p.mu.Unlock()
	a := APIAudio{Level: p.meter.Level, Speaker: p.speaker, Problem: p.problem, Outputs: p.outputs}
	if p.capStop != nil {
		a.Hearing = p.hearing
	}
	s := p.engine.Settings()
	switch {
	case p.clip != nil:
		a.Source, a.Name = p.clip.kind, p.clip.name
		a.Pos = float64(p.clip.pos) / AudioRate
		a.Dur = float64(len(p.clip.frames)) / AudioRate
	case s.SoundMode == "both" && p.capStop != nil:
		a.Source = "pc"
	}
	if p.capErr != "" {
		a.Problem = p.capErr
	}
	return a
}

// soundCache keeps each synthesised sound once it's been made.
var soundCache sync.Map // "style/kind/rate" -> []float32

func portalSound(style, kind string, rate int) []float32 {
	if kind == "place" || kind == "trap" {
		style, kind = "any", "place" // one sound for every figure, on either portal
	}
	key := style + "/" + kind + "/" + strconv.Itoa(rate)
	if v, ok := soundCache.Load(key); ok {
		return v.([]float32)
	}
	m := PortalSound(style, kind, rate)
	soundCache.Store(key, m)
	return m
}

var inTurn sync.Mutex

// PlayInTurn plays a sound after any other turn-taking sound has finished, so two portals
// turning on together are heard one after the other.
func (p *Player) PlayInTurn(ev SoundEvent) {
	inTurn.Lock()
	defer inTurn.Unlock()
	if d := p.PlayEvent(ev); d > 0 {
		time.Sleep(d - min(d, 250*time.Millisecond))
	}
}

// PlayEvent plays a portal sound wherever the sound mode says, and returns how long it lasts.
// A placement sound is skipped while another portal sound is still playing, so putting
// several figures down at once (or having them on the portal when it connects) plays it once.
func (p *Player) PlayEvent(ev SoundEvent) time.Duration {
	s := p.engine.Settings()
	place := ev.Kind == "place" || ev.Kind == "trap"
	if !ev.Force && ((place && !s.SummonSound) || (!place && !s.PowerSounds)) {
		return 0
	}
	p.mu.Lock()
	if place && !ev.Force && time.Now().Before(p.soundUntil) {
		p.mu.Unlock()
		return 0
	}
	pcSound := portalSound(ev.Style, ev.Kind, pcRate)
	dur := time.Duration(float64(len(pcSound)) / pcRate * float64(time.Second))
	p.soundUntil = time.Now().Add(dur)
	p.mu.Unlock()
	if s.SoundMode != "portal" { // on the PC
		w := wavBytes(pcSound, pcRate, 0.9)
		p.mu.Lock()
		pcWav[1], pcWav[0] = pcWav[0], w
		p.mu.Unlock()
		pPlaySound.Call(uintptr(unsafe.Pointer(&w[0])), 0, 0x1|0x2|0x4) // SND_ASYNC|SND_NODEFAULT|SND_MEMORY
	}
	if s.SoundMode == "pc" || !p.engine.HasSpeaker() {
		return dur
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	// When the PC's default output is already being copied to the portal, the sound gets there that way.
	if s.SoundMode == "both" && s.CaptureDevice == "" && p.capStop != nil && p.capOK {
		return dur
	}
	p.fx = &clip{kind: "fx", name: "Portal sound", frames: monoFrames(portalSound(ev.Style, ev.Kind, AudioRate))}
	if p.clip == nil && !(s.SoundMode == "both" && p.capStop != nil) {
		p.lead = true
	}
	return dur
}

const pcRate = 22050

// watchOutputs keeps the list of the PC's sound outputs up to date.
func (p *Player) watchOutputs() {
	for {
		outs, err := listOutputs()
		if err == nil {
			p.mu.Lock()
			p.outputs = outs
			stopping := p.stopping
			p.mu.Unlock()
			if stopping {
				return
			}
		}
		time.Sleep(3 * time.Second)
	}
}

func (p *Player) startCapture(device string) {
	stop := make(chan struct{})
	p.capStop = stop
	p.capDev = device
	p.ring = nil
	p.filling = true
	go func() {
		for {
			err := captureLoopback(stop, device, func(name string) {
				p.mu.Lock()
				p.hearing = name
				p.mu.Unlock()
				log.Printf("copying sound from %q", name)
			}, func(frames [][2]float32) {
				p.mu.Lock()
				p.capOK, p.capErr = true, ""
				p.ring = append(p.ring, frames...)
				if len(p.ring) > pcBufferMax { // fallen behind: drop the oldest so it stays in sync
					p.ring = append([][2]float32(nil), p.ring[len(p.ring)-pcBufferTrim:]...)
				}
				p.mu.Unlock()
			})
			select {
			case <-stop:
				return
			default:
			}
			if errors.Is(err, errOutputChanged) {
				p.mu.Lock()
				p.ring, p.filling = nil, true
				p.mu.Unlock()
				continue
			}
			if err != nil {
				log.Println("PC sound capture:", err)
				p.mu.Lock()
				p.capErr = "Couldn't listen to your PC's sound (" + err.Error() + "). Trying again in a moment."
				p.mu.Unlock()
			}
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Second):
			}
		}
	}()
}

// Run paces audio at 8 kHz in 32-sample packets.
func (p *Player) Run() {
	pTimeBeginPeriod.Call(1)
	const pkt = SamplesPerPacket
	start := time.Now()
	produced := 0
	frames := make([][2]float32, 0, pkt)
	var samples []int16
	var lastErrLog time.Time

	for {
		time.Sleep(2 * time.Millisecond)
		s := p.engine.Settings()

		p.mu.Lock()
		if p.stopping {
			p.mu.Unlock()
			return
		}
		wantCapture := s.Effect == "music" || s.SoundMode == "both"
		if wantCapture && p.capStop != nil && p.capDev != s.CaptureDevice { // a different output was chosen
			close(p.capStop)
			p.capStop, p.ring, p.capErr = nil, nil, ""
		}
		if wantCapture && p.capStop == nil {
			p.startCapture(s.CaptureDevice)
		} else if !wantCapture && p.capStop != nil {
			close(p.capStop)
			p.capStop, p.ring, p.capErr = nil, nil, ""
		}
		p.mu.Unlock()

		p.mu.Lock()
		if p.lead {
			p.lead = false
			produced -= AudioRate * 60 / 1000 // 60 ms head start
		}
		p.mu.Unlock()
		due := int(time.Since(start).Seconds()*AudioRate) - produced
		if due > AudioRate { // more than a second behind (the PC was asleep or very busy): start fresh
			produced += due - pkt
			due = pkt
		}
		if due > 8*pkt { // otherwise catch up gradually, without dropping any sound
			due = 8 * pkt
		}
		for ; due >= pkt; due -= pkt {
			produced += pkt
			frames = frames[:0]

			p.mu.Lock()
			fromClip := false
			if c := p.clip; c != nil {
				end := min(c.pos+pkt, len(c.frames))
				frames = append(frames, c.frames[c.pos:end]...)
				c.pos = end
				fromClip = true
				if c.pos >= len(c.frames) {
					p.clip = nil
					p.problem = ""
				}
			} else if p.capStop != nil {
				// PC sound arrives in bursts, so play from a small buffer: wait until it has
				// 100 ms, and if it ever runs dry, wait for it to refill rather than stutter.
				if p.filling && len(p.ring) >= pcBufferStart {
					p.filling = false
				}
				if !p.filling && len(p.ring) < pkt {
					p.filling = true
				}
				if !p.filling {
					frames = append(frames, p.ring[:pkt]...)
					p.ring = p.ring[pkt:]
				} else {
					frames = frames[:pkt]
					for i := range frames {
						frames[i] = [2]float32{}
					}
				}
			}
			if len(frames) > 0 {
				p.meter.Feed(frames)
			} else {
				p.meter.Decay(1)
			}
			level := p.meter.Level
			pcToSpeaker := s.SoundMode == "both" && p.capStop != nil
			if !fromClip && !pcToSpeaker {
				frames = frames[:0] // captured only for the Music lights
			}
			fromFX := false
			if f := p.fx; f != nil { // mix the portal sound on top
				fromFX = true
				for len(frames) < pkt {
					frames = append(frames, [2]float32{})
				}
				for i := 0; i < pkt && f.pos < len(f.frames); i++ {
					v := f.frames[f.pos]
					frames[i][0] = (frames[i][0]*0.6 + v[0])
					frames[i][1] = (frames[i][1]*0.6 + v[1])
					f.pos++
				}
				if f.pos >= len(f.frames) {
					p.fx = nil
				}
			}
			wantSpeaker := fromClip || fromFX || pcToSpeaker || p.clip != nil
			p.mu.Unlock()

			p.engine.SetLevel(level)
			hasSpeaker := p.engine.HasSpeaker()
			p.engine.SetSpeaker(wantSpeaker) // also switches on newly plugged-in Trap Team portals
			speaker := wantSpeaker && hasSpeaker
			p.mu.Lock()
			p.speaker = speaker
			p.mu.Unlock()
			if speaker && len(frames) > 0 {
				vol := s.Volume * s.Volume
				samples = ToSpeaker(frames, vol, samples)
				for len(samples) < pkt {
					samples = append(samples, 0)
				}
				err := p.engine.SendAudio(samples)
				p.mu.Lock()
				if err != nil {
					p.problem = "Couldn't send sound to the portal: " + err.Error()
					if time.Since(lastErrLog) > 5*time.Second {
						log.Println("speaker:", err)
						lastErrLog = time.Now()
					}
				} else {
					p.problem = ""
				}
				p.mu.Unlock()
			} else if fromClip && !hasSpeaker {
				p.mu.Lock()
				p.problem = "No Trap Team portal is connected, so the sound isn't going anywhere yet."
				p.mu.Unlock()
			}
		}
	}
}
