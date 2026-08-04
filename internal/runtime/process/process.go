// Package process starts and cleans up bounded child processes.
package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const pipeDrainAllowance = 50 * time.Millisecond

// ErrInvalidSpec reports a launch contract that is implicit or unbounded.
var ErrInvalidSpec = errors.New("invalid process launch spec")

// ErrorKind classifies a process failure without parsing error text.
type ErrorKind string

const (
	StartFailure ErrorKind = "start_failure"
	ExitFailure  ErrorKind = "exit_failure"
	Canceled     ErrorKind = "canceled"
	Timeout      ErrorKind = "timeout"
	WaitFailure  ErrorKind = "wait_failure"
)

// Error is a typed process failure. It intentionally excludes environment and
// argument values so logging the error cannot expose those inputs.
type Error struct {
	Kind       ErrorKind
	Executable string
	ExitCode   int
	Cause      error
}

// Error returns a safe operational description.
func (e *Error) Error() string {
	if e.ExitCode >= 0 {
		return fmt.Sprintf("process %s for executable %q (exit %d): %v", e.Kind, e.Executable, e.ExitCode, e.Cause)
	}
	return fmt.Sprintf("process %s for executable %q: %v", e.Kind, e.Executable, e.Cause)
}

// Unwrap returns the underlying OS or context failure.
func (e *Error) Unwrap() error {
	return e.Cause
}

// Spec is an explicit, bounded child launch contract.
type Spec struct {
	Argv          []string
	Env           []string
	CWD           string
	Stdin         io.Reader
	StdoutLimit   int64
	StderrLimit   int64
	Timeout       time.Duration
	GracePeriod   time.Duration
	CombineOutput bool
}

// Result is the complete bounded outcome of one child process.
type Result struct {
	Command         []string
	ExitCode        int
	Stdout          []byte
	Stderr          []byte
	StdoutTruncated bool
	StderrTruncated bool
	Forced          bool
	StartedAt       time.Time
	Duration        time.Duration
}

// Child is one running or completed managed process.
type Child struct {
	command    []string
	executable string
	cancel     context.CancelFunc
	term       *termination
	done       chan struct{}
	pid        int

	mu      sync.Mutex
	result  Result
	waitErr error
}

// Run starts a child and waits for its bounded cleanup even after cancellation.
func Run(ctx context.Context, spec Spec) (Result, error) {
	child, err := Start(ctx, spec)
	if err != nil {
		return Result{Command: append([]string(nil), spec.Argv...), ExitCode: -1}, err
	}
	return child.Wait(context.Background())
}

// Start launches a child process and attaches timeout/cancellation cleanup.
func Start(parent context.Context, spec Spec) (*Child, error) {
	if err := validateSpec(spec); err != nil {
		return nil, err
	}

	spec.Argv = append([]string(nil), spec.Argv...)
	spec.Env = append([]string(nil), spec.Env...)
	runCtx, cancel := processContext(parent, spec.Timeout)
	command := exec.CommandContext(runCtx, spec.Argv[0], spec.Argv[1:]...)
	command.Env = spec.Env
	command.Dir = spec.CWD
	command.Stdin = spec.Stdin
	configureProcessGroup(command)

	stdout := newLimitedBuffer(spec.StdoutLimit)
	stderr := newLimitedBuffer(spec.StderrLimit)
	if spec.CombineOutput {
		combined := newLimitedBuffer(spec.StdoutLimit + spec.StderrLimit)
		stdout = combined
		stderr = nil
		command.Stdout = combined
		command.Stderr = combined
	} else {
		command.Stdout = stdout
		command.Stderr = stderr
	}

	finished := make(chan struct{})
	term := &termination{
		process:     func() *os.Process { return command.Process },
		gracePeriod: spec.GracePeriod,
		finished:    finished,
	}
	command.Cancel = term.requestGraceful
	command.WaitDelay = spec.GracePeriod + pipeDrainAllowance

	startedAt := time.Now()
	if err := command.Start(); err != nil {
		kind, cause := classifyContext(runCtx, err)
		cancel()
		return nil, &Error{
			Kind:       kind,
			Executable: spec.Argv[0],
			ExitCode:   -1,
			Cause:      cause,
		}
	}

	child := &Child{
		command:    spec.Argv,
		executable: spec.Argv[0],
		cancel:     cancel,
		term:       term,
		done:       make(chan struct{}),
		pid:        command.Process.Pid,
	}
	go child.collect(command, runCtx, stdout, stderr, spec.CombineOutput, startedAt, finished)
	return child, nil
}

// PID returns the operating-system process identifier.
func (c *Child) PID() int {
	return c.pid
}

// Wait waits for completion without changing the process lifetime.
func (c *Child) Wait(ctx context.Context) (Result, error) {
	select {
	case <-c.done:
		c.mu.Lock()
		defer c.mu.Unlock()
		return cloneResult(c.result), c.waitErr
	case <-ctx.Done():
		return Result{Command: append([]string(nil), c.command...), ExitCode: -1}, ctx.Err()
	}
}

// Close requests graceful shutdown and waits for cleanup. If ctx expires first,
// Close escalates immediately and returns the waiting-context error unless the
// child completes in the same observation.
func (c *Child) Close(ctx context.Context) error {
	select {
	case <-c.done:
		return nil
	default:
	}

	c.cancel()
	_ = c.term.requestGraceful()
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		_ = c.term.force()
		select {
		case <-c.done:
			return nil
		default:
			return ctx.Err()
		}
	}
}

func (c *Child) collect(
	command *exec.Cmd,
	runCtx context.Context,
	stdout *limitedBuffer,
	stderr *limitedBuffer,
	combined bool,
	startedAt time.Time,
	finished chan struct{},
) {
	waitErr := command.Wait()
	contextErr := runCtx.Err()
	close(finished)
	c.cancel()

	exitCode := -1
	if command.ProcessState != nil {
		exitCode = command.ProcessState.ExitCode()
	}
	stdoutBytes, stdoutTruncated := stdout.snapshot()
	var stderrBytes []byte
	var stderrTruncated bool
	if !combined {
		stderrBytes, stderrTruncated = stderr.snapshot()
	}
	result := Result{
		Command:         append([]string(nil), c.command...),
		ExitCode:        exitCode,
		Stdout:          stdoutBytes,
		Stderr:          stderrBytes,
		StdoutTruncated: stdoutTruncated,
		StderrTruncated: stderrTruncated,
		Forced:          c.term.forced.Load(),
		StartedAt:       startedAt,
		Duration:        time.Since(startedAt),
	}
	classified := classifyWait(c.executable, exitCode, contextErr, waitErr)

	c.mu.Lock()
	c.result = result
	c.waitErr = classified
	c.mu.Unlock()
	close(c.done)
}

func validateSpec(spec Spec) error {
	switch {
	case len(spec.Argv) == 0 || spec.Argv[0] == "":
		return fmt.Errorf("%w: argv must include an executable", ErrInvalidSpec)
	case spec.Env == nil:
		return fmt.Errorf("%w: env must be explicit (an empty slice is allowed)", ErrInvalidSpec)
	case spec.CWD == "":
		return fmt.Errorf("%w: cwd must be explicit", ErrInvalidSpec)
	case !filepath.IsAbs(spec.CWD):
		return fmt.Errorf("%w: cwd must be absolute", ErrInvalidSpec)
	case spec.StdoutLimit <= 0:
		return fmt.Errorf("%w: stdout limit must be positive", ErrInvalidSpec)
	case spec.StderrLimit <= 0:
		return fmt.Errorf("%w: stderr limit must be positive", ErrInvalidSpec)
	case spec.StdoutLimit > math.MaxInt64-spec.StderrLimit:
		return fmt.Errorf("%w: combined output limit overflows", ErrInvalidSpec)
	case spec.GracePeriod <= 0:
		return fmt.Errorf("%w: grace period must be positive", ErrInvalidSpec)
	case spec.Timeout < 0:
		return fmt.Errorf("%w: timeout cannot be negative", ErrInvalidSpec)
	}
	for _, argument := range spec.Argv {
		if strings.IndexByte(argument, 0) >= 0 {
			return fmt.Errorf("%w: argv contains NUL", ErrInvalidSpec)
		}
	}
	for _, variable := range spec.Env {
		name, _, ok := strings.Cut(variable, "=")
		if !ok || name == "" || strings.IndexByte(variable, 0) >= 0 {
			return fmt.Errorf("%w: env contains an invalid entry", ErrInvalidSpec)
		}
	}
	return nil
}

func processContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout > 0 {
		return context.WithTimeout(parent, timeout)
	}
	return context.WithCancel(parent)
}

func classifyContext(ctx context.Context, fallback error) (ErrorKind, error) {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return Timeout, context.DeadlineExceeded
	case errors.Is(ctx.Err(), context.Canceled):
		return Canceled, context.Canceled
	default:
		return StartFailure, fallback
	}
}

func classifyWait(executable string, exitCode int, contextErr, waitErr error) error {
	if contextErr != nil {
		kind, cause := classifyContextError(contextErr)
		return &Error{Kind: kind, Executable: executable, ExitCode: exitCode, Cause: cause}
	}
	if waitErr == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return &Error{Kind: ExitFailure, Executable: executable, ExitCode: exitCode, Cause: waitErr}
	}
	return &Error{Kind: WaitFailure, Executable: executable, ExitCode: exitCode, Cause: waitErr}
}

func classifyContextError(err error) (ErrorKind, error) {
	if errors.Is(err, context.DeadlineExceeded) {
		return Timeout, context.DeadlineExceeded
	}
	return Canceled, context.Canceled
}

func cloneResult(result Result) Result {
	result.Command = append([]string(nil), result.Command...)
	result.Stdout = append([]byte(nil), result.Stdout...)
	result.Stderr = append([]byte(nil), result.Stderr...)
	return result
}

type limitedBuffer struct {
	mu        sync.Mutex
	limit     int64
	data      []byte
	truncated bool
}

func newLimitedBuffer(limit int64) *limitedBuffer {
	return &limitedBuffer{limit: limit}
}

func (b *limitedBuffer) Write(payload []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	remaining := b.limit - int64(len(b.data))
	if remaining > 0 {
		keep := int64(len(payload))
		if keep > remaining {
			keep = remaining
		}
		b.data = append(b.data, payload[:int(keep)]...)
	}
	if int64(len(payload)) > remaining {
		b.truncated = true
	}
	return len(payload), nil
}

func (b *limitedBuffer) snapshot() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.data...), b.truncated
}

type termination struct {
	process     func() *os.Process
	gracePeriod time.Duration
	finished    <-chan struct{}
	graceOnce   sync.Once
	forceOnce   sync.Once
	forced      atomic.Bool
}

func (t *termination) requestGraceful() error {
	var signalErr error
	t.graceOnce.Do(func() {
		process := t.process()
		if process == nil {
			signalErr = os.ErrProcessDone
			return
		}
		signalErr = signalGraceful(process)
		if errors.Is(signalErr, os.ErrProcessDone) {
			return
		}
		go func() {
			timer := time.NewTimer(t.gracePeriod)
			defer timer.Stop()
			select {
			case <-t.finished:
			case <-timer.C:
				_ = t.force()
			}
		}()
	})
	return signalErr
}

func (t *termination) force() error {
	var signalErr error
	t.forceOnce.Do(func() {
		select {
		case <-t.finished:
			return
		default:
		}
		process := t.process()
		if process == nil {
			signalErr = os.ErrProcessDone
			return
		}
		t.forced.Store(true)
		signalErr = signalForce(process)
	})
	return signalErr
}
