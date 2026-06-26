package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Regression for the "Cannot read properties of null (reading 'length')" crash when a
// fresh app (no sessions, no screens) pushed its console state: nil slices were
// serialized as JSON null, and the console frontend dereferences them as arrays
// (state.screens.length, state.sessions.forEach). The snapshot must always send arrays.
func TestConsoleSnapshotNeverNullArrays(t *testing.T) {
	s := newTestServer(t)
	s.scanSessions() // fresh photos dir -> s.sessions stays nil

	snap := s.consoleSnapshot()

	for _, bad := range []string{`"sessions":null`, `"screens":null`, `"categories":null`} {
		if strings.Contains(string(snap), bad) {
			t.Errorf("console snapshot contains %s; frontend would crash on a fresh app\nsnapshot: %s", bad, snap)
		}
	}

	// And they decode as (possibly empty) arrays, not null.
	var got struct {
		Sessions   []json.RawMessage `json:"sessions"`
		Screens    []json.RawMessage `json:"screens"`
		Categories []string          `json:"categories"`
	}
	if err := json.Unmarshal(snap, &got); err != nil {
		t.Fatalf("snapshot did not unmarshal: %v", err)
	}
	if got.Sessions == nil || got.Screens == nil || got.Categories == nil {
		t.Errorf("expected non-nil arrays, got sessions=%v screens=%v categories=%v",
			got.Sessions, got.Screens, got.Categories)
	}
}
