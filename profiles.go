package main

import "encoding/json"

// Profile is what's remembered about one portal: what kind it is and its own lights.
type Profile struct {
	Type   string   // "classic", "trapteam", or "" to use what the app detects
	Lights Settings // only the light fields are used
}

// lightKeys are the settings that belong to each portal rather than the whole app.
var lightKeys = map[string]bool{"effect": true, "left": true, "right": true, "trap": true,
	"trapBrightness": true, "speed": true, "brightness": true}

// splitPatch separates per-portal light changes from app-wide ones.
func splitPatch(patch map[string]json.RawMessage) (lights, global map[string]json.RawMessage) {
	lights, global = map[string]json.RawMessage{}, map[string]json.RawMessage{}
	for k, v := range patch {
		if lightKeys[k] {
			lights[k] = v
		} else if k != "portal" {
			global[k] = v
		}
	}
	return lights, global
}

// withLights returns base with the light fields taken from l.
func withLights(base, l Settings) Settings {
	base.Effect, base.Left, base.Right = l.Effect, l.Left, l.Right
	base.Trap, base.TrapBrightness, base.Speed, base.Brightness = l.Trap, l.TrapBrightness, l.Speed, l.Brightness
	return base
}

type profileJSON struct {
	Type           string  `json:"type,omitempty"`
	Effect         string  `json:"effect"`
	Left           string  `json:"colour"`
	Right          string  `json:"secondColour"`
	Trap           string  `json:"trapLight"`
	TrapBrightness float64 `json:"trapBrightness"`
	Speed          float64 `json:"speed"`
	Brightness     float64 `json:"brightness"`
}

func encodeProfiles(m map[string]Profile) []byte {
	out := map[string]profileJSON{}
	for k, p := range m {
		l := p.Lights
		out[k] = profileJSON{Type: p.Type, Effect: l.Effect, Left: l.Left.Hex(), Right: l.Right.Hex(), Trap: l.Trap,
			TrapBrightness: l.TrapBrightness, Speed: l.Speed, Brightness: l.Brightness}
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return b
}

func decodeProfiles(b []byte, fallback Settings) map[string]Profile {
	in := map[string]profileJSON{}
	m := map[string]Profile{}
	if json.Unmarshal(b, &in) != nil {
		return m
	}
	for k, j := range in {
		l := fallback
		l.Effect, l.Trap = j.Effect, j.Trap
		if c, err := parseColour(j.Left); err == nil {
			l.Left = c
		}
		if c, err := parseColour(j.Right); err == nil {
			l.Right = c
		}
		l.TrapBrightness, l.Speed, l.Brightness = j.TrapBrightness, j.Speed, j.Brightness
		t := j.Type
		if t != "classic" && t != "trapteam" {
			t = ""
		}
		m[k] = Profile{Type: t, Lights: l.Normalize()}
	}
	return m
}
