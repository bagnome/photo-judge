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
	ID                string             `json:"id"`
	Date              string             `json:"date"`
	Description       string             `json:"description,omitempty"`
	Total             int                `json:"total"`
	ByCategory        []catCount         `json:"byCategory"`
	ByPhotographer    []photographerStat `json:"byPhotographer"` // this session's photographers (count desc, Unattributed last)
	Winners           int                `json:"winners"`
	WinnersByCategory []catCount         `json:"winnersByCategory"`
}

type photographerStat struct {
	Name       string     `json:"name"`
	Count      int        `json:"count"`
	ByCategory []catCount `json:"byCategory"`
}

type statsTotals struct {
	Entries              int `json:"entries"`
	Sessions             int `json:"sessions"`
	Categories           int `json:"categories"`
	Photographers        int `json:"photographers"`
	Winners              int `json:"winners"`
	WinningPhotographers int `json:"winningPhotographers"`
}

// mediumStat is a digital-or-physical tally. compareCat/compareSession hold the
// digital-vs-physical split for the Compare tab's side-by-side charts.
type mediumStat struct {
	Entries int `json:"entries"`
	Winners int `json:"winners"`
}
type compareCat struct {
	Category string `json:"category"`
	Digital  int    `json:"digital"`
	Physical int    `json:"physical"`
}
type compareSession struct {
	ID          string `json:"id"`
	Date        string `json:"date"`
	Description string `json:"description,omitempty"`
	Digital     int    `json:"digital"`
	Physical    int    `json:"physical"`
}
type compareStats struct {
	Digital    mediumStat       `json:"digital"`
	Physical   mediumStat       `json:"physical"`
	ByCategory []compareCat     `json:"byCategory"` // entries per category, digital vs physical
	BySession  []compareSession `json:"bySession"`  // entries per session, digital vs physical
}

type statsResult struct {
	From                  string             `json:"from"` // requested range bounds ("" = open)
	To                    string             `json:"to"`
	Medium                string             `json:"medium"`     // "" (both) | "digital" | "physical" — the active filter
	Categories            []string           `json:"categories"` // stable union order → shared chart colors/legend
	Totals                statsTotals        `json:"totals"`
	ByCategory            []catCount         `json:"byCategory"`
	BySession             []sessionStat      `json:"bySession"`      // chronological (drives both per-session charts)
	ByPhotographer        []photographerStat `json:"byPhotographer"` // count desc, Unattributed last
	WinnersByCategory     []catCount         `json:"winnersByCategory"`
	WinnersByPhotographer []photographerStat `json:"winnersByPhotographer"`
	// Compare always covers BOTH media regardless of the Medium filter, for the side-by-side view.
	Compare compareStats `json:"compare"`
}

// mediumMatch reports whether a record (physical or not) passes the medium filter
// ("" / "both" = everything).
func mediumMatch(physical bool, medium string) bool {
	switch medium {
	case "digital":
		return !physical
	case "physical":
		return physical
	default:
		return true
	}
}

// handleStats serves the aggregated statistics JSON. Optional from/to query params
// (YYYY-MM-DD) bound the session dates; medium ("digital"/"physical"/"both") filters
// which entries the main aggregates count (the Compare block always covers both).
func (s *server) handleStats(w http.ResponseWriter, r *http.Request) {
	from := strings.TrimSpace(r.URL.Query().Get("from"))
	to := strings.TrimSpace(r.URL.Query().Get("to"))
	medium := strings.TrimSpace(r.URL.Query().Get("medium"))
	if from != "" && !validDate(from) {
		http.Error(w, "bad from date", http.StatusBadRequest)
		return
	}
	if to != "" && !validDate(to) {
		http.Error(w, "bad to date", http.StatusBadRequest)
		return
	}
	if medium == "both" {
		medium = ""
	}
	if medium != "" && medium != "digital" && medium != "physical" {
		http.Error(w, "bad medium", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	res := s.computeStats(from, to, medium)
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

// photoRec is one counted entry: its category, who shot it, whether its score reached
// the session's win threshold, and whether it's a physical print (vs a digital photo).
type photoRec struct {
	category     string
	photographer string
	win          bool
	physical     bool
}

// rawSession is a date-placed session (live or archived) reduced to its photos, so
// live and archived sessions aggregate through exactly the same code path.
type rawSession struct {
	id, date, desc string
	photos         []photoRec
}

func inRange(date, from, to string) bool {
	if from != "" && date < from {
		return false
	}
	if to != "" && date > to {
		return false
	}
	return true
}

// upsertPhoto records one photo against a photographer aggregate map.
func upsertPhoto(m map[string]*phAgg, photographer, cat string) {
	disp := strings.TrimSpace(photographer)
	key := normalizeNameKey(disp)
	if key == "" {
		key, disp = unattributedKey, unattributedLabel
	}
	p := m[key]
	if p == nil {
		p = &phAgg{display: disp, byCat: map[string]int{}}
		m[key] = p
	}
	p.total++
	p.byCat[cat]++
}

// sortedCatCounts turns a category→count map into a list ordered by count desc then
// name (charts and tables look up by name, so the order is just for tidiness).
func sortedCatCounts(m map[string]int) []catCount {
	out := make([]catCount, 0, len(m))
	for cat, n := range m {
		out = append(out, catCount{Category: cat, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Category < out[j].Category
	})
	return out
}

// sortPhotographers turns a photographer aggregate map into a stable list: most
// entries first, ties by name, Unattributed always last. Each photographer's
// byCategory is likewise ordered by count then name.
func sortPhotographers(m map[string]*phAgg) []photographerStat {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		ki, kj := keys[i], keys[j]
		ui, uj := ki == unattributedKey, kj == unattributedKey
		if ui != uj {
			return uj // unattributed sinks to the bottom
		}
		pi, pj := m[ki], m[kj]
		if pi.total != pj.total {
			return pi.total > pj.total
		}
		return pi.display < pj.display
	})
	out := make([]photographerStat, 0, len(keys))
	for _, k := range keys {
		p := m[k]
		cc := make([]catCount, 0, len(p.byCat))
		for cat, n := range p.byCat {
			cc = append(cc, catCount{Category: cat, Count: n})
		}
		sort.Slice(cc, func(i, j int) bool {
			if cc[i].Count != cc[j].Count {
				return cc[i].Count > cc[j].Count
			}
			return cc[i].Category < cc[j].Category
		})
		out = append(out, photographerStat{Name: p.display, Count: p.total, ByCategory: cc})
	}
	return out
}

// computeStats tallies entries by category, by session, and by photographer across
// every session — live AND archived — whose Date falls within [from,to] (inclusive;
// blank bound = open). Entries are digital photos AND physical prints; the medium
// filter ("digital"/"physical"/"" = both) narrows the main aggregates, while the
// Compare block always covers both. Archived sessions have no image files left on
// disk, so their counts come from the saved archive metadata. Assumes s.mu is held.
func (s *server) computeStats(from, to, medium string) statsResult {
	res := statsResult{
		From:                  from,
		To:                    to,
		Medium:                medium,
		Categories:            []string{},
		ByCategory:            []catCount{},
		BySession:             []sessionStat{},
		ByPhotographer:        []photographerStat{},
		WinnersByCategory:     []catCount{},
		WinnersByPhotographer: []photographerStat{},
		Compare:               compareStats{ByCategory: []compareCat{}, BySession: []compareSession{}},
	}

	// Reduce every in-range session (live + archived) to its photos.
	var raw []rawSession
	for _, ss := range s.sessions {
		if !inRange(ss.Date, from, to) {
			continue
		}
		rs := rawSession{id: ss.ID, date: ss.Date, desc: ss.Description}
		// Photos can live under active OR previously-deactivated categories.
		cats := append(append([]string{}, ss.Categories...), ss.InactiveCategories...)
		for _, cat := range cats {
			for _, orient := range []string{"Landscape", "Portrait"} {
				dir := s.photosDir(ss.ID, cat, orient)
				files := s.photoFiles(ss.ID, cat, orient)
				if len(files) == 0 {
					continue
				}
				names := loadNames(dir)
				scores := loadScores(dir)
				for _, f := range files {
					rs.photos = append(rs.photos, photoRec{category: cat, photographer: names[f], win: ss.isWinner(scores[f])})
				}
			}
		}
		for _, p := range s.loadPhysical(ss.ID) {
			rs.photos = append(rs.photos, photoRec{category: p.Category, photographer: p.Photographer, win: ss.isWinner(p.Score), physical: true})
		}
		raw = append(raw, rs)
	}
	for _, a := range s.loadArchives() {
		if !inRange(a.Date, from, to) {
			continue
		}
		rs := rawSession{id: a.SessionID, date: a.Date, desc: a.Description}
		for _, p := range a.Photos {
			rs.photos = append(rs.photos, photoRec{category: p.Category, photographer: p.Photographer, win: scoreWins(a.WinThreshold, p.Score)})
		}
		for _, p := range a.PhysicalPrints {
			rs.photos = append(rs.photos, photoRec{category: p.Category, photographer: p.Photographer, win: scoreWins(a.WinThreshold, p.Score), physical: true})
		}
		raw = append(raw, rs)
	}
	// Chronological, ties by ID, so BySession reads left-to-right by date.
	sort.Slice(raw, func(i, j int) bool {
		if raw[i].date != raw[j].date {
			return raw[i].date < raw[j].date
		}
		return raw[i].id < raw[j].id
	})

	catTotals := map[string]int{}
	photographers := map[string]*phAgg{}
	winCatTotals := map[string]int{}
	winPhotographers := map[string]*phAgg{}
	compDigCat := map[string]int{} // Compare accumulators — always both media.
	compPhyCat := map[string]int{}

	for _, rs := range raw {
		if len(rs.photos) == 0 {
			continue // skip empty sessions so the charts stay meaningful
		}
		sessCat := map[string]int{}
		sessPh := map[string]*phAgg{}
		sessWinCat := map[string]int{}
		winners, filtered, cDig, cPhy := 0, 0, 0, 0
		for _, pr := range rs.photos {
			// Compare counters count every record, ignoring the medium filter.
			if pr.physical {
				cPhy++
				compPhyCat[pr.category]++
				res.Compare.Physical.Entries++
				if pr.win {
					res.Compare.Physical.Winners++
				}
			} else {
				cDig++
				compDigCat[pr.category]++
				res.Compare.Digital.Entries++
				if pr.win {
					res.Compare.Digital.Winners++
				}
			}
			if !mediumMatch(pr.physical, medium) {
				continue
			}
			filtered++
			catTotals[pr.category]++
			sessCat[pr.category]++
			upsertPhoto(photographers, pr.photographer, pr.category)
			upsertPhoto(sessPh, pr.photographer, pr.category)
			if pr.win {
				winners++
				winCatTotals[pr.category]++
				sessWinCat[pr.category]++
				upsertPhoto(winPhotographers, pr.photographer, pr.category)
			}
		}
		if cDig+cPhy > 0 {
			res.Compare.BySession = append(res.Compare.BySession, compareSession{ID: rs.id, Date: rs.date, Description: rs.desc, Digital: cDig, Physical: cPhy})
		}
		if filtered == 0 {
			continue // the chosen medium has nothing in this session
		}
		sc := sessionStat{ID: rs.id, Date: rs.date, Description: rs.desc, Total: filtered, Winners: winners}
		// Session category breakdown, ordered by count then name (charts/table look up
		// by name, so this order is just for tidiness).
		sc.ByCategory = sortedCatCounts(sessCat)
		sc.WinnersByCategory = sortedCatCounts(sessWinCat)
		sc.ByPhotographer = sortPhotographers(sessPh)
		res.BySession = append(res.BySession, sc)
		res.Totals.Entries += filtered
		res.Totals.Winners += winners
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
		if n := winCatTotals[cat]; n > 0 {
			res.WinnersByCategory = append(res.WinnersByCategory, catCount{Category: cat, Count: n})
		}
	}
	res.Totals.Categories = len(catOrder)

	res.ByPhotographer = sortPhotographers(photographers)
	for k := range photographers {
		if k != unattributedKey {
			res.Totals.Photographers++
		}
	}

	res.WinnersByPhotographer = sortPhotographers(winPhotographers)
	for k := range winPhotographers {
		if k != unattributedKey {
			res.Totals.WinningPhotographers++
		}
	}

	// Compare-by-category: digital vs physical entries per category, ordered by combined
	// total (both media), so it's complete regardless of the medium filter.
	compTotal := map[string]int{}
	for c, n := range compDigCat {
		compTotal[c] += n
	}
	for c, n := range compPhyCat {
		compTotal[c] += n
	}
	compOrder := make([]string, 0, len(compTotal))
	for c := range compTotal {
		compOrder = append(compOrder, c)
	}
	sort.Slice(compOrder, func(i, j int) bool {
		a, b := compOrder[i], compOrder[j]
		if compTotal[a] != compTotal[b] {
			return compTotal[a] > compTotal[b]
		}
		return a < b
	})
	for _, c := range compOrder {
		res.Compare.ByCategory = append(res.Compare.ByCategory, compareCat{Category: c, Digital: compDigCat[c], Physical: compPhyCat[c]})
	}

	return res
}
