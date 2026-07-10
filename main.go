// Photo Judge — portable local app for presenting competition photos to judges
// across orientation-fixed monitors, driven from a private operator console.
//
// Skeleton scope: portable (paths resolved next to the exe), first-run self-seed
// of categories.txt + photos\, session scan/create/select, categories.txt driving
// the menu, and named output windows you can load a category onto, step through,
// black out, and "make live". Upload/reorder and session edit/delete come next.
//
// Standard library only, so it builds offline into a single self-contained exe.
package main

import (
	"context"
	"embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg" // registered so image.DecodeConfig can read JPEG/PNG dimensions
	_ "image/png"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed web
var webFS embed.FS

// gettingStartedFS holds the Getting Started screenshots, baked into the binary so
// the page works on a copied, offline exe like everything else.
//
//go:embed getting-started-images
var gettingStartedFS embed.FS

// appVersion is baked in from the VERSION file at build time (single source of
// truth, MAJOR.RELEASE.FEATURES.PATCH). See the "Versioning" section in README.md.
//
//go:embed VERSION
var versionRaw string

var appVersion = strings.TrimSpace(versionRaw)

var defaultCategories = []string{
	"Pictorial",
	"Wildlife",
	"Altered Reality",
	"Portraiture",
	"Macro",
	"Landscapes, Cityscapes, and Travel",
	"Black and White",
}

// imageExts is what the app will list/serve from the photos tree (kept broad so
// anything already on disk still shows). uploadExts is the stricter set accepted
// for new uploads — JPG/PNG only, which keeps dimension/EXIF reading simple.
var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".bmp": true,
}

var uploadExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true,
}

// ---- data types -----------------------------------------------------------

type Session struct {
	ID          string   `json:"id"`                    // "001" — stable folder name, never changes
	Date        string   `json:"date"`                  // human label, freely editable
	Description string   `json:"description,omitempty"` // optional free-text note so an operator can tell sessions apart
	Created     string   `json:"created"`               // RFC3339
	Categories  []string `json:"categories"`            // ACTIVE categories, in display order
	// InactiveCategories are this session's deactivated categories (shown
	// alphabetically in the manager). Omitted from older session.json files, which
	// load as an empty set.
	InactiveCategories []string `json:"inactiveCategories,omitempty"`
	// WinThreshold is the score at/above which a photo is a "winner" (eligible for the
	// annual competition). nil = unset → this session has no winners. MaxPoints is the
	// total a photo is scored out of (e.g. 11 of 15). Both are per-session settings,
	// pointers so "unset" is distinct from 0. New sessions default to 11 / 15.
	WinThreshold *float64 `json:"winThreshold,omitempty"`
	MaxPoints    *float64 `json:"maxPoints,omitempty"`
	// Solo operator mode: one person drives the slideshow from the Scoring page. These
	// say which screen presents each orientation and which orientation a category shows
	// first. SoloFirst is "Landscape" (default) or "Portrait". Per session, omitempty.
	SoloEnabled         bool   `json:"soloEnabled,omitempty"`
	SoloLandscapeScreen string `json:"soloLandscapeScreen,omitempty"`
	SoloPortraitScreen  string `json:"soloPortraitScreen,omitempty"`
	SoloFirst           string `json:"soloFirst,omitempty"`
	// SoloEnd is what happens after the last category: "" = show "complete" and wait,
	// "loop" = restart from the first category, "close" = black the monitors and end.
	SoloEnd string `json:"soloEnd,omitempty"`
	// Judge scoring config (per session). JudgeAggregation is "average" (default) or
	// "total"; JudgesNeeded is how many scores make a photo "complete". Alternate enables
	// a backup judge; Autodetect routes a photo by a judge to the alternate automatically;
	// ShowPhotographer reveals the photographer to judges (off = impartial). Min/Max/
	// Increment constrain the score box (pointers: nil = unset).
	JudgeAggregation      string   `json:"judgeAggregation,omitempty"`
	JudgesNeeded          int      `json:"judgesNeeded,omitempty"`
	JudgeAnonymize        bool     `json:"judgeAnonymize,omitempty"`
	JudgeAlternate        bool     `json:"judgeAlternate,omitempty"`
	JudgeAutodetect       bool     `json:"judgeAutodetect,omitempty"`
	JudgeShowPhotographer bool     `json:"judgeShowPhotographer,omitempty"`
	JudgeMin              *float64 `json:"judgeMin,omitempty"`
	JudgeMax              *float64 `json:"judgeMax,omitempty"`
	JudgeIncrement        *float64 `json:"judgeIncrement,omitempty"`
	// Presentation lifecycle (set when the operator runs a guided session from the
	// console). StartedAt is stamped on the first Start; EndedAt is stamped on End and,
	// once set, locks the session's scores (read-only). Both RFC3339, omitempty.
	StartedAt string `json:"startedAt,omitempty"`
	EndedAt   string `json:"endedAt,omitempty"`
}

// locked reports whether the session's scores are frozen because its judging/scoring
// run has been ended.
func (ss *Session) locked() bool { return ss != nil && ss.EndedAt != "" }

// soloScreenFor returns the screen assigned to present the given orientation.
func (ss *Session) soloScreenFor(orient string) string {
	if orient == "Portrait" {
		return ss.SoloPortraitScreen
	}
	return ss.SoloLandscapeScreen
}

// soloOrientations returns the two orientations in presentation order (the configured
// first, then the other).
func (ss *Session) soloOrientations() []string {
	if ss.SoloFirst == "Portrait" {
		return []string{"Portrait", "Landscape"}
	}
	return []string{"Landscape", "Portrait"}
}

// isWinner reports whether a photo's score (a free-text string) reaches this session's
// win threshold. Only numeric scores can win, and only when a threshold is set.
func (ss *Session) isWinner(score string) bool {
	return ss != nil && scoreWins(ss.WinThreshold, score)
}

// scoreWins reports whether score (free text) reaches threshold. nil threshold or a
// non-numeric score never wins. Shared by live sessions and archived ones.
func scoreWins(threshold *float64, score string) bool {
	if threshold == nil {
		return false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(score), 64)
	return err == nil && v >= *threshold
}

func floatPtr(v float64) *float64 { return &v }

// Screen is the live state of one output window. Position: 0 = title card,
// 1..Count = photo n, Count+1 = end black. Blackout is an independent overlay.
type Screen struct {
	Name        string `json:"name"`
	Type        string `json:"type"` // "slideshow" (default/"") | "entry" — shows the member-entry QR
	SessionID   string `json:"sessionId"`
	Category    string `json:"category"`
	Orientation string `json:"orientation"`
	Position    int    `json:"position"`
	Count       int    `json:"count"`
	Blackout    bool   `json:"blackout"`
	// Files is the ordered filename list captured at load time. Not persisted:
	// photos are addressed by stable filename so URLs cache correctly across reorders.
	Files []string `json:"-"`
}

// View is what an output window should render right now.
type View struct {
	Mode        string `json:"mode"` // idle | black | title | photo | entry
	Category    string `json:"category"`
	Orientation string `json:"orientation"`
	PhotoURL    string `json:"photoUrl"`
	LogoURL     string `json:"logoUrl,omitempty"` // set on title views when a logo exists
	Position    int    `json:"position"`
	Count       int    `json:"count"`
	// Entry-QR screens (Mode == "entry") carry the details the output window needs to
	// render the join-Wi-Fi + scan-to-enter instructions. Omitted for normal modes.
	EntryURL     string `json:"entryUrl,omitempty"`     // page competitors open (empty = LAN access off)
	EntryOpen    bool   `json:"entryOpen,omitempty"`    // false = "entries are closed" banner
	WifiSSID     string `json:"wifiSSID,omitempty"`     // network name to join (if known)
	WifiPassword string `json:"wifiPassword,omitempty"` // network password (if known)
	WifiQR       string `json:"wifiQR,omitempty"`       // WIFI: join string for a scannable QR
	// Judge-QR screens (Mode == "judge"): page judges open + whether judge scoring is on.
	JudgeURL string `json:"judgeUrl,omitempty"`
	JudgeOn  bool   `json:"judgeOn,omitempty"`
}

// ---- SSE hub --------------------------------------------------------------

type hub struct {
	mu       sync.Mutex
	consoles map[chan []byte]bool
	outputs  map[string]map[chan []byte]bool
	entries  map[chan []byte]bool // landing + member-entry pages (role=entry)
	judges   map[chan []byte]bool // judge phone pages (role=judge)
}

func newHub() *hub {
	return &hub{consoles: map[chan []byte]bool{}, outputs: map[string]map[chan []byte]bool{}, entries: map[chan []byte]bool{}, judges: map[chan []byte]bool{}}
}
func (h *hub) addConsole(ch chan []byte)    { h.mu.Lock(); h.consoles[ch] = true; h.mu.Unlock() }
func (h *hub) removeConsole(ch chan []byte) { h.mu.Lock(); delete(h.consoles, ch); h.mu.Unlock() }
func (h *hub) addEntry(ch chan []byte)      { h.mu.Lock(); h.entries[ch] = true; h.mu.Unlock() }
func (h *hub) removeEntry(ch chan []byte)   { h.mu.Lock(); delete(h.entries, ch); h.mu.Unlock() }
func (h *hub) addJudge(ch chan []byte)      { h.mu.Lock(); h.judges[ch] = true; h.mu.Unlock() }
func (h *hub) removeJudge(ch chan []byte)   { h.mu.Lock(); delete(h.judges, ch); h.mu.Unlock() }
func (h *hub) addOutput(name string, ch chan []byte) {
	h.mu.Lock()
	if h.outputs[name] == nil {
		h.outputs[name] = map[chan []byte]bool{}
	}
	h.outputs[name][ch] = true
	h.mu.Unlock()
}
func (h *hub) removeOutput(name string, ch chan []byte) {
	h.mu.Lock()
	if set := h.outputs[name]; set != nil {
		delete(set, ch)
	}
	h.mu.Unlock()
}
func (h *hub) sendConsole(data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.consoles {
		select {
		case ch <- data:
		default:
		}
	}
}
func (h *hub) sendOutput(name string, data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.outputs[name] {
		select {
		case ch <- data:
		default:
		}
	}
}
func (h *hub) sendEntries(data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.entries {
		select {
		case ch <- data:
		default:
		}
	}
}
func (h *hub) sendJudges(data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.judges {
		select {
		case ch <- data:
		default:
		}
	}
}

// ---- server ---------------------------------------------------------------

type server struct {
	baseDir    string
	mu         sync.Mutex
	categories []string
	logoFile   string // optional brand logo for title cards ("" = none)
	sessions   []*Session
	screens    map[string]*Screen
	h          *hub

	// newerVersion is set (guarded by mu) when a newer build of the exe is launched
	// while this one is running; the console shows an "update available" banner.
	newerVersion string

	// port is the TCP port the server actually bound to (set once at startup). Used
	// to build the LAN URLs shown on the console for remote control.
	port string

	// lanAccess is the EFFECTIVE value the server bound with (properties AND settings).
	// When false the server is bound to loopback only and the console hides the LAN bar.
	lanAccess bool

	// propsLanAccess / propsImportMetadata are the properties-file values. They act as
	// a ceiling over the matching Settings fields (a "false" here wins).
	propsLanAccess      bool
	propsImportMetadata bool

	// settings holds the operator-tunable options (settings.json), guarded by mu.
	settings Settings

	// exportPageSize is the default page size for the export picker's session list,
	// from photo-judge.properties (exportPageSize). Set once at startup.
	exportPageSize int

	// selectedSessionID is the session the operator console currently has selected.
	// Tracked server-side so the Session Management page can open to it (guarded by mu).
	selectedSessionID string

	// solo is the active guided presentation run (nil = not running), driving the
	// configured screens through each category's orientations. Shared by all presentation
	// modes (guided/score-keeper/judge). runPaused suspends it so the operator can
	// reconfigure screens without ending the run. Guarded by mu. See solo.go / presentation.go.
	solo      *soloRun
	runPaused bool

	// Judge scoring state (guarded by mu). judgeActive gates a running judging session
	// (locked to judgeSessionID): until it's started the slideshow stays black, the
	// operator can't drive it, and judges can't score. judgeRoster tracks connected
	// judges by name key; deferred/requested are transient per-photo flags keyed by
	// "<target>|<judgeKey>". See judge.go.
	judgeActive    bool
	judgeSessionID string
	judgeRoster    map[string]*judgeRosterEntry
	judgeDeferred  map[string]bool
	judgeRequested map[string]bool

	// Member-entry state (guarded by mu). When entryOpen is true, competitors may
	// submit photos to entrySessionID — the session locked in when the form was opened.
	entryOpen      bool
	entrySessionID string

	// detectedWifiSSID/Password are the host's current Wi-Fi, best-effort detected at
	// startup (netsh). Used as a fallback when the operator leaves the Settings Wi-Fi
	// fields blank, and surfaced as placeholders on the Settings page.
	detectedWifiSSID     string
	detectedWifiPassword string

	// Debug logging (see logging.go). The atomics are the hot-path snapshot read on
	// every request without taking s.mu; applyLogConfig refreshes them from settings.
	// logMu guards the on-disk index (logIndex, oldest-first day files) and its running
	// byte total used to enforce the size cap, plus the currently-open day file.
	logDebugOn   atomic.Bool  // logging master switch is on AND not auto-expired
	logLevelV    atomic.Int32 // 0 events, 1 requests, 2 all
	logAlwaysErr atomic.Bool  // capture errors even when logDebugOn is false
	logMaxBytes  atomic.Int64 // folder cap in bytes
	logMu        sync.Mutex
	logIndex     []logEntry // day files, oldest-first (name sorts chronologically)
	logTotal     int64      // sum of logIndex sizes
	logCurDay    string     // date (2006-01-02) of the currently-open day file
	logCurFile   *os.File   // append handle for today's log file (nil until first write)

	shutdownCh   chan struct{}
	shutdownOnce sync.Once
}

func (s *server) photosDir(sid, cat, orient string) string {
	return filepath.Join(s.baseDir, "photos", sid, cat, orient)
}

func (s *server) photoFiles(sid, cat, orient string) []string {
	dir := s.photosDir(sid, cat, orient)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if imageExts[strings.ToLower(filepath.Ext(e.Name()))] {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	return applyOrder(dir, files)
}

// applyOrder honours an order.json (array of filenames) if present, appending
// any files not listed in it. Missing/invalid order.json falls back to name sort.
func applyOrder(dir string, files []string) []string {
	data, err := os.ReadFile(filepath.Join(dir, "order.json"))
	if err != nil {
		return files
	}
	var order []string
	if json.Unmarshal(data, &order) != nil {
		return files
	}
	present := map[string]bool{}
	for _, f := range files {
		present[f] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, f := range order {
		if present[f] && !seen[f] {
			out = append(out, f)
			seen[f] = true
		}
	}
	for _, f := range files {
		if !seen[f] {
			out = append(out, f)
		}
	}
	return out
}

// buildView assumes s.mu is held. No disk IO (Count is precomputed at load).
func (s *server) buildView(sc *Screen) View {
	if sc.Type == "entry" {
		return s.entryView()
	}
	if sc.Type == "judge" {
		return s.judgeView()
	}
	// While a presentation mode is on but no guided run is active, the slideshow stays
	// black — the operator can't present and nobody can score until they Start. Once a run
	// is active the guided engine controls per-screen reveal/blackout (below).
	if s.settings.presentationMode() && s.solo == nil {
		return View{Mode: "black", Category: sc.Category, Orientation: sc.Orientation, Position: sc.Position, Count: sc.Count}
	}
	if sc.Category == "" {
		return View{Mode: "idle"}
	}
	if sc.Blackout {
		return View{Mode: "black", Category: sc.Category, Orientation: sc.Orientation, Position: sc.Position, Count: sc.Count}
	}
	if sc.Position <= 0 {
		v := View{Mode: "title", Category: sc.Category, Orientation: sc.Orientation, Position: 0, Count: sc.Count}
		if s.logoFile != "" {
			v.LogoURL = "/api/logo"
		}
		return v
	}
	if sc.Position > sc.Count {
		return View{Mode: "black", Category: sc.Category, Orientation: sc.Orientation, Position: sc.Position, Count: sc.Count}
	}
	// Address the photo by stable filename (not ordinal position): position-keyed
	// URLs map to different files after a reorder, and the browser would serve the
	// stale cached image for that URL. Filename URLs stay unique and cache correctly.
	if sc.Position-1 >= len(sc.Files) {
		return View{Mode: "black", Category: sc.Category, Orientation: sc.Orientation, Position: sc.Position, Count: sc.Count}
	}
	u := fmt.Sprintf("/api/photo?session=%s&category=%s&orientation=%s&file=%s",
		url.QueryEscape(sc.SessionID), url.QueryEscape(sc.Category), url.QueryEscape(sc.Orientation), url.QueryEscape(sc.Files[sc.Position-1]))
	return View{Mode: "photo", Category: sc.Category, Orientation: sc.Orientation, PhotoURL: u, Position: sc.Position, Count: sc.Count}
}

func (s *server) pushScreen(name string) {
	s.mu.Lock()
	sc := s.screens[name]
	if sc == nil {
		s.mu.Unlock()
		return
	}
	v := s.buildView(sc)
	s.mu.Unlock()
	data, _ := json.Marshal(v)
	s.h.sendOutput(name, data)
}

func (s *server) consoleSnapshot() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Empty (not nil) slices so these serialize as [] rather than JSON null. The console
	// frontend dereferences state.sessions/.screens/.categories as arrays (e.g.
	// state.screens.length), so a fresh app with no sessions/screens yet would otherwise
	// crash render() — e.g. right after an import onto an app that has no screens.
	screens := []*Screen{}
	for _, sc := range s.screens {
		screens = append(screens, sc)
	}
	sort.Slice(screens, func(i, j int) bool { return screens[i].Name < screens[j].Name })
	sessions := s.sessions
	if sessions == nil {
		sessions = []*Session{}
	}
	categories := s.categories
	if categories == nil {
		categories = []string{}
	}
	payload := struct {
		Version           string         `json:"version"`
		NewerVersion      string         `json:"newerVersion,omitempty"`
		Sessions          []*Session     `json:"sessions"`
		Categories        []string       `json:"categories"`
		Screens           []*Screen      `json:"screens"`
		Settings          Settings       `json:"settings"`
		EntryOpen         bool           `json:"entryOpen"`
		EntrySessionID    string         `json:"entrySessionId,omitempty"`
		PendingCount      int            `json:"pendingCount"`
		SelectedSessionID string         `json:"selectedSessionId,omitempty"`
		Solo              *soloView      `json:"solo,omitempty"`
		Judges            map[string]any `json:"judges,omitempty"`
	}{appVersion, s.newerVersion, sessions, categories, screens, s.settings,
		s.entryOpen, s.entrySessionID, s.pendingCount(s.entrySessionID), s.selectedSessionID, s.soloViewLocked(),
		s.judgeConsoleSnapshotLocked()}
	data, _ := json.Marshal(payload)
	return data
}

func (s *server) pushConsole() { s.h.sendConsole(s.consoleSnapshot()) }

// ---- startup: categories + sessions ---------------------------------------

func (s *server) loadCategories() {
	path := filepath.Join(s.baseDir, "categories.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		var b strings.Builder
		b.WriteString("# categories.txt — seeds the FIRST session's categories (one per line).\n")
		b.WriteString("# After that, manage categories per session in the app (\"Manage categories\").\n")
		b.WriteString("# These names appear on the title cards the judges see, so spelling counts.\n")
		for _, c := range defaultCategories {
			b.WriteString(c + "\n")
		}
		_ = os.WriteFile(path, []byte(b.String()), 0o644)
		s.categories = append([]string{}, defaultCategories...)
		log.Printf("seeded default categories.txt (%d categories)", len(s.categories))
		return
	}
	var cats []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cats = append(cats, line)
	}
	s.categories = cats
}

// handleLogo serves the active title-card logo, or a specific one from the library
// when ?file=<name> is given (used by the Settings page gallery).
func (s *server) handleLogo(w http.ResponseWriter, r *http.Request) {
	if name := r.URL.Query().Get("file"); name != "" {
		if !safeName(name) {
			http.Error(w, "bad file", 400)
			return
		}
		path := filepath.Join(s.logoDir(), name)
		if _, err := os.Stat(path); err != nil {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, path)
		return
	}
	s.mu.Lock()
	lf := s.logoFile
	s.mu.Unlock()
	if lf == "" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, lf)
}

func (s *server) scanSessions() {
	entries, err := os.ReadDir(filepath.Join(s.baseDir, "photos"))
	if err != nil {
		return
	}
	var sess []*Session
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.baseDir, "photos", e.Name(), "session.json"))
		if err != nil {
			continue
		}
		var ss Session
		if json.Unmarshal(data, &ss) == nil {
			sess = append(sess, &ss)
		}
	}
	sort.Slice(sess, func(i, j int) bool { return sess[i].Date < sess[j].Date })
	s.sessions = sess
	log.Printf("loaded %d existing session(s)", len(sess))
}

// nextID = max-ever + 1, counting soft-deleted AND archived sessions, so IDs are
// never reused even after an archived session's photo folder has been removed.
func (s *server) nextID() string {
	max := 0
	bump := func(name string) {
		if n, err := strconv.Atoi(name); err == nil && n > max {
			max = n
		}
	}
	checkDirs := func(dir string) {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.IsDir() {
				bump(e.Name())
			}
		}
	}
	checkDirs(filepath.Join(s.baseDir, "photos"))
	checkDirs(filepath.Join(s.baseDir, "photos", "_deleted"))
	// Archived sessions live only as archives/<id>.json — the photo folders are gone.
	for _, e := range readDirNames(s.archivesDir()) {
		bump(strings.TrimSuffix(e, ".json"))
	}
	return fmt.Sprintf("%03d", max+1)
}

func (s *server) createSession(date, description string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextID()
	// A new session inherits the most recent session's category slate — the active
	// order and the inactive set — so the operator's setup carries forward. The very
	// first session falls back to the categories.txt / built-in default seed.
	var active, inactive []string
	// Scoring settings carry forward from the latest session; the very first session
	// falls back to the club's usual 11-of-15 default.
	win, max := floatPtr(11), floatPtr(15)
	latest := s.latestSession()
	if latest != nil {
		active = append([]string{}, latest.Categories...)
		inactive = append([]string{}, latest.InactiveCategories...)
		win, max = latest.WinThreshold, latest.MaxPoints
	} else {
		active = append([]string{}, s.categories...)
	}
	ss := &Session{ID: id, Date: date, Description: strings.TrimSpace(description), Created: time.Now().Format(time.RFC3339), Categories: active, InactiveCategories: inactive, WinThreshold: win, MaxPoints: max}
	if latest != nil { // carry the per-session presentation/judging config forward
		ss.SoloEnabled, ss.SoloLandscapeScreen, ss.SoloPortraitScreen, ss.SoloFirst, ss.SoloEnd = latest.SoloEnabled, latest.SoloLandscapeScreen, latest.SoloPortraitScreen, latest.SoloFirst, latest.SoloEnd
		ss.JudgeAggregation, ss.JudgesNeeded, ss.JudgeAnonymize, ss.JudgeAlternate, ss.JudgeAutodetect, ss.JudgeShowPhotographer = latest.JudgeAggregation, latest.JudgesNeeded, latest.JudgeAnonymize, latest.JudgeAlternate, latest.JudgeAutodetect, latest.JudgeShowPhotographer
		ss.JudgeMin, ss.JudgeMax, ss.JudgeIncrement = latest.JudgeMin, latest.JudgeMax, latest.JudgeIncrement
	}
	base := filepath.Join(s.baseDir, "photos", id)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return nil, err
	}
	for _, c := range ss.Categories {
		if err := s.ensureCategoryDirs(id, c); err != nil {
			return nil, err
		}
	}
	b, _ := json.MarshalIndent(ss, "", "  ")
	if err := os.WriteFile(filepath.Join(base, "session.json"), b, 0o644); err != nil {
		return nil, err
	}
	s.sessions = append(s.sessions, ss)
	sort.Slice(s.sessions, func(i, j int) bool { return s.sessions[i].Date < s.sessions[j].Date })
	log.Printf("created session %s (%s) with %d categories", id, date, len(ss.Categories))
	return ss, nil
}

// ---- HTTP handlers --------------------------------------------------------

func (s *server) handleState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(s.consoleSnapshot())
}

// handleReportVersion is called by a newly-launched, newer exe that found this
// instance already running. If the reported version really is newer than ours, we
// remember it (the highest seen) and the console shows an "update available" banner.
func (s *server) handleReportVersion(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Version string `json:"version"`
	}
	if decode(r, &body) != nil || !newerVer(body.Version, appVersion) {
		w.WriteHeader(204) // ignore anything not strictly newer than us
		return
	}
	s.mu.Lock()
	changed := s.newerVersion == "" || newerVer(body.Version, s.newerVersion)
	if changed {
		s.newerVersion = body.Version
	}
	s.mu.Unlock()
	if changed {
		log.Printf("a newer version (v%s) was launched — advising restart in the console", body.Version)
		s.pushConsole()
	}
	w.WriteHeader(204)
}

func (s *server) handleSessionCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Date        string `json:"date"`
		Description string `json:"description"`
	}
	if decode(r, &body) != nil || !validDate(body.Date) {
		http.Error(w, "date must be YYYY-MM-DD", 400)
		return
	}
	ss, err := s.createSession(body.Date, body.Description)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.pushConsole()
	writeJSON(w, ss)
}

func (s *server) sessionByID(id string) *Session {
	for _, ss := range s.sessions {
		if ss.ID == id {
			return ss
		}
	}
	return nil
}

// screensUsing lists screens that currently have this session loaded. Assumes s.mu held.
func (s *server) screensUsing(id string) []string {
	var names []string
	for _, sc := range s.screens {
		if sc.SessionID == id {
			names = append(names, sc.Name)
		}
	}
	sort.Strings(names)
	return names
}

// handleSessionEdit changes the date label and description (session.json). The ID and
// folder are untouched, so there's no rename and nothing else has to move.
func (s *server) handleSessionEdit(w http.ResponseWriter, r *http.Request) {
	var body struct{ ID, Date, Description string }
	if decode(r, &body) != nil || !safeName(body.ID) {
		http.Error(w, "bad request", 400)
		return
	}
	if !validDate(body.Date) {
		http.Error(w, "date must be YYYY-MM-DD", 400)
		return
	}
	s.mu.Lock()
	ss := s.sessionByID(body.ID)
	if ss == nil {
		s.mu.Unlock()
		http.Error(w, "no such session", 404)
		return
	}
	ss.Date = body.Date
	ss.Description = strings.TrimSpace(body.Description)
	b, _ := json.MarshalIndent(ss, "", "  ")
	err := os.WriteFile(filepath.Join(s.baseDir, "photos", ss.ID, "session.json"), b, 0o644)
	sort.Slice(s.sessions, func(i, j int) bool { return s.sessions[i].Date < s.sessions[j].Date })
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	log.Printf("session %s edited (date %s)", body.ID, body.Date)
	s.pushConsole()
	writeJSON(w, ss)
}

// handleSessionSettings updates a session's per-session settings from the Session
// Management page: date, description, and the scoring fields (win threshold + total
// points). Threshold/points come in as strings; blank clears them (nil → no winners).
func (s *server) handleSessionSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID, Date, Description, WinThreshold, MaxPoints              string
		SoloEnabled                                                 bool
		SoloLandscapeScreen, SoloPortraitScreen, SoloFirst, SoloEnd string
		JudgeAggregation                                            string
		JudgesNeeded                                                int
		JudgeAnonymize, JudgeAlternate, JudgeAutodetect             bool
		JudgeShowPhotographer                                       bool
		JudgeMin, JudgeMax, JudgeIncrement                          string
	}
	if decode(r, &body) != nil || !safeName(body.ID) {
		http.Error(w, "bad request", 400)
		return
	}
	if !validDate(body.Date) {
		http.Error(w, "date must be YYYY-MM-DD", 400)
		return
	}
	win, err := parseOptFloat(body.WinThreshold)
	if err != nil {
		http.Error(w, "win threshold must be a number", 400)
		return
	}
	max, err := parseOptFloat(body.MaxPoints)
	if err != nil {
		http.Error(w, "total points must be a number", 400)
		return
	}
	jMin, e1 := parseOptFloat(body.JudgeMin)
	jMax, e2 := parseOptFloat(body.JudgeMax)
	jInc, e3 := parseOptFloat(body.JudgeIncrement)
	if e1 != nil || e2 != nil || e3 != nil {
		http.Error(w, "judge min/max/increment must be numbers", 400)
		return
	}
	s.mu.Lock()
	ss := s.sessionByID(body.ID)
	if ss == nil {
		s.mu.Unlock()
		http.Error(w, "no such session", 404)
		return
	}
	ss.Date = body.Date
	ss.Description = strings.TrimSpace(body.Description)
	ss.WinThreshold = win
	ss.MaxPoints = max
	ss.SoloEnabled = body.SoloEnabled
	ss.SoloLandscapeScreen = strings.TrimSpace(body.SoloLandscapeScreen)
	ss.SoloPortraitScreen = strings.TrimSpace(body.SoloPortraitScreen)
	if body.SoloFirst == "Portrait" {
		ss.SoloFirst = "Portrait"
	} else {
		ss.SoloFirst = "Landscape"
	}
	if body.SoloEnd == "loop" || body.SoloEnd == "close" {
		ss.SoloEnd = body.SoloEnd
	} else {
		ss.SoloEnd = ""
	}
	if body.JudgeAggregation == "total" {
		ss.JudgeAggregation = "total"
	} else {
		ss.JudgeAggregation = "average"
	}
	if body.JudgesNeeded < 0 {
		body.JudgesNeeded = 0
	}
	ss.JudgesNeeded = body.JudgesNeeded
	ss.JudgeAnonymize, ss.JudgeAlternate, ss.JudgeAutodetect = body.JudgeAnonymize, body.JudgeAlternate, body.JudgeAutodetect
	ss.JudgeShowPhotographer = body.JudgeShowPhotographer
	ss.JudgeMin, ss.JudgeMax, ss.JudgeIncrement = jMin, jMax, jInc
	b, _ := json.MarshalIndent(ss, "", "  ")
	werr := os.WriteFile(filepath.Join(s.baseDir, "photos", ss.ID, "session.json"), b, 0o644)
	sort.Slice(s.sessions, func(i, j int) bool { return s.sessions[i].Date < s.sessions[j].Date })
	s.mu.Unlock()
	if werr != nil {
		http.Error(w, werr.Error(), 500)
		return
	}
	log.Printf("session %s settings saved (threshold=%v points=%v)", body.ID, body.WinThreshold, body.MaxPoints)
	s.pushConsole()
	writeJSON(w, ss)
}

// handleConsoleSession records the operator's currently selected session. It's the
// app-wide current session: every operator page posts here when its session dropdown
// changes, and opens to selectedSessionID (broadcast in the console snapshot) so the
// choice sticks as the operator navigates between pages.
func (s *server) handleConsoleSession(w http.ResponseWriter, r *http.Request) {
	var body struct{ SessionID string }
	if decode(r, &body) != nil {
		http.Error(w, "bad request", 400)
		return
	}
	s.mu.Lock()
	s.selectedSessionID = body.SessionID
	s.mu.Unlock()
	s.pushConsole()
	w.WriteHeader(204)
}

// parseOptFloat parses an optional numeric field: blank → nil, otherwise a *float64.
func parseOptFloat(s string) (*float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// ---- per-session category management --------------------------------------

// latestSession returns the most recently created session (highest numeric ID),
// or nil if none exist. Caller holds s.mu.
func (s *server) latestSession() *Session {
	var latest *Session
	max := -1
	for _, ss := range s.sessions {
		if n, err := strconv.Atoi(ss.ID); err == nil && n > max {
			max, latest = n, ss
		}
	}
	return latest
}

// ensureCategoryDirs creates the Landscape/Portrait folders for a category.
func (s *server) ensureCategoryDirs(sid, cat string) error {
	if err := os.MkdirAll(s.photosDir(sid, cat, "Landscape"), 0o755); err != nil {
		return err
	}
	return os.MkdirAll(s.photosDir(sid, cat, "Portrait"), 0o755)
}

// categoryUsed reports whether a category holds any photos in a session.
func (s *server) categoryUsed(sid, cat string) bool {
	return len(s.photoFiles(sid, cat, "Landscape")) > 0 || len(s.photoFiles(sid, cat, "Portrait")) > 0
}

// saveSession writes a session's session.json back to disk. Caller holds s.mu.
func (s *server) saveSession(ss *Session) error {
	b, _ := json.MarshalIndent(ss, "", "  ")
	return os.WriteFile(filepath.Join(s.baseDir, "photos", ss.ID, "session.json"), b, 0o644)
}

// categoryDetail is the manager's view of one session: the active (ordered) and
// inactive (set) lists plus which categories currently hold photos (so the UI can
// disable delete). Caller holds s.mu.
func (s *server) categoryDetail(ss *Session) map[string]any {
	used := []string{}
	for _, c := range append(append([]string{}, ss.Categories...), ss.InactiveCategories...) {
		if s.categoryUsed(ss.ID, c) {
			used = append(used, c)
		}
	}
	return map[string]any{
		"active":   append([]string{}, ss.Categories...),
		"inactive": append([]string{}, ss.InactiveCategories...),
		"used":     used,
	}
}

// validCategory guards a category name that will be used as a folder name.
func validCategory(name string) bool {
	if !safeName(name) || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
		return false
	}
	return len(name) <= 100
}

func containsFold(list []string, name string) bool {
	for _, v := range list {
		if strings.EqualFold(v, name) {
			return true
		}
	}
	return false
}

// removeFold returns list without the first case-insensitive match of name, plus
// whether one was removed.
func removeFold(list []string, name string) ([]string, bool) {
	for i, v := range list {
		if strings.EqualFold(v, name) {
			out := append([]string{}, list[:i]...)
			return append(out, list[i+1:]...), true
		}
	}
	return list, false
}

func (s *server) handleSessionCategories(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("session")
	if !safeName(id) {
		http.Error(w, "bad request", 400)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.sessionByID(id)
	if ss == nil {
		http.Error(w, "no such session", 404)
		return
	}
	writeJSON(w, s.categoryDetail(ss))
}

// categoryMutation runs the shared decode/lookup/save/push flow for the
// {session, name} category endpoints. fn mutates the session under the lock and
// returns (0, "") on success, or an (httpCode, message) to report instead.
func (s *server) categoryMutation(w http.ResponseWriter, r *http.Request, fn func(ss *Session, name string) (int, string)) {
	var body struct{ Session, Name string }
	if decode(r, &body) != nil || !safeName(body.Session) {
		http.Error(w, "bad request", 400)
		return
	}
	name := strings.TrimSpace(body.Name)
	if !safeName(name) {
		http.Error(w, "bad request", 400)
		return
	}
	s.mu.Lock()
	ss := s.sessionByID(body.Session)
	if ss == nil {
		s.mu.Unlock()
		http.Error(w, "no such session", 404)
		return
	}
	if s.solo != nil && s.solo.sessionID == ss.ID {
		s.mu.Unlock()
		http.Error(w, "a presentation is running on this session — End it first to change categories", http.StatusConflict)
		return
	}
	if code, msg := fn(ss, name); code != 0 {
		s.mu.Unlock()
		http.Error(w, msg, code)
		return
	}
	err := s.saveSession(ss)
	detail := s.categoryDetail(ss)
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.pushConsole()
	writeJSON(w, detail)
}

func (s *server) handleCategoryAdd(w http.ResponseWriter, r *http.Request) {
	s.categoryMutation(w, r, func(ss *Session, name string) (int, string) {
		if !validCategory(name) {
			return 400, "invalid category name"
		}
		if containsFold(ss.Categories, name) || containsFold(ss.InactiveCategories, name) {
			return 409, "that category already exists in this session"
		}
		if err := s.ensureCategoryDirs(ss.ID, name); err != nil {
			return 500, err.Error()
		}
		ss.Categories = append(ss.Categories, name)
		return 0, ""
	})
}

func (s *server) handleCategoryActivate(w http.ResponseWriter, r *http.Request) {
	s.categoryMutation(w, r, func(ss *Session, name string) (int, string) {
		rest, ok := removeFold(ss.InactiveCategories, name)
		if !ok {
			return 404, "category is not inactive"
		}
		if err := s.ensureCategoryDirs(ss.ID, name); err != nil {
			return 500, err.Error()
		}
		ss.InactiveCategories = rest
		ss.Categories = append(ss.Categories, name)
		return 0, ""
	})
}

func (s *server) handleCategoryDeactivate(w http.ResponseWriter, r *http.Request) {
	s.categoryMutation(w, r, func(ss *Session, name string) (int, string) {
		rest, ok := removeFold(ss.Categories, name)
		if !ok {
			return 404, "category is not active"
		}
		ss.Categories = rest
		ss.InactiveCategories = append(ss.InactiveCategories, name)
		return 0, ""
	})
}

func (s *server) handleCategoryDelete(w http.ResponseWriter, r *http.Request) {
	s.categoryMutation(w, r, func(ss *Session, name string) (int, string) {
		if s.categoryUsed(ss.ID, name) {
			return 409, "category has photos — deactivate it instead of deleting"
		}
		ra, oka := removeFold(ss.Categories, name)
		ri, oki := removeFold(ss.InactiveCategories, name)
		if !oka && !oki {
			return 404, "no such category"
		}
		ss.Categories, ss.InactiveCategories = ra, ri
		_ = os.RemoveAll(filepath.Join(s.baseDir, "photos", ss.ID, name))
		return 0, ""
	})
}

// handleCategoryReorder sets a session's active category order. The posted order
// must be a permutation of the session's current active set.
func (s *server) handleCategoryReorder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Session string
		Order   []string
	}
	if decode(r, &body) != nil || !safeName(body.Session) {
		http.Error(w, "bad request", 400)
		return
	}
	s.mu.Lock()
	ss := s.sessionByID(body.Session)
	if ss == nil {
		s.mu.Unlock()
		http.Error(w, "no such session", 404)
		return
	}
	if s.solo != nil && s.solo.sessionID == ss.ID {
		s.mu.Unlock()
		http.Error(w, "a presentation is running on this session — End it first to reorder categories", http.StatusConflict)
		return
	}
	seen := map[string]bool{}
	for _, c := range body.Order {
		if !containsFold(ss.Categories, c) {
			s.mu.Unlock()
			http.Error(w, "order must be a permutation of the active categories", 400)
			return
		}
		seen[strings.ToLower(c)] = true
	}
	if len(seen) != len(ss.Categories) {
		s.mu.Unlock()
		http.Error(w, "order must be a permutation of the active categories", 400)
		return
	}
	ss.Categories = append([]string{}, body.Order...)
	err := s.saveSession(ss)
	detail := s.categoryDetail(ss)
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.pushConsole()
	writeJSON(w, detail)
}

// handleSessionDelete soft-deletes a session by moving its folder into
// photos\_deleted (recoverable). Blocked while any screen has it loaded.
func (s *server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
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
	if using := s.screensUsing(body.ID); len(using) > 0 {
		s.mu.Unlock()
		http.Error(w, "Session is loaded on screen(s): "+strings.Join(using, ", ")+". Load a different category there first.", 409)
		return
	}
	src := filepath.Join(s.baseDir, "photos", ss.ID)
	deletedDir := filepath.Join(s.baseDir, "photos", "_deleted")
	_ = os.MkdirAll(deletedDir, 0o755)
	dest := filepath.Join(deletedDir, ss.ID)
	if _, err := os.Stat(dest); err == nil {
		dest = dest + "-" + strconv.FormatInt(time.Now().Unix(), 10)
	}
	var rerr error
	if _, err := os.Stat(src); err == nil {
		rerr = os.Rename(src, dest)
	}
	var out []*Session
	for _, x := range s.sessions {
		if x.ID != ss.ID {
			out = append(out, x)
		}
	}
	s.sessions = out
	s.mu.Unlock()
	if rerr != nil {
		http.Error(w, rerr.Error(), 500)
		return
	}
	log.Printf("session %s soft-deleted to %s", body.ID, dest)
	s.pushConsole()
	w.WriteHeader(204)
}

// ensureScreen returns the named screen, creating and persisting it if new.
func (s *server) ensureScreen(name string) *Screen {
	s.mu.Lock()
	sc := s.screens[name]
	created := false
	if sc == nil {
		sc = &Screen{Name: name}
		s.screens[name] = sc
		created = true
	}
	s.mu.Unlock()
	if created {
		s.saveScreens()
		log.Printf("screen created: %q", name)
	}
	return sc
}

func (s *server) saveScreens() {
	s.mu.Lock()
	names := make([]string, 0, len(s.screens))
	for n := range s.screens {
		names = append(names, n)
	}
	s.mu.Unlock()
	sort.Strings(names)
	b, _ := json.MarshalIndent(names, "", "  ")
	_ = os.WriteFile(filepath.Join(s.baseDir, "screens.json"), b, 0o644)
}

// loadScreens restores saved screens at startup with a blank category, so the
// operator is forced to choose one before presenting.
func (s *server) loadScreens() {
	data, err := os.ReadFile(filepath.Join(s.baseDir, "screens.json"))
	if err != nil {
		return
	}
	var names []string
	if json.Unmarshal(data, &names) != nil {
		return
	}
	for _, n := range names {
		if n != "" && s.screens[n] == nil {
			s.screens[n] = &Screen{Name: n}
		}
	}
	log.Printf("loaded %d saved screen(s)", len(names))
}

// handleScreenRegister is called by an output window on load to make sure its
// screen exists (idempotent).
func (s *server) handleScreenRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if decode(r, &body) != nil || !safeName(body.Name) {
		http.Error(w, "valid name required", 400)
		return
	}
	sc := s.ensureScreen(body.Name)
	s.pushConsole()
	s.pushScreen(body.Name)
	writeJSON(w, sc)
}

// handleScreenCreate is the console "Create Screen" action — a persisted screen
// with a blank category.
func (s *server) handleScreenCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	name := ""
	if decode(r, &body) == nil {
		name = strings.TrimSpace(body.Name)
	}
	if !safeName(name) {
		http.Error(w, "valid name required", 400)
		return
	}
	sc := s.ensureScreen(name)
	s.pushConsole()
	writeJSON(w, sc)
}

// handleScreenDelete removes a screen, blanks any open output window for it, and persists.
func (s *server) handleScreenDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if decode(r, &body) != nil {
		http.Error(w, "bad body", 400)
		return
	}
	s.mu.Lock()
	_, ok := s.screens[body.Name]
	if ok {
		delete(s.screens, body.Name)
	}
	s.mu.Unlock()
	if !ok {
		http.Error(w, "no such screen", 404)
		return
	}
	idle, _ := json.Marshal(View{Mode: "idle"})
	s.h.sendOutput(body.Name, idle) // blank its window if one is open
	s.saveScreens()
	s.pushConsole()
	log.Printf("screen deleted: %q", body.Name)
	w.WriteHeader(204)
}

func (s *server) handleScreenLoad(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name, SessionID, Category, Orientation string
	}
	if decode(r, &body) != nil {
		http.Error(w, "bad body", 400)
		return
	}
	if !safeName(body.SessionID) || !safeName(body.Category) || !safeName(body.Orientation) {
		http.Error(w, "bad names", 400)
		return
	}
	s.mu.Lock()
	sc := s.screens[body.Name]
	if sc == nil {
		s.mu.Unlock()
		http.Error(w, "no such screen", 404)
		return
	}
	s.loadScreenLocked(sc, body.SessionID, body.Category, body.Orientation)
	s.mu.Unlock()
	s.pushScreen(body.Name)
	s.pushConsole()
	s.pushJudge() // the live photo may have changed — refresh the judges' phones
	w.WriteHeader(204)
}

// loadScreenLocked points a screen at a category/orientation: captures the ordered
// file list and resets to the title card (Position 0), un-blacked. Assumes s.mu held.
// Shared by the console's Load and the solo controller.
func (s *server) loadScreenLocked(sc *Screen, sid, cat, orient string) {
	sc.SessionID, sc.Category, sc.Orientation = sid, cat, orient
	sc.Files = s.photoFiles(sid, cat, orient)
	sc.Count = len(sc.Files)
	sc.Position = 0 // selecting a category resets to the start (title card)
	sc.Blackout = false
}

func (s *server) handleScreenCmd(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name, Action string
	}
	if decode(r, &body) != nil {
		http.Error(w, "bad body", 400)
		return
	}
	s.mu.Lock()
	sc := s.screens[body.Name]
	if sc == nil {
		s.mu.Unlock()
		http.Error(w, "no such screen", 404)
		return
	}
	// In a presentation mode the slideshow is driven by the guided run, not free-form: the
	// table's nav is locked before Start (screens are black) and while a run is actively
	// presenting (the console panel is the sole driver). It's only operable when the run is
	// paused, so the operator can reconfigure screens.
	if s.settings.presentationMode() && (sc.Type == "" || sc.Type == "slideshow") {
		if s.solo == nil {
			s.mu.Unlock()
			http.Error(w, "start the presentation before driving the screens", http.StatusConflict)
			return
		}
		if !s.runPaused {
			s.mu.Unlock()
			http.Error(w, "the presentation panel is driving — pause to reconfigure screens", http.StatusConflict)
			return
		}
	}
	switch body.Action {
	case "next":
		if sc.Position < sc.Count+1 {
			sc.Position++
		}
	case "prev":
		if sc.Position > 0 {
			sc.Position--
		}
	case "blackout":
		sc.Blackout = !sc.Blackout
	case "makelive":
		sc.Blackout = false
		for n, other := range s.screens {
			if n != sc.Name {
				other.Blackout = true
			}
		}
	default:
		s.mu.Unlock()
		http.Error(w, "unknown action", 400)
		return
	}
	// "Only one live screen" setting (forced on while a scoring mode is enabled, so there's
	// a single scoring target): revealing a screen blacks out all the others, so the rest
	// never show at once (same effect as Make live, applied automatically).
	revealedOthers := false
	if !sc.Blackout && (s.settings.SingleLiveScreen || s.settings.ScoreKeeperEnabled || s.settings.JudgeScoringEnabled) {
		for n, other := range s.screens {
			if n != sc.Name {
				other.Blackout = true
			}
		}
		revealedOthers = true
	}
	var names []string
	for n := range s.screens {
		names = append(names, n)
	}
	s.mu.Unlock()

	if body.Action == "makelive" || revealedOthers {
		for _, n := range names {
			s.pushScreen(n)
		}
	} else {
		s.pushScreen(body.Name)
	}
	s.pushConsole()
	s.pushJudge() // the live photo may have changed — refresh the judges' phones
	w.WriteHeader(204)
}

func (s *server) handlePhoto(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sid, cat, orient := q.Get("session"), q.Get("category"), q.Get("orientation")
	if !safeName(sid) || !safeName(cat) || !safeName(orient) {
		http.Error(w, "bad names", 400)
		return
	}
	dir := s.photosDir(sid, cat, orient)
	// file= serves a specific image by name (stable for thumbnails across reorder);
	// otherwise n= serves the nth image in display order (used by output windows).
	if name := q.Get("file"); name != "" {
		if !safeName(name) {
			http.Error(w, "bad file", 400)
			return
		}
		full := filepath.Join(dir, filepath.Base(name))
		if _, err := os.Stat(full); err != nil {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, full)
		return
	}
	n, _ := strconv.Atoi(q.Get("n"))
	files := s.photoFiles(sid, cat, orient)
	if n < 1 || n > len(files) {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(dir, files[n-1]))
}

func (s *server) handlePhotosList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sid, cat, orient := q.Get("session"), q.Get("category"), q.Get("orientation")
	if !safeName(sid) || !safeName(cat) || !safeName(orient) {
		http.Error(w, "bad names", 400)
		return
	}
	dir := s.photosDir(sid, cat, orient)
	s.mu.Lock()
	var win, max *float64
	if ss := s.sessionByID(sid); ss != nil {
		win, max = ss.WinThreshold, ss.MaxPoints
	}
	s.mu.Unlock()
	writeJSON(w, map[string]any{"files": s.photoFiles(sid, cat, orient), "names": loadNames(dir), "scores": loadScores(dir), "winThreshold": win, "maxPoints": max})
}

// handlePhotoName sets (or clears, when name is empty) the photographer associated
// with one photo, persisted to the folder's names.json.
func (s *server) handlePhotoName(w http.ResponseWriter, r *http.Request) {
	var body struct{ Session, Category, Orientation, File, Name string }
	if decode(r, &body) != nil {
		http.Error(w, "bad body", 400)
		return
	}
	if !safeName(body.Session) || !safeName(body.Category) || !safeName(body.Orientation) || !safeName(body.File) {
		http.Error(w, "bad names", 400)
		return
	}
	dir := s.photosDir(body.Session, body.Category, body.Orientation)
	base := filepath.Base(body.File)
	if _, err := os.Stat(filepath.Join(dir, base)); err != nil {
		http.NotFound(w, r)
		return
	}
	if err := setName(dir, base, body.Name); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(204)
}

// handlePhotoScore sets (or, when score is empty, clears) the score for one photo,
// persisted to the folder's scores.json. It pushes the console so the Upload /
// Reorder grid (which refreshes from the console stream) shows the new score.
func (s *server) handlePhotoScore(w http.ResponseWriter, r *http.Request) {
	var body struct{ Session, Category, Orientation, File, Score string }
	if decode(r, &body) != nil {
		http.Error(w, "bad body", 400)
		return
	}
	if !safeName(body.Session) || !safeName(body.Category) || !safeName(body.Orientation) || !safeName(body.File) {
		http.Error(w, "bad names", 400)
		return
	}
	s.mu.Lock()
	locked := s.sessionByID(body.Session).locked()
	s.mu.Unlock()
	if locked {
		http.Error(w, "this session has ended — scores are locked", http.StatusConflict)
		return
	}
	dir := s.photosDir(body.Session, body.Category, body.Orientation)
	base := filepath.Base(body.File)
	if _, err := os.Stat(filepath.Join(dir, base)); err != nil {
		http.NotFound(w, r)
		return
	}
	if err := setScore(dir, base, body.Score); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.pushConsole()
	w.WriteHeader(204)
}

// handleUpload accepts multipart image uploads into a session/category. Each photo
// is auto-sorted into the Landscape or Portrait folder by its pixel dimensions
// (taller than wide = Portrait), so the operator no longer picks an orientation.
// Folders are created lazily; non-images are skipped; clashes get a " (n)" suffix.
func (s *server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "bad upload", 400)
		return
	}
	sid := r.FormValue("session")
	cat := r.FormValue("category")
	if !safeName(sid) || !safeName(cat) {
		http.Error(w, "bad names", 400)
		return
	}
	s.mu.Lock()
	exists := s.sessionByID(sid) != nil
	s.mu.Unlock()
	if !exists {
		http.Error(w, "no such session", 404)
		return
	}
	type savedItem struct {
		File        string `json:"file"`
		Orientation string `json:"orientation"`
	}
	s.mu.Lock()
	wantPhotographer := s.effImportPhotographer()
	wantTitle := s.effImportTitle()
	s.mu.Unlock()
	var saved []savedItem
	var skipped []string
	added := map[string][]string{} // orientation -> filenames, for per-folder order.json
	for _, fh := range r.MultipartForm.File["files"] {
		base := filepath.Base(fh.Filename)
		if base == "" || base == "." || strings.Contains(base, "..") || !uploadExts[strings.ToLower(filepath.Ext(base))] {
			skipped = append(skipped, fh.Filename)
			continue
		}
		f, err := fh.Open()
		if err != nil {
			skipped = append(skipped, fh.Filename)
			continue
		}
		orient := orientationOf(f, base) // reads dimensions, then seeks f back to start

		// Optionally pull the title (used as the saved filename) and photographer
		// from the photo's embedded metadata. Each is gated separately (properties
		// importMetadata AND the matching Settings toggle); anything off/missing keeps
		// the defaults.
		var photographer string
		if wantPhotographer || wantTitle {
			title, ph := imageMetadata(f, base) // seeks f, rewinds to start
			if wantPhotographer {
				photographer = ph
			}
			if wantTitle && title != "" {
				if n := sanitizeUploadName(title, filepath.Ext(base)); n != "" {
					base = n
				}
			}
		}

		dir := s.photosDir(sid, cat, orient)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			f.Close()
			http.Error(w, err.Error(), 500)
			return
		}
		name := uniqueName(dir, base)
		err = saveReader(f, filepath.Join(dir, name))
		f.Close()
		if err != nil {
			skipped = append(skipped, fh.Filename)
			continue
		}
		if photographer != "" {
			_ = setName(dir, name, photographer)
		}
		saved = append(saved, savedItem{File: name, Orientation: orient})
		added[orient] = append(added[orient], name)
	}
	for orient, names := range added {
		appendToOrder(s.photosDir(sid, cat, orient), names)
	}
	log.Printf("upload: %d saved (L:%d P:%d), %d skipped -> %s/%s",
		len(saved), len(added["Landscape"]), len(added["Portrait"]), len(skipped), sid, cat)
	writeJSON(w, map[string]any{"saved": saved, "skipped": skipped})
}

// orientationOf reports "Portrait" if the image is taller than it is wide, else
// "Landscape" (the default for squares or undetectable dimensions). It leaves the
// reader rewound to the start so the caller can copy the full file.
func orientationOf(f io.ReadSeeker, name string) string {
	w, h := imageDims(f, name)
	f.Seek(0, io.SeekStart)
	if w > 0 && h > w {
		return "Portrait"
	}
	return "Landscape"
}

// imageDims returns an image's effective display width and height (0,0 if it can't
// be read). Dimensions come from image.DecodeConfig (JPEG/PNG). For JPEGs, a 90°/270°
// rotation recorded in EXIF orientation swaps width and height, so a photo shot in
// portrait but stored with landscape pixels is still treated as portrait.
func imageDims(f io.ReadSeeker, name string) (int, int) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, 0
	}
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0
	}
	w, h := cfg.Width, cfg.Height
	if ext := strings.ToLower(filepath.Ext(name)); ext == ".jpg" || ext == ".jpeg" {
		if _, err := f.Seek(0, io.SeekStart); err == nil {
			head := make([]byte, 64*1024) // EXIF lives in the APP1 segment near the start
			n, _ := io.ReadFull(f, head)
			if o := exifOrientation(head[:n]); o >= 5 && o <= 8 {
				w, h = h, w // 90°/270° rotation swaps the displayed dimensions
			}
		}
	}
	return w, h
}

// exifOrientation scans a JPEG's markers for the APP1/Exif segment and returns its
// Orientation tag (1–8), or 0 if absent.
func exifOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 { // SOI
		return 0
	}
	for i := 2; i+4 <= len(b); {
		if b[i] != 0xFF {
			return 0
		}
		marker := b[i+1]
		if marker == 0xDA || marker == 0xD9 { // SOS / EOI — image data, no more metadata
			return 0
		}
		if marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) { // standalone markers, no length
			i += 2
			continue
		}
		segLen := int(b[i+2])<<8 | int(b[i+3]) // includes its own 2 length bytes
		segStart, segEnd := i+4, i+2+segLen
		if segLen < 2 || segEnd > len(b) {
			return 0
		}
		if marker == 0xE1 { // APP1
			if o := exifFromAPP1(b[segStart:segEnd]); o != 0 {
				return o
			}
		}
		i = segEnd
	}
	return 0
}

// exifFromAPP1 reads the Orientation tag (0x0112) out of an APP1 "Exif" segment's
// TIFF/IFD0 directory.
func exifFromAPP1(seg []byte) int {
	if len(seg) < 14 || string(seg[0:6]) != "Exif\x00\x00" {
		return 0
	}
	tiff := seg[6:]
	var bo binary.ByteOrder
	switch string(tiff[0:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0
	}
	if bo.Uint16(tiff[2:4]) != 0x2A {
		return 0
	}
	ifd := int(bo.Uint32(tiff[4:8]))
	if ifd < 8 || ifd+2 > len(tiff) {
		return 0
	}
	count := int(bo.Uint16(tiff[ifd : ifd+2]))
	for k, p := 0, ifd+2; k < count; k, p = k+1, p+12 {
		if p+12 > len(tiff) {
			return 0
		}
		if bo.Uint16(tiff[p:p+2]) == 0x0112 { // Orientation, stored as a SHORT
			return int(bo.Uint16(tiff[p+8 : p+10]))
		}
	}
	return 0
}

// handleOrderSet overwrites a folder's order.json with the given filename order,
// keeping only names that actually exist in the folder.
func (s *server) handleOrderSet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Session, Category, Orientation string
		Order                          []string
	}
	if decode(r, &body) != nil {
		http.Error(w, "bad body", 400)
		return
	}
	if !safeName(body.Session) || !safeName(body.Category) || !safeName(body.Orientation) {
		http.Error(w, "bad names", 400)
		return
	}
	dir := s.photosDir(body.Session, body.Category, body.Orientation)
	var valid []string
	seen := map[string]bool{}
	for _, n := range body.Order {
		b := filepath.Base(n)
		if seen[b] || !safeName(b) {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, b)); err == nil {
			valid = append(valid, b)
			seen[b] = true
		}
	}
	bts, _ := json.MarshalIndent(valid, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "order.json"), bts, 0o644); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(204)
}

// handlePhotoDelete soft-deletes one photo by moving it to photos\_deleted\_photos
// (recoverable) and dropping it from the folder's order.json.
func (s *server) handlePhotoDelete(w http.ResponseWriter, r *http.Request) {
	var body struct{ Session, Category, Orientation, File string }
	if decode(r, &body) != nil {
		http.Error(w, "bad body", 400)
		return
	}
	if !safeName(body.Session) || !safeName(body.Category) || !safeName(body.Orientation) || !safeName(body.File) {
		http.Error(w, "bad names", 400)
		return
	}
	dir := s.photosDir(body.Session, body.Category, body.Orientation)
	base := filepath.Base(body.File)
	src := filepath.Join(dir, base)
	if _, err := os.Stat(src); err != nil {
		http.NotFound(w, r)
		return
	}
	trash := filepath.Join(s.baseDir, "photos", "_deleted", "_photos")
	if err := os.MkdirAll(trash, 0o755); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	dest := filepath.Join(trash, fmt.Sprintf("%d__%s", time.Now().UnixNano(), base))
	if err := os.Rename(src, dest); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	removeFromOrder(dir, base)
	removeFromNames(dir, base)
	removeFromScores(dir, base)
	log.Printf("photo soft-deleted: %s -> %s", src, dest)
	writeJSON(w, map[string]any{"files": s.photoFiles(body.Session, body.Category, body.Orientation)})
}

// names.json maps a photo's filename to an operator-entered photographer name,
// kept per orientation folder alongside order.json. Used to pre-fill the upload
// grid and the Photographer column of the score-sheet PDF.

func loadNames(dir string) map[string]string {
	m := map[string]string{}
	data, err := os.ReadFile(filepath.Join(dir, "names.json"))
	if err != nil {
		return m
	}
	_ = json.Unmarshal(data, &m)
	if m == nil {
		m = map[string]string{}
	}
	return m
}

func writeNames(dir string, m map[string]string) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(filepath.Join(dir, "names.json"), b, 0o644)
}

// setName records (or, when name is blank, clears) the photographer for one file.
func setName(dir, file, name string) error {
	m := loadNames(dir)
	name = strings.TrimSpace(name)
	if len(name) > 120 {
		name = name[:120]
	}
	if name == "" {
		if _, ok := m[file]; !ok {
			return nil
		}
		delete(m, file)
	} else {
		m[file] = name
	}
	return writeNames(dir, m)
}

func removeFromNames(dir, file string) {
	m := loadNames(dir)
	if _, ok := m[file]; !ok {
		return
	}
	delete(m, file)
	_ = writeNames(dir, m)
}

// scores.json maps a photo's filename to a judge's score, kept per orientation
// folder alongside order.json/names.json. Scores are entered on the Scoring page
// and surface on the Upload / Reorder grid and the Score column of the PDF. A score
// is stored as a free-form string (a number like "8.5", but anything short is fine).

func loadScores(dir string) map[string]string {
	m := map[string]string{}
	data, err := os.ReadFile(filepath.Join(dir, "scores.json"))
	if err != nil {
		return m
	}
	_ = json.Unmarshal(data, &m)
	if m == nil {
		m = map[string]string{}
	}
	return m
}

func writeScores(dir string, m map[string]string) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(filepath.Join(dir, "scores.json"), b, 0o644)
}

// setScore records (or, when score is blank, clears) the score for one file.
func setScore(dir, file, score string) error {
	m := loadScores(dir)
	score = strings.TrimSpace(score)
	if len(score) > 32 {
		score = score[:32]
	}
	if score == "" {
		if _, ok := m[file]; !ok {
			return nil
		}
		delete(m, file)
	} else {
		m[file] = score
	}
	return writeScores(dir, m)
}

func removeFromScores(dir, file string) {
	m := loadScores(dir)
	if _, ok := m[file]; !ok {
		return
	}
	delete(m, file)
	_ = writeScores(dir, m)
}

func removeFromOrder(dir, name string) {
	path := filepath.Join(dir, "order.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var order []string
	if json.Unmarshal(data, &order) != nil {
		return
	}
	var out []string
	for _, n := range order {
		if n != name {
			out = append(out, n)
		}
	}
	if b, err := json.MarshalIndent(out, "", "  "); err == nil {
		_ = os.WriteFile(path, b, 0o644)
	}
}

func uniqueName(dir, name string) string {
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		return name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if _, err := os.Stat(filepath.Join(dir, cand)); err != nil {
			return cand
		}
	}
}

// saveReader writes the (already-rewound) upload stream to dest.
func saveReader(src io.Reader, dest string) error {
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, src)
	return err
}

func appendToOrder(dir string, names []string) {
	if len(names) == 0 {
		return
	}
	path := filepath.Join(dir, "order.json")
	var order []string
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &order)
	}
	have := map[string]bool{}
	for _, n := range order {
		have[n] = true
	}
	for _, n := range names {
		if !have[n] {
			order = append(order, n)
			have[n] = true
		}
	}
	if b, err := json.MarshalIndent(order, "", "  "); err == nil {
		_ = os.WriteFile(path, b, 0o644)
	}
}

func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")

	ch := make(chan []byte, 16)
	role := r.URL.Query().Get("role")
	if role == "console" {
		s.h.addConsole(ch)
		defer s.h.removeConsole(ch)
		ch <- s.consoleSnapshot()
	} else if role == "entry" {
		// Landing + member-entry pages: get the entry-form state, refreshed whenever
		// the operator opens/closes it or the locked session's details change.
		s.h.addEntry(ch)
		defer s.h.removeEntry(ch)
		ch <- s.entryStateJSON()
	} else if role == "judge" {
		// Judge phones: presence is the live SSE connection (name + alternate from the
		// query). The shared judging state is pushed; the page GETs its personal status.
		key := normalizeNameKey(r.URL.Query().Get("name"))
		alt := r.URL.Query().Get("alt") == "1"
		s.judgeJoin(key, strings.TrimSpace(r.URL.Query().Get("name")), alt)
		s.h.addJudge(ch)
		defer func() { s.h.removeJudge(ch); s.judgeLeave(key) }()
		ch <- s.judgeStateJSON()
	} else {
		name := r.URL.Query().Get("screen")
		s.h.addOutput(name, ch)
		defer s.h.removeOutput(name, ch)
		s.mu.Lock()
		v := View{Mode: "idle"}
		if sc := s.screens[name]; sc != nil {
			v = s.buildView(sc)
		}
		s.mu.Unlock()
		b, _ := json.Marshal(v)
		ch <- b
	}

	ctx := r.Context()
	tick := time.NewTicker(25 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		case <-tick.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// handleShutdown lets the operator stop the app cleanly from the console, so
// the exe exits instead of lingering in memory after a session.
func (s *server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"status": "closing"})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	s.shutdownOnce.Do(func() { close(s.shutdownCh) })
}

// lanURLs returns the http URLs by which this server can be reached from other
// machines on the LAN — one per usable non-loopback IPv4 address. The port is
// omitted when it's the default 80, so the URL is as short as possible.
func (s *server) lanURLs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var urls []string
	for _, iface := range ifaces {
		// Skip interfaces that are down or loopback.
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP.To4()
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue // IPv4 only; skip 169.254.x.x auto-config addresses
			}
			host := ip.String()
			if s.port == "80" || s.port == "" {
				urls = append(urls, "http://"+host)
			} else {
				urls = append(urls, "http://"+host+":"+s.port)
			}
		}
	}
	return urls
}

// handleNetInfo reports the LAN URLs and port so the console can show the operator
// how a second machine can reach the app.
func (s *server) handleNetInfo(w http.ResponseWriter, r *http.Request) {
	host, _ := os.Hostname()
	var urls []string
	if s.lanAccess {
		urls = s.lanURLs()
	}
	writeJSON(w, map[string]any{
		"hostname":  host,
		"port":      s.port,
		"lanAccess": s.lanAccess,
		"urls":      urls,
	})
}

// handleQR renders the ?data= text as a QR-code PNG (stdlib-only encoder, see
// qr.go). Used by the console's "connect over LAN" modal.
func (s *server) handleQR(w http.ResponseWriter, r *http.Request) {
	data := r.URL.Query().Get("data")
	if data == "" {
		http.Error(w, "missing data", http.StatusBadRequest)
		return
	}
	if len(data) > 200 {
		http.Error(w, "data too long", http.StatusBadRequest)
		return
	}
	png, err := qrPNGForText(data, 8, 4)
	if err != nil {
		http.Error(w, "could not encode QR: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(png)
}

// ---- helpers --------------------------------------------------------------

func decode(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// safeName rejects anything that could escape the photos\ tree.
func safeName(s string) bool {
	if s == "" || strings.Contains(s, "..") {
		return false
	}
	return !strings.ContainsAny(s, `/\`)
}

func validDate(d string) bool {
	_, err := time.Parse("2006-01-02", d)
	return err == nil
}

// newerVer reports whether version a is strictly newer than b, comparing the
// MAJOR.RELEASE.FEATURES.PATCH numbers slot by slot. Versions with fewer than
// four parts are zero-padded (so a legacy "1.1.0" compares as "1.1.0.0"),
// keeping older three-part builds comparable. Unparseable versions are treated
// as "not newer".
func newerVer(a, b string) bool {
	pa, oka := parseVer(a)
	pb, okb := parseVer(b)
	if !oka || !okb {
		return false
	}
	for i := 0; i < 4; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

// parseVer parses a dot-separated numeric version into a fixed four-slot array
// (MAJOR.RELEASE.FEATURES.PATCH), zero-padding any missing trailing parts. It
// accepts one to four parts so legacy three-part versions (e.g. "1.1.0") still
// parse. Returns false for too many parts, or a non-numeric/negative component.
func parseVer(s string) ([4]int, bool) {
	var v [4]int
	parts := strings.Split(strings.TrimSpace(s), ".")
	if len(parts) < 1 || len(parts) > 4 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// probeRunning asks an already-bound address whether it's a Photo Judge instance,
// returning its reported version. Used when our own port bind fails.
func probeRunning(addr string) (bool, string) {
	client := http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get("http://" + addr + "/api/state")
	if err != nil {
		return false, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, ""
	}
	var st struct {
		Version string `json:"version"`
	}
	if json.NewDecoder(resp.Body).Decode(&st) != nil {
		return false, ""
	}
	return true, st.Version
}

// reportVersion tells the already-running instance that a newer build (ours) was
// launched, so its console can advise a restart. Best-effort; errors are ignored.
func reportVersion(addr string) {
	body := strings.NewReader(`{"version":"` + appVersion + `"}`)
	client := http.Client{Timeout: 1500 * time.Millisecond}
	if resp, err := client.Post("http://"+addr+"/api/report-version", "application/json", body); err == nil {
		resp.Body.Close()
	}
}

func serveAsset(w http.ResponseWriter, sub fs.FS, name string) {
	data, err := fs.ReadFile(sub, name)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}
	ct := "text/html; charset=utf-8"
	switch {
	case strings.HasSuffix(name, ".js"):
		ct = "application/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".css"):
		ct = "text/css; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	// Always revalidate so an upgraded build's pages/scripts aren't served stale from
	// the browser cache (it's all localhost, so there's no real bandwidth cost).
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data)
}

func openBrowser(u string) {
	_ = exec.Command("cmd", "/c", "start", "", u).Start()
}

func main() {
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	baseDir := filepath.Dir(exe)
	if d := os.Getenv("PHOTOJUDGE_DATA"); d != "" {
		baseDir = d // dev override (e.g. when running a temp build)
	}

	s := &server{baseDir: baseDir, screens: map[string]*Screen{}, h: newHub(), shutdownCh: make(chan struct{})}
	// Mirror the standard logger into the debug-log tee so log.Printf lines can be
	// captured as EVENT rows when logging is on (it still writes to stderr, so the
	// console window keeps showing them). Lshortfile adds a "file.go:NN" source
	// location, which becomes the file:line column in the log file. Installed early;
	// nothing is captured until the operator turns logging on (the atomics default off).
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.SetOutput(&logTee{s: s, std: os.Stderr})
	if err := os.MkdirAll(filepath.Join(baseDir, "photos"), 0o755); err != nil {
		log.Fatalf("cannot write to %s — is the folder writable? (%v)", baseDir, err)
	}
	if err := os.MkdirAll(filepath.Join(baseDir, "logo"), 0o755); err != nil {
		log.Printf("note: could not create logo folder: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(baseDir, "archives"), 0o755); err != nil {
		log.Printf("note: could not create archives folder: %v", err)
	}
	s.loadCategories()
	s.scanSessions()
	s.loadSettings()
	s.refreshLogo() // resolve the active logo (settings + logo\ contents)
	s.loadScreens()
	s.sweepImportTmp() // clear any leftover import staging from a previous run
	s.detectWifi()     // best-effort host Wi-Fi (name/password) for entry instructions

	// Debug logging: if a previous run left it on but its auto-off window already
	// lapsed, turn it off now; then publish the config to the hot-path atomics, load
	// any existing log files into the size index, and start the auto-off ticker.
	if s.settings.LoggingEnabled && s.logExpired() {
		s.settings.LoggingEnabled = false
		s.settings.LogEnabledAt = 0
		_ = s.saveSettings()
	}
	s.applyLogConfig()
	s.seedLogIndex()
	s.startLogAutoOff()

	sub, _ := fs.Sub(webFS, "web")
	mux := http.NewServeMux()
	// Root is the PUBLIC landing page (club logo + entry direction). The operator
	// console lives at /console so competitors who reach the app never land on it.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		serveAsset(w, sub, "landing.html")
	})
	mux.HandleFunc("/console", func(w http.ResponseWriter, r *http.Request) { serveAsset(w, sub, "console.html") })
	mux.HandleFunc("/entry", func(w http.ResponseWriter, r *http.Request) { serveAsset(w, sub, "entry.html") })
	mux.HandleFunc("/judge", func(w http.ResponseWriter, r *http.Request) { serveAsset(w, sub, "judge.html") })
	mux.HandleFunc("/review", func(w http.ResponseWriter, r *http.Request) { serveAsset(w, sub, "review.html") })
	mux.HandleFunc("/output", func(w http.ResponseWriter, r *http.Request) { serveAsset(w, sub, "output.html") })
	mux.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) { serveAsset(w, sub, "admin.html") })
	mux.HandleFunc("/categories", func(w http.ResponseWriter, r *http.Request) { serveAsset(w, sub, "categories.html") })
	mux.HandleFunc("/session", func(w http.ResponseWriter, r *http.Request) { serveAsset(w, sub, "categories.html") })
	mux.HandleFunc("/how-to", func(w http.ResponseWriter, r *http.Request) { serveAsset(w, sub, "how-to.html") })
	// Old link target — the Getting Started walkthrough is now the first tab of How To.
	mux.HandleFunc("/getting-started", func(w http.ResponseWriter, r *http.Request) { serveAsset(w, sub, "how-to.html") })
	mux.HandleFunc("/score", func(w http.ResponseWriter, r *http.Request) { serveAsset(w, sub, "score.html") })
	mux.HandleFunc("/archived", func(w http.ResponseWriter, r *http.Request) { serveAsset(w, sub, "archived.html") })
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) { serveAsset(w, sub, "stats.html") })
	mux.HandleFunc("/settings", func(w http.ResponseWriter, r *http.Request) { serveAsset(w, sub, "settings.html") })
	mux.HandleFunc("/logs", func(w http.ResponseWriter, r *http.Request) { serveAsset(w, sub, "logs.html") })
	mux.HandleFunc("/nav.js", func(w http.ResponseWriter, r *http.Request) { serveAsset(w, sub, "nav.js") })
	mux.HandleFunc("/modal.js", func(w http.ResponseWriter, r *http.Request) { serveAsset(w, sub, "modal.js") })
	mux.HandleFunc("/portation.js", func(w http.ResponseWriter, r *http.Request) { serveAsset(w, sub, "portation.js") })
	mux.Handle("/getting-started-images/", http.FileServer(http.FS(gettingStartedFS)))
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/report-version", s.handleReportVersion)
	mux.HandleFunc("/api/session/create", s.handleSessionCreate)
	mux.HandleFunc("/api/session/edit", s.handleSessionEdit)
	mux.HandleFunc("/api/session/settings", s.handleSessionSettings)
	mux.HandleFunc("/api/console/session", s.handleConsoleSession)
	mux.HandleFunc("/api/session/delete", s.handleSessionDelete)
	mux.HandleFunc("/api/session/archive", s.handleSessionArchive)
	mux.HandleFunc("/api/session/pdf", s.handleSessionPDF)
	mux.HandleFunc("/api/session/physical", s.handlePhysicalList)
	mux.HandleFunc("/api/session/physical/set", s.handlePhysicalSet)
	mux.HandleFunc("/api/archives", s.handleArchivesList)
	mux.HandleFunc("/api/archive/download", s.handleArchiveDownload)
	mux.HandleFunc("/api/archive/pdf", s.handleArchivePDF)
	mux.HandleFunc("/api/sessions/all", s.handleSessionsAll)
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/api/export", s.handleExport)
	mux.HandleFunc("/api/import/preview", s.handleImportPreview)
	mux.HandleFunc("/api/import/commit", s.handleImportCommit)
	mux.HandleFunc("/api/session/categories", s.handleSessionCategories)
	mux.HandleFunc("/api/session/category/add", s.handleCategoryAdd)
	mux.HandleFunc("/api/session/category/activate", s.handleCategoryActivate)
	mux.HandleFunc("/api/session/category/deactivate", s.handleCategoryDeactivate)
	mux.HandleFunc("/api/session/category/reorder", s.handleCategoryReorder)
	mux.HandleFunc("/api/session/category/delete", s.handleCategoryDelete)
	mux.HandleFunc("/api/screen/register", s.handleScreenRegister)
	mux.HandleFunc("/api/screen/create", s.handleScreenCreate)
	mux.HandleFunc("/api/screen/delete", s.handleScreenDelete)
	mux.HandleFunc("/api/screen/load", s.handleScreenLoad)
	mux.HandleFunc("/api/screen/cmd", s.handleScreenCmd)
	mux.HandleFunc("/api/screen/type", s.handleScreenType)
	mux.HandleFunc("/api/solo/start", s.handleSoloStart)
	mux.HandleFunc("/api/solo/advance", s.handleSoloAdvance)
	mux.HandleFunc("/api/solo/back", s.handleSoloBack)
	mux.HandleFunc("/api/solo/stop", s.handleSoloStop)
	mux.HandleFunc("/api/presentation/start", s.handlePresentationStart)
	mux.HandleFunc("/api/presentation/pause", s.handlePresentationPause)
	mux.HandleFunc("/api/presentation/resume", s.handlePresentationResume)
	mux.HandleFunc("/api/presentation/end", s.handlePresentationEnd)
	mux.HandleFunc("/api/presentation/restart", s.handlePresentationRestart)
	mux.HandleFunc("/api/judge/state", s.handleJudgeState)
	mux.HandleFunc("/api/judge/submit", s.handleJudgeSubmit)
	mux.HandleFunc("/api/judge/defer", s.handleJudgeDefer)
	mux.HandleFunc("/api/judge/rescore", s.handleJudgeRescore)
	mux.HandleFunc("/api/judge/board", s.handleJudgeBoard)
	mux.HandleFunc("/api/judge/start", s.handleJudgeStart)
	mux.HandleFunc("/api/judge/stop", s.handleJudgeStop)
	mux.HandleFunc("/api/entry/state", s.handleEntryState)
	mux.HandleFunc("/api/entry/open", s.handleEntryOpen)
	mux.HandleFunc("/api/entry/close", s.handleEntryClose)
	mux.HandleFunc("/api/entry/submit", s.handleEntrySubmit)
	mux.HandleFunc("/api/entry/mine", s.handleEntryMine)
	mux.HandleFunc("/api/entry/edit", s.handleEntryEdit)
	mux.HandleFunc("/api/entry/remove", s.handleEntryRemove)
	mux.HandleFunc("/api/entry/pending", s.handleEntryPending)
	mux.HandleFunc("/api/entry/photo", s.handleEntryPhoto)
	mux.HandleFunc("/api/entry/approve", s.handleEntryApprove)
	mux.HandleFunc("/api/entry/reject", s.handleEntryReject)
	mux.HandleFunc("/api/photo", s.handlePhoto)
	mux.HandleFunc("/api/photos", s.handlePhotosList)
	mux.HandleFunc("/api/logo", s.handleLogo)
	mux.HandleFunc("/api/upload", s.handleUpload)
	mux.HandleFunc("/api/order", s.handleOrderSet)
	mux.HandleFunc("/api/order/randomize", s.handleOrderRandomize)
	mux.HandleFunc("/api/photo/delete", s.handlePhotoDelete)
	mux.HandleFunc("/api/photo/name", s.handlePhotoName)
	mux.HandleFunc("/api/photo/score", s.handlePhotoScore)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/shutdown", s.handleShutdown)
	mux.HandleFunc("/api/netinfo", s.handleNetInfo)
	mux.HandleFunc("/api/qr", s.handleQR)
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/logo/upload", s.handleLogoUpload)
	mux.HandleFunc("/api/logo/delete", s.handleLogoDelete)
	mux.HandleFunc("/api/logs", s.handleLogsList)
	mux.HandleFunc("/api/log/file", s.handleLogFile)
	mux.HandleFunc("/api/log/view", s.handleLogView)
	mux.HandleFunc("/api/logs/zip", s.handleLogsZip)
	mux.HandleFunc("/api/logs/clear", s.handleLogsClear)

	// Resolve the listen port: photo-judge.properties (default 80, or a free port
	// when autoPort=true) sets it; PHOTOJUDGE_PORT overrides for dev/testing.
	cfg := loadConfig(baseDir)
	// The properties values gate the matching Settings (a "false" in properties wins).
	s.propsLanAccess = cfg.LanAccess
	s.propsImportMetadata = cfg.ImportMetadata
	s.exportPageSize = cfg.ExportPageSize
	port := strconv.Itoa(cfg.Port)
	if cfg.AutoPort {
		port = "0" // let the OS pick any free port
	}
	if env := os.Getenv("PHOTOJUDGE_PORT"); env != "" {
		port = env
	}
	// Bind to all interfaces (0.0.0.0) when LAN access is allowed, so other devices
	// on the network — and this machine's own LAN IP — can reach the console; bind to
	// loopback only when it's turned off. Either way the operator's own browser is
	// opened at 127.0.0.1, a secure context that keeps the Window Management API working.
	// LAN access is the effective value: properties AND the Settings toggle.
	s.lanAccess = s.effLanAccess()
	host := "127.0.0.1"
	if s.lanAccess {
		host = "0.0.0.0"
	}
	addr := host + ":" + port
	loopAddr := "127.0.0.1:" + port // how a second launch / the local browser reaches us
	reqURL := "http://" + loopAddr + "/console"

	// Bind the port up front. If it's already taken, Photo Judge is almost certainly
	// already running — hand the user off to that instance instead of dying with a
	// raw "address in use" error. A second double-click thus just opens the console.
	// (With autoPort the bind can't collide, so this hand-off only applies to a
	// fixed port.)
	ln, lerr := net.Listen("tcp", addr)
	if lerr != nil {
		if running, runningVer := probeRunning(loopAddr); running {
			fmt.Printf("Photo Judge is already running. Opening the console at %s\n", reqURL)
			if newerVer(appVersion, runningVer) {
				reportVersion(loopAddr) // make the running console show an update banner
				fmt.Printf("\nHeads up: the copy you just launched is v%s, but the running app is v%s.\n", appVersion, runningVer)
				fmt.Printf("To update, click \"Close App\" in the running Photo Judge, then start it again.\n")
			}
			openBrowser(reqURL)
			os.Exit(0)
		}
		log.Fatalf("could not start Photo Judge on %s: %v\n"+
			"If another program is using this port, edit %s (change \"port\" or set autoPort=true) and restart.",
			addr, lerr, configFileName)
	}

	// Read the real port back from the listener — with autoPort (port 0) the OS chose
	// it — and always point the local browser at loopback (a secure context).
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		s.port = strconv.Itoa(tcp.Port)
	}
	u := "http://127.0.0.1:" + s.port + "/console"

	srv := &http.Server{Handler: s.withLogging(mux)}
	go func() {
		<-s.shutdownCh
		log.Println("Close App pressed — shutting down.")
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	log.Printf("Photo Judge v%s — data dir: %s", appVersion, baseDir)
	log.Printf("Console ready at %s", u)
	go openBrowser(u)
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	log.Println("Stopped.")
}
