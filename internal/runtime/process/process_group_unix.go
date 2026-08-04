//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package process

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalGraceful(process *os.Process) error {
	return normalizeSignalError(syscall.Kill(-process.Pid, syscall.SIGTERM))
}

func signalForce(process *os.Process) error {
	return normalizeSignalError(syscall.Kill(-process.Pid, syscall.SIGKILL))
}

func normalizeSignalError(err error) error {
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
