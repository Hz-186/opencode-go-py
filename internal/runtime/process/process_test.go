package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunUsesExplicitArgvEnvAndCWD(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	spec := shellSpec(t, directory, `printf '%s' "$TEST_VALUE"; printf '%s' "$PWD" >&2`)
	spec.Env = []string{"PATH=" + os.Getenv("PATH"), "TEST_VALUE=expected"}

	result, err := Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", result.ExitCode)
	}
	if got := string(result.Stdout); got != "expected" {
		t.Fatalf("stdout = %q, want expected", got)
	}
	realDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatalf("resolve cwd: %v", err)
	}
	if got := string(result.Stderr); got != realDirectory {
		t.Fatalf("stderr = %q, want %q", got, realDirectory)
	}
	if len(result.Command) != len(spec.Argv) {
		t.Fatalf("command = %v, want %v", result.Command, spec.Argv)
	}
	for i := range spec.Argv {
		if result.Command[i] != spec.Argv[i] {
			t.Fatalf("command = %v, want %v", result.Command, spec.Argv)
		}
	}
}

func TestRunBoundsAndMarksStdoutAndStderr(t *testing.T) {
	t.Parallel()

	spec := shellSpec(t, t.TempDir(), `printf '1234567890'; printf 'abcdefghij' >&2`)
	spec.StdoutLimit = 5
	spec.StderrLimit = 4

	result, err := Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := string(result.Stdout); got != "12345" {
		t.Fatalf("stdout = %q, want 12345", got)
	}
	if got := string(result.Stderr); got != "abcd" {
		t.Fatalf("stderr = %q, want abcd", got)
	}
	if !result.StdoutTruncated || !result.StderrTruncated {
		t.Fatalf("truncation flags = stdout:%v stderr:%v, want both true", result.StdoutTruncated, result.StderrTruncated)
	}
}

func TestRunBoundsCombinedOutput(t *testing.T) {
	t.Parallel()

	spec := shellSpec(t, t.TempDir(), `printf 'out'; printf 'err' >&2`)
	spec.CombineOutput = true
	spec.StdoutLimit = 3
	spec.StderrLimit = 2

	result, err := Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Stdout) != 5 {
		t.Fatalf("combined output length = %d, want 5: %q", len(result.Stdout), result.Stdout)
	}
	if len(result.Stderr) != 0 {
		t.Fatalf("stderr = %q, want empty in combined mode", result.Stderr)
	}
	if !result.StdoutTruncated {
		t.Fatal("combined output was not marked truncated")
	}
}

func TestRunReturnsTypedExitFailure(t *testing.T) {
	t.Parallel()

	spec := shellSpec(t, t.TempDir(), `printf 'failure' >&2; exit 7`)
	result, err := Run(context.Background(), spec)
	if err == nil {
		t.Fatal("run unexpectedly succeeded")
	}
	var processErr *Error
	if !errors.As(err, &processErr) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if processErr.Kind != ExitFailure {
		t.Fatalf("error kind = %q, want %q", processErr.Kind, ExitFailure)
	}
	if processErr.ExitCode != 7 || result.ExitCode != 7 {
		t.Fatalf("exit codes = error:%d result:%d, want 7", processErr.ExitCode, result.ExitCode)
	}
	if got := string(result.Stderr); got != "failure" {
		t.Fatalf("stderr = %q, want failure", got)
	}
}

func TestRunTimeoutRequestsGracefulShutdown(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	ready := filepath.Join(directory, "ready")
	spec := shellSpec(t, directory, `trap 'printf term >&2; exit 0' TERM; printf ready > "$READY_FILE"; while :; do :; done`)
	spec.Env = append(spec.Env, "READY_FILE="+ready)
	spec.Timeout = time.Second
	spec.GracePeriod = 300 * time.Millisecond

	result, err := Run(context.Background(), spec)
	assertProcessErrorKind(t, err, Timeout)
	if _, err := os.Stat(ready); err != nil {
		t.Fatalf("cooperative child did not become ready before timeout: %v", err)
	}
	if !strings.Contains(string(result.Stderr), "term") {
		t.Fatalf("stderr = %q, want graceful TERM evidence", result.Stderr)
	}
	if result.Forced {
		t.Fatal("cooperative child was force killed")
	}
}

func TestRunTimeoutEscalatesIgnoredSignal(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	ready := filepath.Join(directory, "ready")
	spec := shellSpec(t, directory, `trap '' TERM; printf ready > "$READY_FILE"; while :; do :; done`)
	spec.Env = append(spec.Env, "READY_FILE="+ready)
	spec.Timeout = time.Second
	spec.GracePeriod = 25 * time.Millisecond

	started := time.Now()
	result, err := Run(context.Background(), spec)
	assertProcessErrorKind(t, err, Timeout)
	if _, err := os.Stat(ready); err != nil {
		t.Fatalf("TERM-ignoring child did not become ready before timeout: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("forced timeout took %s, want <= 3s", elapsed)
	}
	if !result.Forced {
		t.Fatal("TERM-ignoring child was not marked force killed")
	}
}

func TestRunCallerCancellationIsDistinctFromTimeout(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	directory := t.TempDir()
	ready := filepath.Join(directory, "ready")
	spec := shellSpec(t, directory, `trap '' TERM; printf ready > "$READY_FILE"; while :; do :; done`)
	spec.Env = append(spec.Env, "READY_FILE="+ready)
	spec.GracePeriod = 20 * time.Millisecond
	type outcome struct {
		result Result
		err    error
	}
	completed := make(chan outcome, 1)
	go func() {
		result, err := Run(ctx, spec)
		completed <- outcome{result: result, err: err}
	}()

	waitForFile(t, ready)
	cancel()
	select {
	case completed := <-completed:
		assertProcessErrorKind(t, completed.err, Canceled)
		if !completed.result.Forced {
			t.Fatal("canceled TERM-ignoring child was not force killed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled TERM-ignoring child did not exit")
	}
}

func TestChildCloseIsConcurrentAndIdempotent(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	ready := filepath.Join(directory, "ready")
	spec := shellSpec(t, directory, `trap '' TERM; printf ready > "$READY_FILE"; while :; do :; done`)
	spec.Env = append(spec.Env, "READY_FILE="+ready)
	spec.GracePeriod = 20 * time.Millisecond
	child, err := Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if child.PID() <= 0 {
		t.Fatalf("pid = %d, want positive", child.PID())
	}
	waitForFile(t, ready)

	const callers = 32
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			errs <- child.Close(ctx)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("close: %v", err)
		}
	}
	result, err := child.Wait(context.Background())
	assertProcessErrorKind(t, err, Canceled)
	if !result.Forced {
		t.Fatal("closed TERM-ignoring child was not force killed")
	}
}

func TestSpecRequiresExplicitBoundedLaunchInputs(t *testing.T) {
	t.Parallel()

	valid := shellSpec(t, t.TempDir(), `exit 0`)
	tests := map[string]Spec{
		"argv":         withSpec(valid, func(spec *Spec) { spec.Argv = nil }),
		"environment":  withSpec(valid, func(spec *Spec) { spec.Env = nil }),
		"cwd":          withSpec(valid, func(spec *Spec) { spec.CWD = "" }),
		"relative cwd": withSpec(valid, func(spec *Spec) { spec.CWD = "." }),
		"stdout limit": withSpec(valid, func(spec *Spec) { spec.StdoutLimit = 0 }),
		"stderr limit": withSpec(valid, func(spec *Spec) { spec.StderrLimit = -1 }),
		"grace period": withSpec(valid, func(spec *Spec) { spec.GracePeriod = 0 }),
		"timeout":      withSpec(valid, func(spec *Spec) { spec.Timeout = -time.Second }),
	}
	for name, spec := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Run(context.Background(), spec)
			if !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("error = %v, want ErrInvalidSpec", err)
			}
		})
	}
}

func TestOneThousandChildRunsDoNotLeakRuntimeResources(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping child process stress test in short mode")
	}

	directory := t.TempDir()
	spec := Spec{
		Argv:          []string{"/usr/bin/true"},
		Env:           []string{},
		CWD:           directory,
		StdoutLimit:   1,
		StderrLimit:   1,
		GracePeriod:   50 * time.Millisecond,
		CombineOutput: false,
	}
	runtime.GC()
	beforeGoroutines := runtime.NumGoroutine()
	beforeFDs := openFDCount()

	for i := 0; i < 1_000; i++ {
		result, err := Run(context.Background(), spec)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if result.ExitCode != 0 {
			t.Fatalf("run %d exit code = %d, want 0", i, result.ExitCode)
		}
	}

	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	afterGoroutines := runtime.NumGoroutine()
	afterFDs := openFDCount()
	if delta := afterGoroutines - beforeGoroutines; delta > 6 {
		t.Fatalf("goroutine delta = %d (before=%d after=%d)", delta, beforeGoroutines, afterGoroutines)
	}
	if beforeFDs >= 0 && afterFDs-beforeFDs > 3 {
		t.Fatalf("fd delta = %d (before=%d after=%d)", afterFDs-beforeFDs, beforeFDs, afterFDs)
	}
}

func shellSpec(t *testing.T, directory, script string) Spec {
	t.Helper()
	return Spec{
		Argv:        []string{"/bin/sh", "-c", script},
		Env:         []string{"PATH=" + os.Getenv("PATH")},
		CWD:         directory,
		StdoutLimit: 64 * 1024,
		StderrLimit: 64 * 1024,
		GracePeriod: 100 * time.Millisecond,
	}
}

func withSpec(spec Spec, mutate func(*Spec)) Spec {
	mutate(&spec)
	return spec
}

func assertProcessErrorKind(t *testing.T, err error, want ErrorKind) {
	t.Helper()
	if err == nil {
		t.Fatalf("process unexpectedly succeeded; want %q", want)
	}
	var processErr *Error
	if !errors.As(err, &processErr) {
		t.Fatalf("error type = %T, want *Error: %v", err, err)
	}
	if processErr.Kind != want {
		t.Fatalf("error kind = %q, want %q: %v", processErr.Kind, want, err)
	}
}

func openFDCount() int {
	for _, directory := range []string{"/proc/self/fd", "/dev/fd"} {
		entries, err := os.ReadDir(directory)
		if err == nil {
			return len(entries)
		}
	}
	return -1
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("child readiness file %q was not created", path)
}
