package main

import "testing"

// soloFixture builds a session with two screens (L/P) and a known photo layout:
//
//	cat0: 2 Landscape, 1 Portrait   cat1: 1 Landscape, 0 Portrait
//
// Returns the session and its first two category names.
func soloFixture(t *testing.T) (*server, *Session, string, string) {
	t.Helper()
	s := newTestServer(t)
	ss, err := s.createSession("2026-01-01", "")
	if err != nil {
		t.Fatal(err)
	}
	c0, c1 := ss.Categories[0], ss.Categories[1]
	s.ensureScreen("L")
	s.ensureScreen("P")
	ss.SoloEnabled = true
	ss.SoloLandscapeScreen = "L"
	ss.SoloPortraitScreen = "P"
	ss.SoloFirst = "Landscape"
	seedNamedPhoto(t, s, ss.ID, c0, "Landscape", "a.jpg", "")
	seedNamedPhoto(t, s, ss.ID, c0, "Landscape", "b.jpg", "")
	seedNamedPhoto(t, s, ss.ID, c0, "Portrait", "c.jpg", "")
	seedNamedPhoto(t, s, ss.ID, c1, "Landscape", "d.jpg", "")
	return s, ss, c0, c1
}

func TestBuildSoloSegments(t *testing.T) {
	s, ss, c0, c1 := soloFixture(t)
	s.mu.Lock()
	segs := s.buildSoloSegments(ss)
	s.mu.Unlock()
	// cat0 L(2), cat0 P(1), cat1 L(1); cat1 Portrait skipped (no photos).
	want := []soloSegment{
		{c0, "Landscape", "L", 2}, {c0, "Portrait", "P", 1}, {c1, "Landscape", "L", 1},
	}
	if len(segs) != len(want) {
		t.Fatalf("segments = %+v, want %d", segs, len(want))
	}
	for i, w := range want {
		if segs[i] != w {
			t.Fatalf("segment %d = %+v want %+v", i, segs[i], w)
		}
	}

	// Portrait-first reorders each category's two orientations.
	ss.SoloFirst = "Portrait"
	s.mu.Lock()
	segs = s.buildSoloSegments(ss)
	s.mu.Unlock()
	if len(segs) != 3 || segs[0].orientation != "Portrait" || segs[1].orientation != "Landscape" {
		t.Fatalf("portrait-first segments = %+v", segs)
	}

	// An unassigned/unknown screen drops that orientation's segments.
	ss.SoloFirst = "Landscape"
	ss.SoloPortraitScreen = ""
	s.mu.Lock()
	segs = s.buildSoloSegments(ss)
	s.mu.Unlock()
	for _, seg := range segs {
		if seg.orientation == "Portrait" {
			t.Fatalf("portrait segment present with no portrait screen: %+v", segs)
		}
	}
}

func TestSoloStartAdvanceWalk(t *testing.T) {
	s, ss, c0, c1 := soloFixture(t)
	if rr := postJSON(t, s.handleSoloStart, map[string]string{"sessionId": ss.ID}); rr.Code != 204 {
		t.Fatalf("start: %d %s", rr.Code, rr.Body.String())
	}
	L, P := s.screens["L"], s.screens["P"]
	// Segment 0: L shows cat0/Landscape title; P is black.
	if s.solo == nil || s.solo.index != 0 || L.Category != c0 || L.Orientation != "Landscape" || L.Position != 0 || L.Count != 2 || L.Blackout {
		t.Fatalf("after start: L=%+v solo=%+v", L, s.solo)
	}
	if !P.Blackout {
		t.Fatal("partner screen P should be black during the landscape segment")
	}

	adv := func() { postJSON(t, s.handleSoloAdvance, nil) }
	adv() // L photo 1
	adv() // L photo 2
	if L.Position != 2 {
		t.Fatalf("L position = %d want 2", L.Position)
	}
	adv() // L end card (count+1)
	if L.Position != L.Count+1 {
		t.Fatalf("L position = %d want end card %d", L.Position, L.Count+1)
	}
	adv() // switch to segment 1: P shows cat0/Portrait title, L now black
	if s.solo.index != 1 || P.Category != c0 || P.Orientation != "Portrait" || P.Position != 0 || P.Blackout || !L.Blackout {
		t.Fatalf("after switch to P: P=%+v L.black=%v idx=%d", P, L.Blackout, s.solo.index)
	}
	adv() // P photo 1
	adv() // P end card
	adv() // switch to segment 2: L shows cat1/Landscape title
	if s.solo.index != 2 || L.Category != c1 || L.Orientation != "Landscape" || L.Position != 0 || L.Blackout {
		t.Fatalf("after switch to seg2: L=%+v idx=%d", L, s.solo.index)
	}
	adv() // L photo 1
	adv() // L end card
	adv() // past the last segment → finished
	if !s.solo.finished {
		t.Fatalf("expected finished after the last segment")
	}
}

func TestSoloBackAcrossBoundary(t *testing.T) {
	s, ss, c0, _ := soloFixture(t)
	postJSON(t, s.handleSoloStart, map[string]string{"sessionId": ss.ID})
	// Advance to segment 1's title (L:0->1->2->end ->switch).
	for i := 0; i < 4; i++ {
		postJSON(t, s.handleSoloAdvance, nil)
	}
	if s.solo.index != 1 || s.screens["P"].Position != 0 {
		t.Fatalf("setup: idx=%d P.pos=%d", s.solo.index, s.screens["P"].Position)
	}
	// Back from a segment's title card steps to the previous segment's END card.
	postJSON(t, s.handleSoloBack, nil)
	L := s.screens["L"]
	if s.solo.index != 0 || L.Category != c0 || L.Orientation != "Landscape" || L.Position != L.Count+1 || L.Blackout {
		t.Fatalf("after back across boundary: L=%+v idx=%d", L, s.solo.index)
	}
}

// reachLastEndCard advances until the run sits on the final segment's end card (the
// fixture has 3 segments; 9 advances gets there). Asserts we're there, not finished.
func reachLastEndCard(t *testing.T, s *server) {
	t.Helper()
	for i := 0; i < 9; i++ {
		postJSON(t, s.handleSoloAdvance, nil)
	}
	if s.solo == nil || s.solo.index != 2 || s.solo.finished {
		t.Fatalf("expected last segment end card, got %+v", s.solo)
	}
}

func TestSoloEndBehaviors(t *testing.T) {
	// Default: one more advance past the last end card finishes and waits.
	s, ss, _, _ := soloFixture(t)
	postJSON(t, s.handleSoloStart, map[string]string{"sessionId": ss.ID})
	reachLastEndCard(t, s)
	postJSON(t, s.handleSoloAdvance, nil)
	if s.solo == nil || !s.solo.finished {
		t.Fatalf("default end: expected finished, got %+v", s.solo)
	}

	// Loop: advancing past the last end card restarts at the first title.
	s2, ss2, c0, _ := soloFixture(t)
	ss2.SoloEnd = "loop"
	postJSON(t, s2.handleSoloStart, map[string]string{"sessionId": ss2.ID})
	reachLastEndCard(t, s2)
	postJSON(t, s2.handleSoloAdvance, nil)
	L := s2.screens["L"]
	if s2.solo == nil || s2.solo.index != 0 || s2.solo.finished || L.Category != c0 || L.Orientation != "Landscape" || L.Position != 0 {
		t.Fatalf("loop end: expected restart at first title, got solo=%+v L=%+v", s2.solo, L)
	}

	// Close: advancing past the last end card blacks the monitors and ends the run.
	s3, ss3, _, _ := soloFixture(t)
	ss3.SoloEnd = "close"
	postJSON(t, s3.handleSoloStart, map[string]string{"sessionId": ss3.ID})
	reachLastEndCard(t, s3)
	postJSON(t, s3.handleSoloAdvance, nil)
	if s3.solo != nil {
		t.Fatalf("close end: run should have ended, got %+v", s3.solo)
	}
	if !s3.screens["L"].Blackout || !s3.screens["P"].Blackout {
		t.Fatal("close end: both monitors should be black")
	}
}

func TestSoloStartErrors(t *testing.T) {
	s, ss, _, _ := soloFixture(t)
	// Disabled → 400.
	ss.SoloEnabled = false
	if rr := postJSON(t, s.handleSoloStart, map[string]string{"sessionId": ss.ID}); rr.Code != 400 {
		t.Fatalf("disabled start: %d want 400", rr.Code)
	}
	// Enabled but no screens with photos → 400.
	ss.SoloEnabled = true
	ss.SoloLandscapeScreen = "nope"
	ss.SoloPortraitScreen = "nope"
	if rr := postJSON(t, s.handleSoloStart, map[string]string{"sessionId": ss.ID}); rr.Code != 400 {
		t.Fatalf("no-segments start: %d want 400", rr.Code)
	}
}

func TestSessionSettingsSoloFields(t *testing.T) {
	s := newTestServer(t)
	a, _ := s.createSession("2026-01-01", "")
	s.ensureScreen("Main")
	rr := postJSON(t, s.handleSessionSettings, map[string]any{
		"id": a.ID, "date": "2026-01-02", "soloEnabled": true,
		"soloLandscapeScreen": "Main", "soloPortraitScreen": "Main", "soloFirst": "Portrait",
	})
	if rr.Code != 200 {
		t.Fatalf("settings: %d %s", rr.Code, rr.Body.String())
	}
	got := s.sessionByID(a.ID)
	if !got.SoloEnabled || got.SoloLandscapeScreen != "Main" || got.SoloFirst != "Portrait" {
		t.Fatalf("solo fields not saved: %+v", got)
	}
	// A later session inherits the solo config.
	b, _ := s.createSession("2026-02-01", "")
	if !b.SoloEnabled || b.SoloLandscapeScreen != "Main" || b.SoloFirst != "Portrait" {
		t.Fatalf("solo config not inherited: %+v", b)
	}
}
