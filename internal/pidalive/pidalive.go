// Package pidalive answers "does this process exist?" in a way that survives sandboxes.
//
// kill(pid, 0) returns EPERM for a process we exist alongside but may not signal: another user's process, or any
// process outside a sandbox that forbids cross-sandbox signals (e.g. srt). That is still a live process. Treating
// EPERM as "dead" makes callers delete live PID files and respawn running agents, so every liveness probe should
// go through here rather than comparing the Signal(0) error to nil.
package pidalive

import (
	"errors"
	"os"
	"syscall"
)

// Alive reports whether a process with the given PID exists.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid) // always succeeds on Unix
	if err != nil {
		return false
	}
	return Proc(p)
}

// Proc reports whether p refers to a live process.
func Proc(p *os.Process) bool {
	if p == nil {
		return false
	}
	return exists(p.Signal(syscall.Signal(0)))
}

func exists(err error) bool {
	return err == nil || errors.Is(err, syscall.EPERM)
}
