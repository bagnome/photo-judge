// Scoring of physical (printed) entries that have no image file in the app. Each
// physical print records only a category, title, photographer and score; they're
// entered on the Scoring page and stored per session in photos\<id>\physical.json.
// Standard library only.
package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// PhysicalPrint is one judged physical print (no image, no orientation).
type PhysicalPrint struct {
	Category     string `json:"category"`
	Title        string `json:"title"`
	Photographer string `json:"photographer"`
	Score        string `json:"score"`
}

func (s *server) physicalPath(sid string) string {
	return filepath.Join(s.baseDir, "photos", sid, "physical.json")
}

// loadPhysical returns a session's physical prints (nil if none/unreadable).
func (s *server) loadPhysical(sid string) []PhysicalPrint {
	data, err := os.ReadFile(s.physicalPath(sid))
	if err != nil {
		return nil
	}
	var list []PhysicalPrint
	if json.Unmarshal(data, &list) != nil {
		return nil
	}
	return list
}

func (s *server) savePhysical(sid string, list []PhysicalPrint) error {
	if list == nil {
		list = []PhysicalPrint{}
	}
	b, _ := json.MarshalIndent(list, "", "  ")
	return os.WriteFile(s.physicalPath(sid), b, 0o644)
}

// trimField trims and caps a free-text physical-print field.
func trimField(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// handlePhysicalList returns all physical prints for a session.
func (s *server) handlePhysicalList(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("session")
	if !safeName(sid) {
		http.Error(w, "bad session", 400)
		return
	}
	s.mu.Lock()
	ok := s.sessionByID(sid) != nil
	s.mu.Unlock()
	if !ok {
		http.Error(w, "no such session", 404)
		return
	}
	writeJSON(w, map[string]any{"prints": s.loadPhysical(sid)})
}

// handlePhysicalSet replaces all of one category's physical prints for a session
// with the supplied rows (empty rows are dropped). Other categories are untouched.
func (s *server) handlePhysicalSet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Session  string `json:"session"`
		Category string `json:"category"`
		Prints   []struct {
			Title, Photographer, Score string
		} `json:"prints"`
	}
	if decode(r, &body) != nil || !safeName(body.Session) || strings.TrimSpace(body.Category) == "" {
		http.Error(w, "bad request", 400)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.sessionByID(body.Session)
	if ss == nil {
		http.Error(w, "no such session", 404)
		return
	}
	if !sessionHasCategory(ss, body.Category) {
		http.Error(w, "no such category in this session", 400)
		return
	}

	// Keep every other category's prints; replace this category's with the new rows.
	var kept []PhysicalPrint
	for _, p := range s.loadPhysical(body.Session) {
		if p.Category != body.Category {
			kept = append(kept, p)
		}
	}
	for _, row := range body.Prints {
		title := trimField(row.Title)
		photog := trimField(row.Photographer)
		score := trimField(row.Score)
		if title == "" && photog == "" && score == "" {
			continue // drop fully-empty rows
		}
		kept = append(kept, PhysicalPrint{Category: body.Category, Title: title, Photographer: photog, Score: score})
	}
	if err := s.savePhysical(body.Session, kept); err != nil {
		http.Error(w, "could not save: "+err.Error(), 500)
		return
	}
	w.WriteHeader(204)
}

// sessionHasCategory reports whether name is one of the session's categories
// (active or deactivated).
func sessionHasCategory(ss *Session, name string) bool {
	for _, c := range ss.Categories {
		if c == name {
			return true
		}
	}
	for _, c := range ss.InactiveCategories {
		if c == name {
			return true
		}
	}
	return false
}
