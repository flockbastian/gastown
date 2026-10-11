package pidalive

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestAliveSelfAndInvalid(t *testing.T) {
	if !Alive(os.Getpid()) {
		t.Error("own pid should be alive")
	}
	for _, pid := range []int{0, -1, -12345} {
		if Alive(pid) {
			t.Errorf("Alive(%d) = true", pid)
		}
	}
}

func TestProcNil(t *testing.T) {
	if Proc(nil) {
		t.Error("Proc(nil) = true")
	}
}

func TestDeadProcess(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skip("cannot run true")
	}
	if Alive(cmd.Process.Pid) {
		t.Error("reaped process reported alive")
	}
}

// EPERM must count as alive: that is what a sandbox or another user's process looks like.
func TestExistsTreatsEPERMAsAlive(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, true},
		{syscall.EPERM, true},
		{&os.SyscallError{Syscall: "kill", Err: syscall.EPERM}, true},
		{os.ErrProcessDone, false},
		{syscall.ESRCH, false},
		{errors.New("boom"), false},
	}
	for _, c := range cases {
		if got := exists(c.err); got != c.want {
			t.Errorf("exists(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// pid 1 is not ours to signal on a normal system: it exists, and must be reported alive even when we get EPERM.
func TestInitIsAlive(t *testing.T) {
	if !Alive(1) {
		t.Error("pid 1 should be alive")
	}
}
