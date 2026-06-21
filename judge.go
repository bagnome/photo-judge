// Judge scoring: judges connect from their phones via a Judge-QR screen and submit a
// score for whatever photo is live; the app combines the scores (average or total) into
// the photo's score, and the scorekeeper sees each judge's score + the combined value.
// Per-judge scores live in a judgescores.json sidecar; deferrals + resubmission requests
// are transient in-memory. Standard library only. See also the Member Entries pattern.
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func parseScore(s string) (float64, error) { return strconv.ParseFloat(strings.TrimSpace(s), 64) }

// trimNum formats a float without trailing zeros (8.50 -> "8.5", 9.0 -> "9").
func trimNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// judgeRosterEntry is a connected judge (presence tracked by SSE connection count).
type judgeRosterEntry struct {
	name      string
	alternate bool
	conns     int
}

// judgeScore is one judge's score for one photo, persisted in judgescores.json.
type judgeScore struct {
	Name      string `json:"name"`
	Score     string `json:"score"`
	Alternate bool   `json:"alternate"`
}

// ---- per-folder judgescores.json: filename -> judgeKey -> judgeScore ----

func loadJudgeScores(dir string) map[string]map[string]judgeScore {
	m := map[string]map[string]judgeScore{}
	data, err := os.ReadFile(filepath.Join(dir, "judgescores.json"))
	if err != nil {
		return m
	}
	_ = json.Unmarshal(data, &m)
	if m == nil {
		m = map[string]map[string]judgeScore{}
	}
	return m
}

func writeJudgeScores(dir string, m map[string]map[string]judgeScore) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(filepath.Join(dir, "judgescores.json"), b, 0o644)
}

func (s *server) ensureJudgeMaps() { // assumes s.mu held
	if s.judgeRoster == nil {
		s.judgeRoster = map[string]*judgeRosterEntry{}
	}
	if s.judgeDeferred == nil {
		s.judgeDeferred = map[string]bool{}
	}
	if s.judgeRequested == nil {
		s.judgeRequested = map[string]bool{}
	}
}

// judgeJoin/Leave track presence from the judge's SSE connection (name + alternate).
func (s *server) judgeJoin(key, name string, alt bool) {
	if key == "" {
		return
	}
	s.mu.Lock()
	s.ensureJudgeMaps()
	e := s.judgeRoster[key]
	if e == nil {
		e = &judgeRosterEntry{}
		s.judgeRoster[key] = e
	}
	e.name, e.alternate = name, alt
	e.conns++
	s.mu.Unlock()
	s.pushConsole()
}

func (s *server) judgeLeave(key string) {
	if key == "" {
		return
	}
	s.mu.Lock()
	if e := s.judgeRoster[key]; e != nil {
		e.conns--
		if e.conns <= 0 {
			delete(s.judgeRoster, key)
		}
	}
	s.mu.Unlock()
	s.pushConsole()
}

func (s *server) pushJudge() { s.h.sendJudges(s.judgeStateJSON()) }

// ---- the live judging target: the single live slideshow photo ----

type judgeTarget struct {
	sessionID, category, orientation, file, title, photographer string
	ok                                                          bool
}

func targetKey(sid, cat, or, file string) string { return sid + "|" + cat + "|" + or + "|" + file }
func (t judgeTarget) key() string                { return targetKey(t.sessionID, t.category, t.orientation, t.file) }
func (t judgeTarget) matches(sid, cat, or, file string) bool {
	return t.ok && t.sessionID == sid && t.category == cat && t.orientation == or && t.file == file
}

// judgingTarget returns the live slideshow screen's current photo (judge mode forces a
// single live screen, so there's one). Assumes s.mu held.
func (s *server) judgingTarget() judgeTarget {
	for _, sc := range s.screens {
		if sc.Type != "" && sc.Type != "slideshow" {
			continue
		}
		if sc.Blackout || sc.Position < 1 || sc.Position > sc.Count || sc.Position-1 >= len(sc.Files) {
			continue
		}
		file := sc.Files[sc.Position-1]
		photog := loadNames(s.photosDir(sc.SessionID, sc.Category, sc.Orientation))[file]
		return judgeTarget{sessionID: sc.SessionID, category: sc.Category, orientation: sc.Orientation,
			file: file, title: strings.TrimSuffix(file, filepath.Ext(file)), photographer: photog, ok: true}
	}
	return judgeTarget{}
}

func aggOf(ss *Session) string {
	if ss != nil && ss.JudgeAggregation == "total" {
		return "total"
	}
	return "average"
}

// alternateActiveLocked reports whether the backup judge should score this photo — i.e.
// some judge deferred it, or auto-detect matched the photographer to a present judge.
func (s *server) alternateActiveLocked(t judgeTarget, ss *Session) bool {
	prefix := t.key() + "|"
	for k, v := range s.judgeDeferred {
		if v && strings.HasPrefix(k, prefix) {
			return true
		}
	}
	if ss != nil && ss.JudgeAutodetect && t.photographer != "" {
		if e := s.judgeRoster[normalizeNameKey(t.photographer)]; e != nil && !e.alternate {
			return true
		}
	}
	return false
}

// ---- Judge-QR monitor view ----

func (s *server) judgeView() View { // assumes s.mu held
	v := View{Mode: "judge", JudgeOn: s.settings.JudgeScoringEnabled}
	if s.lanAccess {
		if urls := s.lanURLs(); len(urls) > 0 {
			v.JudgeURL = urls[0] + "/judge"
		}
	}
	ssid, pass := s.resolvedWifi()
	v.WifiSSID, v.WifiPassword = ssid, pass
	if ssid != "" {
		v.WifiQR = wifiJoinString(ssid, pass)
	}
	return v
}

// ---- shared judge state (SSE broadcast + the GET base) ----

func (s *server) judgeSharedLocked(t judgeTarget) map[string]any { // assumes s.mu held
	m := map[string]any{"enabled": s.settings.JudgeScoringEnabled, "active": s.judgeActive, "open": t.ok}
	if !t.ok {
		return m
	}
	ss := s.sessionByID(t.sessionID)
	m["title"], m["category"] = t.title, t.category
	m["session"], m["orientation"], m["file"] = t.sessionID, t.orientation, t.file
	if ss != nil {
		if ss.JudgeShowPhotographer {
			m["photographer"] = t.photographer
		}
		m["aggregation"] = aggOf(ss)
		m["judgesNeeded"] = ss.JudgesNeeded
		m["anonymize"], m["alternate"] = ss.JudgeAnonymize, ss.JudgeAlternate
		m["min"], m["max"], m["increment"] = ss.JudgeMin, ss.JudgeMax, ss.JudgeIncrement
	}
	return m
}

func (s *server) judgeStateJSON() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(s.judgeSharedLocked(s.judgingTarget()))
	return b
}

// handleJudgeState returns the shared state plus this judge's personal status.
func (s *server) handleJudgeState(w http.ResponseWriter, r *http.Request) {
	key := normalizeNameKey(r.URL.Query().Get("name"))
	alt := r.URL.Query().Get("alt") == "1"
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureJudgeMaps()
	t := s.judgingTarget()
	resp := s.judgeSharedLocked(t)
	you := map[string]any{"isAlternate": alt}
	if t.ok && key != "" {
		ss := s.sessionByID(t.sessionID)
		tk := t.key()
		autoConflict := ss != nil && ss.JudgeAutodetect && !alt && key == normalizeNameKey(t.photographer)
		deferred := s.judgeDeferred[tk+"|"+key] || autoConflict
		you["deferred"], you["requested"] = deferred, s.judgeRequested[tk+"|"+key]
		if js := loadJudgeScores(s.photosDir(t.sessionID, t.category, t.orientation))[t.file]; js != nil {
			if e, okk := js[key]; okk {
				you["submitted"], you["score"] = e.Score != "", e.Score
			}
		}
		you["mayScore"] = s.judgeActive && !deferred && (!alt || s.alternateActiveLocked(t, ss))
	}
	resp["you"] = you
	writeJSON(w, resp)
}

// ---- scoring rule validation + combined recompute ----

// validateJudgeScore returns "" when score is allowed by the session's min/max/increment.
func validateJudgeScore(score string, ss *Session) string {
	v, err := parseScore(score)
	if err != nil {
		return "score must be a number"
	}
	if ss == nil {
		return ""
	}
	if ss.JudgeMin != nil && v < *ss.JudgeMin-1e-9 {
		return fmt.Sprintf("score can't be below %s", trimNum(*ss.JudgeMin))
	}
	if ss.JudgeMax != nil && v > *ss.JudgeMax+1e-9 {
		return fmt.Sprintf("score can't be above %s", trimNum(*ss.JudgeMax))
	}
	if ss.JudgeIncrement != nil && *ss.JudgeIncrement > 0 {
		base := 0.0
		if ss.JudgeMin != nil {
			base = *ss.JudgeMin
		}
		steps := (v - base) / *ss.JudgeIncrement
		if math.Abs(steps-math.Round(steps)) > 1e-6 {
			return fmt.Sprintf("score must be in steps of %s", trimNum(*ss.JudgeIncrement))
		}
	}
	return ""
}

// recomputeCombinedLocked aggregates the photo's non-deferred judge scores (average or
// total) and writes the result to the normal scores.json. Assumes s.mu held.
func (s *server) recomputeCombinedLocked(t judgeTarget) {
	if s.sessionByID(t.sessionID).locked() {
		return // scores are frozen once the session has ended
	}
	dir := s.photosDir(t.sessionID, t.category, t.orientation)
	js := loadJudgeScores(dir)[t.file]
	prefix := t.key() + "|"
	var sum float64
	var n int
	for jk, e := range js {
		if s.judgeDeferred[prefix+jk] {
			continue
		}
		if v, err := parseScore(e.Score); err == nil {
			sum += v
			n++
		}
	}
	if n == 0 {
		setScore(dir, t.file, "")
		return
	}
	combined := sum
	if aggOf(s.sessionByID(t.sessionID)) == "average" {
		combined = sum / float64(n)
	}
	setScore(dir, t.file, trimNum(math.Round(combined*100)/100))
}

// ---- judge actions ----

func (s *server) handleJudgeSubmit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Session, Category, Orientation, File, Name, Score string
		Alternate                                         bool
	}
	if decode(r, &body) != nil {
		http.Error(w, "bad body", 400)
		return
	}
	key := normalizeNameKey(body.Name)
	if key == "" {
		http.Error(w, "enter your name first", 400)
		return
	}
	score := strings.TrimSpace(body.Score)
	s.mu.Lock()
	s.ensureJudgeMaps()
	if !s.judgeActive {
		s.mu.Unlock()
		http.Error(w, "the judging session hasn't started yet", http.StatusConflict)
		return
	}
	t := s.judgingTarget()
	if !t.matches(body.Session, body.Category, body.Orientation, body.File) {
		s.mu.Unlock()
		http.Error(w, "the photo changed — check the screen before scoring", http.StatusConflict)
		return
	}
	if s.sessionByID(t.sessionID).locked() {
		s.mu.Unlock()
		http.Error(w, "this session has ended — scores are locked", http.StatusConflict)
		return
	}
	if msg := validateJudgeScore(score, s.sessionByID(t.sessionID)); msg != "" {
		s.mu.Unlock()
		http.Error(w, msg, 400)
		return
	}
	dir := s.photosDir(t.sessionID, t.category, t.orientation)
	js := loadJudgeScores(dir)
	if js[t.file] == nil {
		js[t.file] = map[string]judgeScore{}
	}
	js[t.file][key] = judgeScore{Name: strings.TrimSpace(body.Name), Score: score, Alternate: body.Alternate}
	_ = writeJudgeScores(dir, js)
	tk := t.key()
	delete(s.judgeDeferred, tk+"|"+key) // submitting un-defers
	delete(s.judgeRequested, tk+"|"+key)
	s.recomputeCombinedLocked(t)
	s.mu.Unlock()
	s.pushJudge()
	s.pushConsole()
	w.WriteHeader(204)
}

func (s *server) handleJudgeDefer(w http.ResponseWriter, r *http.Request) {
	var body struct{ Session, Category, Orientation, File, Name string }
	if decode(r, &body) != nil {
		http.Error(w, "bad body", 400)
		return
	}
	key := normalizeNameKey(body.Name)
	if key == "" {
		http.Error(w, "enter your name first", 400)
		return
	}
	s.mu.Lock()
	s.ensureJudgeMaps()
	if !s.judgeActive {
		s.mu.Unlock()
		http.Error(w, "the judging session hasn't started yet", http.StatusConflict)
		return
	}
	t := s.judgingTarget()
	if !t.matches(body.Session, body.Category, body.Orientation, body.File) {
		s.mu.Unlock()
		http.Error(w, "the photo changed", http.StatusConflict)
		return
	}
	if s.sessionByID(t.sessionID).locked() {
		s.mu.Unlock()
		http.Error(w, "this session has ended — scores are locked", http.StatusConflict)
		return
	}
	s.judgeDeferred[t.key()+"|"+key] = true
	dir := s.photosDir(t.sessionID, t.category, t.orientation)
	if js := loadJudgeScores(dir); js[t.file] != nil {
		delete(js[t.file], key) // a deferred judge contributes no score
		_ = writeJudgeScores(dir, js)
	}
	delete(s.judgeRequested, t.key()+"|"+key)
	s.recomputeCombinedLocked(t)
	s.mu.Unlock()
	s.pushJudge()
	s.pushConsole()
	w.WriteHeader(204)
}

// handleJudgeRescore (scorekeeper) clears a judge's score for a photo and flags a
// resubmission request. handleJudgeRescoreAll does the same for every judge.
func (s *server) handleJudgeRescore(w http.ResponseWriter, r *http.Request) {
	var body struct{ Session, Category, Orientation, File, Judge string }
	if decode(r, &body) != nil || !safeName(body.Session) {
		http.Error(w, "bad body", 400)
		return
	}
	all := body.Judge == ""
	jk := normalizeNameKey(body.Judge)
	s.mu.Lock()
	s.ensureJudgeMaps()
	if s.sessionByID(body.Session).locked() {
		s.mu.Unlock()
		http.Error(w, "this session has ended — scores are locked", http.StatusConflict)
		return
	}
	dir := s.photosDir(body.Session, body.Category, body.Orientation)
	tk := targetKey(body.Session, body.Category, body.Orientation, body.File)
	js := loadJudgeScores(dir)
	if js[body.File] != nil {
		for k := range js[body.File] {
			if all || k == jk {
				s.judgeRequested[tk+"|"+k] = true
				delete(s.judgeDeferred, tk+"|"+k)
				delete(js[body.File], k)
			}
		}
		_ = writeJudgeScores(dir, js)
	} else if !all {
		s.judgeRequested[tk+"|"+jk] = true
	}
	s.recomputeCombinedLocked(judgeTarget{sessionID: body.Session, category: body.Category, orientation: body.Orientation, file: body.File, ok: true})
	s.mu.Unlock()
	s.pushJudge()
	s.pushConsole()
	w.WriteHeader(204)
}

// handleJudgeBoard (scorekeeper) returns every judge's score for the LIVE photo plus the
// combined value. The server resolves the live target itself, so the scorekeeper always
// shows the photo the judges are scoring.
func (s *server) handleJudgeBoard(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureJudgeMaps()
	t := s.judgingTarget()
	if !t.ok {
		writeJSON(w, map[string]any{"open": false, "active": s.judgeActive})
		return
	}
	ss := s.sessionByID(t.sessionID)
	dir := s.photosDir(t.sessionID, t.category, t.orientation)
	scored := loadJudgeScores(dir)[t.file]
	tk := t.key()
	altActive := s.alternateActiveLocked(t, ss)
	keys := map[string]bool{}
	for k, e := range s.judgeRoster {
		if !e.alternate || altActive { // hide the idle alternate until they're in play
			keys[k] = true
		}
	}
	for k := range scored {
		keys[k] = true
	}
	type row struct {
		Name      string `json:"name"`
		Key       string `json:"key"`
		Alternate bool   `json:"alternate"`
		Score     string `json:"score"`
		Deferred  bool   `json:"deferred"`
		Requested bool   `json:"requested"`
		Present   bool   `json:"present"`
	}
	rows := []row{}
	counting := 0
	for k := range keys {
		rw := row{Key: k}
		if e := s.judgeRoster[k]; e != nil {
			rw.Name, rw.Alternate, rw.Present = e.name, e.alternate, true
		}
		if e, okk := scored[k]; okk {
			rw.Score, rw.Name = e.Score, e.Name
			if e.Alternate {
				rw.Alternate = true
			}
		}
		rw.Deferred = s.judgeDeferred[tk+"|"+k]
		rw.Requested = s.judgeRequested[tk+"|"+k]
		if rw.Score != "" && !rw.Deferred {
			counting++
		}
		rows = append(rows, rw)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Alternate != rows[j].Alternate {
			return rows[j].Alternate // alternate last
		}
		return rows[i].Name < rows[j].Name
	})
	needed := 0
	if ss != nil {
		needed = ss.JudgesNeeded
	}
	writeJSON(w, map[string]any{
		"open": true, "active": s.judgeActive,
		"session": t.sessionID, "category": t.category, "orientation": t.orientation,
		"file": t.file, "title": t.title,
		"judges":      rows,
		"combined":    loadScores(dir)[t.file],
		"aggregation": aggOf(ss),
		"needed":      needed,
		"counting":    counting,
		"complete":    needed > 0 && counting >= needed,
		"anonymize":   ss != nil && ss.JudgeAnonymize,
	})
}

// ---- judging-session lifecycle (start / stop) ----

type judgeName struct {
	Name      string `json:"name"`
	Alternate bool   `json:"alternate"`
}

// presentJudgesLocked lists connected judges and counts present primaries (the alternate
// is a backup and doesn't count toward "judges needed"). Assumes s.mu held.
func (s *server) presentJudgesLocked() (names []judgeName, primaries int) {
	names = []judgeName{}
	for _, e := range s.judgeRoster {
		names = append(names, judgeName{Name: e.name, Alternate: e.alternate})
		if !e.alternate {
			primaries++
		}
	}
	sort.Slice(names, func(i, j int) bool {
		if names[i].Alternate != names[j].Alternate {
			return names[j].Alternate // alternate last
		}
		return names[i].Name < names[j].Name
	})
	return names, primaries
}

// judgeConsoleSnapshotLocked is the judge block in the console snapshot (nil when judge
// scoring is off, so it's omitted). Assumes s.mu held.
func (s *server) judgeConsoleSnapshotLocked() map[string]any {
	if !s.settings.JudgeScoringEnabled {
		return nil
	}
	names, primaries := s.presentJudgesLocked()
	return map[string]any{
		"active":    s.judgeActive,
		"sessionId": s.judgeSessionID,
		"joined":    names,
		"primaries": primaries,
	}
}

// judgeStartGateLocked checks that a judging session may start for ss: the rules must be
// set (judges needed + score range) and all needed primary judges must have joined. It
// returns (0, "") when ok, else an HTTP status + message. Assumes s.mu held and judge
// maps ensured. Shared by handleJudgeStart and the presentation lifecycle.
func (s *server) judgeStartGateLocked(ss *Session) (int, string) {
	if ss.JudgesNeeded < 1 {
		return http.StatusConflict, "set how many judges are needed on Session Management first"
	}
	if ss.JudgeMin == nil || ss.JudgeMax == nil {
		return http.StatusConflict, "set the score range on Session Management first"
	}
	if _, primaries := s.presentJudgesLocked(); primaries < ss.JudgesNeeded {
		return http.StatusConflict, fmt.Sprintf("waiting for judges — %d of %d have joined", primaries, ss.JudgesNeeded)
	}
	return 0, ""
}

// handleJudgeStart opens a judging session locked to the chosen session. It refuses until
// the session's rules are set (judges needed + score range) and all needed judges have
// joined — then the slideshow comes off black and judges can score.
func (s *server) handleJudgeStart(w http.ResponseWriter, r *http.Request) {
	var body struct{ Session string }
	if decode(r, &body) != nil || !safeName(body.Session) {
		http.Error(w, "bad body", 400)
		return
	}
	s.mu.Lock()
	s.ensureJudgeMaps()
	if !s.settings.JudgeScoringEnabled {
		s.mu.Unlock()
		http.Error(w, "judge scoring is turned off in Settings", http.StatusConflict)
		return
	}
	ss := s.sessionByID(body.Session)
	if ss == nil {
		s.mu.Unlock()
		http.Error(w, "session not found", 404)
		return
	}
	if code, msg := s.judgeStartGateLocked(ss); code != 0 {
		s.mu.Unlock()
		http.Error(w, msg, code)
		return
	}
	s.judgeActive, s.judgeSessionID = true, ss.ID
	s.mu.Unlock()
	s.pushAllScreens() // slideshow comes off black
	s.pushJudge()
	s.pushConsole()
	w.WriteHeader(204)
}

func (s *server) handleJudgeStop(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.judgeActive, s.judgeSessionID = false, ""
	s.mu.Unlock()
	s.pushAllScreens() // slideshow goes black again
	s.pushJudge()
	s.pushConsole()
	w.WriteHeader(204)
}
