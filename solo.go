// Solo operator mode: one person runs the whole night from the Scoring page. A single
// Advance steps the live output forward (title -> photos -> end); at the end of a
// category's orientation it switches to the other orientation's monitor, then to the
// next category, following the per-session category order and screen assignments. The
// idle monitor goes black. Standard library only.
package main

import "net/http"

// soloSegment is one (category, orientation) shown on a specific screen.
type soloSegment struct {
	category, orientation, screen string
	count                         int
}

// soloRun is the active presentation: an ordered segment list and a cursor. The
// within-segment position lives on the active screen (Screen.Position).
type soloRun struct {
	sessionID string
	segments  []soloSegment
	index     int
	finished  bool
}

// soloView is the solo state surfaced to the Scoring page via the console snapshot.
type soloView struct {
	Active      bool   `json:"active"`
	SessionID   string `json:"sessionId"`
	Screen      string `json:"screen"` // the screen currently presenting
	Index       int    `json:"index"`  // 0-based segment
	Total       int    `json:"total"`  // number of segments
	Category    string `json:"category"`
	Orientation string `json:"orientation"`
	Finished    bool   `json:"finished"`
}

// buildSoloSegments lays out the presentation: each active category in order, each
// orientation in the configured order, on its assigned screen — skipping orientations
// with no photos or no valid screen. Assumes s.mu is held.
func (s *server) buildSoloSegments(ss *Session) []soloSegment {
	var segs []soloSegment
	for _, cat := range ss.Categories {
		for _, orient := range ss.soloOrientations() {
			screen := ss.soloScreenFor(orient)
			if screen == "" || s.screens[screen] == nil {
				continue
			}
			n := len(s.photoFiles(ss.ID, cat, orient))
			if n == 0 {
				continue
			}
			segs = append(segs, soloSegment{category: cat, orientation: orient, screen: screen, count: n})
		}
	}
	return segs
}

// soloShowLocked presents segment index: loads its screen at the title card (or the end
// card when atEnd, for backward stepping), reveals it, and blacks every other screen.
// Returns the screen names to push. Assumes s.mu is held.
func (s *server) soloShowLocked(index int, atEnd bool) []string {
	run := s.solo
	run.index = index
	seg := run.segments[index]
	if sc := s.screens[seg.screen]; sc != nil {
		s.loadScreenLocked(sc, run.sessionID, seg.category, seg.orientation)
		if atEnd {
			sc.Position = sc.Count + 1 // land on the end card
		}
		sc.Blackout = false
	}
	for n, other := range s.screens {
		if n != seg.screen {
			other.Blackout = true
		}
	}
	return s.allScreenNamesLocked()
}

func (s *server) allScreenNamesLocked() []string {
	names := make([]string, 0, len(s.screens))
	for n := range s.screens {
		names = append(names, n)
	}
	return names
}

// soloViewLocked builds the snapshot view of the current run (nil when not running).
// Assumes s.mu is held.
func (s *server) soloViewLocked() *soloView {
	if s.solo == nil {
		return nil
	}
	run := s.solo
	v := &soloView{Active: true, SessionID: run.sessionID, Index: run.index, Total: len(run.segments), Finished: run.finished}
	if run.index >= 0 && run.index < len(run.segments) {
		seg := run.segments[run.index]
		v.Screen, v.Category, v.Orientation = seg.screen, seg.category, seg.orientation
	}
	return v
}

func (s *server) handleSoloStart(w http.ResponseWriter, r *http.Request) {
	var body struct{ SessionID string }
	if decode(r, &body) != nil || !safeName(body.SessionID) {
		http.Error(w, "bad request", 400)
		return
	}
	s.mu.Lock()
	ss := s.sessionByID(body.SessionID)
	if ss == nil {
		s.mu.Unlock()
		http.Error(w, "no such session", 404)
		return
	}
	if !ss.SoloEnabled {
		s.mu.Unlock()
		http.Error(w, "solo mode is not enabled for this session", 400)
		return
	}
	segs := s.buildSoloSegments(ss)
	if len(segs) == 0 {
		s.mu.Unlock()
		http.Error(w, "nothing to present — assign a screen and make sure the categories have photos", 400)
		return
	}
	s.solo = &soloRun{sessionID: ss.ID, segments: segs}
	names := s.soloShowLocked(0, false)
	s.mu.Unlock()
	for _, n := range names {
		s.pushScreen(n)
	}
	s.pushConsole()
	w.WriteHeader(204)
}

func (s *server) handleSoloAdvance(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.solo == nil {
		s.mu.Unlock()
		http.Error(w, "no solo run", 409)
		return
	}
	run := s.solo
	seg := run.segments[run.index]
	sc := s.screens[seg.screen]
	var names []string
	event := "advance" // what happened, so the Scoring page can toast loop/close
	if sc != nil && sc.Position < sc.Count+1 {
		sc.Position++ // title -> photo 1 -> ... -> end card
		names = []string{seg.screen}
	} else if run.index+1 < len(run.segments) {
		names = s.soloShowLocked(run.index+1, false) // switch to the next segment's title
	} else {
		// Past the last segment's end card — behavior depends on the session's SoloEnd.
		end := ""
		if ss := s.sessionByID(run.sessionID); ss != nil {
			end = ss.SoloEnd
		}
		switch end {
		case "loop":
			names = s.soloShowLocked(0, false) // restart from the first category
			event = "loop"
		case "close":
			s.blackSoloScreensLocked()
			s.solo = nil
			names = s.allScreenNamesLocked()
			event = "close"
		default:
			run.finished = true // sit on the last end card until Stop
			if sc != nil {
				sc.Position = sc.Count + 1
			}
			names = []string{seg.screen}
			event = "finished"
		}
	}
	s.mu.Unlock()
	for _, n := range names {
		s.pushScreen(n)
	}
	s.pushConsole()
	s.pushJudge() // the live photo may have changed — refresh the judges' phones
	writeJSON(w, map[string]string{"event": event})
}

func (s *server) handleSoloBack(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.solo == nil {
		s.mu.Unlock()
		http.Error(w, "no solo run", 409)
		return
	}
	run := s.solo
	run.finished = false
	seg := run.segments[run.index]
	sc := s.screens[seg.screen]
	var names []string
	if sc != nil && sc.Position > 0 {
		sc.Position--
		names = []string{seg.screen}
	} else if run.index > 0 {
		names = s.soloShowLocked(run.index-1, true) // back to the previous segment's end card
	} else {
		names = []string{seg.screen} // already at the very first title card
	}
	s.mu.Unlock()
	for _, n := range names {
		s.pushScreen(n)
	}
	s.pushConsole()
	s.pushJudge() // the live photo may have changed — refresh the judges' phones
	w.WriteHeader(204)
}

// blackSoloScreensLocked blacks out the current run's configured monitors. Assumes
// s.mu is held and s.solo is non-nil.
func (s *server) blackSoloScreensLocked() {
	ss := s.sessionByID(s.solo.sessionID)
	if ss == nil {
		return
	}
	for _, name := range []string{ss.SoloLandscapeScreen, ss.SoloPortraitScreen} {
		if sc := s.screens[name]; sc != nil {
			sc.Blackout = true
		}
	}
}

func (s *server) handleSoloStop(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.solo != nil {
		s.blackSoloScreensLocked()
		s.solo = nil
	}
	names := s.allScreenNamesLocked()
	s.mu.Unlock()
	for _, n := range names {
		s.pushScreen(n)
	}
	s.pushConsole()
	w.WriteHeader(204)
}
