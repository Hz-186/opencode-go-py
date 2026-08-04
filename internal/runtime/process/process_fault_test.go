package process

import (
	"context"
	"errors"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestStartFailureIsTypedWithoutArgumentOrEnvironmentLeak(t *testing.T) {
	t.Parallel()

	secret := "fixture-secret-never-log"
	spec := Spec{
		Argv:        []string{"/definitely/missing/opencode-fixture", secret},
		Env:         []string{"API_KEY=" + secret},
		CWD:         t.TempDir(),
		StdoutLimit: 1,
		StderrLimit: 1,
		GracePeriod: 10 * time.Millisecond,
	}
	_, err := Run(context.Background(), spec)
	assertProcessErrorKind(t, err, StartFailure)
	if errors.Is(err, context.Canceled) {
		t.Fatalf("start failure was misclassified as cancellation: %v", err)
	}
	if got := err.Error(); containsAny(got, secret, "API_KEY") {
		t.Fatalf("start error leaked argument or environment: %s", got)
	}
}

func TestWaitContextDoesNotCancelChild(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	ready := filepath.Join(directory, "ready")
	spec := shellSpec(t, directory, `trap 'exit 0' TERM; printf ready > "$READY_FILE"; while :; do :; done`)
	spec.Env = append(spec.Env, "READY_FILE="+ready)
	child, err := Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitForFile(t, ready)

	waitCtx, cancelWait := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancelWait()
	if _, err := child.Wait(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait error = %v, want context.DeadlineExceeded", err)
	}
	if !processExists(child.PID()) {
		t.Fatal("wait-context cancellation terminated the child")
	}
	closeCtx, cancelClose := context.WithTimeout(context.Background(), time.Second)
	defer cancelClose()
	if err := child.Close(closeCtx); err != nil {
		t.Fatalf("close child: %v", err)
	}
}

func TestOutputAfterCancellationRemainsBounded(t *testing.T) {
	t.Parallel()

	spec := shellSpec(t, t.TempDir(), `trap 'printf 0123456789 >&2; exit 0' TERM; while :; do :; done`)
	spec.Timeout = 50 * time.Millisecond
	spec.GracePeriod = 200 * time.Millisecond
	spec.StderrLimit = 4

	result, err := Run(context.Background(), spec)
	assertProcessErrorKind(t, err, Timeout)
	if got := string(result.Stderr); got != "0123" {
		t.Fatalf("stderr = %q, want 0123", got)
	}
	if !result.StderrTruncated {
		t.Fatal("post-cancellation stderr was not marked truncated")
	}
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if candidate != "" && len(value) >= len(candidate) {
			for i := 0; i+len(candidate) <= len(value); i++ {
				if value[i:i+len(candidate)] == candidate {
					return true
				}
			}
		}
	}
	return false
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
