package main

import (
	"encoding/json"
	"testing"
)

func TestProfilesRoundTrip(t *testing.T) {
	a := defaults()
	a.Effect, a.Left, a.Right, a.Speed, a.Brightness = "pulse", RGB{255, 75, 31}, RGB{1, 2, 3}, 2.5, 0.4
	m := map[string]Profile{`usb\vid_1430&pid_1f17\120f2ba9`: {Type: "trapteam", Lights: a}, "x": {Lights: defaults()}}
	got := decodeProfiles(encodeProfiles(m), defaults())
	if got[`usb\vid_1430&pid_1f17\120f2ba9`] != m[`usb\vid_1430&pid_1f17\120f2ba9`] || got["x"].Type != "" {
		t.Fatalf("%+v", got)
	}
}

func TestSplitPatch(t *testing.T) {
	var patch map[string]json.RawMessage
	json.Unmarshal([]byte(`{"portal":"k","effect":"solid","left":"#ff0000","volume":0.3}`), &patch)
	l, g := splitPatch(patch)
	if len(l) != 2 || len(g) != 1 || g["volume"] == nil {
		t.Fatalf("%v %v", l, g)
	}
	s := withLights(defaults(), ApplyPatch(defaults(), l))
	if s.Effect != "solid" || s.Left != (RGB{255, 0, 0}) || s.Volume != defaults().Volume {
		t.Fatalf("%+v", s)
	}
}
