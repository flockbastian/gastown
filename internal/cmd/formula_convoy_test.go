package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/formula"
)

// formulaStubTown is a throwaway town with a "willow" rig (prefix wi-) routed
// in routes.jsonl, plus bd and gt shell stubs on PATH that log every call.
//
// Stub behaviour is driven by files/env so tests can script failures:
//   - BD_FAIL_CREATE: substring; a "bd create" whose args contain it fails
//   - GT_FAIL_SLING:  substring; a "gt sling" whose args contain it fails
//   - <town>/list-legs.json, list-syn.json, show.json: canned bd output
type formulaStubTown struct {
	root   string
	log    string
	rigDir string // willow's .beads directory
}

const formulaStubBd = `#!/bin/sh
echo "bd[$PWD] $*" | tr '\n' ' ' >> "LOGPATH"; echo >> "LOGPATH"
case "$1" in
create)
  if [ -n "$BD_FAIL_CREATE" ]; then
    case "$*" in *"$BD_FAIL_CREATE"*) echo "bd: create exploded" >&2; exit 1;; esac
  fi
  ;;
list)
  f="ROOT/list-legs.json"
  case "$*" in *gt:formula-synthesis:*) f="ROOT/list-syn.json";; esac
  if [ -f "$f" ]; then cat "$f"; else echo "[]"; fi
  ;;
show)
  if [ -f "ROOT/show.json" ]; then cat "ROOT/show.json"; else echo "[]"; fi
  ;;
esac
exit 0
`

const formulaStubGt = `#!/bin/sh
echo "gt $*" | tr '\n' ' ' >> "LOGPATH"; echo >> "LOGPATH"
if [ -n "$GT_FAIL_SLING" ]; then
  case "$*" in *"$GT_FAIL_SLING"*) echo "sling: no polecat capacity" >&2; exit 1;; esac
fi
exit 0
`

func setupFormulaStubTown(t *testing.T) *formulaStubTown {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs not supported on windows")
	}

	root, logPath := setupTownWithBdStub(t, "")
	binDir := filepath.Join(root, "bin")
	for name, script := range map[string]string{"bd": formulaStubBd, "gt": formulaStubGt} {
		script = strings.NewReplacer("LOGPATH", logPath, "ROOT", root).Replace(script)
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(script), 0o755); err != nil {
			t.Fatalf("write %s stub: %v", name, err)
		}
	}

	rigDir := filepath.Join(root, "willow", "mayor", "rig", ".beads")
	if err := os.MkdirAll(rigDir, 0o755); err != nil {
		t.Fatalf("mkdir rig beads: %v", err)
	}
	routes := `{"prefix":"hq-","path":"."}` + "\n" + `{"prefix":"wi-","path":"willow/mayor/rig"}` + "\n"
	if err := os.WriteFile(filepath.Join(root, ".beads", "routes.jsonl"), []byte(routes), 0o644); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	// Formula run flags are package globals; restore them after the test.
	oldPR, oldRetry, oldAgent, oldFiles, oldSet := formulaRunPR, formulaRunRetry, formulaRunAgent, formulaRunFiles, formulaRunSet
	formulaRunPR, formulaRunRetry, formulaRunAgent, formulaRunFiles, formulaRunSet = 0, "", "", nil, nil
	t.Cleanup(func() {
		formulaRunPR, formulaRunRetry, formulaRunAgent, formulaRunFiles, formulaRunSet = oldPR, oldRetry, oldAgent, oldFiles, oldSet
	})

	return &formulaStubTown{root: root, log: logPath, rigDir: rigDir}
}

func (s *formulaStubTown) logLines(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(s.log)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read stub log: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// logMatching returns the logged calls that contain every given substring.
func (s *formulaStubTown) logMatching(t *testing.T, substrs ...string) []string {
	t.Helper()
	var out []string
	for _, line := range s.logLines(t) {
		ok := true
		for _, sub := range substrs {
			if !strings.Contains(line, sub) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, line)
		}
	}
	return out
}

func (s *formulaStubTown) writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.root, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func testConvoyFormula() *formula.Formula {
	return &formula.Formula{
		Name:        "design",
		Type:        formula.TypeConvoy,
		Description: "Design review",
		Legs: []formula.Leg{
			{ID: "api", Title: "API Leg", Focus: "api", Description: "Review the API"},
			{ID: "security", Title: "Security Leg", Focus: "security", Description: "Review security"},
			{ID: "scale", Title: "Scale Leg", Focus: "scale", Description: "Review scale"},
		},
	}
}

type trackingCall struct{ root, tracker, issue string }

// captureTracking replaces addTrackingRelationFn for the test and records calls.
func captureTracking(t *testing.T) *[]trackingCall {
	t.Helper()
	var calls []trackingCall
	old := addTrackingRelationFn
	addTrackingRelationFn = func(townRoot, trackerID, issueID string) error {
		calls = append(calls, trackingCall{townRoot, trackerID, issueID})
		return nil
	}
	t.Cleanup(func() { addTrackingRelationFn = old })
	return &calls
}

func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

// TestExecuteConvoyFormula_TracksLegsViaTownRoot guards hq-szdh: legs must be
// tracked by passing the town ROOT (not <root>/.beads) to addTrackingRelationFn
// so rig-prefixed leg IDs are rewritten to external:wi:<id> references, and the
// convoy must be created with the hq-cv- prefix because it lives in the hq DB.
func TestExecuteConvoyFormula_TracksLegsViaTownRoot(t *testing.T) {
	town := setupFormulaStubTown(t)
	calls := captureTracking(t)

	if err := executeConvoyFormula(testConvoyFormula(), "design", "willow"); err != nil {
		t.Fatalf("executeConvoyFormula: %v", err)
	}

	if len(*calls) != 3 {
		t.Fatalf("tracking calls = %d, want 3 (one per leg): %+v", len(*calls), *calls)
	}
	for _, c := range *calls {
		if !sameDir(t, c.root, town.root) {
			t.Errorf("addTrackingRelationFn root = %q, want town root %q", c.root, town.root)
		}
		if !strings.HasPrefix(c.tracker, "hq-cv-") {
			t.Errorf("convoy ID %q should have hq-cv- prefix (convoy lives in the hq DB)", c.tracker)
		}
		if !strings.HasPrefix(c.issue, "wi-leg-") {
			t.Errorf("tracked issue %q should be a wi-leg-* bead", c.issue)
		}
		// With the town root, prefix routing rewrites the leg to an external ref,
		// which is what lets the hq DB store the dependency (no "issue not found").
		got := trackingDependsOnID(c.root, c.issue)
		if want := "external:wi:" + c.issue; got != want {
			t.Errorf("trackingDependsOnID(%q, %q) = %q, want %q", c.root, c.issue, got, want)
		}
	}

	if creates := town.logMatching(t, "create", "--id=hq-cv-", "--labels=gt:convoy"); len(creates) != 1 {
		t.Errorf("expected one hq-cv- convoy create, got %d:\n%s", len(creates), strings.Join(town.logLines(t), "\n"))
	}
	if bad := town.logMatching(t, "create", "--id=wi-cv-"); len(bad) != 0 {
		t.Errorf("convoy must not be created with the rig prefix: %v", bad)
	}
}
