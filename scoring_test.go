package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedPhoto writes a placeholder image file into a session/category/orientation
// folder so the score/name endpoints (which require the file to exist) accept it.
func seedPhoto(t *testing.T, s *server, sid, cat, orient, file string) {
	t.Helper()
	dir := s.photosDir(sid, cat, orient)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPhotoScore(t *testing.T) {
	s := newTestServer(t)
	ss, err := s.createSession("2026-06-01")
	if err != nil {
		t.Fatal(err)
	}
	cat := ss.Categories[0]
	seedPhoto(t, s, ss.ID, cat, "Landscape", "a.jpg")

	score := func(file, val string) *httptest.ResponseRecorder {
		return postJSON(t, s.handlePhotoScore, map[string]string{
			"session": ss.ID, "category": cat, "orientation": "Landscape", "file": file, "score": val,
		})
	}

	// Setting a score persists it to scores.json and surfaces in /api/photos.
	if rr := score("a.jpg", "8.5"); rr.Code != 204 {
		t.Fatalf("set score: %d %s", rr.Code, rr.Body.String())
	}
	if got := photosScores(t, s, ss.ID, cat, "Landscape")["a.jpg"]; got != "8.5" {
		t.Fatalf("score not returned by /api/photos: %q", got)
	}

	// Scoring a non-existent file is a 404.
	if rr := score("ghost.jpg", "5"); rr.Code != 404 {
		t.Fatalf("score missing file want 404, got %d", rr.Code)
	}

	// An over-long score is clamped to 32 chars on disk.
	long := strings.Repeat("9", 50)
	if rr := score("a.jpg", long); rr.Code != 204 {
		t.Fatalf("long score: %d", rr.Code)
	}
	if got := photosScores(t, s, ss.ID, cat, "Landscape")["a.jpg"]; len(got) != 32 {
		t.Fatalf("score not clamped: len=%d", len(got))
	}

	// Blank clears the score.
	if rr := score("a.jpg", "  "); rr.Code != 204 {
		t.Fatalf("clear score: %d", rr.Code)
	}
	if _, ok := photosScores(t, s, ss.ID, cat, "Landscape")["a.jpg"]; ok {
		t.Fatal("score should be cleared")
	}

	// The score lands in the PDF Score column (pre-filled), proving the PDF path reads it.
	score("a.jpg", "8.5")
	pdf := s.buildScoreSheetPDF(ss)
	if !bytes.Contains(pdf, []byte("8.5")) {
		t.Fatal("score 8.5 not present in generated PDF")
	}
}

// TestScoreSurvivesPhotoDelete checks a soft-deleted photo's score is dropped.
func TestScoreSurvivesPhotoDelete(t *testing.T) {
	s := newTestServer(t)
	ss, err := s.createSession("2026-06-01")
	if err != nil {
		t.Fatal(err)
	}
	cat := ss.Categories[0]
	seedPhoto(t, s, ss.ID, cat, "Landscape", "a.jpg")
	postJSON(t, s.handlePhotoScore, map[string]string{
		"session": ss.ID, "category": cat, "orientation": "Landscape", "file": "a.jpg", "score": "7",
	})
	rr := postJSON(t, s.handlePhotoDelete, map[string]string{
		"session": ss.ID, "category": cat, "orientation": "Landscape", "file": "a.jpg",
	})
	if rr.Code != 200 {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}
	dir := s.photosDir(ss.ID, cat, "Landscape")
	if got := loadScores(dir)["a.jpg"]; got != "" {
		t.Fatalf("score should be gone after delete, got %q", got)
	}
}

func photosScores(t *testing.T, s *server, sid, cat, orient string) map[string]string {
	t.Helper()
	rr := httptest.NewRecorder()
	s.handlePhotosList(rr, httptest.NewRequest("GET",
		"/api/photos?session="+sid+"&category="+cat+"&orientation="+orient, nil))
	if rr.Code != 200 {
		t.Fatalf("photos list: %d %s", rr.Code, rr.Body.String())
	}
	var j struct {
		Scores map[string]string `json:"scores"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &j); err != nil {
		t.Fatal(err)
	}
	if j.Scores == nil {
		j.Scores = map[string]string{}
	}
	return j.Scores
}
