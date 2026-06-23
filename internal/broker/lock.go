package broker

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// lockfile is a held single-instance PID lockfile.
type lockfile struct {
	path string
}

// acquireLock takes the broker singleton lockfile at path. If a live process
// already holds it, an error is returned. The current PID is written.
func acquireLock(path string) (*lockfile, error) {
	if data, err := os.ReadFile(path); err == nil {
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(data))); perr == nil && pid > 0 {
			if processAlive(pid) {
				return nil, fmt.Errorf("broker already running (pid %d, lock %s)", pid, path)
			}
		}
		// Stale lock: remove and continue.
		_ = os.Remove(path)
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		return nil, fmt.Errorf("write lockfile %s: %w", path, err)
	}
	return &lockfile{path: path}, nil
}

// release removes the lockfile.
func (l *lockfile) release() {
	if l == nil {
		return
	}
	_ = os.Remove(l.path)
}

// processAlive reports whether a process with the given PID is running.
func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}
