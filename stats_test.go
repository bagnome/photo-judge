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

// seedArchive writes an archived-session JSON file (its photos are metadata only, as
// after a real archive deletes the image files) so computeStats can fold it in.
func seedArchive(t *testing.T, s *server, id, date string, photos []ArchivedPhoto) {
	t.Helper()
	if err := os.MkdirAll(s.archivesDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	a := ArchivedSession{SessionID: id, Date: date, Categories: []string{}, PhotoCount: len(photos), Photos: photos}
	b, _ := json.Marshal(a)
	if err := os.WriteFile(filepath.Join(s.archivesDir(), id+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
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
	// Per-session photographer breakdown (drives the clustered chart): session 1 has
	// Alice (2: one each in catA/catB) ahead of Bob (1).
	s1ph := res.BySession[0].ByPhotographer
	if len(s1ph) != 2 || s1ph[0].Name != "Alice Smith" || s1ph[0].Count != 2 || s1ph[1].Name != "Bob Jones" {
		t.Fatalf("session1 byPhotographer = %+v", s1ph)
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

func TestStatsIncludesArchives(t *testing.T) {
	s := newTestServer(t)
	statsFixture(t, s) // 9 live entries across 2025-2026

	// An archived 2024 session (image files long deleted; only metadata remains).
	seedArchive(t, s, "900", "2024-04-01", []ArchivedPhoto{
		{Category: "Pictorial", Orientation: "Landscape", Title: "A", Photographer: "Alice Smith"},
		{Category: "Pictorial", Orientation: "Portrait", Title: "B", Photographer: "Zoe New"},
		{Category: "Wildlife", Orientation: "Landscape", Title: "C", Photographer: ""},
	})

	s.mu.Lock()
	all := s.computeStats("", "")
	s.mu.Unlock()

	// Archived 3 entries fold into the all-time totals (9 + 3 = 12, 4 sessions).
	if all.Totals.Entries != 12 || all.Totals.Sessions != 4 {
		t.Fatalf("with archive: entries=%d sessions=%d want 12/4", all.Totals.Entries, all.Totals.Sessions)
	}
	// Zoe only appears in the archive, so she must be a counted photographer.
	if !containsPhotographer(all.ByPhotographer, "Zoe New") {
		t.Fatalf("archived photographer Zoe New missing: %+v", all.ByPhotographer)
	}
	// The archived session is chronologically first (2024).
	if all.BySession[0].Date != "2024-04-01" {
		t.Fatalf("first session date = %q want 2024-04-01", all.BySession[0].Date)
	}

	// A range that excludes the archive year leaves the live total untouched.
	s.mu.Lock()
	since2025 := s.computeStats("2025-01-01", "")
	s.mu.Unlock()
	if since2025.Totals.Entries != 9 {
		t.Fatalf("since 2025 entries = %d want 9 (archive excluded)", since2025.Totals.Entries)
	}
}

func containsPhotographer(list []photographerStat, name string) bool {
	for _, p := range list {
		if p.Name == name {
			return true
		}
	}
	return false
}

func TestStatsWinners(t *testing.T) {
	s := newTestServer(t)
	ss, err := s.createSession("2026-03-01", "Spring")
	if err != nil {
		t.Fatal(err)
	}
	// createSession defaults the win threshold to 11.
	if ss.WinThreshold == nil || *ss.WinThreshold != 11 {
		t.Fatalf("default threshold = %v want 11", ss.WinThreshold)
	}
	cat := ss.Categories[0]
	seedNamedPhoto(t, s, ss.ID, cat, "Landscape", "a.jpg", "Alice Smith")
	seedNamedPhoto(t, s, ss.ID, cat, "Landscape", "b.jpg", "Bob Jones")
	seedNamedPhoto(t, s, ss.ID, cat, "Landscape", "c.jpg", "Alice Smith")
	seedNamedPhoto(t, s, ss.ID, cat, "Landscape", "d.jpg", "Bob Jones")
	dir := s.photosDir(ss.ID, cat, "Landscape")
	setScore(dir, "a.jpg", "12") // winner
	setScore(dir, "b.jpg", "8")  // below threshold
	setScore(dir, "c.jpg", "11") // winner (boundary: == threshold wins)
	setScore(dir, "d.jpg", "HM") // non-numeric never wins

	s.mu.Lock()
	res := s.computeStats("", "")
	s.mu.Unlock()

	if res.Totals.Winners != 2 {
		t.Fatalf("winners = %d want 2", res.Totals.Winners)
	}
	if res.Totals.WinningPhotographers != 1 { // only Alice
		t.Fatalf("winning photographers = %d want 1", res.Totals.WinningPhotographers)
	}
	if len(res.WinnersByCategory) != 1 || res.WinnersByCategory[0].Count != 2 {
		t.Fatalf("winnersByCategory = %+v", res.WinnersByCategory)
	}
	if len(res.WinnersByPhotographer) != 1 || res.WinnersByPhotographer[0].Name != "Alice Smith" || res.WinnersByPhotographer[0].Count != 2 {
		t.Fatalf("winnersByPhotographer = %+v", res.WinnersByPhotographer)
	}
	if res.BySession[0].Winners != 2 {
		t.Fatalf("session winners = %d want 2", res.BySession[0].Winners)
	}

	// Clearing the threshold removes all winners (entries are unchanged).
	s.mu.Lock()
	ss.WinThreshold = nil
	res2 := s.computeStats("", "")
	s.mu.Unlock()
	if res2.Totals.Winners != 0 {
		t.Fatalf("winners after clearing threshold = %d want 0", res2.Totals.Winners)
	}
	if res2.Totals.Entries != 4 {
		t.Fatalf("entries = %d want 4", res2.Totals.Entries)
	}
}

func TestStatsArchivedWinners(t *testing.T) {
	s := newTestServer(t)
	thr := floatPtr(11)
	a := ArchivedSession{SessionID: "900", Date: "2024-04-01", Categories: []string{}, WinThreshold: thr,
		Photos: []ArchivedPhoto{
			{Category: "Pictorial", Title: "A", Photographer: "Zoe New", Score: "13"}, // winner
			{Category: "Pictorial", Title: "B", Photographer: "Zoe New", Score: "9"},  // not
		}}
	if err := os.MkdirAll(s.archivesDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(a)
	if err := os.WriteFile(filepath.Join(s.archivesDir(), "900.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	res := s.computeStats("", "")
	s.mu.Unlock()
	if res.Totals.Winners != 1 || res.Totals.Entries != 2 {
		t.Fatalf("archived: winners=%d entries=%d want 1/2", res.Totals.Winners, res.Totals.Entries)
	}
}
