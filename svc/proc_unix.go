//go:build !windows && !plan9 && !js

package svc

import (
	"errors"
	"os"
	"syscall"
)

// pidAlive reports whether pid refers to a live process. Signal 0 performs the
// permission and existence check without delivering anything.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
