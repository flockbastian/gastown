//go:build !windows

package pidalive

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// Children in a different process group or session are the closest we can get to "a process we may not be able to
// signal" without a sandbox: they must still be reported alive while running, and dead once reaped.
func TestChildProcessGroups(t *testing.T) {
	cases := []struct {
		name string
		attr *syscall.SysProcAttr
	}{
		{"same process group", nil},
		{"own process group", &syscall.SysProcAttr{Setpgid: true}},
		{"own session", &syscall.SysProcAttr{Setsid: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := exec.Command("sleep", "30")
			cmd.SysProcAttr = c.attr
			if err := cmd.Start(); err != nil {
				t.Skipf("cannot start sleep: %v", err)
			}
			pid := cmd.Process.Pid
			t.Cleanup(func() {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			})

			if c.attr != nil && c.attr.Setpgid {
				if pgid, err := syscall.Getpgid(pid); err != nil || pgid == syscall.Getpgrp() {
					t.Fatalf("Getpgid(%d) = %d, %v; want a group other than ours", pid, pgid, err)
				}
			}
			if !Alive(pid) {
				t.Errorf("Alive(%d) = false for running child", pid)
			}
			if !Proc(cmd.Process) {
				t.Errorf("Proc(child %d) = false for running child", pid)
			}

			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			if Alive(pid) {
				t.Errorf("Alive(%d) = true after child was reaped", pid)
			}
			if Proc(cmd.Process) {
				t.Errorf("Proc(child %d) = true after child was reaped", pid)
			}
		})
	}
}

// Proc must report false for a handle obtained via os.FindProcess when the pid no longer exists.
func TestProcFindProcessGonePID(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skip("cannot run true")
	}
	p, err := os.FindProcess(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("FindProcess: %v", err)
	}
	if Proc(p) {
		t.Errorf("Proc(FindProcess(%d)) = true for a pid that no longer exists", p.Pid)
	}
}
