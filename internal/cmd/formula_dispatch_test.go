package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/formula"
)

// runConvoyCapturingOutput runs executeConvoyFormula and returns its error and stdout.
func runConvoyCapturingOutput(t *testing.T, f *formula.Formula) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		err = executeConvoyFormula(f, "design", "willow")
	})
	return out, err
}

// TestExecuteConvoyFormula_AllLegsDispatched is the happy path: nil error and
// an N/N summary, with every leg labelled so --retry-failed can find it again.
func TestExecuteConvoyFormula_AllLegsDispatched(t *testing.T) {
	town := setupFormulaStubTown(t)
	captureTracking(t)

	out, err := runConvoyCapturingOutput(t, testConvoyFormula())
	if err != nil {
		t.Fatalf("executeConvoyFormula: %v", err)
	}
	if !strings.Contains(out, "Convoy dispatched!") || !strings.Contains(out, "3/3 dispatched") {
		t.Errorf("summary should report 3/3 dispatched, got:\n%s", out)
	}
	if got := len(town.logMatching(t, "gt sling")); got != 3 {
		t.Errorf("sling calls = %d, want 3", got)
	}
	for _, leg := range []string{"api", "security", "scale"} {
		if got := town.logMatching(t, "create", "wi-leg-", "gt:formula-leg:"+leg); len(got) != 1 {
			t.Errorf("leg %s: want one labelled create, got %d", leg, len(got))
		}
	}
	if got := town.logMatching(t, "dispatch-failed"); len(got) != 0 {
		t.Errorf("no leg should be labelled dispatch-failed on success: %v", got)
	}
}

// TestExecuteConvoyFormula_SlingFailureFailsRun guards hq-clrp: a leg whose
// sling fails must not be reported as dispatched, the run must return an
// error (non-zero exit), and the bead must be labelled/commented for retry.
func TestExecuteConvoyFormula_SlingFailureFailsRun(t *testing.T) {
	town := setupFormulaStubTown(t)
	captureTracking(t)
	t.Setenv("GT_FAIL_SLING", "Security Leg")

	out, err := runConvoyCapturingOutput(t, testConvoyFormula())
	if err == nil {
		t.Fatalf("expected an error when a leg fails to sling; output:\n%s", out)
	}
	for _, want := range []string{"2/3", "security", "--retry-failed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
	if strings.Contains(out, "Convoy dispatched!") {
		t.Errorf("must not claim success on partial dispatch:\n%s", out)
	}
	for _, want := range []string{"2/3 legs dispatched", "Leg security", "sling failed", "no polecat capacity"} {
		if !strings.Contains(out, want) {
			t.Errorf("output should contain %q:\n%s", want, out)
		}
	}

	// The failed leg's bead is labelled and commented; the others are not.
	failedBead := ""
	for _, line := range town.logMatching(t, "create", "--title=Security Leg") {
		for _, f := range strings.Fields(line) {
			if id, ok := strings.CutPrefix(f, "--id="); ok {
				failedBead = id
			}
		}
	}
	if failedBead == "" {
		t.Fatal("could not find security leg bead ID in stub log")
	}
	if got := town.logMatching(t, "update "+failedBead, "--add-label gt:dispatch-failed"); len(got) != 1 {
		t.Errorf("failed leg should be labelled dispatch-failed, got %v", got)
	}
	if got := town.logMatching(t, "comments add "+failedBead, "Failed to sling"); len(got) != 1 {
		t.Errorf("failed leg should get a failure comment, got %v", got)
	}
	if got := town.logMatching(t, "--add-label gt:dispatch-failed"); len(got) != 1 {
		t.Errorf("only the failed leg should be labelled, got %v", got)
	}
}

// TestExecuteConvoyFormula_CreateFailureFailsRun: a leg bead that cannot be
// created is never slung and must fail the run too.
func TestExecuteConvoyFormula_CreateFailureFailsRun(t *testing.T) {
	town := setupFormulaStubTown(t)
	captureTracking(t)
	t.Setenv("BD_FAIL_CREATE", "--title=Scale Leg")

	out, err := runConvoyCapturingOutput(t, testConvoyFormula())
	if err == nil {
		t.Fatalf("expected an error when a leg bead cannot be created; output:\n%s", out)
	}
	if !strings.Contains(err.Error(), "2/3") || !strings.Contains(err.Error(), "scale") {
		t.Errorf("error %q should name the failed leg and 2/3", err)
	}
	if !strings.Contains(out, "Leg scale") || !strings.Contains(out, "create failed") || !strings.Contains(out, "create exploded") {
		t.Errorf("output should list the failed leg with its error:\n%s", out)
	}
	if got := town.logMatching(t, "gt sling", "Scale Leg"); len(got) != 0 {
		t.Errorf("a leg without a bead must not be slung: %v", got)
	}
	if got := town.logMatching(t, "gt sling"); len(got) != 2 {
		t.Errorf("sling calls = %d, want 2", len(got))
	}
}

const retryConvoyShowJSON = `[{"id":"hq-cv-old","description":"Formula convoy: design\n\nLegs: 3\nRig: willow\nReview ID: rev42"}]`

const retryLegsJSON = `[
 {"id":"wi-leg-api1","labels":["gt:formula-convoy:hq-cv-old","gt:formula-leg:api"]},
 {"id":"wi-leg-sec1","labels":["gt:formula-convoy:hq-cv-old","gt:formula-leg:security","gt:dispatch-failed"]}
]`

// TestExecuteConvoyFormula_RetryFailed re-slings only the undispatched legs of
// an existing convoy: it must not create a new convoy or re-sling legs that
// already went out, re-creates legs that never got a bead, and clears the
// failure label once the sling succeeds.
func TestExecuteConvoyFormula_RetryFailed(t *testing.T) {
	town := setupFormulaStubTown(t)
	calls := captureTracking(t)
	town.writeFile(t, "show.json", retryConvoyShowJSON)
	town.writeFile(t, "list-legs.json", retryLegsJSON)
	formulaRunRetry = "hq-cv-old"

	out, err := runConvoyCapturingOutput(t, testConvoyFormula())
	if err != nil {
		t.Fatalf("retry should succeed, got %v\n%s", err, out)
	}
	if !strings.Contains(out, "3/3 dispatched") {
		t.Errorf("retry summary should report 3/3 (1 earlier + 2 now):\n%s", out)
	}
	if got := town.logMatching(t, "create", "--id=hq-cv-"); len(got) != 0 {
		t.Errorf("retry must reuse the existing convoy, not create one: %v", got)
	}

	slings := town.logMatching(t, "gt sling")
	if len(slings) != 2 {
		t.Fatalf("sling calls = %d, want 2 (security + scale): %v", len(slings), slings)
	}
	if !strings.Contains(slings[0], "gt sling wi-leg-sec1 willow") && !strings.Contains(slings[1], "gt sling wi-leg-sec1 willow") {
		t.Errorf("security's existing bead should be re-slung: %v", slings)
	}
	if got := town.logMatching(t, "gt sling wi-leg-api1"); len(got) != 0 {
		t.Errorf("already dispatched leg must not be re-slung: %v", got)
	}

	// Only scale (no bead before) gets a new bead, tracked by the old convoy.
	creates := town.logMatching(t, "create", "wi-leg-")
	if len(creates) != 1 || !strings.Contains(creates[0], "--title=Scale Leg") || !strings.Contains(creates[0], "gt:formula-leg:scale") {
		t.Errorf("want exactly one create for the scale leg, got %v", creates)
	}
	if !strings.Contains(creates[0], "gt:formula-convoy:hq-cv-old") {
		t.Errorf("new leg must be labelled with the existing convoy: %v", creates)
	}
	if len(*calls) != 1 || (*calls)[0].tracker != "hq-cv-old" || !strings.HasPrefix((*calls)[0].issue, "wi-leg-") {
		t.Errorf("only the new leg should be tracked by hq-cv-old: %+v", *calls)
	}
	if got := town.logMatching(t, "update wi-leg-sec1", "--remove-label gt:dispatch-failed"); len(got) != 1 {
		t.Errorf("successful retry should clear the failure label, got %v", got)
	}
}

// TestExecuteConvoyFormula_RetryFailedStillFailing keeps failing (non-zero) and
// keeps the label when the retried sling fails again.
func TestExecuteConvoyFormula_RetryFailedStillFailing(t *testing.T) {
	town := setupFormulaStubTown(t)
	captureTracking(t)
	town.writeFile(t, "show.json", retryConvoyShowJSON)
	town.writeFile(t, "list-legs.json", retryLegsJSON)
	formulaRunRetry = "hq-cv-old"
	t.Setenv("GT_FAIL_SLING", "wi-leg-sec1")

	_, err := runConvoyCapturingOutput(t, testConvoyFormula())
	if err == nil || !strings.Contains(err.Error(), "security") {
		t.Fatalf("retry with a still-failing leg must return an error naming it, got %v", err)
	}
	if got := town.logMatching(t, "--remove-label gt:dispatch-failed"); len(got) != 0 {
		t.Errorf("label must stay while the leg is undispatched: %v", got)
	}
}

func TestExecuteConvoyFormula_RetryFailedUnknownConvoy(t *testing.T) {
	town := setupFormulaStubTown(t)
	captureTracking(t)
	formulaRunRetry = "hq-cv-missing" // show.json absent: stub prints []

	_, err := runConvoyCapturingOutput(t, testConvoyFormula())
	if err == nil || !strings.Contains(err.Error(), "hq-cv-missing") {
		t.Fatalf("expected a not-found error naming the convoy, got %v", err)
	}
	if got := town.logMatching(t, "create"); len(got) != 0 {
		t.Errorf("nothing may be created for an unknown convoy: %v", got)
	}
}

// TestExecuteWorkflowFormula_SlingFailureFailsRun: workflow steps had the same
// swallowed-failure behaviour as convoy legs.
func TestExecuteWorkflowFormula_SlingFailureFailsRun(t *testing.T) {
	town := setupFormulaStubTown(t)
	calls := captureTracking(t)
	t.Setenv("GT_FAIL_SLING", "Build Step")

	f := &formula.Formula{
		Name:        "wf",
		Type:        formula.TypeWorkflow,
		Description: "Workflow",
		Steps: []formula.Step{
			{ID: "build", Title: "Build Step", Description: "build it"},
			{ID: "ship", Title: "Ship Step", Description: "ship it", Needs: []string{"build"}},
		},
	}
	var err error
	out := captureStdout(t, func() { err = executeWorkflowFormula(f, "wf", "willow") })
	if err == nil || !strings.Contains(err.Error(), "build") {
		t.Fatalf("expected an error naming the failed step, got %v\n%s", err, out)
	}
	if strings.Contains(out, "Workflow dispatched!") {
		t.Errorf("must not claim success:\n%s", out)
	}
	for _, c := range *calls {
		if !sameDir(t, c.root, town.root) {
			t.Errorf("step tracking root = %q, want town root %q", c.root, town.root)
		}
	}
}

func TestConvoyReviewID(t *testing.T) {
	t.Parallel()
	if got := convoyReviewID("Formula convoy: x\n\nLegs: 2\nReview ID: abc12\n"); got != "abc12" {
		t.Errorf("convoyReviewID = %q, want abc12", got)
	}
	if got := convoyReviewID("Formula convoy: x\nLegs: 2"); got != "" {
		t.Errorf("convoyReviewID without ID = %q, want empty", got)
	}
}

func TestErrWithOutput(t *testing.T) {
	t.Parallel()
	base := errors.New("exit status 1")
	if got := errWithOutput(base, "noise\nsling: no capacity\n"); got != "exit status 1: sling: no capacity" {
		t.Errorf("errWithOutput = %q", got)
	}
	if got := errWithOutput(base, "  \n"); got != "exit status 1" {
		t.Errorf("errWithOutput with empty stderr = %q", got)
	}
}
