package cmd

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// stubAWSPreflight installs a fake preflight script (so the existence check
// passes) under a fresh town root and replaces the runner. It returns the town
// root and a pointer to the number of runner invocations.
func stubAWSPreflight(t *testing.T, out string, code int, err error) (string, *int) {
	t.Helper()
	t.Setenv(awsPreflightSkipEnv, "")
	root := t.TempDir()
	writeStubPreflightScript(t, root, "#!/bin/sh\nexit 0\n")
	calls := 0
	old := awsPreflightRunner
	awsPreflightRunner = func(ctx context.Context, script string) (string, int, error) {
		calls++
		return out, code, err
	}
	t.Cleanup(func() { awsPreflightRunner = old })
	return root, &calls
}

func writeStubPreflightScript(t *testing.T, root, body string) string {
	t.Helper()
	dir := filepath.Join(root, "scripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "aws-preflight")
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

const expiredOut = "aws-preflight: AWS credentials expired or invalid (profile bedrock)\naws-preflight: fix: scripts/gt-sso-login\n"

func TestCheckAWSPreflight_ExpiredBlocks(t *testing.T) {
	root, calls := stubAWSPreflight(t, expiredOut, 1, nil)
	err := checkAWSPreflight(root)
	if err == nil {
		t.Fatal("exit 1 must block")
	}
	for _, want := range []string{"credentials expired or invalid", "profile bedrock", "gt-sso-login", awsPreflightSkipEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
	if *calls != 1 {
		t.Errorf("runner calls = %d, want 1", *calls)
	}
}

func TestCheckAWSPreflight_FailsOpen(t *testing.T) {
	cases := []struct {
		name string
		out  string
		code int
		err  error
	}{
		{"ok", "aws-preflight: AWS credentials OK", 0, nil},
		{"exit 2 (cannot verify)", "aws CLI missing", 2, nil},
		{"other status", "boom", 127, nil},
		{"timeout", "", -1, context.DeadlineExceeded},
		{"exec error", "", -1, os.ErrPermission},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, calls := stubAWSPreflight(t, tc.out, tc.code, tc.err)
			if err := checkAWSPreflight(root); err != nil {
				t.Errorf("must fail open, got %v", err)
			}
			if *calls != 1 {
				t.Errorf("runner calls = %d, want 1", *calls)
			}
		})
	}
}

func TestCheckAWSPreflight_MissingOrNonExecutableScriptFailsOpen(t *testing.T) {
	root, calls := stubAWSPreflight(t, expiredOut, 1, nil)
	script := filepath.Join(root, "scripts", "aws-preflight")

	if runtime.GOOS != "windows" {
		if err := os.Chmod(script, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := checkAWSPreflight(root); err != nil {
			t.Errorf("non-executable script must fail open, got %v", err)
		}
	}
	if err := os.Remove(script); err != nil {
		t.Fatal(err)
	}
	if err := checkAWSPreflight(root); err != nil {
		t.Errorf("missing script must fail open, got %v", err)
	}
	if err := checkAWSPreflight(""); err != nil {
		t.Errorf("empty town root must fail open, got %v", err)
	}
	if *calls != 0 {
		t.Errorf("runner must not be called without an executable script, calls = %d", *calls)
	}
}

func TestCheckAWSPreflight_SkipEnvBypasses(t *testing.T) {
	root, calls := stubAWSPreflight(t, expiredOut, 1, nil)
	t.Setenv(awsPreflightSkipEnv, "1")
	if err := checkAWSPreflight(root); err != nil {
		t.Errorf("GT_SKIP_AWS_PREFLIGHT=1 must bypass, got %v", err)
	}
	if *calls != 0 {
		t.Errorf("runner must not be called when skipped, calls = %d", *calls)
	}
}

// The tests below use the real runner against small shell scripts.

func useRealRunnerWithScript(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on windows")
	}
	t.Setenv(awsPreflightSkipEnv, "")
	root := t.TempDir()
	writeStubPreflightScript(t, root, body)
	return root
}

func TestCheckAWSPreflight_RealRunnerExit1Blocks(t *testing.T) {
	root := useRealRunnerWithScript(t, "#!/bin/sh\necho 'aws-preflight: AWS credentials expired or invalid (profile x)' >&2\nexit 1\n")
	err := checkAWSPreflight(root)
	if err == nil || !strings.Contains(err.Error(), "profile x") {
		t.Fatalf("exit 1 must block and include the script output, got %v", err)
	}
}

func TestCheckAWSPreflight_RealRunnerExit0And2FailOpen(t *testing.T) {
	for _, code := range []string{"0", "2", "3"} {
		root := useRealRunnerWithScript(t, "#!/bin/sh\necho hi\nexit "+code+"\n")
		if err := checkAWSPreflight(root); err != nil {
			t.Errorf("exit %s must fail open, got %v", code, err)
		}
	}
}

func TestCheckAWSPreflight_RealRunnerTimeoutFailsOpen(t *testing.T) {
	root := useRealRunnerWithScript(t, "#!/bin/sh\nsleep 30\nexit 1\n")
	old := awsPreflightTimeout
	awsPreflightTimeout = 200 * time.Millisecond
	t.Cleanup(func() { awsPreflightTimeout = old })

	start := time.Now()
	if err := checkAWSPreflight(root); err != nil {
		t.Errorf("timeout must fail open, got %v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("timeout not enforced, took %v", d)
	}
}

// --- wiring: gt formula run / gt sling -------------------------------------

const preflightConvoyFormulaTOML = `formula = "preflight-test"
description = "Preflight test"
type = "convoy"
version = 1

[[legs]]
id = "api"
title = "API Leg"
focus = "api"
description = "Review the API"
`

func setupFormulaRunPreflight(t *testing.T) *formulaStubTown {
	t.Helper()
	town := setupFormulaStubTown(t)
	captureTracking(t)
	dir := filepath.Join(town.root, ".beads", "formulas")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "preflight-test.formula.toml"), []byte(preflightConvoyFormulaTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	oldRig, oldDry := formulaRunRig, formulaRunDryRun
	formulaRunRig, formulaRunDryRun = "willow", false
	t.Cleanup(func() { formulaRunRig, formulaRunDryRun = oldRig, oldDry })
	return town
}

func TestRunFormulaRun_ExpiredCredentialsBlockBeforeAnythingIsCreated(t *testing.T) {
	town := setupFormulaRunPreflight(t)
	writeStubPreflightScript(t, town.root, "#!/bin/sh\nexit 1\n")
	t.Setenv(awsPreflightSkipEnv, "")
	calls := 0
	old := awsPreflightRunner
	awsPreflightRunner = func(context.Context, string) (string, int, error) { calls++; return expiredOut, 1, nil }
	t.Cleanup(func() { awsPreflightRunner = old })

	for _, retry := range []string{"", "hq-cv-existing"} {
		formulaRunRetry = retry
		var err error
		captureStdout(t, func() { err = runFormulaRun(nil, []string{"preflight-test"}) })
		if err == nil || !strings.Contains(err.Error(), "credentials are expired") {
			t.Fatalf("retry=%q: expected preflight refusal, got %v", retry, err)
		}
		if lines := town.logLines(t); len(lines) != 0 {
			t.Errorf("retry=%q: nothing may be created/slung, bd/gt log: %v", retry, lines)
		}
	}
	if calls != 2 {
		t.Errorf("runner calls = %d, want 2", calls)
	}
}

func TestRunFormulaRun_DryRunNotBlocked(t *testing.T) {
	town := setupFormulaRunPreflight(t)
	writeStubPreflightScript(t, town.root, "#!/bin/sh\nexit 1\n")
	t.Setenv(awsPreflightSkipEnv, "")
	calls := 0
	old := awsPreflightRunner
	awsPreflightRunner = func(context.Context, string) (string, int, error) { calls++; return expiredOut, 1, nil }
	t.Cleanup(func() { awsPreflightRunner = old })
	formulaRunDryRun = true

	var err error
	captureStdout(t, func() { err = runFormulaRun(nil, []string{"preflight-test"}) })
	if err != nil {
		t.Fatalf("dry-run must not be blocked: %v", err)
	}
	if calls != 0 {
		t.Errorf("dry-run must not run the preflight, calls = %d", calls)
	}
}

func TestRunFormulaRun_CredentialsOKDispatches(t *testing.T) {
	town := setupFormulaRunPreflight(t)
	writeStubPreflightScript(t, town.root, "#!/bin/sh\nexit 0\n")
	t.Setenv(awsPreflightSkipEnv, "")
	old := awsPreflightRunner
	awsPreflightRunner = func(context.Context, string) (string, int, error) { return "", 0, nil }
	t.Cleanup(func() { awsPreflightRunner = old })

	var err error
	captureStdout(t, func() { err = runFormulaRun(nil, []string{"preflight-test"}) })
	if err != nil {
		t.Fatalf("runFormulaRun: %v", err)
	}
	if got := len(town.logMatching(t, "gt sling")); got != 1 {
		t.Errorf("sling calls = %d, want 1", got)
	}
}

func TestRunSling_ExpiredCredentialsBlockBeforeAnythingIsCreated(t *testing.T) {
	town := setupFormulaStubTown(t)
	t.Setenv("GT_ROLE", "")
	t.Setenv("GT_POLECAT", "")
	writeStubPreflightScript(t, town.root, "#!/bin/sh\nexit 1\n")
	t.Setenv(awsPreflightSkipEnv, "")
	calls := 0
	old := awsPreflightRunner
	awsPreflightRunner = func(context.Context, string) (string, int, error) { calls++; return expiredOut, 1, nil }
	t.Cleanup(func() { awsPreflightRunner = old })
	oldDry := slingDryRun
	t.Cleanup(func() { slingDryRun = oldDry })

	// Single, batch (explicit rig) and batch (auto-resolve) shapes all stop at the one check.
	for _, args := range [][]string{
		{"wi-abc", "willow"},
		{"wi-abc", "wi-def", "wi-ghi", "willow"},
		{"wi-abc", "wi-def", "wi-ghi"},
	} {
		slingDryRun = false
		err := runSling(nil, append([]string(nil), args...))
		if err == nil || !strings.Contains(err.Error(), "credentials are expired") {
			t.Fatalf("args %v: expected preflight refusal, got %v", args, err)
		}
	}
	if calls != 3 {
		t.Errorf("runner calls = %d, want 3 (once per sling invocation, not per bead)", calls)
	}
	if lines := town.logLines(t); len(lines) != 0 {
		t.Errorf("nothing may be created/hooked, bd/gt log: %v", lines)
	}

	// --dry-run is never blocked and never even runs the preflight.
	calls = 0
	slingDryRun = true
	err := runSling(nil, []string{"wi-abc", "willow"})
	if err != nil && strings.Contains(err.Error(), "credentials are expired") {
		t.Errorf("dry-run must not be blocked by the preflight: %v", err)
	}
	if calls != 0 {
		t.Errorf("dry-run must not run the preflight, calls = %d", calls)
	}
}
