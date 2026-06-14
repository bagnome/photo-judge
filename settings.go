// App-wide settings (settings.json next to the exe), edited on the Settings page.
// Two of them — LAN access and metadata import — are gated by the properties file:
// a "false" there takes priority (the effective value is propertiesValue AND
// settingsValue). Logos are a small library here too: upload several, pick the
// active one, delete the rest. Standard library only.
package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const pdfHeaderMax = 90 // characters — kept short enough to stay within ~2 PDF lines

// Settings holds the operator-tunable options. LanAccess / ImportPhotographer /
// ImportTitle are the SETTINGS side of a gate with the properties file.
type Settings struct {
	LockScorekeeper    bool   `json:"lockScorekeeper"`    // force the scoring page to follow the operator
	SingleLiveScreen   bool   `json:"singleLiveScreen"`   // revealing a screen blacks out all the others
	ActiveLogo         string `json:"activeLogo"`         // filename in logo\ to show on title cards
	PDFHeader          string `json:"pdfHeader"`          // custom heading on the PDFs ("" = "Photo Judge")
	LanAccess          bool   `json:"lanAccess"`          // gated by properties lanAccess
	ImportPhotographer bool   `json:"importPhotographer"` // gated by properties importMetadata
	ImportTitle        bool   `json:"importTitle"`        // gated by properties importMetadata
}

func defaultSettings() Settings {
	return Settings{LanAccess: true, ImportPhotographer: true, ImportTitle: true}
}

func (s *server) settingsPath() string { return filepath.Join(s.baseDir, "settings.json") }

// loadSettings reads settings.json, seeding defaults on first run. Unmarshaling into
// a defaults-populated struct means settings a newer version added keep their default.
func (s *server) loadSettings() {
	st := defaultSettings()
	data, err := os.ReadFile(s.settingsPath())
	if err != nil {
		s.settings = st
		_ = s.saveSettings()
		return
	}
	if json.Unmarshal(data, &st) != nil {
		st = defaultSettings()
	}
	s.settings = st
	s.sanitizeSettings()
}

func (s *server) saveSettings() error {
	b, _ := json.MarshalIndent(s.settings, "", "  ")
	return os.WriteFile(s.settingsPath(), b, 0o644)
}

func (s *server) sanitizeSettings() {
	h := strings.TrimSpace(s.settings.PDFHeader)
	if len([]rune(h)) > pdfHeaderMax {
		h = string([]rune(h)[:pdfHeaderMax])
	}
	s.settings.PDFHeader = h
}

// Effective values combine the properties gate with the setting (properties false
// wins). Callers hold s.mu (or run at startup).
func (s *server) effLanAccess() bool          { return s.propsLanAccess && s.settings.LanAccess }
func (s *server) effImportPhotographer() bool { return s.propsImportMetadata && s.settings.ImportPhotographer }
func (s *server) effImportTitle() bool        { return s.propsImportMetadata && s.settings.ImportTitle }

// ---- logo library ---------------------------------------------------------

func (s *server) logoDir() string { return filepath.Join(s.baseDir, "logo") }

// listLogos returns the image filenames in logo\, sorted.
func (s *server) listLogos() []string {
	entries, err := os.ReadDir(s.logoDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && imageExts[strings.ToLower(filepath.Ext(e.Name()))] {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// refreshLogo resolves the active logo (settings.ActiveLogo if it still exists, else
// the first logo, else none) into s.logoFile. Caller holds s.mu.
func (s *server) refreshLogo() {
	logos := s.listLogos()
	pick := ""
	if a := s.settings.ActiveLogo; a != "" {
		for _, l := range logos {
			if l == a {
				pick = a
				break
			}
		}
	}
	if pick == "" && len(logos) > 0 {
		pick = logos[0]
	}
	if pick != "" {
		s.logoFile = filepath.Join(s.logoDir(), pick)
	} else {
		s.logoFile = ""
	}
}

// pushAllScreens re-pushes every output window (e.g. after the logo changes so title
// cards refresh). Acquires its own locking via pushScreen.
func (s *server) pushAllScreens() {
	s.mu.Lock()
	var names []string
	for n := range s.screens {
		names = append(names, n)
	}
	s.mu.Unlock()
	for _, n := range names {
		s.pushScreen(n)
	}
}

// ---- handlers -------------------------------------------------------------

// handleSettings: GET returns current settings + context; POST saves new settings.
func (s *server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var body Settings
		if decode(r, &body) != nil {
			http.Error(w, "bad body", 400)
			return
		}
		s.mu.Lock()
		// Preserve fields the client shouldn't overwrite is unnecessary — the page
		// always sends the full set. Just sanitize and apply.
		s.settings = body
		s.sanitizeSettings()
		s.refreshLogo()
		_ = s.saveSettings()
		s.mu.Unlock()
		s.pushConsole()     // scoring page picks up lockScorekeeper, etc.
		s.pushAllScreens()  // title cards may need the (possibly new) active logo
	}
	s.mu.Lock()
	resp := map[string]any{
		"settings":            s.settings,
		"logos":               s.listLogos(),
		"propsLanAccess":      s.propsLanAccess,
		"propsImportMetadata": s.propsImportMetadata,
		"effLanAccess":        s.effLanAccess(),
		"boundLanAccess":      s.lanAccess, // what the running server actually bound with
	}
	s.mu.Unlock()
	writeJSON(w, resp)
}

// handleLogoUpload saves uploaded image(s) into logo\ and (if none was active) makes
// the first one active.
func (s *server) handleLogoUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "bad upload", 400)
		return
	}
	if err := os.MkdirAll(s.logoDir(), 0o755); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	var saved, skipped []string
	for _, fh := range r.MultipartForm.File["files"] {
		base := filepath.Base(fh.Filename)
		if base == "" || strings.Contains(base, "..") || !uploadExts[strings.ToLower(filepath.Ext(base))] {
			skipped = append(skipped, fh.Filename)
			continue
		}
		f, err := fh.Open()
		if err != nil {
			skipped = append(skipped, fh.Filename)
			continue
		}
		name := uniqueName(s.logoDir(), base)
		err = saveReader(f, filepath.Join(s.logoDir(), name))
		f.Close()
		if err == nil {
			saved = append(saved, name)
		} else {
			skipped = append(skipped, fh.Filename)
		}
	}
	s.mu.Lock()
	if s.settings.ActiveLogo == "" && len(saved) > 0 {
		s.settings.ActiveLogo = saved[0]
		_ = s.saveSettings()
	}
	s.refreshLogo()
	s.mu.Unlock()
	s.pushAllScreens()
	writeJSON(w, map[string]any{"saved": saved, "skipped": skipped, "logos": s.listLogos()})
}

// handleLogoDelete removes a logo from logo\ (clearing it as active if it was).
func (s *server) handleLogoDelete(w http.ResponseWriter, r *http.Request) {
	var body struct{ File string }
	if decode(r, &body) != nil || !safeName(body.File) {
		http.Error(w, "bad request", 400)
		return
	}
	s.mu.Lock()
	err := os.Remove(filepath.Join(s.logoDir(), body.File))
	if s.settings.ActiveLogo == body.File {
		s.settings.ActiveLogo = ""
		_ = s.saveSettings()
	}
	s.refreshLogo()
	s.mu.Unlock()
	if err != nil && !os.IsNotExist(err) {
		http.Error(w, err.Error(), 500)
		return
	}
	s.pushAllScreens()
	writeJSON(w, map[string]any{"logos": s.listLogos()})
}
