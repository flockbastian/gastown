//go:build !windows

package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests guard the sandbox (srt) compatibility patch in ensureLiveSocketSafe: when connect() to the tmux
// socket is denied (EPERM/EACCES) the guard must fall through to the list-sessions probe instead of failing.
// A chmod 0 socket makes the dial fail with EACCES for non-root users, which stands in for srt's EPERM.

// deniedSocket creates a live unix socket under SocketDir() whose dial is denied, and verifies the precondition.
func deniedSocket(t *testing.T, prefix string) (socket, socketPath string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("chmod 0 does not deny root")
	}
	socket = uniqueSocketName(t, prefix)
	_, socketPath = listenOnSocketPath(t, socket)
	if err := os.Chmod(socketPath, 0); err != nil {
		t.Fatalf("Chmod(%s, 0): %v", socketPath, err)
	}
	if _, err := unixSocketStale(socketPath); !isSocketDialDenied(err) {
		t.Skipf("dial of chmod 0 socket returned %v, want EPERM/EACCES; cannot simulate sandbox", err)
	}
	return socket, socketPath
}

// fakeTmux puts a tmux shell script with the given body first on PATH.
func fakeTmux(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestSocketGuardSandboxDialDeniedProbeSucceeds(t *testing.T) {
	socket, socketPath := deniedSocket(t, "gt-h9z-sbx-ok")
	fakeTmux(t, "exit 0")
	tm := NewTmuxWithSocket(socket)

	if err := tm.ensureLiveSocketSafe(socketPath); err != nil {
		t.Fatalf("ensureLiveSocketSafe(dial denied, list-sessions ok) = %v, want nil", err)
	}
	if err := tm.ensureNewSessionSocketSafe(); err != nil {
		t.Fatalf("ensureNewSessionSocketSafe(dial denied, list-sessions ok) = %v, want nil", err)
	}
}

func TestSocketGuardSandboxDialDeniedNoServer(t *testing.T) {
	socket, socketPath := deniedSocket(t, "gt-h9z-sbx-noserver")
	fakeTmux(t, `echo "no server running on `+socketPath+`" >&2; exit 1`)
	tm := NewTmuxWithSocket(socket)

	for name, check := range map[string]func() error{
		"ensureLiveSocketSafe":       func() error { return tm.ensureLiveSocketSafe(socketPath) },
		"ensureNewSessionSocketSafe": tm.ensureNewSessionSocketSafe,
	} {
		err := check()
		if err == nil {
			t.Fatalf("%s(dial denied, no server) = nil, want error", name)
		}
		if !strings.Contains(err.Error(), "gt-h9z") {
			t.Fatalf("%s error %q does not mention gt-h9z", name, err)
		}
	}
}

func TestSocketGuardSandboxMissingSocket(t *testing.T) {
	// Fail loudly if the guard ever shells out when there is no socket to protect.
	fakeTmux(t, "exit 1")
	socket := uniqueSocketName(t, "gt-h9z-sbx-missing")
	tm := NewTmuxWithSocket(socket)

	if err := tm.ensureNewSessionSocketSafe(); err != nil {
		t.Fatalf("ensureNewSessionSocketSafe(missing socket) = %v, want nil", err)
	}
}
