package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// awsCommandRunner runs an external command and returns its stdout. On a
// non-zero exit the returned error should carry stderr when available (as
// *exec.ExitError does when produced by exec.Cmd.Output). Injectable so tests
// can stub the aws CLI.
type awsCommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

func defaultAWSCommandRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	//nolint:gosec // G204: fixed command, no user-supplied arguments
	return exec.CommandContext(ctx, name, args...).Output()
}

// awsStsTimeout bounds the sts call so an unreachable endpoint cannot hang doctor.
const awsStsTimeout = 15 * time.Second

// AWSCredentialsCheck verifies that AWS credentials are valid when Claude is
// configured to use Bedrock (CLAUDE_CODE_USE_BEDROCK). Expired SSO tokens leave
// agent sessions alive but unable to make progress, which no other check
// notices. The check is a no-op when Bedrock is not in use.
type AWSCredentialsCheck struct {
	BaseCheck
	run awsCommandRunner
}

// NewAWSCredentialsCheck creates a new AWS credentials check.
func NewAWSCredentialsCheck() *AWSCredentialsCheck {
	return &AWSCredentialsCheck{
		BaseCheck: BaseCheck{
			CheckName:        "aws-credentials",
			CheckDescription: "Check AWS credentials are valid when Claude uses Bedrock",
			CheckCategory:    CategoryInfrastructure,
		},
		run: defaultAWSCommandRunner,
	}
}

// bedrockEnabled reports whether CLAUDE_CODE_USE_BEDROCK is set to a truthy value.
func bedrockEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CLAUDE_CODE_USE_BEDROCK"))) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

const awsLoginHint = "Re-authenticate with scripts/gt-sso-login (or: aws sso login --profile <profile>)"

// Run executes the AWS credentials check.
func (c *AWSCredentialsCheck) Run(ctx *CheckContext) *CheckResult {
	if !bedrockEnabled() {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "Bedrock not in use (CLAUDE_CODE_USE_BEDROCK unset, skipped)",
		}
	}

	profile := os.Getenv("AWS_PROFILE")
	profileLabel := profile
	if profileLabel == "" {
		profileLabel = "default"
	}

	runCtx, cancel := context.WithTimeout(context.Background(), awsStsTimeout)
	defer cancel()
	out, err := c.run(runCtx, "aws", "sts", "get-caller-identity", "--output", "json")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return &CheckResult{
				Name:    c.Name(),
				Status:  StatusWarning,
				Message: fmt.Sprintf("aws CLI not found; cannot verify credentials for profile %s", profileLabel),
				FixHint: "Install the AWS CLI, then " + awsLoginHint,
			}
		}
		var details []string
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if msg := strings.TrimSpace(string(exitErr.Stderr)); msg != "" {
				details = append(details, msg)
			}
		}
		if len(details) == 0 {
			details = append(details, err.Error())
		}
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusError,
			Message: fmt.Sprintf("AWS credentials expired or invalid (profile %s)", profileLabel),
			Details: details,
			FixHint: awsLoginHint,
		}
	}

	var ident struct {
		Account string `json:"Account"`
		Arn     string `json:"Arn"`
	}
	_ = json.Unmarshal(out, &ident) // identity detail is informational only

	result := &CheckResult{
		Name:    c.Name(),
		Status:  StatusOK,
		Message: fmt.Sprintf("AWS credentials OK (profile %s)", profileLabel),
	}
	if ident.Arn != "" {
		result.Details = append(result.Details, "Identity: "+ident.Arn)
	}
	if ident.Account != "" {
		result.Details = append(result.Details, "Account: "+ident.Account)
	}
	return result
}
