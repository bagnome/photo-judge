package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// seedNamedPhoto drops a real (tiny) JPEG into a session's category/orientation folder
// and records its photographer in names.json — the same on-disk shape the upload/approve
// paths produce, which is what computeStats reads. (scoring_test.go has a name-less
// seedPhoto; this variant also writes the photographer for the per-photographer stats.)
func seedNamedPhoto(t *testing.T, s *server, sid, cat, orient, file, photographer string) {
	t.Helper()
	dir := s.photosDir(sid, cat, orient)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), jpegBytes(8, 8), 0o644); err != nil {
		t.Fatal(err)
	}
	if photographer != "" {
		if err := setName(dir, file, photographer); err != nil {
			t.Fatal(err)
		}
	}
}

// statsFixture builds three sessions across two years with a known entry layout.
// Returns the two category names used. Counts (all-time):
//
//	entries 9, sessions 3, categories 2
//	catA 6, catB 3
//	Alice 5, Bob 2, Carol 1, Unattributed 1  (3 named photographers)
func statsFixture(t *testing.T, s *server) (catA, catB string) {
	t.Helper()
	s1, err := s.createSession("2025-01-10", "Winter")
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := s.createSession("2025-06-15", "Summer")
	s3, _ := s.createSession("2026-03-01", "Spring")
	catA, catB = s1.Categories[0], s1.Categories[1]

	seedNamedPhoto(t, s, s1.ID, catA, "Landscape", "a.jpg", "Alice Smith")
	seedNamedPhoto(t, s, s1.ID, catA, "Landscape", "b.jpg", "Bob Jones")
	seedNamedPhoto(t, s, s1.ID, catB, "Portrait", "c.jpg", "Alice Smith")

	seedNamedPhoto(t, s, s2.ID, catA, "Landscape", "a.jpg", "Bob Jones")
	seedNamedPhoto(t, s, s2.ID, catB, "Landscape", "a.jpg", "Alice Smith")
	seedNamedPhoto(t, s, s2.ID, catB, "Landscape", "b.jpg", "") // unattributed

	seedNamedPhoto(t, s, s3.ID, catA, "Landscape", "a.jpg", "Alice Smith")
	seedNamedPhoto(t, s, s3.ID, catA, "Landscape", "b.jpg", "Alice Smith")
	seedNamedPhoto(t, s, s3.ID, catA, "Landscape", "c.jpg", "Carol Lee")
	return catA, catB
}

func TestStatsAllTime(t *testing.T) {
	s := newTestServer(t)
	catA, catB := statsFixture(t, s)

	s.mu.Lock()
	res := s.computeStats("", "")
	s.mu.Unlock()

	if res.Totals.Entries != 9 || res.Totals.Sessions != 3 || res.Totals.Categories != 2 || res.Totals.Photographers != 3 {
		t.Fatalf("totals = %+v", res.Totals)
	}
	// byCategory sorted by count desc → catA(6) then catB(3).
	if len(res.ByCategory) != 2 || res.ByCategory[0].Category != catA || res.ByCategory[0].Count != 6 ||
		res.ByCategory[1].Category != catB || res.ByCategory[1].Count != 3 {
		t.Fatalf("byCategory = %+v", res.ByCategory)
	}
	// bySession chronological.
	if len(res.BySession) != 3 {
		t.Fatalf("bySession len = %d", len(res.BySession))
	}
	wantDates := []string{"2025-01-10", "2025-06-15", "2026-03-01"}
	for i, d := range wantDates {
		if res.BySession[i].Date != d {
			t.Fatalf("bySession[%d].Date = %q want %q", i, res.BySession[i].Date, d)
		}
	}
	if res.BySession[0].Total != 3 {
		t.Fatalf("session1 total = %d want 3", res.BySession[0].Total)
	}
	// byPhotographer: Alice(5) first, Unattributed last.
	if res.ByPhotographer[0].Name != "Alice Smith" || res.ByPhotographer[0].Count != 5 {
		t.Fatalf("top photographer = %+v", res.ByPhotographer[0])
	}
	last := res.ByPhotographer[len(res.ByPhotographer)-1]
	if last.Name != unattributedLabel || last.Count != 1 {
		t.Fatalf("last photographer = %+v want Unattributed/1", last)
	}
	// Sum of all photographer counts equals total entries.
	sum := 0
	for _, p := range res.ByPhotographer {
		sum += p.Count
	}
	if sum != res.Totals.Entries {
		t.Fatalf("photographer sum %d != entries %d", sum, res.Totals.Entries)
	}
}

func TestStatsDateFilter(t *testing.T) {
	s := newTestServer(t)
	statsFixture(t, s)

	cases := []struct {
		from, to          string
		entries, sessions int
	}{
		{"2026-01-01", "", 3, 1},           // only the 2026 session
		{"", "2025-12-31", 6, 2},           // both 2025 sessions
		{"2025-06-01", "2025-06-30", 3, 1}, // just the Summer session
		{"2030-01-01", "", 0, 0},           // nothing in range
	}
	for _, c := range cases {
		s.mu.Lock()
		res := s.computeStats(c.from, c.to)
		s.mu.Unlock()
		if res.Totals.Entries != c.entries || res.Totals.Sessions != c.sessions {
			t.Fatalf("range [%s..%s]: entries=%d sessions=%d, want %d/%d",
				c.from, c.to, res.Totals.Entries, res.Totals.Sessions, c.entries, c.sessions)
		}
	}
}

func TestStatsEmptyJSON(t *testing.T) {
	s := newTestServer(t)
	// No sessions: arrays must serialize as [] (not null) so the page renders cleanly.
	rr := httptest.NewRecorder()
	s.handleStats(rr, httptest.NewRequest("GET", "/api/stats", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	var res statsResult
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Categories == nil || res.ByCategory == nil || res.BySession == nil || res.ByPhotographer == nil {
		t.Fatalf("nil slice in empty result: %+v", res)
	}
	if res.Totals.Entries != 0 {
		t.Fatalf("entries = %d want 0", res.Totals.Entries)
	}
}

func TestStatsBadDate(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	s.handleStats(rr, httptest.NewRequest("GET", "/api/stats?from=not-a-date", nil))
	if rr.Code != 400 {
		t.Fatalf("bad from date: status %d want 400", rr.Code)
	}
}
