package daemon

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/tmux"
)

func TestIsClaudeAuthError(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"token expired", "Error: Token is expired\n", true},
		{"aws creds", "API Error: Could not load AWS credentials", true},
		{"sso login hint", "Run `aws sso login --profile dev` to refresh", true},
		{"sso session expired", "The SSO session associated with this profile has expired or is otherwise invalid.", true},
		{"invalid api key", `{"error":"invalid x-api-key"}`, true},
		{"api 401", "API Error: 401 Unauthorized", true},
		{"authentication_error json", `{"type": "authentication_error"}`, true},
		{"healthy pane", "Reading internal/foo.go\n> working on gt-123\n", false},
		{"rate limit is not auth", "API Error: Rate limit reached", false},
		{"bare 401 in code", "line 401: func foo()", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsClaudeAuthError(tt.in); got != tt.want {
				t.Errorf("IsClaudeAuthError(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

type authAlertRecorder struct {
	notifies   []string
	escalates  []string
	notifyArgs [][3]string
}

func newAuthTestDaemon(t *testing.T, rec *authAlertRecorder) *Daemon {
	t.Helper()
	return &Daemon{
		config: &Config{TownRoot: t.TempDir()},
		logger: log.New(os.Stderr, "", 0),
		notifyHumanFn: func(priority, title, msg string) {
			rec.notifies = append(rec.notifies, title)
			rec.notifyArgs = append(rec.notifyArgs, [3]string{priority, title, msg})
		},
		escalateFn: func(source, message string) {
			rec.escalates = append(rec.escalates, source+": "+message)
		},
	}
}

func TestHandleAuthStuckPane_DedupAcrossTicks(t *testing.T) {
	rec := &authAlertRecorder{}
	d := newAuthTestDaemon(t, rec)
	pane := "> doing work\n  Error: Token is expired\n  Run aws sso login\n"
	hook := func() string { return "gt-abc" }

	for i := 0; i < 5; i++ {
		d.handleAuthStuckPane("gt-myr-mycat", "myr/mycat", pane, hook)
	}
	if len(rec.notifies) != 1 || len(rec.escalates) != 1 {
		t.Fatalf("want exactly 1 notify and 1 escalate, got %d/%d", len(rec.notifies), len(rec.escalates))
	}
	if got := rec.notifyArgs[0][0]; got != "high" {
		t.Errorf("priority = %q, want high", got)
	}
	if !strings.Contains(rec.escalates[0], "gt-abc") {
		t.Errorf("escalation should mention hooked bead: %q", rec.escalates[0])
	}
}

func TestHandleAuthStuckPane_NoMatchNoAlert(t *testing.T) {
	rec := &authAlertRecorder{}
	d := newAuthTestDaemon(t, rec)
	for i := 0; i < 3; i++ {
		d.handleAuthStuckPane("s", "myr/mycat", "all good, compiling\n", func() string { return "gt-abc" })
	}
	if len(rec.notifies)+len(rec.escalates) != 0 {
		t.Fatalf("expected no alerts, got %v %v", rec.notifies, rec.escalates)
	}
}

func TestHandleAuthStuckPane_NoHookNoAlert(t *testing.T) {
	rec := &authAlertRecorder{}
	d := newAuthTestDaemon(t, rec)
	d.handleAuthStuckPane("s", "myr/mycat", "Token is expired", func() string { return "" })
	if len(rec.notifies)+len(rec.escalates) != 0 {
		t.Fatalf("idle agent must not alert, got %v %v", rec.notifies, rec.escalates)
	}
}

func TestHandleAuthStuckPane_RealertsAfterWindowAndRecovery(t *testing.T) {
	rec := &authAlertRecorder{}
	d := newAuthTestDaemon(t, rec)
	hook := func() string { return "gt-abc" }
	bad := "Token is expired"

	d.handleAuthStuckPane("s", "a", bad, hook)
	// Age the recorded alert beyond the window: should alert again.
	d.authStuck.mu.Lock()
	d.authStuck.lastAlert["s|gt-abc"] = time.Now().Add(-2 * authStuckAlertWindow)
	d.authStuck.mu.Unlock()
	d.handleAuthStuckPane("s", "a", bad, hook)
	if len(rec.escalates) != 2 {
		t.Fatalf("want re-alert after window, got %d alerts", len(rec.escalates))
	}

	// Recovery clears dedup state, so a new stuck episode alerts immediately.
	d.handleAuthStuckPane("s", "a", "healthy", hook)
	d.handleAuthStuckPane("s", "a", bad, hook)
	if len(rec.escalates) != 3 {
		t.Fatalf("want alert for new stuck episode, got %d alerts", len(rec.escalates))
	}
}

// fakeTmuxWithPane simulates a live session whose capture-pane returns pane.
func fakeTmuxWithPane(t *testing.T, dir, pane string) {
	t.Helper()
	paneFile := filepath.Join(dir, "pane.txt")
	if err := os.WriteFile(paneFile, []byte(pane), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$*\" in\n" +
		"  *has-session*) exit 0;;\n" +
		"  *capture-pane*) cat '" + paneFile + "'; exit 0;;\n" +
		"  *) exit 0;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// End to end through checkPolecatHealth: live session, hooked bead, stuck pane
// => exactly one alert across repeated ticks, and no kill-session issued.
func TestCheckPolecatHealth_AuthStuckAlertsOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks")
	}
	for _, tc := range []struct {
		name string
		pane string
		want int
	}{
		{"stuck", "Error: Token is expired\n", 1},
		{"healthy", "Running tests...\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binDir := t.TempDir()
			fakeTmuxWithPane(t, binDir, tc.pane)
			bdPath := writeFakeTestBD(t, binDir, "working", "working", "gt-xyz", time.Now().UTC().Format(time.RFC3339))
			t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

			rec := &authAlertRecorder{}
			d := newAuthTestDaemon(t, rec)
			d.tmux = tmux.NewTmux()
			d.bdPath = bdPath

			for i := 0; i < 4; i++ {
				d.checkPolecatHealth("myr", "mycat")
			}
			if len(rec.escalates) != tc.want || len(rec.notifies) != tc.want {
				t.Fatalf("want %d alerts, got notify=%d escalate=%d", tc.want, len(rec.notifies), len(rec.escalates))
			}
		})
	}
}

func TestNotifyHuman_ScriptHandling(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script")
	}
	rec := &authAlertRecorder{}
	d := newAuthTestDaemon(t, rec)
	// Missing script: must not panic or fail.
	d.notifyHuman("high", "t", "m")

	scriptsDir := filepath.Join(d.config.TownRoot, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(d.config.TownRoot, "out.txt")
	script := "#!/bin/sh\nprintf '%s|%s|%s' \"$1\" \"$2\" \"$3\" > '" + out + "'\n"
	path := filepath.Join(scriptsDir, "notify-human")

	// Not executable: skipped.
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	d.notifyHuman("high", "title", "msg")
	if _, err := os.Stat(out); err == nil {
		t.Fatal("non-executable script must not run")
	}

	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	d.notifyHuman("high", "title", "msg")
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("script did not run: %v", err)
	}
	if string(got) != "high|title|msg" {
		t.Errorf("script args = %q", got)
	}
}
