package main

import (
	"testing"
)

func TestIsWinner(t *testing.T) {
	ss := &Session{WinThreshold: floatPtr(11)}
	cases := []struct {
		score string
		want  bool
	}{
		{"11", true},   // boundary: at threshold wins
		{"11.5", true}, // above
		{"10.9", false},
		{"8", false},
		{"HM", false}, // non-numeric never wins
		{"", false},
	}
	for _, c := range cases {
		if got := ss.isWinner(c.score); got != c.want {
			t.Fatalf("isWinner(%q) = %v want %v", c.score, got, c.want)
		}
	}
	// No threshold set → nothing wins.
	if (&Session{}).isWinner("99") {
		t.Fatal("session with no threshold should have no winners")
	}
}

func TestCreateSessionScoringDefaults(t *testing.T) {
	s := newTestServer(t)
	a, err := s.createSession("2026-01-01", "")
	if err != nil {
		t.Fatal(err)
	}
	if a.WinThreshold == nil || *a.WinThreshold != 11 || a.MaxPoints == nil || *a.MaxPoints != 15 {
		t.Fatalf("defaults = threshold %v / points %v, want 11 / 15", a.WinThreshold, a.MaxPoints)
	}
	// A later session inherits the latest session's scoring settings.
	a.WinThreshold = floatPtr(9)
	a.MaxPoints = floatPtr(12)
	b, _ := s.createSession("2026-02-01", "")
	if b.WinThreshold == nil || *b.WinThreshold != 9 || b.MaxPoints == nil || *b.MaxPoints != 12 {
		t.Fatalf("inherited = threshold %v / points %v, want 9 / 12", b.WinThreshold, b.MaxPoints)
	}
}

func TestHandleSessionSettings(t *testing.T) {
	s := newTestServer(t)
	ss, _ := s.createSession("2026-01-01", "orig")

	rr := postJSON(t, s.handleSessionSettings, map[string]string{
		"id": ss.ID, "date": "2026-01-05", "description": "new note", "winThreshold": "10", "maxPoints": "14",
	})
	if rr.Code != 200 {
		t.Fatalf("settings POST: %d %s", rr.Code, rr.Body.String())
	}
	got := s.sessionByID(ss.ID)
	if got.Date != "2026-01-05" || got.Description != "new note" {
		t.Fatalf("date/desc = %q / %q", got.Date, got.Description)
	}
	if got.WinThreshold == nil || *got.WinThreshold != 10 || got.MaxPoints == nil || *got.MaxPoints != 14 {
		t.Fatalf("threshold/points = %v / %v want 10 / 14", got.WinThreshold, got.MaxPoints)
	}

	// Blank threshold clears it (→ no winners for the session).
	rr = postJSON(t, s.handleSessionSettings, map[string]string{
		"id": ss.ID, "date": "2026-01-05", "description": "", "winThreshold": "", "maxPoints": "14",
	})
	if rr.Code != 200 {
		t.Fatalf("clear threshold POST: %d %s", rr.Code, rr.Body.String())
	}
	if got := s.sessionByID(ss.ID); got.WinThreshold != nil {
		t.Fatalf("threshold = %v want nil after blank", got.WinThreshold)
	}

	// A non-numeric threshold is rejected.
	rr = postJSON(t, s.handleSessionSettings, map[string]string{
		"id": ss.ID, "date": "2026-01-05", "winThreshold": "abc",
	})
	if rr.Code != 400 {
		t.Fatalf("bad threshold: %d want 400", rr.Code)
	}
}

func TestConsoleSessionTracking(t *testing.T) {
	s := newTestServer(t)
	if rr := postJSON(t, s.handleConsoleSession, map[string]string{"sessionId": "003"}); rr.Code != 204 {
		t.Fatalf("console session POST: %d", rr.Code)
	}
	if s.selectedSessionID != "003" {
		t.Fatalf("selectedSessionID = %q want 003", s.selectedSessionID)
	}
}
