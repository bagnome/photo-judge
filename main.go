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
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

var defaultCategories = []string{
	"Pictorial",
	"Wildlife",
	"Altered Reality",
	"Portraiture",
	"Macro",
	"Landscapes, Cityscapes, and Travel",
	"Black and White",
}

var imageExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".bmp": true,
}

// ---- data types -----------------------------------------------------------

type Session struct {
	ID         string   `json:"id"`         // "001" — stable folder name, never changes
	Date       string   `json:"date"`       // human label, freely editable
	Created    string   `json:"created"`    // RFC3339
	Categories []string `json:"categories"` // slate snapshotted at creation
}

// Screen is the live state of one output window. Position: 0 = title card,
// 1..Count = photo n, Count+1 = end black. Blackout is an independent overlay.
type Screen struct {
	Name        string `json:"name"`
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
	Mode        string `json:"mode"` // idle | black | title | photo
	Category    string `json:"category"`
	Orientation string `json:"orientation"`
	PhotoURL    string `json:"photoUrl"`
	LogoURL     string `json:"logoUrl,omitempty"` // set on title views when a logo exists
	Position    int    `json:"position"`
	Count       int    `json:"count"`
}

// ---- SSE hub --------------------------------------------------------------

type hub struct {
	mu       sync.Mutex
	consoles map[chan []byte]bool
	outputs  map[string]map[chan []byte]bool
}

func newHub() *hub {
	return &hub{consoles: map[chan []byte]bool{}, outputs: map[string]map[chan []byte]bool{}}
}
func (h *hub) addConsole(ch chan []byte)    { h.mu.Lock(); h.consoles[ch] = true; h.mu.Unlock() }
func (h *hub) removeConsole(ch chan []byte) { h.mu.Lock(); delete(h.consoles, ch); h.mu.Unlock() }
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

// ---- server ---------------------------------------------------------------

type server struct {
	baseDir    string
	mu         sync.Mutex
	categories []string
	logoFile   string // optional brand logo for title cards ("" = none)
	sessions   []*Session
	screens    map[string]*Screen
	h          *hub

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
	var screens []*Screen
	for _, sc := range s.screens {
		screens = append(screens, sc)
	}
	sort.Slice(screens, func(i, j int) bool { return screens[i].Name < screens[j].Name })
	payload := struct {
		Sessions   []*Session `json:"sessions"`
		Categories []string   `json:"categories"`
		Screens    []*Screen  `json:"screens"`
	}{s.sessions, s.categories, screens}
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
		b.WriteString("# categories.txt — one per line. Order here = order shown to the operator.\n")
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

// scanLogo finds an optional brand logo to show on title cards. Any image dropped
// into the logo\ folder is used (first by name); detected once at startup.
func (s *server) scanLogo() {
	dir := filepath.Join(s.baseDir, "logo")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var found []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if imageExts[strings.ToLower(filepath.Ext(e.Name()))] {
			found = append(found, e.Name())
		}
	}
	if len(found) == 0 {
		return
	}
	sort.Strings(found)
	s.logoFile = filepath.Join(dir, found[0])
	log.Printf("logo: showing %s on title cards", found[0])
}

func (s *server) handleLogo(w http.ResponseWriter, r *http.Request) {
	if s.logoFile == "" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, s.logoFile)
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

// nextID = max-ever + 1, counting soft-deleted sessions, so IDs are never reused.
func (s *server) nextID() string {
	max := 0
	check := func(dir string) {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if n, err := strconv.Atoi(e.Name()); err == nil && n > max {
				max = n
			}
		}
	}
	check(filepath.Join(s.baseDir, "photos"))
	check(filepath.Join(s.baseDir, "photos", "_deleted"))
	return fmt.Sprintf("%03d", max+1)
}

func (s *server) createSession(date string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextID()
	ss := &Session{ID: id, Date: date, Created: time.Now().Format(time.RFC3339), Categories: append([]string{}, s.categories...)}
	base := filepath.Join(s.baseDir, "photos", id)
	for _, c := range ss.Categories {
		if err := os.MkdirAll(filepath.Join(base, c, "Landscape"), 0o755); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Join(base, c, "Portrait"), 0o755); err != nil {
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

func (s *server) handleSessionCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Date string `json:"date"`
	}
	if decode(r, &body) != nil || !validDate(body.Date) {
		http.Error(w, "date must be YYYY-MM-DD", 400)
		return
	}
	ss, err := s.createSession(body.Date)
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

// handleSessionEdit changes only the date label (session.json). The ID and folder
// are untouched, so there's no rename and nothing else has to move.
func (s *server) handleSessionEdit(w http.ResponseWriter, r *http.Request) {
	var body struct{ ID, Date string }
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
	b, _ := json.MarshalIndent(ss, "", "  ")
	err := os.WriteFile(filepath.Join(s.baseDir, "photos", ss.ID, "session.json"), b, 0o644)
	sort.Slice(s.sessions, func(i, j int) bool { return s.sessions[i].Date < s.sessions[j].Date })
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	log.Printf("session %s date edited to %s", body.ID, body.Date)
	s.pushConsole()
	writeJSON(w, ss)
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
	files := s.photoFiles(body.SessionID, body.Category, body.Orientation)
	sc.SessionID, sc.Category, sc.Orientation = body.SessionID, body.Category, body.Orientation
	sc.Files = files
	sc.Count = len(files)
	sc.Position = 0 // selecting a category resets to the start (title card)
	sc.Blackout = false
	s.mu.Unlock()
	s.pushScreen(body.Name)
	s.pushConsole()
	w.WriteHeader(204)
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
	var names []string
	for n := range s.screens {
		names = append(names, n)
	}
	s.mu.Unlock()

	if body.Action == "makelive" {
		for _, n := range names {
			s.pushScreen(n)
		}
	} else {
		s.pushScreen(body.Name)
	}
	s.pushConsole()
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
	writeJSON(w, map[string]any{"files": s.photoFiles(sid, cat, orient)})
}

// handleUpload accepts multipart image uploads into a session/category/orientation
// folder, creating it lazily. Non-images are skipped; name clashes get a " (n)" suffix.
func (s *server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "bad upload", 400)
		return
	}
	sid := r.FormValue("session")
	cat := r.FormValue("category")
	orient := r.FormValue("orientation")
	if !safeName(sid) || !safeName(cat) || !safeName(orient) {
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
	dir := s.photosDir(sid, cat, orient)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	var saved, skipped []string
	for _, fh := range r.MultipartForm.File["files"] {
		base := filepath.Base(fh.Filename)
		if base == "" || base == "." || strings.Contains(base, "..") || !imageExts[strings.ToLower(filepath.Ext(base))] {
			skipped = append(skipped, fh.Filename)
			continue
		}
		name := uniqueName(dir, base)
		if err := saveUpload(fh, filepath.Join(dir, name)); err != nil {
			skipped = append(skipped, fh.Filename)
			continue
		}
		saved = append(saved, name)
	}
	appendToOrder(dir, saved)
	log.Printf("upload: %d saved, %d skipped -> %s", len(saved), len(skipped), dir)
	writeJSON(w, map[string]any{"saved": saved, "skipped": skipped, "files": s.photoFiles(sid, cat, orient)})
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
	log.Printf("photo soft-deleted: %s -> %s", src, dest)
	writeJSON(w, map[string]any{"files": s.photoFiles(body.Session, body.Category, body.Orientation)})
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

func saveUpload(fh *multipart.FileHeader, dest string) error {
	src, err := fh.Open()
	if err != nil {
		return err
	}
	defer src.Close()
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

func serveAsset(w http.ResponseWriter, sub fs.FS, name string) {
	data, err := fs.ReadFile(sub, name)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
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
	if err := os.MkdirAll(filepath.Join(baseDir, "photos"), 0o755); err != nil {
		log.Fatalf("cannot write to %s — is the folder writable? (%v)", baseDir, err)
	}
	if err := os.MkdirAll(filepath.Join(baseDir, "logo"), 0o755); err != nil {
		log.Printf("note: could not create logo folder: %v", err)
	}
	s.loadCategories()
	s.scanSessions()
	s.scanLogo()
	s.loadScreens()

	sub, _ := fs.Sub(webFS, "web")
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		serveAsset(w, sub, "console.html")
	})
	mux.HandleFunc("/output", func(w http.ResponseWriter, r *http.Request) { serveAsset(w, sub, "output.html") })
	mux.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) { serveAsset(w, sub, "admin.html") })
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/session/create", s.handleSessionCreate)
	mux.HandleFunc("/api/session/edit", s.handleSessionEdit)
	mux.HandleFunc("/api/session/delete", s.handleSessionDelete)
	mux.HandleFunc("/api/screen/register", s.handleScreenRegister)
	mux.HandleFunc("/api/screen/create", s.handleScreenCreate)
	mux.HandleFunc("/api/screen/delete", s.handleScreenDelete)
	mux.HandleFunc("/api/screen/load", s.handleScreenLoad)
	mux.HandleFunc("/api/screen/cmd", s.handleScreenCmd)
	mux.HandleFunc("/api/photo", s.handlePhoto)
	mux.HandleFunc("/api/photos", s.handlePhotosList)
	mux.HandleFunc("/api/logo", s.handleLogo)
	mux.HandleFunc("/api/upload", s.handleUpload)
	mux.HandleFunc("/api/order", s.handleOrderSet)
	mux.HandleFunc("/api/photo/delete", s.handlePhotoDelete)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/shutdown", s.handleShutdown)

	port := os.Getenv("PHOTOJUDGE_PORT")
	if port == "" {
		port = "8753"
	}
	addr := "127.0.0.1:" + port
	u := "http://" + addr + "/"
	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		<-s.shutdownCh
		log.Println("Close App pressed — shutting down.")
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	log.Printf("Photo Judge — data dir: %s", baseDir)
	log.Printf("Console ready at %s", u)
	go openBrowser(u)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	log.Println("Stopped.")
}
