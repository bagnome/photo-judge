package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func listArchives(t *testing.T, s *server, qs string) []ArchivedSession {
	t.Helper()
	rr := httptest.NewRecorder()
	s.handleArchivesList(rr, httptest.NewRequest("GET", "/api/archives?"+qs, nil))
	if rr.Code != 200 {
		t.Fatalf("list %q: %d %s", qs, rr.Code, rr.Body.String())
	}
	var resp struct {
		Archives []ArchivedSession `json:"archives"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Archives
}

func TestSessionArchive(t *testing.T) {
	s := newTestServer(t)
	ss, err := s.createSession("2000-01-01") // safely in the past whenever the test runs
	if err != nil {
		t.Fatal(err)
	}
	cat := ss.Categories[0]
	seedPhoto(t, s, ss.ID, cat, "Landscape", "sunset.jpg")
	seedPhoto(t, s, ss.ID, cat, "Portrait", "tower.jpg")

	if rr := postJSON(t, s.handlePhotoName, map[string]string{
		"session": ss.ID, "category": cat, "orientation": "Landscape", "file": "sunset.jpg", "name": "Jane Doe",
	}); rr.Code != 204 {
		t.Fatalf("name: %d %s", rr.Code, rr.Body.String())
	}
	if rr := postJSON(t, s.handlePhotoScore, map[string]string{
		"session": ss.ID, "category": cat, "orientation": "Landscape", "file": "sunset.jpg", "score": "9.0",
	}); rr.Code != 204 {
		t.Fatalf("score: %d %s", rr.Code, rr.Body.String())
	}

	// Archive the session.
	if rr := postJSON(t, s.handleSessionArchive, map[string]string{"id": ss.ID}); rr.Code != 200 {
		t.Fatalf("archive: %d %s", rr.Code, rr.Body.String())
	}

	// Photo files are gone, the session is dropped, the archive JSON exists.
	if _, err := os.Stat(filepath.Join(s.baseDir, "photos", ss.ID)); !os.IsNotExist(err) {
		t.Errorf("photo folder should be deleted (stat err=%v)", err)
	}
	if s.sessionByID(ss.ID) != nil {
		t.Error("archived session should be removed from the live list")
	}
	data, err := os.ReadFile(filepath.Join(s.archivesDir(), ss.ID+".json"))
	if err != nil {
		t.Fatalf("archive json missing: %v", err)
	}
	var a ArchivedSession
	if err := json.Unmarshal(data, &a); err != nil {
		t.Fatal(err)
	}
	if a.SessionID != ss.ID || a.Date != "2000-01-01" || a.ArchivedDate != todayStr() {
		t.Errorf("archive metadata wrong: %+v", a)
	}
	if a.Created == "" {
		t.Error("archive should capture the session's created timestamp")
	}
	if a.PhotoCount != 2 || len(a.Photos) != 2 {
		t.Fatalf("expected 2 photos, got %d", a.PhotoCount)
	}
	var found bool
	for _, p := range a.Photos {
		if p.Title == "sunset" {
			found = true
			if p.Photographer != "Jane Doe" || p.Score != "9.0" || p.Orientation != "Landscape" || p.Category != cat {
				t.Errorf("archived photo metadata wrong: %+v", p)
			}
		}
	}
	if !found {
		t.Error("archived photo 'sunset' not found")
	}

	// IDs must never be reused — a new session gets a higher ID than the archived one.
	ss2, err := s.createSession("2000-02-01")
	if err != nil {
		t.Fatal(err)
	}
	if ss2.ID == ss.ID {
		t.Errorf("nextID reused archived id %s", ss.ID)
	}

	// Search filters.
	if got := listArchives(t, s, ""); len(got) != 1 {
		t.Fatalf("list all: want 1, got %d", len(got))
	}
	if got := listArchives(t, s, "photographer=jane"); len(got) != 1 {
		t.Errorf("photographer match: want 1, got %d", len(got))
	}
	if got := listArchives(t, s, "photographer=nobody"); len(got) != 0 {
		t.Errorf("photographer no-match: want 0, got %d", len(got))
	}
	if got := listArchives(t, s, "title=sun"); len(got) != 1 {
		t.Errorf("title match: want 1, got %d", len(got))
	}
	if got := listArchives(t, s, "sessionId="+ss.ID); len(got) != 1 {
		t.Errorf("id match: want 1, got %d", len(got))
	}
	if got := listArchives(t, s, "dateFrom=1999-01-01&dateTo=2001-01-01"); len(got) != 1 {
		t.Errorf("date range include: want 1, got %d", len(got))
	}
	if got := listArchives(t, s, "dateFrom=2010-01-01"); len(got) != 0 {
		t.Errorf("date range exclude: want 0, got %d", len(got))
	}

	// Download serves the archive as an attachment.
	dr := httptest.NewRecorder()
	s.handleArchiveDownload(dr, httptest.NewRequest("GET", "/api/archive/download?session="+ss.ID, nil))
	if dr.Code != 200 {
		t.Fatalf("download: %d", dr.Code)
	}
	if cd := dr.Header().Get("Content-Disposition"); !strings.Contains(cd, "archived-session-"+ss.ID) {
		t.Errorf("download Content-Disposition: %q", cd)
	}

	// PDF report renders for the archived session.
	pr := httptest.NewRecorder()
	s.handleArchivePDF(pr, httptest.NewRequest("GET", "/api/archive/pdf?session="+ss.ID, nil))
	if pr.Code != 200 {
		t.Fatalf("archive pdf: %d", pr.Code)
	}
	if ct := pr.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("archive pdf Content-Type: %q", ct)
	}
	if !strings.HasPrefix(pr.Body.String(), "%PDF-") {
		t.Errorf("archive pdf body does not start with %%PDF-")
	}
}

func TestArchiveRejectsNonPastSession(t *testing.T) {
	s := newTestServer(t)
	ss, err := s.createSession("2999-01-01") // future
	if err != nil {
		t.Fatal(err)
	}
	cat := ss.Categories[0]
	seedPhoto(t, s, ss.ID, cat, "Landscape", "a.jpg")
	if rr := postJSON(t, s.handleSessionArchive, map[string]string{"id": ss.ID}); rr.Code != 400 {
		t.Fatalf("archiving a future session should be 400, got %d %s", rr.Code, rr.Body.String())
	}
	if s.sessionByID(ss.ID) == nil {
		t.Error("session should still exist after a rejected archive")
	}
	if _, err := os.Stat(s.photosDir(ss.ID, cat, "Landscape")); err != nil {
		t.Errorf("photos should be intact after a rejected archive: %v", err)
	}
}
