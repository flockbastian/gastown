package cmd

import (
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

func TestMergeAgentBeadMapsPrimaryWins(t *testing.T) {
	primary := map[string]*beads.Issue{
		"lp-launchpad-polecat-iceking": {ID: "lp-launchpad-polecat-iceking", Description: "cleanup_status: clean"},
	}
	fallback := map[string]*beads.Issue{
		"lp-launchpad-polecat-iceking": {ID: "lp-launchpad-polecat-iceking", Description: "cleanup_status: has_stash"},
		"lp-launchpad-polecat-legacy":  {ID: "lp-launchpad-polecat-legacy"},
	}
	got := mergeAgentBeadMaps(primary, fallback)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got["lp-launchpad-polecat-iceking"].Description != "cleanup_status: clean" {
		t.Fatalf("town (primary) entry must win, got %q", got["lp-launchpad-polecat-iceking"].Description)
	}
	if got["lp-launchpad-polecat-legacy"] == nil {
		t.Fatal("rig-local legacy entry must be kept")
	}
	if len(mergeAgentBeadMaps(nil, nil)) != 0 {
		t.Fatal("nil maps must merge to empty")
	}
}
