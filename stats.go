package main

// stats.go computes club competition statistics over a date range and serves them
// to the Statistics page. An "entry" is any photo in a session — operator-uploaded
// or member-submitted — counted from the on-disk category/orientation folders the
// rest of the app uses (photos/<sid>/<cat>/<Landscape|Portrait>) via photoFiles.
// Sessions are placed in time by their Date (the YYYY-MM-DD session label), so the
// page can filter to a year, the last 12 months, or any custom range. Photographer
// names come from each folder's names.json. Standard library only.

import (
	"net/http"
	"sort"
	"strings"
)

// unattributedKey buckets photos with no recorded photographer. The leading NUL
// keeps it from colliding with any real normalized name and lets it sort last.
const unattributedKey = "\x00unattributed"
const unattributedLabel = "Unattributed"

type catCount struct {
	Category string `json:"category"`
	Count    int    `json:"count"`
}

type sessionStat struct {
	ID          string     `json:"id"`
	Date        string     `json:"date"`
	Description string     `json:"description,omitempty"`
	Total       int        `json:"total"`
	ByCategory  []catCount `json:"byCategory"`
}

type photographerStat struct {
	Name       string     `json:"name"`
	Count      int        `json:"count"`
	ByCategory []catCount `json:"byCategory"`
}

type statsTotals struct {
	Entries       int `json:"entries"`
	Sessions      int `json:"sessions"`
	Categories    int `json:"categories"`
	Photographers int `json:"photographers"`
}

type statsResult struct {
	From           string             `json:"from"` // requested range bounds ("" = open)
	To             string             `json:"to"`
	Categories     []string           `json:"categories"` // stable union order → shared chart colors/legend
	Totals         statsTotals        `json:"totals"`
	ByCategory     []catCount         `json:"byCategory"`
	BySession      []sessionStat      `json:"bySession"`      // chronological (drives both per-session charts)
	ByPhotographer []photographerStat `json:"byPhotographer"` // count desc, Unattributed last
}

// handleStats serves the aggregated statistics JSON. Optional from/to query params
// (YYYY-MM-DD) bound the session dates included; either may be omitted for an open end.
func (s *server) handleStats(w http.ResponseWriter, r *http.Request) {
	from := strings.TrimSpace(r.URL.Query().Get("from"))
	to := strings.TrimSpace(r.URL.Query().Get("to"))
	if from != "" && !validDate(from) {
		http.Error(w, "bad from date", http.StatusBadRequest)
		return
	}
	if to != "" && !validDate(to) {
		http.Error(w, "bad to date", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	res := s.computeStats(from, to)
	s.mu.Unlock()
	writeJSON(w, res)
}

// phAgg accumulates one photographer's counts while scanning. assumes s.mu is held
// (called only from computeStats).
type phAgg struct {
	display string
	total   int
	byCat   map[string]int
}

// computeStats walks every session whose Date falls within [from,to] (inclusive;
// blank bound = open) and tallies entries by category, by session, and by
// photographer. Assumes s.mu is held.
func (s *server) computeStats(from, to string) statsResult {
	res := statsResult{
		From:           from,
		To:             to,
		Categories:     []string{},
		ByCategory:     []catCount{},
		BySession:      []sessionStat{},
		ByPhotographer: []photographerStat{},
	}

	catTotals := map[string]int{}
	photographers := map[string]*phAgg{}

	// s.sessions is kept sorted by Date ascending, so BySession comes out chronological.
	for _, ss := range s.sessions {
		if from != "" && ss.Date < from {
			continue
		}
		if to != "" && ss.Date > to {
			continue
		}
		// Photos can live under active OR previously-deactivated categories.
		cats := append(append([]string{}, ss.Categories...), ss.InactiveCategories...)
		sessTotal := 0
		sessCat := map[string]int{}
		for _, cat := range cats {
			for _, orient := range []string{"Landscape", "Portrait"} {
				files := s.photoFiles(ss.ID, cat, orient)
				if len(files) == 0 {
					continue
				}
				names := loadNames(s.photosDir(ss.ID, cat, orient))
				for _, f := range files {
					sessTotal++
					sessCat[cat]++
					catTotals[cat]++

					disp := strings.TrimSpace(names[f])
					key := normalizeNameKey(disp)
					if key == "" {
						key, disp = unattributedKey, unattributedLabel
					}
					p := photographers[key]
					if p == nil {
						p = &phAgg{display: disp, byCat: map[string]int{}}
						photographers[key] = p
					}
					p.total++
					p.byCat[cat]++
				}
			}
		}
		if sessTotal == 0 {
			continue // skip empty sessions so the charts stay meaningful
		}
		sc := sessionStat{ID: ss.ID, Date: ss.Date, Description: ss.Description, Total: sessTotal}
		for _, cat := range cats { // session's own category order
			if sessCat[cat] > 0 {
				sc.ByCategory = append(sc.ByCategory, catCount{Category: cat, Count: sessCat[cat]})
			}
		}
		res.BySession = append(res.BySession, sc)
		res.Totals.Entries += sessTotal
		res.Totals.Sessions++
	}

	// Global category order: most entries first (so the strongest colors go to the
	// busiest categories), ties broken alphabetically. Shared by every chart's legend.
	catOrder := make([]string, 0, len(catTotals))
	for cat := range catTotals {
		catOrder = append(catOrder, cat)
	}
	sort.Slice(catOrder, func(i, j int) bool {
		a, b := catOrder[i], catOrder[j]
		if catTotals[a] != catTotals[b] {
			return catTotals[a] > catTotals[b]
		}
		return a < b
	})
	res.Categories = catOrder
	for _, cat := range catOrder {
		res.ByCategory = append(res.ByCategory, catCount{Category: cat, Count: catTotals[cat]})
	}
	res.Totals.Categories = len(catOrder)

	// Photographers: most entries first, Unattributed always last.
	keys := make([]string, 0, len(photographers))
	named := 0
	for k := range photographers {
		keys = append(keys, k)
		if k != unattributedKey {
			named++
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		ki, kj := keys[i], keys[j]
		ui, uj := ki == unattributedKey, kj == unattributedKey
		if ui != uj {
			return uj // unattributed sinks to the bottom
		}
		pi, pj := photographers[ki], photographers[kj]
		if pi.total != pj.total {
			return pi.total > pj.total
		}
		return pi.display < pj.display
	})
	for _, k := range keys {
		p := photographers[k]
		ps := photographerStat{Name: p.display, Count: p.total}
		for _, cat := range catOrder { // global category order
			if p.byCat[cat] > 0 {
				ps.ByCategory = append(ps.ByCategory, catCount{Category: cat, Count: p.byCat[cat]})
			}
		}
		res.ByPhotographer = append(res.ByPhotographer, ps)
	}
	res.Totals.Photographers = named

	return res
}
