package main

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"unicode"
)

type RGB [3]int

type Settings struct {
	Type           string  `json:"type"`   // "classic" or "trapteam"
	Effect         string  `json:"effect"` // rainbow, solid, pulse, alternate, music, off
	Left           RGB     `json:"-"`      // classic: colour; trap team: left side
	Right          RGB     `json:"-"`      // classic: second colour; trap team: right side
	Trap           string  `json:"trap"`   // on, off, pulse
	TrapBrightness float64 `json:"trapBrightness"`
	Speed          float64 `json:"speed"`
	Brightness     float64 `json:"brightness"`
	XboxHeader     bool    `json:"xboxHeader"`
	SoundMode      string  `json:"soundMode"`     // where sounds go, see SoundModes
	SummonSound    bool    `json:"summonSound"`   // play a sound when a figure is placed
	PowerSounds    bool    `json:"powerSounds"`   // sounds when portals turn on and off
	Remote         bool    `json:"remote"`        // the phone remote over Wi-Fi is on
	CaptureDevice  string  `json:"captureDevice"` // Windows output whose sound is copied to the portal ("" = default)
	Volume         float64 `json:"volume"`        // speaker volume 0..1
	FigureFlash    bool    `json:"figureFlash"`   // light the portal in a figure's element colour when it's placed
	AudioRoute     string  `json:"audioRoute"`    // how speaker audio is sent, see Route
	ToldBackground bool    `json:"-"`
}

// Frame is what the portal should show right now.
type Frame struct {
	Single      RGB // classic portals (one colour)
	Left, Right RGB // trap team sides
	Trap        int // trap team trap light, white only
	Fade        int `json:"-"` // Trap Team fade time in ms for this frame; 0 means the usual
}

var Effects = []string{"rainbow", "solid", "pulse", "alternate", "music", "spin", "fire", "aurora", "storm", "heartbeat", "elements", "charge", "off"}
var TrapModes = []string{"match", "pulse", "on", "off"}

// SoundModes: "both" plays PC sound and portal sounds on the portal and the PC; "pc" keeps the
// portal silent; "portal" sends only the app's portal sounds to the portal (PC sound stays on the PC).
var SoundModes = []string{"both", "pc", "portal"}

var presets = map[string]RGB{
	"red": {255, 0, 0}, "green": {0, 255, 0}, "blue": {0, 0, 255},
	"white": {255, 255, 255}, "purple": {160, 0, 255}, "orange": {255, 90, 0},
	"cyan": {0, 255, 255}, "yellow": {255, 200, 0}, "pink": {255, 40, 140},
	"off": {0, 0, 0}, "black": {0, 0, 0},
}

func defaults() Settings {
	return Settings{Type: "classic", Effect: "rainbow", Left: RGB{0, 90, 255}, Right: RGB{160, 0, 255},
		Trap: "match", TrapBrightness: 1, Speed: 1, Brightness: 1, XboxHeader: true,
		Volume: 0.6, AudioRoute: "auto", FigureFlash: true, SoundMode: "portal", SummonSound: true, PowerSounds: true}
}

func parseColour(s string) (RGB, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if c, ok := presets[s]; ok {
		return c, nil
	}
	if strings.HasPrefix(s, "#") && len(s) == 7 {
		var c RGB
		for i := 0; i < 3; i++ {
			v, err := strconv.ParseUint(s[1+2*i:3+2*i], 16, 8)
			if err != nil {
				return c, err
			}
			c[i] = int(v)
		}
		return c, nil
	}
	parts := strings.Fields(strings.ReplaceAll(s, ",", " "))
	if len(parts) == 3 {
		var c RGB
		for i, p := range parts {
			v, err := strconv.Atoi(p)
			if err != nil {
				return c, err
			}
			c[i] = clamp(v, 0, 255)
		}
		return c, nil
	}
	return RGB{}, fmt.Errorf("unknown colour %q", s)
}

func (c RGB) Hex() string { return fmt.Sprintf("#%02x%02x%02x", c[0], c[1], c[2]) }

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampf(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func percent(v string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), "%"), 64)
	if err != nil {
		return 0, false
	}
	return clampf(f, 0, 100) / 100, true
}

func onOff(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "no", "false", "0":
		return false
	}
	return true
}

func oneOf(v string, list []string) (string, bool) {
	for _, x := range list {
		if strings.EqualFold(strings.TrimSpace(v), x) {
			return x, true
		}
	}
	return "", false
}

// Normalize fixes out-of-range values (e.g. from the web UI).
func (s Settings) Normalize() Settings {
	d := defaults()
	if s.Type != "trapteam" {
		s.Type = "classic"
	}
	if _, ok := oneOf(s.Effect, Effects); !ok {
		s.Effect = d.Effect
	}
	if _, ok := oneOf(s.Trap, TrapModes); !ok {
		s.Trap = d.Trap
	}
	s.TrapBrightness = clampf(s.TrapBrightness, 0, 1)
	s.Brightness = clampf(s.Brightness, 0, 1)
	s.Speed = clampf(s.Speed, 0.1, 5)
	s.Volume = clampf(s.Volume, 0, 1)
	if _, ok := oneOf(s.SoundMode, SoundModes); !ok {
		s.SoundMode = d.SoundMode
	}
	if len(s.CaptureDevice) > 200 || strings.ContainsFunc(s.CaptureDevice, unicode.IsControl) {
		s.CaptureDevice = ""
	}
	if s.AudioRoute == "" || len(s.AudioRoute) > 40 || strings.ContainsAny(s.AudioRoute, " =\r\n") {
		s.AudioRoute = "auto"
	}
	return s
}

func parseSettings(text string) Settings {
	s := defaults()
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		switch k {
		case "portal", "type":
			switch strings.ToLower(v) {
			case "trapteam", "trap team", "trap":
				s.Type = "trapteam"
			case "classic", "original":
				s.Type = "classic"
			}
		case "effect":
			if e, ok := oneOf(v, Effects); ok {
				s.Effect = e
			}
		case "colour", "color", "left_colour", "left":
			if c, err := parseColour(v); err == nil {
				s.Left = c
			}
		case "second_colour", "right_colour", "right":
			if c, err := parseColour(v); err == nil {
				s.Right = c
			}
		case "trap_light":
			if m, ok := oneOf(v, TrapModes); ok {
				s.Trap = m
			}
		case "trap_brightness":
			if f, ok := percent(v); ok {
				s.TrapBrightness = f
			}
		case "speed":
			if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0.1 && f <= 5 {
				s.Speed = f
			}
		case "brightness":
			if f, ok := percent(v); ok {
				s.Brightness = f
			}
		case "xbox_header":
			s.XboxHeader = onOff(v)
		case "pc_sound": // older versions: on meant "copy PC sound to the portal"
			if onOff(v) {
				s.SoundMode = "both"
			}
		case "sound_mode":
			if m, ok := oneOf(v, SoundModes); ok {
				s.SoundMode = m
			}
		case "summon_sound":
			s.SummonSound = onOff(v)
		case "power_sounds":
			s.PowerSounds = onOff(v)
		case "remote":
			s.Remote = onOff(v)
		case "capture_device":
			s.CaptureDevice = v
		case "volume":
			if f, ok := percent(v); ok {
				s.Volume = f
			}
		case "speaker_route": // (older audio_route/audio_variant values are ignored: they never worked on Xbox)
			s.AudioRoute = v
		case "figure_flash":
			s.FigureFlash = onOff(v)
		case "told_background":
			s.ToldBackground = onOff(v)
		}
	}
	return s
}

func (s Settings) String() string {
	yn := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	pc := func(f float64) int { return int(math.Round(f * 100)) }
	return fmt.Sprintf(`# Skylanders Portal Master settings. Easiest to change these from the app window.
portal = %s
effect = %s
colour = %s
second_colour = %s
trap_light = %s
trap_brightness = %d
speed = %.2f
brightness = %d
xbox_header = %s
sound_mode = %s
summon_sound = %s
power_sounds = %s
remote = %s
capture_device = %s
volume = %d
speaker_route = %s
figure_flash = %s
told_background = %s
`, s.Type, s.Effect, s.Left.Hex(), s.Right.Hex(), s.Trap, pc(s.TrapBrightness),
		s.Speed, pc(s.Brightness), yn(s.XboxHeader), s.SoundMode, yn(s.SummonSound), yn(s.PowerSounds), yn(s.Remote), s.CaptureDevice, pc(s.Volume), s.AudioRoute, yn(s.FigureFlash), yn(s.ToldBackground))
}

func loadSettings(path string) Settings {
	b, err := os.ReadFile(path)
	if err != nil {
		return defaults()
	}
	return parseSettings(string(b))
}

func saveSettings(path string, s Settings) error {
	return os.WriteFile(path, []byte(strings.ReplaceAll(s.String(), "\n", "\r\n")), 0o644)
}

// hsv -> rgb, h in [0,1)
func hsv(h float64) RGB {
	h = math.Mod(h, 1)
	if h < 0 {
		h++
	}
	h *= 6
	i := int(h)
	f := h - float64(i)
	q, t := 1-f, f
	var r, g, b float64
	switch i {
	case 0:
		r, g, b = 1, t, 0
	case 1:
		r, g, b = q, 1, 0
	case 2:
		r, g, b = 0, 1, t
	case 3:
		r, g, b = 0, q, 1
	case 4:
		r, g, b = t, 0, 1
	default:
		r, g, b = 1, 0, q
	}
	return RGB{int(r * 255), int(g * 255), int(b * 255)}
}

func dim(c RGB, lvl float64) RGB {
	return RGB{
		clamp(int(math.Round(float64(c[0])*lvl)), 0, 255),
		clamp(int(math.Round(float64(c[1])*lvl)), 0, 255),
		clamp(int(math.Round(float64(c[2])*lvl)), 0, 255),
	}
}

func mix(a, b RGB, t float64) RGB {
	var c RGB
	for i := range c {
		c[i] = clamp(int(math.Round(float64(a[i])*(1-t)+float64(b[i])*t)), 0, 255)
	}
	return c
}

var elementColours = map[string]RGB{
	"Magic": {195, 59, 255}, "Water": {31, 139, 255}, "Air": {159, 227, 255}, "Earth": {176, 105, 44},
	"Life": {63, 212, 106}, "Undead": {138, 107, 255}, "Tech": {255, 168, 0}, "Fire": {255, 75, 31},
	"Light": {255, 241, 184}, "Dark": {91, 43, 179}, "Kaos": {120, 255, 60},
}

// flashFrame blends a figure's element colour over a frame for t seconds after it was placed.
func flashFrame(f Frame, col RGB, t float64) Frame {
	const total = 3.0
	if t < 0 || t > total {
		return f
	}
	e := math.Min(1, t/0.25)
	if t > total-0.8 {
		e = (total - t) / 0.8
	}
	e *= 0.8 + 0.2*math.Cos(t*9)
	f.Single, f.Left, f.Right = mix(f.Single, col, e), mix(f.Left, col, e), mix(f.Right, col, e)
	if tr := int(255 * e); tr > f.Trap {
		f.Trap = tr
	}
	return f
}

// soft maps a 0..1 brightness to what looks evenly spaced to the eye. LEDs look like they
// jump at the dim end if driven in straight steps, so dim levels change more slowly; it never
// quite reaches off, which avoids a visible pop at the bottom of a pulse.
func soft(x float64) float64 {
	x = clampf(x, 0, 1)
	return 0.025 + 0.975*math.Pow(x, 2.2)
}

// smoothNoise is a gentle wobble between 0 and 1 for flickers that never repeat exactly.
func smoothNoise(x, seed float64) float64 {
	v := math.Sin(x*1.0+seed*1.7)*0.5 + math.Sin(x*1.93+seed*3.1)*0.3 + math.Sin(x*3.71+seed*0.7)*0.2
	return clampf((v+1)/2, 0, 1)
}

// hash01 turns a number into a repeatable pseudo-random value between 0 and 1.
func hash01(k, seed float64) float64 {
	v := math.Sin(k*12.9898+seed*78.233) * 43758.5453
	return v - math.Floor(v)
}

// makeFrame returns what to show at animation time t (seconds). level is the
// current sound level per channel (0..1), used by the music effect.
func makeFrame(s Settings, t float64, level [2]float64) Frame {
	var f Frame
	wave := (math.Sin(t*2.4*s.Speed) + 1) / 2 // 0..1
	trapLvl := 1.0                            // how bright the trap light is when it matches the effect
	ts := t * s.Speed
	switch s.Effect {
	case "rainbow":
		h := t * 0.13 * s.Speed
		f.Single, f.Left, f.Right = hsv(h), hsv(h), hsv(h-0.12)
		trapLvl = 0.35 + 0.65*wave
	case "pulse":
		lv := soft(wave)
		f.Single, f.Left, f.Right = dim(s.Left, lv), dim(s.Left, lv), dim(s.Right, lv)
		trapLvl = lv
	case "alternate":
		f.Single = mix(s.Left, s.Right, wave)
		f.Left, f.Right = dim(s.Left, soft(wave)), dim(s.Right, soft(1-wave))
		trapLvl = 0.3 + 0.7*math.Abs(wave-0.5)*2
	case "solid":
		f.Single, f.Left, f.Right = s.Left, s.Left, s.Right
	case "music":
		l, r := level[0], level[1]
		m := math.Max(l, r)
		floor := 0.06
		f.Single = mix(dim(s.Left, floor+(1-floor)*m), dim(s.Right, floor+(1-floor)*m), clampf((m-0.55)*2.5, 0, 1))
		f.Left, f.Right = dim(s.Left, floor+(1-floor)*l), dim(s.Right, floor+(1-floor)*r)
		trapLvl = 0.1 + 0.9*m
	case "spin":
		// a comet in the first colour circles over the second colour; it passes the trap slot at the front
		a := ts * 2.2
		l := math.Pow((1+math.Cos(a))/2, 1.5)
		r := math.Pow((1-math.Cos(a))/2, 1.5)
		f.Left, f.Right = mix(dim(s.Right, 0.35), s.Left, l), mix(dim(s.Right, 0.35), s.Left, r)
		f.Single = mix(dim(s.Right, 0.35), s.Left, (1+math.Sin(a))/2)
		trapLvl = math.Pow(math.Max(0, math.Sin(a)), 8)
	case "fire":
		flame := func(seed float64) (RGB, float64) {
			n := smoothNoise(ts*2.2, seed)
			return mix(RGB{255, 24, 0}, RGB{255, 160, 18}, n), 0.45 + 0.55*smoothNoise(ts*3.1, seed+7)
		}
		cl, bl := flame(1)
		cr, br := flame(4)
		f.Left, f.Right = dim(cl, bl), dim(cr, br)
		f.Single = dim(mix(cl, cr, 0.5), (bl+br)/2)
		trapLvl = 0.25 + 0.5*smoothNoise(ts*1.3, 11) // a glowing ember
	case "aurora":
		pal := []RGB{{0, 255, 170}, {40, 255, 70}, {30, 140, 255}, {150, 60, 255}}
		at := func(x float64) RGB {
			x = math.Mod(x, float64(len(pal)))
			i := int(x)
			k := x - float64(i)
			k = k * k * (3 - 2*k)
			return mix(pal[i], pal[(i+1)%len(pal)], k)
		}
		g := ts * 0.28
		f.Left = dim(at(g), 0.55+0.45*smoothNoise(ts*0.7, 2))
		f.Right = dim(at(g+1.3), 0.55+0.45*smoothNoise(ts*0.7, 5))
		f.Single = dim(at(g), 0.6+0.4*smoothNoise(ts*0.7, 2))
		trapLvl = 0.15 + 0.35*smoothNoise(ts*0.5, 9)
	case "storm":
		base := RGB{30, 60, 200}
		swell := 0.35 + 0.2*smoothNoise(ts*0.6, 3)
		f.Left, f.Right, f.Single = dim(base, swell), dim(base, swell), dim(base, swell)
		// lightning: at most one strike every ~2.4 s, each a flash and a smaller second flicker
		const period = 2.4
		ss := math.Min(s.Speed, 1.5) // the storm only speeds up a little, so strikes stay seconds apart
		ts := t * ss
		k := math.Floor(ts / period)
		h1, h2, h3 := hash01(k, 1), hash01(k, 2), hash01(k, 3)
		if h1 < 0.75 {
			dt := (ts - k*period - h2*1.2) / ss // seconds since the strike
			if dt >= 0 && dt < 0.9 {
				e := math.Exp(-dt * 9)
				if dt > 0.22 {
					e = math.Max(e, 0.65*math.Exp(-(dt-0.22)*8))
				}
				bolt := RGB{225, 232, 255}
				wl, wr := 1.0, 1.0
				if h3 < 0.33 {
					wr = 0.25
				} else if h3 < 0.66 {
					wl = 0.25
				}
				f.Left, f.Right = mix(f.Left, bolt, e*wl), mix(f.Right, bolt, e*wr)
				f.Single = mix(f.Single, bolt, e)
				trapLvl = e
				if dt < 0.06 || (dt > 0.22 && dt < 0.28) {
					f.Fade = 20 // snap up to the flash
				}
			} else {
				trapLvl = 0.05
			}
		} else {
			trapLvl = 0.05
		}
	case "heartbeat":
		x := math.Mod(ts, 1.15)
		beat := math.Exp(-math.Pow(x/0.07, 2)) + 0.7*math.Exp(-math.Pow((x-0.26)/0.08, 2))
		lvl := 0.1 + 0.9*math.Min(1, beat)
		f.Single, f.Left, f.Right = dim(s.Left, lvl), dim(s.Left, lvl), dim(s.Left, lvl)
		trapLvl = math.Min(1, beat)
	case "elements":
		order := []string{"Fire", "Water", "Air", "Earth", "Life", "Undead", "Magic", "Tech", "Light", "Dark"}
		const hold = 2.4
		at := func(x float64) RGB {
			i := int(math.Floor(x / hold))
			k := clampf((x-float64(i)*hold)/hold*3.3-2.3, 0, 1) // hold, then cross-fade in the last third
			n := len(order)
			return mix(elementColours[order[((i%n)+n)%n]], elementColours[order[(((i+1)%n)+n)%n]], k)
		}
		f.Left, f.Single = at(ts), at(ts)
		f.Right = at(ts + hold/2)
		trapLvl = 0.4
	case "charge":
		// powers up in the first colour, bursts white, then rests
		ts := t * math.Min(s.Speed, 2) // keep the white burst under one a second or so
		p := math.Mod(ts, 3.6) / 3.6
		var lvl, white float64
		switch {
		case p < 0.72:
			q := p / 0.72
			lvl = 0.05 + 0.95*q*q*(0.85+0.15*math.Sin(ts*30*q))
			trapLvl = q * q
		case p < 0.8:
			white = 1 - (p-0.72)/0.08*0.4
			lvl, trapLvl = 1, 1
			if p < 0.735 {
				f.Fade = 20
			}
		default:
			q := (p - 0.8) / 0.2
			lvl = 1 - q
			white = 0.6 * (1 - q)
			trapLvl = 1 - q
		}
		c := mix(dim(s.Left, lvl), RGB{255, 255, 255}, white)
		f.Single, f.Left, f.Right = c, c, c
	}
	f.Single = dim(f.Single, s.Brightness)
	f.Left, f.Right = dim(f.Left, s.Brightness), dim(f.Right, s.Brightness)

	switch s.Trap {
	case "match":
		f.Trap = int(math.Round(255 * s.TrapBrightness * clampf(trapLvl, 0, 1)))
	case "on":
		f.Trap = int(math.Round(255 * s.TrapBrightness))
	case "pulse":
		lvl := soft(0.12 + 0.88*(math.Sin(t*1.6*s.Speed)+1)/2)
		if s.Effect == "music" {
			lvl = 0.1 + 0.9*math.Max(level[0], level[1])
		}
		f.Trap = int(math.Round(255 * s.TrapBrightness * lvl))
	}
	f.Trap = clamp(f.Trap, 0, 255)
	return f
}
