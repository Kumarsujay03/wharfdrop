//go:build windows

package update

import (
	"os"
	"os/exec"
)

// Restart starts the (updated) executable with the same arguments and exits.
func Restart() error {
	exe, err := Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
