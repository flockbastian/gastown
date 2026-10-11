//go:build !windows

package pidalive

import (
	"os"
	"strconv"
	"syscall"
	"testing"
)

// TestPidaliveProbe is driven by the sandbox-compat workflow (see docs/sandbox-compat.md). It is skipped unless
// PIDALIVE_PROBE_PID names a live process outside the test's own sandbox; the workflow then runs the compiled test
// binary under srt, where kill(pid, 0) on that foreign pid returns EPERM.
func TestPidaliveProbe(t *testing.T) {
	raw := os.Getenv("PIDALIVE_PROBE_PID")
	if raw == "" {
		t.Skip("PIDALIVE_PROBE_PID not set; run via the sandbox-compat workflow")
	}
	pid, err := strconv.Atoi(raw)
	if err != nil || pid <= 0 {
		t.Fatalf("PIDALIVE_PROBE_PID=%q is not a valid pid", raw)
	}

	p, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("FindProcess(%d): %v", pid, err)
	}
	sigErr := p.Signal(syscall.Signal(0))
	oldIdiom := sigErr == nil
	t.Logf("old idiom (p.Signal(0) == nil) for pid %d = %v (err=%v)", pid, oldIdiom, sigErr)

	if !Alive(pid) {
		t.Errorf("Alive(%d) = false, want true (Signal(0) err: %v)", pid, sigErr)
	}
	if !Proc(p) {
		t.Errorf("Proc(pid %d) = false, want true (Signal(0) err: %v)", pid, sigErr)
	}

	// Canary: if the sandbox is really confining us, the naive check must be fooled. If it is not, this probe
	// proves nothing about the EPERM path.
	if os.Getenv("SANDBOX_RUNTIME") == "1" && oldIdiom {
		t.Errorf("running under srt but p.Signal(0) on foreign pid %d succeeded; sandbox is not confining signals", pid)
	}
}
