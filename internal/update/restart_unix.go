//go:build !windows

package update

import (
	"os"
	"syscall"
)

// Restart replaces the current process with the (updated) executable,
// keeping the same arguments, environment and PID (so systemd is happy).
func Restart() error {
	exe, err := Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}
