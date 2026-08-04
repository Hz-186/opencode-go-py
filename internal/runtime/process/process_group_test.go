//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTimeoutKillsDescendantProcessGroup(t *testing.T) {
	directory := t.TempDir()
	pidFile := filepath.Join(directory, "descendant.pid")
	spec := shellSpec(t, directory, `trap '' TERM; /bin/sh -c 'trap "" TERM; while :; do :; done' & printf '%s' "$!" > "$PID_FILE"; wait`)
	spec.Env = append(spec.Env, "PID_FILE="+pidFile)
	spec.Timeout = 100 * time.Millisecond
	spec.GracePeriod = 20 * time.Millisecond

	result, err := Run(context.Background(), spec)
	assertProcessErrorKind(t, err, Timeout)
	if !result.Forced {
		t.Fatal("process group was not force killed")
	}
	payload, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read descendant pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(payload)))
	if err != nil {
		t.Fatalf("parse descendant pid %q: %v", payload, err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("descendant process %d survived group cleanup", pid)
}
