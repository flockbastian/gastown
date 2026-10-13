package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/steveyegge/gastown/internal/util"
)

// claudeAuthErrorPatterns are signatures indicating that an agent session is
// blocked on an authentication / credential failure (for example an expired
// AWS SSO token when Claude runs through Bedrock). Matched against the tail of
// the agent's tmux pane.
//
// Like claudeUsageLimitPatterns, these are deliberately specific. A match only
// ever produces a notification (never a kill or restart), so a rare false
// positive costs one deduplicated alert.
var claudeAuthErrorPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)Could not load AWS credentials`),
	regexp.MustCompile(`(?i)Token is expired`),
	regexp.MustCompile(`(?i)aws sso login`),
	regexp.MustCompile(`(?i)SSO session associated with this profile has expired`),
	regexp.MustCompile(`(?i)invalid x-api-key`),
	regexp.MustCompile(`(?i)API Error:\s*401\b`),
	regexp.MustCompile(`(?i)"type"\s*:\s*"authentication_error"`),
}

// IsClaudeAuthError reports whether the given pane output contains a signature
// of an authentication failure that will not resolve without human action
// (re-login, new API key). Keep all classification logic here so it can be
// unit-tested with synthetic fixtures.
func IsClaudeAuthError(output string) bool {
	if output == "" {
		return false
	}
	for _, re := range claudeAuthErrorPatterns {
		if re.MatchString(output) {
			return true
		}
	}
	return false
}

const (
	// authStuckAlertWindow is the minimum time between repeated alerts for the
	// same stuck session + hooked bead. Heartbeat ticks are minutes apart, so
	// without a window the human would be paged every tick.
	authStuckAlertWindow = 1 * time.Hour

	// authStuckPaneLines is how many lines of pane tail are scanned.
	authStuckPaneLines = 30

	// notifyHumanTimeout bounds the notify-human script so it can never stall
	// the heartbeat loop.
	notifyHumanTimeout = 10 * time.Second
)

// authStuckTracker deduplicates auth-stuck alerts per session. Safe for
// concurrent use (per-rig health checks run in a worker pool).
type authStuckTracker struct {
	mu        sync.Mutex
	lastAlert map[string]time.Time // key: session + "|" + hooked bead
}

// shouldAlert returns true (and records the alert) if no alert for key was
// sent within window.
func (a *authStuckTracker) shouldAlert(key string, now time.Time, window time.Duration) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lastAlert == nil {
		a.lastAlert = make(map[string]time.Time)
	}
	if last, ok := a.lastAlert[key]; ok && now.Sub(last) < window {
		return false
	}
	a.lastAlert[key] = now
	return true
}

// clearSession forgets all alerts for a session so that a recovered session
// that later gets stuck again alerts afresh.
func (a *authStuckTracker) clearSession(sessionName string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	prefix := sessionName + "|"
	for k := range a.lastAlert {
		if strings.HasPrefix(k, prefix) {
			delete(a.lastAlert, k)
		}
	}
}

// checkAuthStuck inspects a LIVE agent session's pane for auth-error
// signatures. If the pane matches and the agent holds a hooked bead, it sends
// a deduplicated human notification and HIGH escalation. It never kills or
// restarts the session: re-running the agent cannot fix expired credentials.
//
// getHook is only invoked when the pane matches, keeping the common (healthy)
// path to a single cheap tmux capture.
func (d *Daemon) checkAuthStuck(sessionName, agentLabel string, getHook func() string) {
	if d.tmux == nil {
		return
	}
	pane, err := d.tmux.CapturePane(sessionName, authStuckPaneLines)
	if err != nil {
		return
	}
	d.handleAuthStuckPane(sessionName, agentLabel, pane, getHook)
}

// handleAuthStuckPane is the pane-content half of checkAuthStuck, split out
// for testing.
func (d *Daemon) handleAuthStuckPane(sessionName, agentLabel, pane string, getHook func() string) {
	if !IsClaudeAuthError(pane) {
		d.authStuck.clearSession(sessionName)
		return
	}
	hookBead := getHook()
	if hookBead == "" {
		return // idle agent with an auth error holds no work; nothing is stuck
	}
	if !d.authStuck.shouldAlert(sessionName+"|"+hookBead, time.Now(), authStuckAlertWindow) {
		return
	}

	title := fmt.Sprintf("Agent %s stuck on auth error", agentLabel)
	msg := fmt.Sprintf("%s (session %s) is holding %s but its pane shows an authentication error "+
		"(e.g. expired AWS SSO token). Re-authenticate (scripts/gt-sso-login); the session was left running.",
		agentLabel, sessionName, hookBead)
	d.logger.Printf("Auth-stuck: %s", msg)

	notify := d.notifyHumanFn
	if notify == nil {
		notify = d.notifyHuman
	}
	notify("high", title, msg)

	escalate := d.escalateFn
	if escalate == nil {
		escalate = d.escalate
	}
	escalate("auth_stuck", msg)
}

// notifyHuman runs <townRoot>/scripts/notify-human <priority> <title> <msg> if
// the script exists and is executable. Best effort: failures are logged only.
func (d *Daemon) notifyHuman(priority, title, msg string) {
	script := filepath.Join(d.config.TownRoot, "scripts", "notify-human")
	info, err := os.Stat(script)
	if err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		d.logger.Printf("Auth-stuck: notify-human not available at %s, skipping notification", script)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), notifyHumanTimeout)
	defer cancel()
	//nolint:gosec // G204: script path is town-local; args are constructed internally
	cmd := exec.CommandContext(ctx, script, priority, title, msg)
	cmd.Dir = d.config.TownRoot
	util.SetDetachedProcessGroup(cmd)
	if output, err := cmd.CombinedOutput(); err != nil {
		d.logger.Printf("Auth-stuck: notify-human failed: %v (output: %s)", err, strings.TrimSpace(string(output)))
	}
}
