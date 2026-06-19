// Randomizing photo display order. The Upload / Reorder page can shuffle one category
// or a whole session in one click. When the "spread photographers" Setting is on, the
// shuffle tries to keep a photographer's photos from sitting next to each other.
// Standard library only.
package main

import (
	"encoding/json"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

// writeOrderFile saves a folder's order.json (the display order, by filename).
func writeOrderFile(dir string, order []string) error {
	bts, _ := json.MarshalIndent(order, "", "  ")
	return os.WriteFile(filepath.Join(dir, "order.json"), bts, 0o644)
}

// weightedShuffleOrder returns files in a fresh random order. When spread is true it
// greedily places, at each step, a photo from the photographer with the most remaining
// that isn't the one just placed — so same-photographer photos end up apart. Adjacent
// repeats are only unavoidable when one photographer has more than half the photos.
func weightedShuffleOrder(files []string, names map[string]string, spread bool) []string {
	out := append([]string{}, files...)
	if !spread || len(out) < 3 {
		rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
		return out
	}
	// Bucket by photographer; unnamed photos get a unique key so they never "match".
	buckets := map[string][]string{}
	for i, f := range files {
		k := normalizeNameKey(names[f])
		if k == "" {
			k = "\x00" + strconv.Itoa(i)
		}
		buckets[k] = append(buckets[k], f)
	}
	for k := range buckets {
		b := buckets[k]
		rand.Shuffle(len(b), func(i, j int) { b[i], b[j] = b[j], b[i] })
	}
	result := make([]string, 0, len(files))
	last := ""
	for len(result) < len(files) {
		bestN := -1
		var cands []string // photographers tied for the most remaining (excluding last)
		for k, b := range buckets {
			if len(b) == 0 || k == last {
				continue
			}
			switch {
			case len(b) > bestN:
				bestN, cands = len(b), []string{k}
			case len(b) == bestN:
				cands = append(cands, k)
			}
		}
		var key string
		if bestN < 0 { // only the last photographer's photos remain → forced adjacency
			for k, b := range buckets {
				if len(b) > 0 {
					key = k
					break
				}
			}
		} else {
			key = cands[rand.Intn(len(cands))]
		}
		b := buckets[key]
		result = append(result, b[len(b)-1])
		buckets[key] = b[:len(b)-1]
		last = key
	}
	return result
}

// handleOrderRandomize shuffles a folder's order.json. With a category it does that
// category (both orientations, or one if given); with no category it does every
// category in the session. Honors the "spread photographers" Setting.
func (s *server) handleOrderRandomize(w http.ResponseWriter, r *http.Request) {
	var body struct{ Session, Category, Orientation string }
	if decode(r, &body) != nil || !safeName(body.Session) {
		http.Error(w, "bad request", 400)
		return
	}
	if body.Category != "" && !safeName(body.Category) {
		http.Error(w, "bad category", 400)
		return
	}
	s.mu.Lock()
	ss := s.sessionByID(body.Session)
	if ss == nil {
		s.mu.Unlock()
		http.Error(w, "no such session", 404)
		return
	}
	spread := s.settings.SpreadPhotographers
	var cats []string
	if body.Category != "" {
		cats = []string{body.Category}
	} else {
		cats = append(append([]string{}, ss.Categories...), ss.InactiveCategories...)
	}
	s.mu.Unlock()

	orients := []string{"Landscape", "Portrait"}
	if body.Orientation == "Landscape" || body.Orientation == "Portrait" {
		orients = []string{body.Orientation}
	}
	for _, cat := range cats {
		for _, orient := range orients {
			files := s.photoFiles(body.Session, cat, orient)
			if len(files) < 2 {
				continue
			}
			dir := s.photosDir(body.Session, cat, orient)
			_ = writeOrderFile(dir, weightedShuffleOrder(files, loadNames(dir), spread))
		}
	}
	s.pushConsole() // nudges any open Upload/Reorder grid to refresh
	w.WriteHeader(204)
}
