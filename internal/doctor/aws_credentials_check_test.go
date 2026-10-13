package doctor

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

type awsStub struct {
	calls int
	out   string
	err   error
	args  []string
}

func (s *awsStub) run(_ context.Context, name string, args ...string) ([]byte, error) {
	s.calls++
	s.args = append([]string{name}, args...)
	return []byte(s.out), s.err
}

func newAWSCheckWithStub(s *awsStub) *AWSCredentialsCheck {
	c := NewAWSCredentialsCheck()
	c.run = s.run
	return c
}

func TestAWSCredentialsCheck_Metadata(t *testing.T) {
	c := NewAWSCredentialsCheck()
	if c.Name() != "aws-credentials" {
		t.Errorf("Name() = %q", c.Name())
	}
	if c.Category() != CategoryInfrastructure {
		t.Errorf("Category() = %q", c.Category())
	}
	if c.CanFix() {
		t.Error("CanFix() should be false")
	}
}

func TestAWSCredentialsCheck_SkipsWhenBedrockUnset(t *testing.T) {
	for _, v := range []string{"", "0", "false"} {
		t.Run("value="+v, func(t *testing.T) {
			t.Setenv("CLAUDE_CODE_USE_BEDROCK", v)
			s := &awsStub{}
			res := newAWSCheckWithStub(s).Run(&CheckContext{TownRoot: t.TempDir()})
			if res.Status != StatusOK {
				t.Errorf("status = %v, want OK", res.Status)
			}
			if s.calls != 0 {
				t.Errorf("aws must not run when Bedrock unused, got %d calls", s.calls)
			}
			if !strings.Contains(res.Message, "skipped") {
				t.Errorf("message should say skipped: %q", res.Message)
			}
		})
	}
}

func TestAWSCredentialsCheck_OK(t *testing.T) {
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	t.Setenv("AWS_PROFILE", "dev-sso")
	s := &awsStub{out: `{"UserId":"X","Account":"123456789012","Arn":"arn:aws:sts::123456789012:assumed-role/Dev/me"}`}
	res := newAWSCheckWithStub(s).Run(&CheckContext{TownRoot: t.TempDir()})
	if res.Status != StatusOK {
		t.Fatalf("status = %v (%s), want OK", res.Status, res.Message)
	}
	if !strings.Contains(res.Message, "dev-sso") {
		t.Errorf("message should include profile: %q", res.Message)
	}
	if strings.Join(s.args, " ") != "aws sts get-caller-identity --output json" {
		t.Errorf("unexpected command: %v", s.args)
	}
	if !strings.Contains(strings.Join(res.Details, "\n"), "123456789012") {
		t.Errorf("details should include account: %v", res.Details)
	}
}

func TestAWSCredentialsCheck_Expired(t *testing.T) {
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	t.Setenv("AWS_PROFILE", "dev-sso")
	s := &awsStub{err: &exec.ExitError{Stderr: []byte("Token has expired and refresh failed\n")}}
	res := newAWSCheckWithStub(s).Run(&CheckContext{TownRoot: t.TempDir()})
	if res.Status != StatusError {
		t.Fatalf("status = %v, want Error", res.Status)
	}
	if !strings.Contains(res.Message, "expired") || !strings.Contains(res.Message, "dev-sso") {
		t.Errorf("message should mention expired + profile: %q", res.Message)
	}
	if !strings.Contains(res.FixHint, "gt-sso-login") {
		t.Errorf("FixHint should mention gt-sso-login: %q", res.FixHint)
	}
	if !strings.Contains(strings.Join(res.Details, "\n"), "Token has expired") {
		t.Errorf("details should carry stderr: %v", res.Details)
	}
}

func TestAWSCredentialsCheck_DefaultProfileLabel(t *testing.T) {
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "true")
	t.Setenv("AWS_PROFILE", "")
	s := &awsStub{out: `{}`}
	res := newAWSCheckWithStub(s).Run(&CheckContext{TownRoot: t.TempDir()})
	if res.Status != StatusOK || !strings.Contains(res.Message, "default") {
		t.Errorf("got %v %q", res.Status, res.Message)
	}
}

func TestAWSCredentialsCheck_AWSCLIMissing(t *testing.T) {
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	s := &awsStub{err: &exec.Error{Name: "aws", Err: exec.ErrNotFound}}
	res := newAWSCheckWithStub(s).Run(&CheckContext{TownRoot: t.TempDir()})
	if res.Status != StatusWarning {
		t.Fatalf("status = %v, want Warning", res.Status)
	}
	if !strings.Contains(res.FixHint, "gt-sso-login") {
		t.Errorf("FixHint: %q", res.FixHint)
	}
}

func TestAWSCredentialsCheck_GenericRunnerError(t *testing.T) {
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	s := &awsStub{err: errors.New("boom")}
	res := newAWSCheckWithStub(s).Run(&CheckContext{TownRoot: t.TempDir()})
	if res.Status != StatusError || len(res.Details) == 0 || res.Details[0] != "boom" {
		t.Errorf("got %v %v", res.Status, res.Details)
	}
}
