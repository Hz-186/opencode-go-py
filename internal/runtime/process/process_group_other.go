//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package process

import (
	"os"
	"os/exec"
)

func configureProcessGroup(_ *exec.Cmd) {}

func signalGraceful(process *os.Process) error {
	return process.Signal(os.Interrupt)
}

func signalForce(process *os.Process) error {
	return process.Kill()
}
