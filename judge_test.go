package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// liveJudge sets up a session with one revealed photo (the judging target) and judge
// config, and returns the server, session, category, and filename.
func liveJudge(t *testing.T, photographer string) (*server, *Session, string, string) {
	t.Helper()
	s := newTestServer(t)
	ss, err := s.createSession("2026-01-01", "")
	if err != nil {
		t.Fatal(err)
	}
	cat := ss.Categories[0]
	seedNamedPhoto(t, s, ss.ID, cat, "Landscape", "a.jpg", photographer)
	ss.JudgeAggregation = "average"
	ss.JudgesNeeded = 3
	ss.JudgeAlternate = true
	s.ensureScreen("Main")
	s.mu.Lock()
	sc := s.screens["Main"]
	s.loadScreenLocked(sc, ss.ID, cat, "Landscape")
	sc.Position = 1                               // reveal photo 1 → it's the live judging target
	s.judgeActive, s.judgeSessionID = true, ss.ID // the judging session is running
	s.mu.Unlock()
	return s, ss, cat, "a.jpg"
}

func submitJudge(t *testing.T, s *server, ss *Session, cat, file, name string, alt bool, score string) *httptest.ResponseRecorder {
	t.Helper()
	return postJSON(t, s.handleJudgeSubmit, map[string]any{
		"session": ss.ID, "category": cat, "orientation": "Landscape", "file": file,
		"name": name, "alternate": alt, "score": score,
	})
}

func combinedScore(t *testing.T, s *server, ss *Session, cat, file string) string {
	t.Helper()
	return loadScores(s.photosDir(ss.ID, cat, "Landscape"))[file]
}

func TestJudgeCombineAverageAndTotal(t *testing.T) {
	s, ss, cat, file := liveJudge(t, "")
	for _, c := range []struct{ name, score string }{{"Ann", "8"}, {"Bob", "9"}, {"Cyd", "10"}} {
		if rr := submitJudge(t, s, ss, cat, file, c.name, false, c.score); rr.Code != 204 {
			t.Fatalf("submit %s: %d %s", c.name, rr.Code, rr.Body.String())
		}
	}
	if got := combinedScore(t, s, ss, cat, file); got != "9" {
		t.Fatalf("average = %q want 9", got)
	}
	ss.JudgeAggregation = "total"
	submitJudge(t, s, ss, cat, file, "Ann", false, "8") // re-trigger recompute
	if got := combinedScore(t, s, ss, cat, file); got != "27" {
		t.Fatalf("total = %q want 27", got)
	}
}

func TestJudgeDeferToAlternate(t *testing.T) {
	s, ss, cat, file := liveJudge(t, "")
	submitJudge(t, s, ss, cat, file, "Bob", false, "8")
	submitJudge(t, s, ss, cat, file, "Cyd", false, "10")
	// Ann (a primary) defers; the alternate Dan scores in her place.
	if rr := postJSON(t, s.handleJudgeDefer, map[string]any{"session": ss.ID, "category": cat, "orientation": "Landscape", "file": file, "name": "Ann"}); rr.Code != 204 {
		t.Fatalf("defer: %d %s", rr.Code, rr.Body.String())
	}
	submitJudge(t, s, ss, cat, file, "Dan", true, "12")
	// Combined = avg of Bob/Cyd/Dan = (8+10+12)/3 = 10; Ann excluded.
	if got := combinedScore(t, s, ss, cat, file); got != "10" {
		t.Fatalf("combined after defer = %q want 10", got)
	}
}

func TestJudgeScoreRules(t *testing.T) {
	s, ss, cat, file := liveJudge(t, "")
	ss.JudgeMin, ss.JudgeMax, ss.JudgeIncrement = floatPtr(0), floatPtr(10), floatPtr(0.5)
	cases := []struct {
		score string
		want  int
	}{
		{"8.5", 204}, // valid
		{"11", 400},  // above max
		{"-1", 400},  // below min
		{"8.3", 400}, // not a 0.5 step
		{"abc", 400}, // non-numeric
	}
	for _, c := range cases {
		if rr := submitJudge(t, s, ss, cat, file, "Ann", false, c.score); rr.Code != c.want {
			t.Fatalf("score %q: %d want %d (%s)", c.score, rr.Code, c.want, rr.Body.String())
		}
	}
}

func TestJudgeStaleTarget(t *testing.T) {
	s, ss, cat, _ := liveJudge(t, "")
	// Submit against a photo that isn't the live one → 409.
	if rr := submitJudge(t, s, ss, cat, "ghost.jpg", "Ann", false, "8"); rr.Code != 409 {
		t.Fatalf("stale submit: %d want 409", rr.Code)
	}
}

func TestJudgeAutodetectConflict(t *testing.T) {
	s, ss, _, _ := liveJudge(t, "Ann Lee") // the live photo is by "Ann Lee"
	ss.JudgeAutodetect = true
	rr := httptest.NewRecorder()
	s.handleJudgeState(rr, httptest.NewRequest("GET", "/api/judge/state?name=Ann%20Lee&alt=0", nil))
	var st struct {
		You struct {
			Deferred bool `json:"deferred"`
			MayScore bool `json:"mayScore"`
		} `json:"you"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !st.You.Deferred || st.You.MayScore {
		t.Fatalf("auto-detect: judge Ann Lee should be deferred and not score her own photo: %+v", st.You)
	}
}

func TestJudgeStartGating(t *testing.T) {
	s := newTestServer(t)
	s.settings.JudgeScoringEnabled = true
	ss, _ := s.createSession("2026-01-01", "")
	ss.JudgesNeeded, ss.JudgeMin, ss.JudgeMax = 0, nil, nil
	start := func() *httptest.ResponseRecorder {
		return postJSON(t, s.handleJudgeStart, map[string]any{"session": ss.ID})
	}
	if rr := start(); rr.Code != 409 { // no "judges needed" set
		t.Fatalf("start with no judges-needed = %d want 409", rr.Code)
	}
	ss.JudgesNeeded = 2
	if rr := start(); rr.Code != 409 { // no score range set
		t.Fatalf("start with no range = %d want 409", rr.Code)
	}
	ss.JudgeMin, ss.JudgeMax = floatPtr(0), floatPtr(10)
	if rr := start(); rr.Code != 409 { // not enough judges joined
		t.Fatalf("start with 0 judges = %d want 409", rr.Code)
	}
	s.judgeJoin("ann", "Ann", false)
	if rr := start(); rr.Code != 409 { // still only 1 of 2
		t.Fatalf("start with 1 judge = %d want 409", rr.Code)
	}
	s.judgeJoin("bob", "Bob", false)
	if rr := start(); rr.Code != 204 {
		t.Fatalf("start with 2 judges = %d want 204 (%s)", rr.Code, rr.Body.String())
	}
	if !s.judgeActive || s.judgeSessionID != ss.ID {
		t.Fatalf("session not marked active: active=%v id=%q", s.judgeActive, s.judgeSessionID)
	}
	if rr := postJSON(t, s.handleJudgeStop, map[string]any{}); rr.Code != 204 || s.judgeActive {
		t.Fatalf("stop = %d active=%v", rr.Code, s.judgeActive)
	}
}

func TestJudgeInactiveBlocksScoring(t *testing.T) {
	s, ss, cat, file := liveJudge(t, "")
	s.mu.Lock()
	s.judgeActive = false // session not started
	s.mu.Unlock()
	if rr := submitJudge(t, s, ss, cat, file, "Ann", false, "8"); rr.Code != 409 {
		t.Fatalf("submit before start = %d want 409", rr.Code)
	}
}

func TestSessionSettingsJudgeFields(t *testing.T) {
	s := newTestServer(t)
	a, _ := s.createSession("2026-01-01", "")
	rr := postJSON(t, s.handleSessionSettings, map[string]any{
		"id": a.ID, "date": "2026-01-02", "judgeAggregation": "total", "judgesNeeded": 4,
		"judgeAnonymize": true, "judgeAlternate": true, "judgeMin": "0", "judgeMax": "20", "judgeIncrement": "0.5",
	})
	if rr.Code != 200 {
		t.Fatalf("settings: %d %s", rr.Code, rr.Body.String())
	}
	got := s.sessionByID(a.ID)
	if got.JudgeAggregation != "total" || got.JudgesNeeded != 4 || !got.JudgeAnonymize ||
		got.JudgeMin == nil || *got.JudgeMax != 20 || got.JudgeIncrement == nil {
		t.Fatalf("judge fields not saved: %+v", got)
	}
	// New session inherits the judge config.
	b, _ := s.createSession("2026-02-01", "")
	if b.JudgeAggregation != "total" || b.JudgesNeeded != 4 || !b.JudgeAnonymize {
		t.Fatalf("judge config not inherited: %+v", b)
	}
}
