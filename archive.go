// Session archiving. Once a competition night is over, the operator can archive
// that session: its photo metadata (titles, photographers, scores, categories,
// orientations, the date, and when it was archived) is written to a single JSON
// file under archives\, the photo image files are permanently deleted to reclaim
// disk space, and the session disappears from the console. Archived sessions are
// read-only and browsable on the Archived Sessions page. Standard library only.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ArchivedPhoto is one photo's preserved metadata (the image itself is gone).
type ArchivedPhoto struct {
	Category     string `json:"category"`
	Orientation  string `json:"orientation"`
	Position     int    `json:"position"` // 1-based display order within its group
	Title        string `json:"title"`    // filename without extension
	Filename     string `json:"filename"` // original stored filename, for reference
	Photographer string `json:"photographer"`
	Score        string `json:"score"`
}

// ArchivedSession is the full record written to archives\<id>.json.
type ArchivedSession struct {
	SessionID    string          `json:"sessionId"`
	Date         string          `json:"date"`         // the competition date (session label)
	Categories   []string        `json:"categories"`   // active category order at archive time
	ArchivedDate   string          `json:"archivedDate"` // YYYY-MM-DD it was archived
	ArchivedAt     string          `json:"archivedAt"`   // RFC3339 timestamp
	PhotoCount     int             `json:"photoCount"`
	Photos         []ArchivedPhoto `json:"photos"`
	PhysicalPrints []PhysicalPrint `json:"physicalPrints,omitempty"` // judged physical prints
}

func (s *server) archivesDir() string { return filepath.Join(s.baseDir, "archives") }

// readDirNames returns the entry names in dir (files and folders), or nil.
func readDirNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func todayStr() string { return time.Now().Format("2006-01-02") }

// buildArchive assembles the ArchivedSession record for a session by walking its
// active categories and both orientations in display order. Caller holds s.mu.
func (s *server) buildArchive(ss *Session) ArchivedSession {
	arch := ArchivedSession{
		SessionID:    ss.ID,
		Date:         ss.Date,
		Categories:   append([]string{}, ss.Categories...),
		ArchivedDate: todayStr(),
		ArchivedAt:   time.Now().Format(time.RFC3339),
		Photos:       []ArchivedPhoto{},
	}
	// Capture active categories (in display order) AND any deactivated ones, since
	// a deactivated category can still hold photos and the whole folder is deleted.
	cats := append(append([]string{}, ss.Categories...), ss.InactiveCategories...)
	for _, cat := range cats {
		for _, orient := range []string{"Landscape", "Portrait"} {
			dir := s.photosDir(ss.ID, cat, orient)
			files := s.photoFiles(ss.ID, cat, orient)
			names := loadNames(dir)
			scores := loadScores(dir)
			for i, f := range files {
				arch.Photos = append(arch.Photos, ArchivedPhoto{
					Category:     cat,
					Orientation:  orient,
					Position:     i + 1,
					Title:        strings.TrimSuffix(f, filepath.Ext(f)),
					Filename:     f,
					Photographer: names[f],
					Score:        scores[f],
				})
			}
		}
	}
	arch.PhotoCount = len(arch.Photos)
	arch.PhysicalPrints = s.loadPhysical(ss.ID) // judged physical prints (no image files)
	return arch
}

// handleSessionArchive archives a PAST session: write its metadata JSON, delete
// the photo files, and remove it from the live session list. Irreversible.
func (s *server) handleSessionArchive(w http.ResponseWriter, r *http.Request) {
	var body struct{ ID string }
	if decode(r, &body) != nil || !safeName(body.ID) {
		http.Error(w, "bad request", 400)
		return
	}
	s.mu.Lock()
	ss := s.sessionByID(body.ID)
	if ss == nil {
		s.mu.Unlock()
		http.Error(w, "no such session", 404)
		return
	}
	if ss.Date >= todayStr() {
		s.mu.Unlock()
		http.Error(w, "Only past sessions can be archived (this session's date is today or in the future).", 400)
		return
	}
	if using := s.screensUsing(body.ID); len(using) > 0 {
		s.mu.Unlock()
		http.Error(w, "Session is loaded on screen(s): "+strings.Join(using, ", ")+". Load a different category there first.", 409)
		return
	}

	arch := s.buildArchive(ss)
	if err := os.MkdirAll(s.archivesDir(), 0o755); err != nil {
		s.mu.Unlock()
		http.Error(w, "could not create archives folder: "+err.Error(), 500)
		return
	}
	data, _ := json.MarshalIndent(arch, "", "  ")
	archPath := filepath.Join(s.archivesDir(), ss.ID+".json")
	if err := os.WriteFile(archPath, data, 0o644); err != nil {
		s.mu.Unlock()
		http.Error(w, "could not write archive: "+err.Error(), 500)
		return
	}

	// Metadata is safely written — now permanently delete the photo files and drop
	// the session from the live list.
	if err := os.RemoveAll(filepath.Join(s.baseDir, "photos", ss.ID)); err != nil {
		s.mu.Unlock()
		http.Error(w, "archive saved but could not delete photos: "+err.Error(), 500)
		return
	}
	var out []*Session
	for _, x := range s.sessions {
		if x.ID != ss.ID {
			out = append(out, x)
		}
	}
	s.sessions = out
	s.mu.Unlock()

	log.Printf("session %s archived (%d photos) to %s; photo files deleted", ss.ID, arch.PhotoCount, archPath)
	s.pushConsole()
	writeJSON(w, map[string]any{"sessionId": ss.ID, "photoCount": arch.PhotoCount})
}

// loadArchives reads every archives\*.json, newest competition date first.
func (s *server) loadArchives() []ArchivedSession {
	var list []ArchivedSession
	for _, name := range readDirNames(s.archivesDir()) {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.archivesDir(), name))
		if err != nil {
			continue
		}
		var a ArchivedSession
		if json.Unmarshal(data, &a) == nil {
			list = append(list, a)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Date != list[j].Date {
			return list[i].Date > list[j].Date
		}
		return list[i].SessionID > list[j].SessionID
	})
	return list
}

// handleArchivesList returns archived sessions, optionally filtered by query:
// dateFrom, dateTo (session date, inclusive), sessionId, photographer, title.
// photographer/title match if ANY photo in the session matches (case-insensitive).
func (s *server) handleArchivesList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from := strings.TrimSpace(q.Get("dateFrom"))
	to := strings.TrimSpace(q.Get("dateTo"))
	id := strings.TrimSpace(q.Get("sessionId"))
	photog := strings.ToLower(strings.TrimSpace(q.Get("photographer")))
	title := strings.ToLower(strings.TrimSpace(q.Get("title")))

	var out []ArchivedSession
	for _, a := range s.loadArchives() {
		if from != "" && a.Date < from {
			continue
		}
		if to != "" && a.Date > to {
			continue
		}
		if id != "" && !strings.Contains(a.SessionID, id) {
			continue
		}
		if photog != "" && !matchesPhotographer(a, photog) {
			continue
		}
		if title != "" && !matchesTitle(a, title) {
			continue
		}
		out = append(out, a)
	}
	writeJSON(w, map[string]any{"archives": out})
}

// matchesPhotographer / matchesTitle report whether any photo OR physical print in
// the session matches the (already lower-cased) substring query.
func matchesPhotographer(a ArchivedSession, q string) bool {
	for _, p := range a.Photos {
		if strings.Contains(strings.ToLower(p.Photographer), q) {
			return true
		}
	}
	for _, p := range a.PhysicalPrints {
		if strings.Contains(strings.ToLower(p.Photographer), q) {
			return true
		}
	}
	return false
}

func matchesTitle(a ArchivedSession, q string) bool {
	for _, p := range a.Photos {
		if strings.Contains(strings.ToLower(p.Title), q) {
			return true
		}
	}
	for _, p := range a.PhysicalPrints {
		if strings.Contains(strings.ToLower(p.Title), q) {
			return true
		}
	}
	return false
}

// loadArchive reads a single archive by id, or returns (zero, false) if missing.
func (s *server) loadArchive(id string) (ArchivedSession, bool) {
	data, err := os.ReadFile(filepath.Join(s.archivesDir(), id+".json"))
	if err != nil {
		return ArchivedSession{}, false
	}
	var a ArchivedSession
	if json.Unmarshal(data, &a) != nil {
		return ArchivedSession{}, false
	}
	return a, true
}

// handleArchiveDownload serves one archive's JSON file as a download.
func (s *server) handleArchiveDownload(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("session")
	if !safeName(id) {
		http.Error(w, "bad session", 400)
		return
	}
	data, err := os.ReadFile(filepath.Join(s.archivesDir(), id+".json"))
	if err != nil {
		http.Error(w, "no such archive", 404)
		return
	}
	name := fmt.Sprintf("archived-session-%s.json", id)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, name))
	w.Write(data)
}

// handleArchivePDF renders one archived session as a printable PDF report.
func (s *server) handleArchivePDF(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("session")
	if !safeName(id) {
		http.Error(w, "bad session", 400)
		return
	}
	arch, ok := s.loadArchive(id)
	if !ok {
		http.Error(w, "no such archive", 404)
		return
	}
	pdf := buildArchivePDF(arch)
	name := fmt.Sprintf("archived-session-%s.pdf", id)
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, name))
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(pdf))
}
