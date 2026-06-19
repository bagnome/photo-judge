// Member entries — last-minute competitor photo submission. A console screen can be
// switched to the "Entry QR" type, which shows competitors how to join the host Wi-Fi
// and scan a code to reach the entry page (/entry). There they enter their name and
// upload photos into a chosen category of the locked session. Submissions wait in a
// per-session review queue (entries.json + a _pending image folder) until the operator
// approves them, at which point they are sorted into the normal category/orientation
// tree. Standard library only, like everything else.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// entryRecord is one row of a session's entries.json ledger. The image bytes live in
// photos/<sid>/_pending/<id>.<ext> until the entry is approved (moved into the category
// tree) or rejected (image discarded). Rejected rows stay in the ledger but no longer
// count against a competitor's limit, so a reject frees the slot.
type entryRecord struct {
	ID          string `json:"id"`
	FirstName   string `json:"firstName"`
	LastName    string `json:"lastName"`
	NameKey     string `json:"nameKey"` // normalized "first last" for limit counting
	Category    string `json:"category"`
	Orientation string `json:"orientation"`
	Title       string `json:"title"`
	Ext         string `json:"ext"`    // image extension without the dot (jpg/jpeg/png)
	Status      string `json:"status"` // pending | approved | rejected
	SubmittedAt string `json:"submittedAt"`
	FinalFile   string `json:"finalFile,omitempty"` // filename in the category folder once approved
}

func (s *server) entriesPath(sid string) string {
	return filepath.Join(s.baseDir, "photos", sid, "entries.json")
}
func (s *server) pendingDir(sid string) string {
	return filepath.Join(s.baseDir, "photos", sid, "_pending")
}

func (s *server) loadEntries(sid string) []entryRecord {
	var recs []entryRecord
	data, err := os.ReadFile(s.entriesPath(sid))
	if err != nil {
		return recs
	}
	_ = json.Unmarshal(data, &recs)
	return recs
}

func (s *server) saveEntries(sid string, recs []entryRecord) error {
	if recs == nil {
		recs = []entryRecord{}
	}
	b, _ := json.MarshalIndent(recs, "", "  ")
	return os.WriteFile(s.entriesPath(sid), b, 0o644)
}

// pendingCount returns the number of pending entries for a session. Assumes s.mu held
// (it's called from consoleSnapshot).
func (s *server) pendingCount(sid string) int {
	if sid == "" {
		return 0
	}
	n := 0
	for _, e := range s.loadEntries(sid) {
		if e.Status == "pending" {
			n++
		}
	}
	return n
}

// normalizeNameKey lower-cases a name and collapses whitespace, so the same person is
// counted the same however their name was typed.
func normalizeNameKey(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// entryNameKey is the limit-counting identity for a competitor's first + last name.
func entryNameKey(first, last string) string { return normalizeNameKey(first + " " + last) }

// countByPhotographerAll tallies how many of a session's photos are attributable to a
// competitor by their (normalized) photographer name. It counts everything already in the
// category folders — operator-uploaded photos AND approved entries, both via names.json —
// plus still-pending entries from the ledger. Rejected entries don't count. Counting by
// name across the whole session (not per-device) means the same name can't bypass a limit
// from a fresh browser. Assumes s.mu held.
func (s *server) countByPhotographerAll(sid, nameKey string) (total int, byCat map[string]int) {
	byCat = map[string]int{}
	if ss := s.sessionByID(sid); ss != nil {
		cats := append(append([]string{}, ss.Categories...), ss.InactiveCategories...)
		for _, c := range cats {
			for _, orient := range []string{"Landscape", "Portrait"} {
				dir := s.photosDir(sid, c, orient)
				for file, ph := range loadNames(dir) {
					if normalizeNameKey(ph) != nameKey {
						continue
					}
					if _, err := os.Stat(filepath.Join(dir, file)); err != nil {
						continue // stale names.json entry — the photo is gone
					}
					total++
					byCat[c]++
				}
			}
		}
	}
	for _, e := range s.loadEntries(sid) {
		if e.Status == "pending" && e.NameKey == nameKey {
			total++
			byCat[e.Category]++
		}
	}
	return
}

// writeEntryImage saves an uploaded entry photo to dst, embedding the title (and author)
// into a JPEG's EXIF so the file carries its own metadata. Returns the detected orientation.
func writeEntryImage(fh multipart.File, name, dst, title, author string) (string, error) {
	orient := orientationOf(fh, name)
	if _, err := fh.Seek(0, 0); err != nil {
		return "", err
	}
	raw, err := io.ReadAll(fh)
	if err != nil {
		return "", err
	}
	if ext := strings.ToLower(filepath.Ext(name)); ext == ".jpg" || ext == ".jpeg" {
		raw = jpegSetTitleArtist(raw, title, author)
	}
	return orient, os.WriteFile(dst, raw, 0o644)
}

// ---- Wi-Fi (host network shown on the Entry-QR screen) --------------------

// detectWifi best-effort reads the host's current Wi-Fi name and password via netsh,
// caching them for the entry instructions. Empty on any failure (e.g. host on Ethernet).
// Called once at startup, single-threaded, so no locking.
func (s *server) detectWifi() {
	ssid := firstNetshValue("SSID", runNetsh("wlan", "show", "interfaces"))
	if ssid == "" {
		return
	}
	s.detectedWifiSSID = ssid
	s.detectedWifiPassword = firstNetshValue("Key Content", runNetsh("wlan", "show", "profile", "name="+ssid, "key=clear"))
}

func runNetsh(args ...string) string {
	out, err := exec.Command("netsh", args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// firstNetshValue returns the value of the first "Key : Value" line whose key matches
// (case-insensitively), as printed by netsh.
func firstNetshValue(key, out string) string {
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(k), key) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// resolvedWifi returns the Wi-Fi to publish: operator-entered Settings values win; if
// the SSID field is blank, fall back entirely to the auto-detected pair. Assumes s.mu held.
func (s *server) resolvedWifi() (ssid, pass string) {
	ssid = strings.TrimSpace(s.settings.WifiSSID)
	if ssid != "" {
		return ssid, s.settings.WifiPassword
	}
	return s.detectedWifiSSID, s.detectedWifiPassword
}

// wifiJoinString builds the standard WIFI: payload phones use to join a network from a QR.
func wifiJoinString(ssid, pass string) string {
	esc := strings.NewReplacer(`\`, `\\`, `;`, `\;`, `,`, `\,`, `:`, `\:`, `"`, `\"`).Replace
	if strings.TrimSpace(pass) == "" {
		return "WIFI:T:nopass;S:" + esc(ssid) + ";;"
	}
	return "WIFI:T:WPA;S:" + esc(ssid) + ";P:" + esc(pass) + ";;"
}

// ---- entry-form state (for the landing + entry pages and the entry screen) -

type entryStatePayload struct {
	Open         bool     `json:"open"`
	SessionID    string   `json:"sessionId,omitempty"`
	Date         string   `json:"date,omitempty"`
	Description  string   `json:"description,omitempty"`
	Categories   []string `json:"categories"`
	LimitPer     int      `json:"limitPerCompetitor"` // 0 = unlimited
	LimitPerCat  int      `json:"limitPerCategory"`   // 0 = unlimited
	WifiSSID     string   `json:"wifiSSID,omitempty"`
	WifiPassword string   `json:"wifiPassword,omitempty"`
}

// entryStateLocked builds the public entry-form state. Assumes s.mu held.
func (s *server) entryStateLocked() entryStatePayload {
	p := entryStatePayload{
		Open:        s.entryOpen && s.settings.EntriesEnabled,
		Categories:  []string{},
		LimitPer:    s.settings.MaxEntriesPerCompetitor,
		LimitPerCat: s.settings.MaxEntriesPerCategory,
	}
	p.WifiSSID, p.WifiPassword = s.resolvedWifi()
	if ss := s.sessionByID(s.entrySessionID); ss != nil {
		p.SessionID, p.Date, p.Description = ss.ID, ss.Date, ss.Description
		p.Categories = append([]string{}, ss.Categories...)
	}
	return p
}

func (s *server) entryStateJSON() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(s.entryStateLocked())
	return b
}

// entryView is the View an Entry-QR output window renders. Assumes s.mu held.
func (s *server) entryView() View {
	v := View{Mode: "entry", EntryOpen: s.entryOpen && s.settings.EntriesEnabled}
	if s.lanAccess {
		if urls := s.lanURLs(); len(urls) > 0 {
			v.EntryURL = urls[0] + "/entry"
		}
	}
	ssid, pass := s.resolvedWifi()
	v.WifiSSID, v.WifiPassword = ssid, pass
	if ssid != "" {
		v.WifiQR = wifiJoinString(ssid, pass)
	}
	return v
}

// pushEntry refreshes the landing + entry pages (role=entry SSE).
func (s *server) pushEntry() { s.h.sendEntries(s.entryStateJSON()) }

// pushEntryAll fans an entry-form change out everywhere it shows: the entry/landing
// pages, the console (status + pending badge), and every screen (Entry-QR views change).
func (s *server) pushEntryAll() {
	s.pushEntry()
	s.pushConsole()
	s.pushAllScreens()
}

// ---- handlers -------------------------------------------------------------

// handleScreenType switches a screen between "slideshow" and "entry". Setting it to
// "entry" opens the entry form (locked to the given session) if it isn't already open.
func (s *server) handleScreenType(w http.ResponseWriter, r *http.Request) {
	var body struct{ Name, Type, SessionID string }
	if decode(r, &body) != nil {
		http.Error(w, "bad body", 400)
		return
	}
	if body.Type != "slideshow" && body.Type != "entry" && body.Type != "judge" {
		http.Error(w, "bad type", 400)
		return
	}
	s.mu.Lock()
	sc := s.screens[body.Name]
	if sc == nil {
		s.mu.Unlock()
		http.Error(w, "no such screen", 404)
		return
	}
	if body.Type == "entry" {
		if !s.settings.EntriesEnabled {
			s.mu.Unlock()
			http.Error(w, "Member entries are turned off in Settings.", http.StatusConflict)
			return
		}
		if !s.entryOpen {
			if !safeName(body.SessionID) || s.sessionByID(body.SessionID) == nil {
				s.mu.Unlock()
				http.Error(w, "no such session", 404)
				return
			}
			s.entryOpen = true
			s.entrySessionID = body.SessionID
		}
	}
	if body.Type == "judge" && !s.settings.JudgeScoringEnabled {
		s.mu.Unlock()
		http.Error(w, "Judge scoring is turned off in Settings.", http.StatusConflict)
		return
	}
	sc.Type = body.Type
	s.mu.Unlock()
	s.pushEntryAll()
	s.pushJudge()
	w.WriteHeader(204)
}

// handleEntryOpen (re)opens the entry form, locking it to the given session.
func (s *server) handleEntryOpen(w http.ResponseWriter, r *http.Request) {
	var body struct{ SessionID string }
	if decode(r, &body) != nil || !safeName(body.SessionID) {
		http.Error(w, "bad body", 400)
		return
	}
	s.mu.Lock()
	if !s.settings.EntriesEnabled {
		s.mu.Unlock()
		http.Error(w, "Member entries are turned off in Settings.", http.StatusConflict)
		return
	}
	if s.sessionByID(body.SessionID) == nil {
		s.mu.Unlock()
		http.Error(w, "no such session", 404)
		return
	}
	s.entryOpen = true
	s.entrySessionID = body.SessionID
	s.mu.Unlock()
	s.pushEntryAll()
	w.WriteHeader(204)
}

// handleEntryClose stops accepting entries. The locked session id is kept so pending
// entries stay reviewable and the form can be reopened to the same session.
func (s *server) handleEntryClose(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.entryOpen = false
	s.mu.Unlock()
	s.pushEntryAll()
	w.WriteHeader(204)
}

func (s *server) handleEntryState(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	p := s.entryStateLocked()
	s.mu.Unlock()
	writeJSON(w, p)
}

// handleEntrySubmit accepts one competitor upload (multipart: firstName, lastName,
// category, title, file) into the locked session's pending queue, enforcing limits.
func (s *server) handleEntrySubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "bad upload", 400)
		return
	}
	s.mu.Lock()
	open := s.entryOpen && s.settings.EntriesEnabled
	sid := s.entrySessionID
	limitPer := s.settings.MaxEntriesPerCompetitor
	limitPerCat := s.settings.MaxEntriesPerCategory
	requireApproval := s.settings.EntryRequireApproval
	var cats []string
	if ss := s.sessionByID(sid); ss != nil {
		cats = append([]string{}, ss.Categories...)
	}
	s.mu.Unlock()

	if !open || sid == "" {
		http.Error(w, "The entry form is closed.", http.StatusConflict)
		return
	}
	first := strings.TrimSpace(r.FormValue("firstName"))
	last := strings.TrimSpace(r.FormValue("lastName"))
	if first == "" || last == "" {
		http.Error(w, "First and last name are required.", 400)
		return
	}
	cat := r.FormValue("category")
	if !safeName(cat) || !containsString(cats, cat) {
		http.Error(w, "Pick a valid category.", 400)
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if len(title) > 120 {
		title = title[:120]
	}
	fh, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Choose a photo to upload.", 400)
		return
	}
	defer fh.Close()
	ext := strings.ToLower(filepath.Ext(hdr.Filename))
	if !uploadExts[ext] {
		http.Error(w, "Only JPG and PNG photos are accepted.", 400)
		return
	}
	// Save the image first (slow I/O, no lock; EXIF title/author embedded for JPEGs),
	// then do the limit check + ledger append atomically under s.mu, deleting the saved
	// file if the competitor is over a limit.
	dir := s.pendingDir(sid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	id := strconv.FormatInt(time.Now().UnixNano(), 10)
	imgPath := filepath.Join(dir, id+ext)
	for { // vanishingly unlikely clash, but keep ids unique
		if _, e := os.Stat(imgPath); e != nil {
			break
		}
		id = strconv.FormatInt(time.Now().UnixNano(), 10)
		imgPath = filepath.Join(dir, id+ext)
	}
	orient, err := writeEntryImage(fh, hdr.Filename, imgPath, title, first+" "+last)
	if err != nil {
		http.Error(w, "could not save photo", 500)
		return
	}

	key := entryNameKey(first, last)
	s.mu.Lock()
	total, byCat := s.countByPhotographerAll(sid, key)
	inCat := byCat[cat]
	if limitPer > 0 && total >= limitPer {
		s.mu.Unlock()
		_ = os.Remove(imgPath)
		http.Error(w, fmt.Sprintf("You've reached your limit of %d photo%s for this competition.", limitPer, plural(limitPer, "", "s")), http.StatusConflict)
		return
	}
	if limitPerCat > 0 && inCat >= limitPerCat {
		s.mu.Unlock()
		_ = os.Remove(imgPath)
		http.Error(w, fmt.Sprintf("You've reached your limit of %d photo%s in %s.", limitPerCat, plural(limitPerCat, "", "s"), cat), http.StatusConflict)
		return
	}
	recs := append(s.loadEntries(sid), entryRecord{
		ID: id, FirstName: first, LastName: last, NameKey: key,
		Category: cat, Orientation: orient, Title: title, Ext: strings.TrimPrefix(ext, "."),
		Status: "pending", SubmittedAt: time.Now().Format(time.RFC3339),
	})
	// When approval is turned off, add the photo straight into the session.
	if !requireApproval {
		if aerr := s.approveEntryLocked(sid, &recs[len(recs)-1]); aerr != nil {
			log.Printf("auto-approve failed for %s: %v — left pending", recs[len(recs)-1].ID, aerr)
		}
	}
	err = s.saveEntries(sid, recs)
	s.mu.Unlock()
	if err != nil {
		_ = os.Remove(imgPath)
		http.Error(w, err.Error(), 500)
		return
	}
	s.pushConsole() // operator's pending badge
	s.pushEntry()   // refresh this competitor's other open tabs
	log.Printf("entry submitted: %s %s -> %s/%s (%s), autoApprove=%v", first, last, sid, cat, orient, !requireApproval)
	writeJSON(w, map[string]any{"ok": true})
}

// handleEntryPending lists a session's pending entries for the review queue. Defaults to
// the currently locked entry session when no session id is given.
func (s *server) handleEntryPending(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("sessionId")
	if sid == "" {
		s.mu.Lock()
		sid = s.entrySessionID
		s.mu.Unlock()
	}
	out := []map[string]any{}
	if safeName(sid) {
		for _, e := range s.loadEntries(sid) {
			if e.Status != "pending" {
				continue
			}
			out = append(out, map[string]any{
				"id": e.ID, "firstName": e.FirstName, "lastName": e.LastName,
				"category": e.Category, "orientation": e.Orientation, "title": e.Title,
				"submittedAt": e.SubmittedAt,
				"photoUrl":    "/api/entry/photo?sessionId=" + sid + "&id=" + e.ID,
			})
		}
	}
	writeJSON(w, map[string]any{"sessionId": sid, "pending": out})
}

// handleEntryPhoto serves a pending entry's image for review.
func (s *server) handleEntryPhoto(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sid, id := q.Get("sessionId"), q.Get("id")
	if !safeName(sid) || !safeName(id) {
		http.Error(w, "bad request", 400)
		return
	}
	for _, e := range s.loadEntries(sid) {
		if e.ID == id {
			if e.Status == "approved" && e.FinalFile != "" {
				http.ServeFile(w, r, filepath.Join(s.photosDir(sid, e.Category, e.Orientation), e.FinalFile))
			} else {
				http.ServeFile(w, r, filepath.Join(s.pendingDir(sid), e.ID+"."+e.Ext))
			}
			return
		}
	}
	http.NotFound(w, r)
}

// handleEntryApprove moves a pending entry into the normal category/orientation tree
// (with the photographer name and order updated) and marks it approved.
func (s *server) handleEntryApprove(w http.ResponseWriter, r *http.Request) {
	var body struct{ SessionID, ID string }
	if decode(r, &body) != nil || !safeName(body.SessionID) || !safeName(body.ID) {
		http.Error(w, "bad body", 400)
		return
	}
	s.mu.Lock()
	recs := s.loadEntries(body.SessionID)
	idx := -1
	for i := range recs {
		if recs[i].ID == body.ID && recs[i].Status == "pending" {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		http.Error(w, "no such pending entry", 404)
		return
	}
	if err := s.approveEntryLocked(body.SessionID, &recs[idx]); err != nil {
		s.mu.Unlock()
		http.Error(w, err.Error(), 500)
		return
	}
	rec := recs[idx]
	_ = s.saveEntries(body.SessionID, recs)
	s.mu.Unlock()
	s.pushConsole()
	s.pushEntry() // the competitor sees their entry flip to "Accepted"
	log.Printf("entry approved: %s -> %s/%s/%s/%s", rec.ID, body.SessionID, rec.Category, rec.Orientation, rec.FinalFile)
	writeJSON(w, map[string]any{"ok": true})
}

// approveEntryLocked moves a pending entry's image into the normal category/orientation
// tree, records the photographer and display order, and marks the record approved with its
// final filename (so the competitor's thumbnail still resolves). Mutates rec; assumes s.mu held.
func (s *server) approveEntryLocked(sid string, rec *entryRecord) error {
	destDir := s.photosDir(sid, rec.Category, rec.Orientation)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	name := sanitizeUploadName(rec.Title, "."+rec.Ext)
	if name == "" {
		name = "entry-" + rec.ID + "." + rec.Ext
	}
	name = uniqueName(destDir, name)
	src := filepath.Join(s.pendingDir(sid), rec.ID+"."+rec.Ext)
	if err := os.Rename(src, filepath.Join(destDir, name)); err != nil {
		return err
	}
	_ = setName(destDir, name, rec.FirstName+" "+rec.LastName)
	appendToOrder(destDir, []string{name})
	rec.Status = "approved"
	rec.FinalFile = name
	return nil
}

// handleEntryReject marks a pending entry rejected, freeing the competitor's slot. The
// image is kept in the pending folder so the competitor still sees their (red-outlined)
// thumbnail on the entry page and can fix and resubmit it.
func (s *server) handleEntryReject(w http.ResponseWriter, r *http.Request) {
	var body struct{ SessionID, ID string }
	if decode(r, &body) != nil || !safeName(body.SessionID) || !safeName(body.ID) {
		http.Error(w, "bad body", 400)
		return
	}
	s.mu.Lock()
	recs := s.loadEntries(body.SessionID)
	idx := -1
	for i := range recs {
		if recs[i].ID == body.ID && recs[i].Status == "pending" {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		http.Error(w, "no such pending entry", 404)
		return
	}
	recs[idx].Status = "rejected"
	_ = s.saveEntries(body.SessionID, recs)
	s.mu.Unlock()
	s.pushConsole()
	s.pushEntry() // the competitor sees their entry flip to "Rejected"
	log.Printf("entry rejected: %s (%s)", recs[idx].ID, body.SessionID)
	writeJSON(w, map[string]any{"ok": true})
}

// handleEntryMine returns a competitor's own entries (all statuses) for the locked session,
// plus their session-wide photo count so the entry page can show an accurate quota.
func (s *server) handleEntryMine(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	first := strings.TrimSpace(q.Get("first"))
	last := strings.TrimSpace(q.Get("last"))
	s.mu.Lock()
	sid := s.entrySessionID
	open := s.entryOpen && s.settings.EntriesEnabled
	limitPer := s.settings.MaxEntriesPerCompetitor
	limitPerCat := s.settings.MaxEntriesPerCategory
	total := 0
	byCat := map[string]int{}
	entries := []map[string]any{}
	if first != "" && last != "" && safeName(sid) {
		key := entryNameKey(first, last)
		total, byCat = s.countByPhotographerAll(sid, key)
		for _, e := range s.loadEntries(sid) {
			if e.NameKey != key {
				continue
			}
			entries = append(entries, map[string]any{
				"id": e.ID, "title": e.Title, "category": e.Category,
				"orientation": e.Orientation, "status": e.Status,
				"photoUrl": "/api/entry/photo?sessionId=" + sid + "&id=" + e.ID,
			})
		}
	}
	s.mu.Unlock()
	writeJSON(w, map[string]any{
		"sessionId": sid, "open": open, "entries": entries,
		"total": total, "byCat": byCat,
		"limitPerCompetitor": limitPer, "limitPerCategory": limitPerCat,
	})
}

// handleEntryEdit lets a competitor change the title/category of their own not-yet-accepted
// entry, optionally replacing the photo, which puts it back to "pending" for review.
func (s *server) handleEntryEdit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "bad upload", 400)
		return
	}
	first := strings.TrimSpace(r.FormValue("firstName"))
	last := strings.TrimSpace(r.FormValue("lastName"))
	id := r.FormValue("id")
	title := strings.TrimSpace(r.FormValue("title"))
	if len(title) > 120 {
		title = title[:120]
	}
	cat := r.FormValue("category")
	if first == "" || last == "" || !safeName(id) {
		http.Error(w, "bad request", 400)
		return
	}
	key := entryNameKey(first, last)
	s.mu.Lock()
	if !(s.entryOpen && s.settings.EntriesEnabled) || s.entrySessionID == "" {
		s.mu.Unlock()
		http.Error(w, "The entry form is closed.", http.StatusConflict)
		return
	}
	sid := s.entrySessionID
	var cats []string
	if ss := s.sessionByID(sid); ss != nil {
		cats = append([]string{}, ss.Categories...)
	}
	if !safeName(cat) || !containsString(cats, cat) {
		s.mu.Unlock()
		http.Error(w, "Pick a valid category.", 400)
		return
	}
	recs := s.loadEntries(sid)
	idx := -1
	for i := range recs {
		if recs[i].ID == id && recs[i].NameKey == key {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		http.Error(w, "no such entry", 404)
		return
	}
	if recs[idx].Status == "approved" {
		s.mu.Unlock()
		http.Error(w, "That entry has already been accepted.", http.StatusConflict)
		return
	}
	pdir := s.pendingDir(sid)
	if fh, hdr, ferr := r.FormFile("file"); ferr == nil {
		defer fh.Close()
		ext := strings.ToLower(filepath.Ext(hdr.Filename))
		if !uploadExts[ext] {
			s.mu.Unlock()
			http.Error(w, "Only JPG and PNG photos are accepted.", 400)
			return
		}
		orient, werr := writeEntryImage(fh, hdr.Filename, filepath.Join(pdir, id+ext), title, first+" "+last)
		if werr != nil {
			s.mu.Unlock()
			http.Error(w, "could not save photo", 500)
			return
		}
		if old := recs[idx].Ext; "."+old != ext {
			_ = os.Remove(filepath.Join(pdir, id+"."+old))
		}
		recs[idx].Ext = strings.TrimPrefix(ext, ".")
		recs[idx].Orientation = orient
	} else if e := recs[idx].Ext; e == "jpg" || e == "jpeg" {
		// No new image — refresh the existing JPEG's EXIF so its embedded title stays correct.
		p := filepath.Join(pdir, id+"."+e)
		if raw, err := os.ReadFile(p); err == nil {
			_ = os.WriteFile(p, jpegSetTitleArtist(raw, title, first+" "+last), 0o644)
		}
	}
	recs[idx].Title = title
	recs[idx].Category = cat
	recs[idx].Status = "pending"
	recs[idx].FinalFile = ""
	err := s.saveEntries(sid, recs)
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.pushConsole()
	s.pushEntry()
	writeJSON(w, map[string]any{"ok": true})
}

// handleEntryRemove deletes a competitor's own not-yet-accepted entry (and its image).
func (s *server) handleEntryRemove(w http.ResponseWriter, r *http.Request) {
	var body struct{ FirstName, LastName, ID string }
	if decode(r, &body) != nil || body.FirstName == "" || body.LastName == "" || !safeName(body.ID) {
		http.Error(w, "bad request", 400)
		return
	}
	key := entryNameKey(body.FirstName, body.LastName)
	s.mu.Lock()
	sid := s.entrySessionID
	recs := s.loadEntries(sid)
	idx := -1
	for i := range recs {
		if recs[i].ID == body.ID && recs[i].NameKey == key {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		http.Error(w, "no such entry", 404)
		return
	}
	if recs[idx].Status == "approved" {
		s.mu.Unlock()
		http.Error(w, "That entry has already been accepted.", http.StatusConflict)
		return
	}
	rec := recs[idx]
	_ = os.Remove(filepath.Join(s.pendingDir(sid), rec.ID+"."+rec.Ext))
	recs = append(recs[:idx], recs[idx+1:]...)
	_ = s.saveEntries(sid, recs)
	s.mu.Unlock()
	s.pushConsole()
	s.pushEntry()
	log.Printf("entry removed: %s (%s)", rec.ID, sid)
	writeJSON(w, map[string]any{"ok": true})
}

// ---- small helpers --------------------------------------------------------

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
