package main

import "testing"

func TestRoundTrip(t *testing.T) {
	s := defaults()
	s.Type, s.Effect, s.Left, s.Right = "trapteam", "music", RGB{255, 136, 0}, RGB{1, 2, 3}
	s.Trap, s.TrapBrightness, s.Speed, s.Brightness, s.XboxHeader = "on", 0.4, 2.5, 0.5, false
	s.SoundMode, s.Volume, s.AudioRoute, s.FigureFlash = "both", 0.35, "if1-ep03-be16", false
	s.SummonSound, s.CaptureDevice = false, "{0.0.0.00000000}.{3c1f6c3e-5e2a-4b1a-9a43-1f2e3d4c5b6a}"
	got := parseSettings(s.String())
	if got != s {
		t.Fatalf("round trip\n got %+v\nwant %+v", got, s)
	}
}

func TestOldSettingsFileStillParses(t *testing.T) {
	s := parseSettings("effect = solid\r\ncolour = #ff8800\r\nbrightness = 50\r\nxbox_header = off\r\n")
	f := makeFrame(s, 0, [2]float64{})
	if s.Type != "classic" || f.Single != (RGB{128, 68, 0}) || s.XboxHeader || s.AudioRoute != "auto" {
		t.Fatalf("%+v %+v", s, f)
	}
}

func TestNormalize(t *testing.T) {
	s := Settings{Type: "x", Effect: "nope", Speed: 99, Volume: -1, AudioRoute: "a b"}.Normalize()
	if s.Type != "classic" || s.Effect != "rainbow" || s.Speed != 5 || s.Volume != 0 || s.AudioRoute != "auto" || s.Trap != "match" {
		t.Fatalf("%+v", s)
	}
}

func TestFrames(t *testing.T) {
	for _, typ := range []string{"classic", "trapteam"} {
		for _, e := range Effects {
			s := defaults()
			s.Type, s.Effect = typ, e
			for ti := 0.0; ti < 20; ti += 0.05 {
				f := makeFrame(s, ti, [2]float64{ti / 20, 1 - ti/20})
				for _, c := range []RGB{f.Single, f.Left, f.Right} {
					for _, v := range c {
						if v < 0 || v > 255 {
							t.Fatalf("%s/%s out of range: %+v", typ, e, f)
						}
					}
				}
			}
		}
	}
	if parseSettings("audio_variant = 2\naudio_route = main-be").AudioRoute != "auto" {
		t.Fatal("old speaker settings should be ignored")
	}
	s := defaults()
	s.Effect, s.Trap, s.Type = "off", "off", "trapteam"
	if f := makeFrame(s, 3, [2]float64{}); f != (Frame{}) {
		t.Fatalf("off should be dark: %+v", f)
	}
	s.Effect = "music"
	loud := makeFrame(s, 0, [2]float64{1, 0})
	if loud.Left[2] < 200 || loud.Right[2] > 60 {
		t.Fatalf("music should follow channel levels: %+v", loud)
	}
}

func TestSoundModeMigration(t *testing.T) {
	if s := parseSettings("pc_sound = on\n"); s.SoundMode != "both" {
		t.Fatalf("old pc_sound=on: %q", s.SoundMode)
	}
	if s := parseSettings("pc_sound = off\n"); s.SoundMode != "portal" || !s.SummonSound {
		t.Fatalf("defaults: %+v", s)
	}
	if s := (Settings{SoundMode: "loud"}).Normalize(); s.SoundMode != "portal" {
		t.Fatalf("normalize: %q", s.SoundMode)
	}
}

// The lights must stay in range and never strobe: count how often each effect jumps to a much
// brighter level, over a minute at the fastest speed.
func TestEffectsStayGentle(t *testing.T) {
	lum := func(c RGB) float64 { return (0.3*float64(c[0]) + 0.59*float64(c[1]) + 0.11*float64(c[2])) / 255 }
	for _, e := range Effects {
		s := defaults()
		s.Effect, s.Speed, s.Left, s.Right = e, 5, RGB{255, 0, 0}, RGB{0, 0, 255}
		jumps, prev, last := 0, -1.0, -10.0
		minGap := 99.0
		for i := 0; i < 60*30; i++ { // 30 frames a second, like the app
			tt := float64(i) / 30
			f := makeFrame(s, tt, [2]float64{0.5, 0.5})
			for _, c := range []RGB{f.Single, f.Left, f.Right} {
				for _, v := range c {
					if v < 0 || v > 255 {
						t.Fatalf("%s: colour out of range %v", e, c)
					}
				}
			}
			if f.Trap < 0 || f.Trap > 255 {
				t.Fatalf("%s: trap %d", e, f.Trap)
			}
			l := lum(f.Left)
			if prev >= 0 && l-prev > 0.45 { // a sudden flash
				jumps++
				minGap = min(minGap, tt-last)
				last = tt
			}
			prev = l
		}
		if jumps > 0 && minGap < 0.34 { // no more than 3 flashes a second
			t.Errorf("%s: flashes %.2fs apart", e, minGap)
		}
	}
}
