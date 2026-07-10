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
	"time"
)

const pdfHeaderMax = 90 // characters — kept short enough to stay within ~2 PDF lines

// Settings holds the operator-tunable options. LanAccess / ImportPhotographer /
// ImportTitle are the SETTINGS side of a gate with the properties file.
type Settings struct {
	LockScorekeeper     bool   `json:"lockScorekeeper"`     // force the scoring page to follow the operator
	SingleLiveScreen    bool   `json:"singleLiveScreen"`    // revealing a screen blacks out all the others
	SpreadPhotographers bool   `json:"spreadPhotographers"` // when randomizing, keep a photographer's photos apart
	JudgeScoringEnabled bool   `json:"judgeScoringEnabled"` // master switch: judges score from their phones
	GuidedPresentation  bool   `json:"guidedPresentation"`  // console-driven guided slideshow, no scoring
	ScoreKeeperEnabled  bool   `json:"scoreKeeperEnabled"`  // guided slideshow + inline score box on the console
	ScorekeeperScreen   bool   `json:"scorekeeperScreen"`   // score/judge on the Scoring page (2nd operator) instead of inline on the console
	ActiveLogo          string `json:"activeLogo"`          // filename in logo\ to show on title cards
	PDFHeader           string `json:"pdfHeader"`           // custom heading on the PDFs ("" = "Photo Judge")
	LanAccess           bool   `json:"lanAccess"`           // gated by properties lanAccess
	ImportPhotographer  bool   `json:"importPhotographer"`  // gated by properties importMetadata
	ImportTitle         bool   `json:"importTitle"`         // gated by properties importMetadata
	// Member entries
	EntriesEnabled          bool   `json:"entriesEnabled"`          // master switch for the whole entry feature
	EntryRequireApproval    bool   `json:"entryRequireApproval"`    // when false, submissions are added straight to the session
	MaxEntriesPerCompetitor int    `json:"maxEntriesPerCompetitor"` // 0 = unlimited
	MaxEntriesPerCategory   int    `json:"maxEntriesPerCategory"`   // 0 = unlimited (per competitor)
	WifiSSID                string `json:"wifiSSID"`                // network competitors join ("" = auto-detect)
	WifiPassword            string `json:"wifiPassword"`            // password for that network
	// Debug logging (see logging.go). One file per request/event under logs\. LogLevel
	// picks how much is captured; LogMaxMB caps the folder (oldest deleted first);
	// LogAutoOffMinutes turns logging back off after a while so it can't be left on;
	// LogAlwaysErrors keeps errors flowing even when the master switch is off.
	LoggingEnabled    bool   `json:"loggingEnabled"`    // master switch for debug logging
	LogLevel          string `json:"logLevel"`          // "events" | "requests" | "all" | "payloads"
	LogMaxMB          int    `json:"logMaxMB"`          // cap on the logs\ folder in MB (oldest deleted first)
	LogAutoOffMinutes int    `json:"logAutoOffMinutes"` // auto-turn-off after N minutes (0 = never)
	LogAlwaysErrors   bool   `json:"logAlwaysErrors"`   // log errors even when LoggingEnabled is off
	LogEnabledAt      int64  `json:"logEnabledAt"`      // unix seconds logging was switched on (server-managed; drives auto-off)
}

func defaultSettings() Settings {
	return Settings{
		LanAccess: true, ImportPhotographer: true, ImportTitle: true,
		EntriesEnabled: true, EntryRequireApproval: true,
		LogLevel: "requests", LogMaxMB: 20, LogAutoOffMinutes: 120, LogAlwaysErrors: true,
	}
}

// Log detail levels, least to most verbose. levelEvents keeps only the app's own notable
// events and errors; levelRequests adds a per-request access line for the meaningful calls
// but skips the high-frequency noise; levelAll logs everything (including the SSE/poll/
// photo traffic); levelPayloads is levelAll plus the data body sent with each request
// (secrets masked). Errors are captured at every level.
const (
	logLevelEvents   = "events"
	logLevelRequests = "requests"
	logLevelAll      = "all"
	logLevelPayloads = "payloads"
)

// presentationMode reports whether any console-driven presentation mode is on. When it
// is, the operator drives a guided run from the console (Start/Pause/End) instead of
// free-form screen control. Guided presentation is the no-scoring base; Score Keeper and
// Judge scoring add inline scoring/judging and are mutually exclusive.
func (st Settings) presentationMode() bool {
	return st.GuidedPresentation || st.ScoreKeeperEnabled || st.JudgeScoringEnabled
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
	// Score Keeper and Judge scoring are mutually exclusive — if both arrive set (or are
	// loaded that way from an old file), Judge scoring wins.
	if s.settings.ScoreKeeperEnabled && s.settings.JudgeScoringEnabled {
		s.settings.ScoreKeeperEnabled = false
	}
	h := strings.TrimSpace(s.settings.PDFHeader)
	if len([]rune(h)) > pdfHeaderMax {
		h = string([]rune(h)[:pdfHeaderMax])
	}
	s.settings.PDFHeader = h
	if s.settings.MaxEntriesPerCompetitor < 0 {
		s.settings.MaxEntriesPerCompetitor = 0
	}
	if s.settings.MaxEntriesPerCategory < 0 {
		s.settings.MaxEntriesPerCategory = 0
	}
	s.settings.WifiSSID = strings.TrimSpace(s.settings.WifiSSID)
	if len(s.settings.WifiSSID) > 64 {
		s.settings.WifiSSID = s.settings.WifiSSID[:64]
	}
	if len(s.settings.WifiPassword) > 64 {
		s.settings.WifiPassword = s.settings.WifiPassword[:64]
	}
	// Debug logging: keep the level to a known value and the numbers in sane bounds.
	switch s.settings.LogLevel {
	case logLevelEvents, logLevelRequests, logLevelAll, logLevelPayloads:
	default:
		s.settings.LogLevel = logLevelRequests
	}
	if s.settings.LogMaxMB < 1 {
		s.settings.LogMaxMB = 1
	}
	if s.settings.LogMaxMB > 5000 {
		s.settings.LogMaxMB = 5000
	}
	if s.settings.LogAutoOffMinutes < 0 {
		s.settings.LogAutoOffMinutes = 0
	}
	if s.settings.LogAutoOffMinutes > 100000 {
		s.settings.LogAutoOffMinutes = 100000
	}
	if !s.settings.LoggingEnabled {
		s.settings.LogEnabledAt = 0 // cleared while off so re-enabling starts a fresh timer
	}
}

// Effective values combine the properties gate with the setting (properties false
// wins). Callers hold s.mu (or run at startup).
func (s *server) effLanAccess() bool { return s.propsLanAccess && s.settings.LanAccess }
func (s *server) effImportPhotographer() bool {
	return s.propsImportMetadata && s.settings.ImportPhotographer
}
func (s *server) effImportTitle() bool { return s.propsImportMetadata && s.settings.ImportTitle }

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
		// LogEnabledAt is server-managed (the client never sends a meaningful value):
		// stamp "now" only on an off→on transition so re-enabling starts the auto-off
		// timer fresh, and preserve it while logging stays on so saving other settings
		// doesn't reset the countdown.
		wasLogging, prevEnabledAt := s.settings.LoggingEnabled, s.settings.LogEnabledAt
		// Preserve fields the client shouldn't overwrite is unnecessary — the page
		// always sends the full set. Just sanitize and apply.
		s.settings = body
		s.sanitizeSettings()
		if s.settings.LoggingEnabled {
			if wasLogging && prevEnabledAt > 0 {
				s.settings.LogEnabledAt = prevEnabledAt
			} else {
				s.settings.LogEnabledAt = time.Now().Unix()
			}
		}
		s.applyLogConfig() // push the new logging config to the hot-path atomics
		s.refreshLogo()
		if !s.settings.EntriesEnabled {
			s.entryOpen = false // turning the feature off closes any open entry form
		}
		if !s.settings.presentationMode() {
			// Turning every presentation mode off tears down any active guided run so the
			// screens return to normal free-form control.
			s.solo, s.runPaused = nil, false
			s.judgeActive, s.judgeSessionID = false, ""
		}
		_ = s.saveSettings()
		s.mu.Unlock()
		s.pushConsole()    // scoring page picks up lockScorekeeper, etc.
		s.pushAllScreens() // title cards (and Entry-QR screens) may need new logo/Wi-Fi
		s.pushEntry()      // entry/landing pages pick up new limits/Wi-Fi
	}
	s.mu.Lock()
	resp := map[string]any{
		"settings":             s.settings,
		"logos":                s.listLogos(),
		"propsLanAccess":       s.propsLanAccess,
		"propsImportMetadata":  s.propsImportMetadata,
		"effLanAccess":         s.effLanAccess(),
		"boundLanAccess":       s.lanAccess, // what the running server actually bound with
		"detectedWifiSSID":     s.detectedWifiSSID,
		"detectedWifiPassword": s.detectedWifiPassword,
		"logDir":               s.logDir(), // absolute folder the log files live in
		"logActive":            s.settings.LoggingEnabled && !s.logExpired(),
		"logRemainingSeconds":  s.logRemainingSeconds(), // -1 = no auto-off, else seconds left
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
