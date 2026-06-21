package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// presFixture builds the solo fixture (two screens, photos in cat0/cat1) and turns on the
// given presentation mode. Returns the server + session.
func presFixture(t *testing.T, mode string) (*server, *Session) {
	t.Helper()
	s, ss, _, _ := soloFixture(t)
	switch mode {
	case "guided":
		s.settings.GuidedPresentation = true
	case "score":
		s.settings.ScoreKeeperEnabled = true
	case "judge":
		s.settings.JudgeScoringEnabled = true
	}
	return s, ss
}

func TestPresentationModesMutuallyExclusive(t *testing.T) {
	s := newTestServer(t)
	s.settings.ScoreKeeperEnabled = true
	s.settings.JudgeScoringEnabled = true
	s.sanitizeSettings()
	if s.settings.ScoreKeeperEnabled || !s.settings.JudgeScoringEnabled {
		t.Fatalf("mutual exclusion: judge should win, got score=%v judge=%v",
			s.settings.ScoreKeeperEnabled, s.settings.JudgeScoringEnabled)
	}
}

func TestPresentationStartGating(t *testing.T) {
	start := func(s *server, sid string) int {
		return postJSON(t, s.handlePresentationStart, map[string]string{"sessionId": sid}).Code
	}

	// Guided: starts with photos.
	s, ss := presFixture(t, "guided")
	if c := start(s, ss.ID); c != 204 {
		t.Fatalf("guided start = %d want 204", c)
	}
	// A session with no presentation screens/photos has no segments → 400 (fresh server so
	// no run is already active).
	sEmpty := newTestServer(t)
	sEmpty.settings.GuidedPresentation = true
	empty, _ := sEmpty.createSession("2026-02-02", "")
	if c := start(sEmpty, empty.ID); c != 400 {
		t.Fatalf("guided start with no segments = %d want 400", c)
	}

	// Score Keeper: no extra gating beyond having segments.
	s2, ss2 := presFixture(t, "score")
	if c := start(s2, ss2.ID); c != 204 {
		t.Fatalf("score-keeper start = %d want 204", c)
	}

	// Judge: gated until rules set + judges joined.
	s3, ss3 := presFixture(t, "judge")
	if c := start(s3, ss3.ID); c != 409 {
		t.Fatalf("judge start with no rules = %d want 409", c)
	}
	ss3.JudgesNeeded = 2
	ss3.JudgeMin, ss3.JudgeMax = floatPtr(0), floatPtr(10)
	if c := start(s3, ss3.ID); c != 409 {
		t.Fatalf("judge start with 0 judges = %d want 409", c)
	}
	s3.judgeJoin("ann", "Ann", false)
	s3.judgeJoin("bob", "Bob", false)
	if c := start(s3, ss3.ID); c != 204 {
		t.Fatalf("judge start with 2 judges = %d want 204", c)
	}
	if !s3.judgeActive {
		t.Fatal("judge mode start should set judgeActive")
	}
}

func TestPresentationAdvanceWalk(t *testing.T) {
	s, ss := presFixture(t, "guided")
	if c := postJSON(t, s.handlePresentationStart, map[string]string{"sessionId": ss.ID}).Code; c != 204 {
		t.Fatalf("start: %d", c)
	}
	L := s.screens["L"]
	if L.Position != 0 {
		t.Fatalf("after start L at title, position = %d want 0", L.Position)
	}
	// Advance steps the live screen: title → photo 1 (returns 200 with the event JSON).
	if c := postJSON(t, s.handleSoloAdvance, map[string]any{}).Code; c != 200 {
		t.Fatalf("advance: %d", c)
	}
	if L.Position != 1 {
		t.Fatalf("after advance L position = %d want 1", L.Position)
	}
}

func TestPresentationPause(t *testing.T) {
	s, ss := presFixture(t, "guided")
	postJSON(t, s.handlePresentationStart, map[string]string{"sessionId": ss.ID})
	if c := postJSON(t, s.handlePresentationPause, map[string]any{}).Code; c != 204 {
		t.Fatalf("pause: %d", c)
	}
	// Paused: the panel can't advance...
	if c := postJSON(t, s.handleSoloAdvance, map[string]any{}).Code; c != 409 {
		t.Fatalf("advance while paused = %d want 409", c)
	}
	// ...but the table nav is operable again for reconfiguration.
	if c := postJSON(t, s.handleScreenCmd, map[string]string{"name": "L", "action": "next"}).Code; c != 204 {
		t.Fatalf("table next while paused = %d want 204", c)
	}
	// Resume re-enables the panel.
	if c := postJSON(t, s.handlePresentationResume, map[string]any{}).Code; c != 204 {
		t.Fatalf("resume: %d", c)
	}
	if c := postJSON(t, s.handleSoloAdvance, map[string]any{}).Code; c != 200 {
		t.Fatalf("advance after resume = %d want 200", c)
	}
}

func TestScoreLockedAfterEnd(t *testing.T) {
	// On-screen score rejected once the session has ended.
	s, ss := presFixture(t, "score")
	cat := ss.Categories[0]
	ss.EndedAt = "2026-01-01T00:00:00Z"
	rr := postJSON(t, s.handlePhotoScore, map[string]string{
		"session": ss.ID, "category": cat, "orientation": "Landscape", "file": "a.jpg", "score": "8",
	})
	if rr.Code != 409 {
		t.Fatalf("score after end = %d want 409", rr.Code)
	}

	// Judge writes rejected too.
	js, jss, jcat, jfile := liveJudge(t, "")
	jss.EndedAt = "2026-01-01T00:00:00Z"
	if rr := submitJudge(t, js, jss, jcat, jfile, "Ann", false, "8"); rr.Code != 409 {
		t.Fatalf("judge submit after end = %d want 409", rr.Code)
	}
	if rr := postJSON(t, js.handleJudgeRescore, map[string]string{"session": jss.ID, "category": jcat, "orientation": "Landscape", "file": jfile, "judge": "Ann"}); rr.Code != 409 {
		t.Fatalf("judge rescore after end = %d want 409", rr.Code)
	}
}

func TestLifecyclePersistAndInherit(t *testing.T) {
	s, ss := presFixture(t, "guided")
	postJSON(t, s.handlePresentationStart, map[string]string{"sessionId": ss.ID})
	if ss.StartedAt == "" {
		t.Fatal("start should stamp StartedAt")
	}
	postJSON(t, s.handlePresentationEnd, map[string]string{"sessionId": ss.ID})
	if ss.EndedAt == "" || !ss.locked() {
		t.Fatalf("end should stamp EndedAt + lock: started=%q ended=%q", ss.StartedAt, ss.EndedAt)
	}
	// Persisted to session.json.
	var disk Session
	b, err := os.ReadFile(filepath.Join(s.baseDir, "photos", ss.ID, "session.json"))
	if err != nil || json.Unmarshal(b, &disk) != nil {
		t.Fatalf("read session.json: %v", err)
	}
	if disk.StartedAt == "" || disk.EndedAt == "" {
		t.Fatalf("lifecycle not persisted: %+v", disk)
	}
	// A new session inherits presentation config but NOT the lifecycle stamps.
	nw, _ := s.createSession("2026-03-03", "")
	if nw.StartedAt != "" || nw.EndedAt != "" {
		t.Fatalf("new session should not inherit lifecycle: %+v", nw)
	}
	if nw.SoloLandscapeScreen != ss.SoloLandscapeScreen {
		t.Fatal("new session should inherit presentation screen config")
	}
}

func TestPresentationRestart(t *testing.T) {
	s, ss := presFixture(t, "score")
	cat := ss.Categories[0]
	postJSON(t, s.handlePresentationStart, map[string]string{"sessionId": ss.ID})
	if rr := postJSON(t, s.handlePhotoScore, map[string]string{
		"session": ss.ID, "category": cat, "orientation": "Landscape", "file": "a.jpg", "score": "7",
	}); rr.Code != 204 {
		t.Fatalf("score: %d", rr.Code)
	}
	postJSON(t, s.handlePresentationEnd, map[string]string{"sessionId": ss.ID})
	if !ss.locked() {
		t.Fatal("should be locked after end")
	}
	// Restart erases the scores and re-opens the session for a fresh run.
	if rr := postJSON(t, s.handlePresentationRestart, map[string]string{"sessionId": ss.ID}); rr.Code != 204 {
		t.Fatalf("restart: %d %s", rr.Code, rr.Body.String())
	}
	if ss.EndedAt != "" || ss.StartedAt == "" {
		t.Fatalf("restart should clear EndedAt + set StartedAt: started=%q ended=%q", ss.StartedAt, ss.EndedAt)
	}
	if s.solo == nil {
		t.Fatal("restart should start a fresh run")
	}
	if got := loadScores(s.photosDir(ss.ID, cat, "Landscape"))["a.jpg"]; got != "" {
		t.Fatalf("restart should erase scores, got %q", got)
	}
	// Scoring works again now the lock is cleared.
	if rr := postJSON(t, s.handlePhotoScore, map[string]string{
		"session": ss.ID, "category": cat, "orientation": "Landscape", "file": "a.jpg", "score": "9",
	}); rr.Code != 204 {
		t.Fatalf("score after restart = %d want 204", rr.Code)
	}
}

func TestPresentationGuards(t *testing.T) {
	s, ss := presFixture(t, "guided")
	// Mode on, no run: the slideshow is black and the table nav is locked.
	s.mu.Lock()
	L := s.screens["L"]
	s.loadScreenLocked(L, ss.ID, ss.Categories[0], "Landscape")
	L.Position = 1
	v := s.buildView(L)
	s.mu.Unlock()
	if v.Mode != "black" {
		t.Fatalf("before start, view = %q want black", v.Mode)
	}
	if c := postJSON(t, s.handleScreenCmd, map[string]string{"name": "L", "action": "next"}).Code; c != 409 {
		t.Fatalf("screen cmd before start = %d want 409", c)
	}
	// After start, the engine controls the screen (no longer forced black).
	postJSON(t, s.handlePresentationStart, map[string]string{"sessionId": ss.ID})
	s.mu.Lock()
	v = s.buildView(s.screens["L"])
	s.mu.Unlock()
	if v.Mode == "black" {
		t.Fatalf("after start, live screen still black: %+v", v)
	}
}
