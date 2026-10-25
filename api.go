package main

import "encoding/json"

// APIState is everything the window needs to draw itself.
type APIState struct {
	Settings   APISettings  `json:"settings"`
	Status     APIStatus    `json:"status"`
	Devices    []APIDevice  `json:"devices"`
	DeviceInfo string       `json:"deviceInfo"`
	Portals    []APIPortal  `json:"portals"`
	Arrival    APIArrival   `json:"arrival"` // the most recently placed figure
	Routes     []APIRoute   `json:"routes"`  // ways to reach the Trap Team speaker
	Frame      APIFrame     `json:"frame"`
	Audio      APIAudio     `json:"audio"`
	Startup    bool         `json:"startup"`
	LogPath    string       `json:"logPath"`
	Remote     APIRemote    `json:"remote"`             // the phone remote
	IsRemote   bool         `json:"isRemote"`           // this request came from a phone, not the PC
	Features   *APIFeatures `json:"features,omitempty"` // what this version can do (nil: the Windows app)
}

// APIFeatures tells the page what the web version can't do.
type APIFeatures struct {
	Music   bool `json:"music"`   // the Music effect (needs to hear the PC's sound)
	Mirror  bool `json:"mirror"`  // copying the PC's sound to the portal
	PC      bool `json:"pc"`      // things done on the PC itself: startup, log, quit, phone remote
	Finder  bool `json:"finder"`  // trying other ways to reach the speaker
	Connect bool `json:"connect"` // portals are added with a Connect button
}

type APIRemote struct {
	On      bool     `json:"on"`
	URL     string   `json:"url"`     // what to open on the phone (includes the key)
	Others  []string `json:"others"`  // the same on this PC's other network addresses
	Problem string   `json:"problem"` // why it isn't reachable, if it isn't
}

type APISettings struct {
	Settings
	Left  string `json:"left"`
	Right string `json:"right"`
}

type APIStatus struct {
	State        string   `json:"state"` // searching, noportal, needsdriver, cantopen, connected
	Text         string   `json:"text"`
	Device       string   `json:"device"`
	DriverNeeded []string `json:"driverNeeded"` // portals still waiting for the Zadig fix
}

type APIPortal struct {
	Key      string       `json:"key"`
	Name     string       `json:"name"`
	USBID    string       `json:"usbId"`
	Type     string       `json:"type"`     // "classic" or "trapteam"
	Detected string       `json:"detected"` // what the app worked out by itself ("" if unsure)
	Info     string       `json:"info"`
	Lights   APISettings  `json:"lights"`  // this portal's own light settings
	Figures  []FigureInfo `json:"figures"` // what's on it right now
	Frame    APIFrame     `json:"frame"`   // what it shows right now
}

type APIArrival struct {
	Seq        int        `json:"seq"` // 0 until something is placed
	Portal     string     `json:"portal"`
	PortalType string     `json:"portalType"`
	Figure     FigureInfo `json:"figure"`
}

type APIRoute struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type APIDevice struct {
	Label string `json:"label"`
	Ready bool   `json:"ready"`
}

type APIFrame struct {
	Single string `json:"single"`
	Left   string `json:"left"`
	Right  string `json:"right"`
	Trap   int    `json:"trap"`
}

type APIAudio struct {
	Source   string      `json:"source"` // "", "file", "test", "pc"
	Name     string      `json:"name"`
	Pos      float64     `json:"pos"` // seconds
	Dur      float64     `json:"dur"`
	Level    [2]float64  `json:"level"`
	Speaker  bool        `json:"speaker"` // currently sending sound to the portal speaker
	Problem  string      `json:"problem"`
	Capturer string      `json:"capturer"` // what the PC-sound capture is doing
	Outputs  []APIOutput `json:"outputs"`  // Windows sound outputs that can be copied to the portal
	Hearing  string      `json:"hearing"`  // name of the output being copied right now
}

type APIOutput struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

func stateSettings(s Settings) APISettings {
	return APISettings{Settings: s, Left: s.Left.Hex(), Right: s.Right.Hex()}
}

func apiFrame(f Frame) APIFrame {
	return APIFrame{Single: f.Single.Hex(), Left: f.Left.Hex(), Right: f.Right.Hex(), Trap: f.Trap}
}

// ApplyPatch updates settings from the window's partial JSON.
func ApplyPatch(s Settings, patch map[string]json.RawMessage) Settings {
	str := func(k string) (string, bool) {
		var v string
		if raw, ok := patch[k]; ok && json.Unmarshal(raw, &v) == nil {
			return v, true
		}
		return "", false
	}
	num := func(k string) (float64, bool) {
		var v float64
		if raw, ok := patch[k]; ok && json.Unmarshal(raw, &v) == nil {
			return v, true
		}
		return 0, false
	}
	flag := func(k string) (bool, bool) {
		var v bool
		if raw, ok := patch[k]; ok && json.Unmarshal(raw, &v) == nil {
			return v, true
		}
		return false, false
	}
	if v, ok := str("type"); ok {
		s.Type = v
	}
	if v, ok := str("effect"); ok {
		s.Effect = v
	}
	if v, ok := str("trap"); ok {
		s.Trap = v
	}
	if v, ok := str("left"); ok {
		if c, err := parseColour(v); err == nil {
			s.Left = c
		}
	}
	if v, ok := str("right"); ok {
		if c, err := parseColour(v); err == nil {
			s.Right = c
		}
	}
	if v, ok := num("trapBrightness"); ok {
		s.TrapBrightness = v
	}
	if v, ok := num("speed"); ok {
		s.Speed = v
	}
	if v, ok := num("brightness"); ok {
		s.Brightness = v
	}
	if v, ok := num("volume"); ok {
		s.Volume = v
	}
	if v, ok := str("audioRoute"); ok {
		s.AudioRoute = v
	}
	if v, ok := flag("xboxHeader"); ok {
		s.XboxHeader = v
	}
	if v, ok := str("soundMode"); ok {
		s.SoundMode = v
	}
	if v, ok := str("captureDevice"); ok {
		s.CaptureDevice = v
	}
	if v, ok := flag("powerSounds"); ok {
		s.PowerSounds = v
	}
	if v, ok := flag("summonSound"); ok {
		s.SummonSound = v
	}
	if v, ok := flag("remote"); ok {
		s.Remote = v
	}
	if v, ok := flag("figureFlash"); ok {
		s.FigureFlash = v
	}
	return s.Normalize()
}
