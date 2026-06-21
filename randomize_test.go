package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func isPermutation(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("length %d != %d (%v)", len(got), len(want), got)
	}
	seen := map[string]int{}
	for _, f := range got {
		seen[f]++
	}
	for _, f := range want {
		if seen[f] != 1 {
			t.Fatalf("not a permutation: %v vs %v", got, want)
		}
	}
}

func TestWeightedShuffleSpread(t *testing.T) {
	files := []string{"f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8"}
	// A feasible mix (no photographer has more than half): Ann 3, Bob 2, Cyd 2, Dan 1.
	names := map[string]string{
		"f1": "Ann", "f2": "Ann", "f3": "Ann", "f4": "Bob",
		"f5": "Bob", "f6": "Cyd", "f7": "Cyd", "f8": "Dan",
	}
	for trial := 0; trial < 40; trial++ {
		out := weightedShuffleOrder(files, names, true)
		isPermutation(t, out, files)
		for i := 1; i < len(out); i++ {
			if names[out[i]] == names[out[i-1]] {
				t.Fatalf("adjacent same photographer at %d: %v", i, out)
			}
		}
	}
}

func TestWeightedShuffleUnnamedNeverMatch(t *testing.T) {
	// All unnamed photos must not be treated as "the same photographer".
	files := []string{"a", "b", "c", "d", "e"}
	out := weightedShuffleOrder(files, map[string]string{}, true)
	isPermutation(t, out, files)
}

func TestWeightedShuffleNoSpread(t *testing.T) {
	files := []string{"a", "b", "c", "d", "e", "f"}
	out := weightedShuffleOrder(files, map[string]string{"a": "X", "b": "X"}, false)
	isPermutation(t, out, files)
}

func readOrder(t *testing.T, s *server, sid, cat, orient string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(s.photosDir(sid, cat, orient), "order.json"))
	if err != nil {
		return nil
	}
	var order []string
	if err := json.Unmarshal(data, &order); err != nil {
		t.Fatalf("order.json: %v", err)
	}
	return order
}

func TestHandleOrderRandomize(t *testing.T) {
	s := newTestServer(t)
	ss, err := s.createSession("2026-01-01", "")
	if err != nil {
		t.Fatal(err)
	}
	c0, c1 := ss.Categories[0], ss.Categories[1]
	for _, f := range []string{"a.jpg", "b.jpg", "c.jpg"} {
		seedNamedPhoto(t, s, ss.ID, c0, "Landscape", f, "Someone")
	}
	seedNamedPhoto(t, s, ss.ID, c1, "Landscape", "x.jpg", "A")
	seedNamedPhoto(t, s, ss.ID, c1, "Landscape", "y.jpg", "B")

	// One category: writes that category's order.json (a permutation of its files).
	if rr := postJSON(t, s.handleOrderRandomize, map[string]string{"session": ss.ID, "category": c0}); rr.Code != 204 {
		t.Fatalf("randomize category: %d %s", rr.Code, rr.Body.String())
	}
	isPermutation(t, readOrder(t, s, ss.ID, c0, "Landscape"), []string{"a.jpg", "b.jpg", "c.jpg"})
	if readOrder(t, s, ss.ID, c1, "Landscape") != nil {
		t.Fatal("c1 should be untouched when only c0 was randomized")
	}

	// Whole session (no category): every category gets an order.json.
	if rr := postJSON(t, s.handleOrderRandomize, map[string]string{"session": ss.ID}); rr.Code != 204 {
		t.Fatalf("randomize session: %d %s", rr.Code, rr.Body.String())
	}
	isPermutation(t, readOrder(t, s, ss.ID, c1, "Landscape"), []string{"x.jpg", "y.jpg"})
}
