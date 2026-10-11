//go:build !windows

package tmux

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSandboxSocketProbe is driven by the sandbox-compat workflow (see docs/sandbox-compat.md). It is skipped
// unless GT_SANDBOX_PROBE_SOCKET names a tmux socket (under SocketDir) served by a real tmux started outside the
// sandbox. The workflow runs the compiled test binary under srt with a fake tmux first on PATH that answers
// list-sessions, so the guard's direct dial is denied (EPERM) but the list-sessions fallback succeeds.
func TestSandboxSocketProbe(t *testing.T) {
	name := os.Getenv("GT_SANDBOX_PROBE_SOCKET")
	if name == "" {
		t.Skip("GT_SANDBOX_PROBE_SOCKET not set; run via the sandbox-compat workflow")
	}

	// A missing socket makes the guard return nil without ever dialing, which would pass vacuously.
	socketPath := filepath.Join(SocketDir(), name)
	info, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatalf("probe socket %s not usable: %v", socketPath, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("probe socket %s is %s, not a Unix socket", socketPath, info.Mode().Type())
	}

	stale, dialErr := unixSocketStale(socketPath)
	t.Logf("unixSocketStale(%s) = %v, err = %v", socketPath, stale, dialErr)

	// Canary: under srt the dial must be denied, otherwise the EPERM fallthrough is not being exercised.
	if os.Getenv("SANDBOX_RUNTIME") == "1" {
		if stale || !isSocketDialDenied(dialErr) {
			t.Errorf("running under srt but dial of %s was not denied (stale=%v, err=%v); sandbox settings must not allow the unix socket", socketPath, stale, dialErr)
		}
	}

	if err := NewTmuxWithSocket(name).ensureNewSessionSocketSafe(); err != nil {
		t.Errorf("ensureNewSessionSocketSafe() = %v, want nil", err)
	}
}
