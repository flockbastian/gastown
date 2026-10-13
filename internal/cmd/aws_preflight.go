package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// awsPreflightSkipEnv disables the AWS credential preflight entirely when set to "1".
const awsPreflightSkipEnv = "GT_SKIP_AWS_PREFLIGHT"

// awsPreflightTimeout bounds the preflight script. The script enforces its own
// ~10s limit on the aws call; this is a backstop so a wedged script can never
// hang dispatch.
var awsPreflightTimeout = 15 * time.Second

// awsPreflightRunner runs the preflight script and returns its combined output
// and exit code. A non-nil error means the script could not be run to
// completion (exec failure, timeout); exit codes are reported via the int.
// It is a variable so tests can stub it.
var awsPreflightRunner = func(ctx context.Context, script string) (string, int, error) {
	cmd := exec.CommandContext(ctx, script, "--quiet")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	cmd.WaitDelay = 2 * time.Second // don't wait forever on pipes held by grandchildren
	err := cmd.Run()
	if err == nil {
		return buf.String(), 0, nil
	}
	if ctx.Err() != nil {
		return buf.String(), -1, ctx.Err()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return buf.String(), exitErr.ExitCode(), nil
	}
	return buf.String(), -1, err
}

// checkAWSPreflight refuses dispatch when AWS credentials are expired
// (bead hq-41af). It runs <townRoot>/scripts/aws-preflight and blocks ONLY on
// exit code 1 (credentials expired/invalid). Everything else fails open so a
// bug or hiccup in the preflight can never stop the whole town: missing or
// non-executable script, exit 0 (ok / nothing to check), exit 2 (aws CLI
// missing, timeout, network), any other status, exec errors and timeouts.
// Set GT_SKIP_AWS_PREFLIGHT=1 to bypass.
func checkAWSPreflight(townRoot string) error {
	if os.Getenv(awsPreflightSkipEnv) == "1" || townRoot == "" {
		return nil
	}
	script := filepath.Join(townRoot, "scripts", "aws-preflight")
	info, err := os.Stat(script)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), awsPreflightTimeout)
	defer cancel()
	out, code, err := awsPreflightRunner(ctx, script)
	if err != nil || code != 1 {
		return nil
	}

	detail := strings.TrimSpace(out)
	if detail == "" {
		detail = "aws-preflight: AWS credentials expired or invalid"
	}
	return fmt.Errorf("refusing to dispatch: AWS credentials are expired or invalid\n  %s\n\n"+
		"Renew them with: %s\n"+
		"(set %s=1 to bypass this check)",
		strings.ReplaceAll(detail, "\n", "\n  "),
		filepath.Join(townRoot, "scripts", "gt-sso-login"), awsPreflightSkipEnv)
}
