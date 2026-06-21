// Presentation lifecycle: the operator runs a guided session from the console, shared by
// all presentation modes (guided slideshow / score keeper / judge scoring). Start builds
// a run on the existing solo engine and (for judge mode) opens scoring; Pause suspends it
// so screens can be reconfigured; Resume continues; End stamps EndedAt and locks the
// session's scores. Standard library only. See solo.go for the engine, judge.go for the
// judge gate.
package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// handlePresentationStart begins (or, for judge mode, opens scoring for) a guided run on
// the session, driven from the console. Gated by mode-specific rules.
func (s *server) handlePresentationStart(w http.ResponseWriter, r *http.Request) {
	var body struct{ SessionID string }
	if decode(r, &body) != nil || !safeName(body.SessionID) {
		http.Error(w, "bad request", 400)
		return
	}
	s.mu.Lock()
	if !s.settings.presentationMode() {
		s.mu.Unlock()
		http.Error(w, "turn on a presentation mode in Settings first", http.StatusConflict)
		return
	}
	ss := s.sessionByID(body.SessionID)
	if ss == nil {
		s.mu.Unlock()
		http.Error(w, "no such session", 404)
		return
	}
	if ss.locked() {
		s.mu.Unlock()
		http.Error(w, "this session has ended — its scores are locked", http.StatusConflict)
		return
	}
	if s.solo != nil {
		s.mu.Unlock()
		http.Error(w, "a presentation is already running — End it first", http.StatusConflict)
		return
	}
	segs := s.buildSoloSegments(ss)
	if len(segs) == 0 {
		s.mu.Unlock()
		http.Error(w, "nothing to present — assign a screen on Session Management and make sure the categories have photos", 400)
		return
	}
	if s.settings.JudgeScoringEnabled {
		s.ensureJudgeMaps()
		if code, msg := s.judgeStartGateLocked(ss); code != 0 {
			s.mu.Unlock()
			http.Error(w, msg, code)
			return
		}
		s.judgeActive, s.judgeSessionID = true, ss.ID
	}
	s.solo, s.runPaused = &soloRun{sessionID: ss.ID, segments: segs}, false
	names := s.soloShowLocked(0, false)
	if ss.StartedAt == "" {
		ss.StartedAt = time.Now().Format(time.RFC3339)
		_ = s.saveSession(ss)
	}
	s.mu.Unlock()
	for _, n := range names {
		s.pushScreen(n)
	}
	s.pushJudge()
	s.pushConsole()
	w.WriteHeader(204)
}

// handlePresentationPause suspends the run so the operator can reconfigure screens. The
// guided panel can't advance and (judge mode) judges can't score while paused; nothing is
// locked.
func (s *server) handlePresentationPause(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.solo == nil {
		s.mu.Unlock()
		http.Error(w, "no presentation is running", http.StatusConflict)
		return
	}
	s.runPaused = true
	s.judgeActive = false // judges can't submit while paused
	s.mu.Unlock()
	s.pushJudge()
	s.pushConsole()
	w.WriteHeader(204)
}

// handlePresentationResume continues a paused run, re-asserting the single live screen.
func (s *server) handlePresentationResume(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.solo == nil {
		s.mu.Unlock()
		http.Error(w, "no presentation is running", http.StatusConflict)
		return
	}
	s.runPaused = false
	if s.settings.JudgeScoringEnabled {
		s.judgeActive, s.judgeSessionID = true, s.solo.sessionID
	}
	// Re-black every screen except the one presenting the current segment, in case the
	// operator revealed others while paused.
	live := s.solo.segments[s.solo.index].screen
	for n, sc := range s.screens {
		sc.Blackout = n != live
	}
	names := s.allScreenNamesLocked()
	s.mu.Unlock()
	for _, n := range names {
		s.pushScreen(n)
	}
	s.pushJudge()
	s.pushConsole()
	w.WriteHeader(204)
}

// eraseScoresLocked deletes every score sidecar (combined + per-judge) across the
// session's category folders and clears its transient judge state. Assumes s.mu held.
func (s *server) eraseScoresLocked(ss *Session) {
	cats := append(append([]string{}, ss.Categories...), ss.InactiveCategories...)
	for _, cat := range cats {
		for _, or := range []string{"Landscape", "Portrait"} {
			dir := s.photosDir(ss.ID, cat, or)
			_ = os.Remove(filepath.Join(dir, "scores.json"))
			_ = os.Remove(filepath.Join(dir, "judgescores.json"))
		}
	}
	prefix := ss.ID + "|"
	for k := range s.judgeDeferred {
		if strings.HasPrefix(k, prefix) {
			delete(s.judgeDeferred, k)
		}
	}
	for k := range s.judgeRequested {
		if strings.HasPrefix(k, prefix) {
			delete(s.judgeRequested, k)
		}
	}
}

// handlePresentationRestart re-opens a session for a fresh run, erasing every score so the
// night can be judged again. Allowed during or after a run (an archived session isn't in
// the live list, so it can't be selected here).
func (s *server) handlePresentationRestart(w http.ResponseWriter, r *http.Request) {
	var body struct{ SessionID string }
	if decode(r, &body) != nil || !safeName(body.SessionID) {
		http.Error(w, "bad request", 400)
		return
	}
	s.mu.Lock()
	if !s.settings.presentationMode() {
		s.mu.Unlock()
		http.Error(w, "turn on a presentation mode in Settings first", http.StatusConflict)
		return
	}
	ss := s.sessionByID(body.SessionID)
	if ss == nil {
		s.mu.Unlock()
		http.Error(w, "no such session", 404)
		return
	}
	segs := s.buildSoloSegments(ss)
	if len(segs) == 0 {
		s.mu.Unlock()
		http.Error(w, "nothing to present — assign a screen on Session Management and make sure the categories have photos", 400)
		return
	}
	if s.settings.JudgeScoringEnabled {
		s.ensureJudgeMaps()
		if code, msg := s.judgeStartGateLocked(ss); code != 0 {
			s.mu.Unlock()
			http.Error(w, msg, code)
			return
		}
	}
	s.eraseScoresLocked(ss)
	ss.StartedAt, ss.EndedAt = time.Now().Format(time.RFC3339), ""
	_ = s.saveSession(ss)
	s.solo, s.runPaused = &soloRun{sessionID: ss.ID, segments: segs}, false
	if s.settings.JudgeScoringEnabled {
		s.judgeActive, s.judgeSessionID = true, ss.ID
	}
	names := s.soloShowLocked(0, false)
	s.mu.Unlock()
	for _, n := range names {
		s.pushScreen(n)
	}
	s.pushJudge()
	s.pushConsole()
	w.WriteHeader(204)
}

// handlePresentationEnd finalizes the run: stamps EndedAt (locking scores), tears down the
// run + judge scoring, and blacks the screens.
func (s *server) handlePresentationEnd(w http.ResponseWriter, r *http.Request) {
	var body struct{ SessionID string }
	_ = decode(r, &body)
	s.mu.Lock()
	sid := body.SessionID
	if s.solo != nil {
		sid = s.solo.sessionID
	}
	ss := s.sessionByID(sid)
	if ss == nil {
		s.mu.Unlock()
		http.Error(w, "no such session", 404)
		return
	}
	if s.solo != nil {
		s.blackSoloScreensLocked()
	}
	ss.EndedAt = time.Now().Format(time.RFC3339)
	_ = s.saveSession(ss)
	s.solo, s.runPaused = nil, false
	s.judgeActive, s.judgeSessionID = false, ""
	names := s.allScreenNamesLocked()
	s.mu.Unlock()
	for _, n := range names {
		s.pushScreen(n)
	}
	s.pushJudge()
	s.pushConsole()
	w.WriteHeader(204)
}
