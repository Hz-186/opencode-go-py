package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hz-186/opencode-go-py/internal/platform/pathx"
	runtimeprocess "github.com/Hz-186/opencode-go-py/internal/runtime/process"
)

func TestCreateCompensatesVerifiedWorktreeWhenResetFails(t *testing.T) {
	repo := initRepo(t)
	root := filepath.Join(t.TempDir(), "managed")
	runner := RunnerFunc(func(ctx context.Context, spec runtimeprocess.Spec) (runtimeprocess.Result, error) {
		if isGitCommand(spec, "reset", "--hard") {
			return fixtureProcessFailure(spec, 71)
		}
		return runtimeprocess.Run(ctx, spec)
	})
	manager := newTestManager(t, repo, root, pathx.CaseSensitive, runner)

	_, err := manager.Create(context.Background(), CreateInput{Name: "reset failure"})
	if !errors.Is(err, ErrCreateFailed) {
		t.Fatalf("create error = %v, want ErrCreateFailed", err)
	}
	assertWorktreeAbsent(t, repo, filepath.Join(root, "reset-failure"), "opencode/reset-failure")
}

func TestCreateCompensatesVerifiedWorktreeWhenStartCommandFails(t *testing.T) {
	repo := initRepo(t)
	root := filepath.Join(t.TempDir(), "managed")
	manager := newTestManager(t, repo, root, pathx.CaseSensitive, nil)

	_, err := manager.Create(context.Background(), CreateInput{Name: "start failure", StartCommand: "exit 23"})
	if !errors.Is(err, ErrStartCommandFailed) {
		t.Fatalf("create error = %v, want ErrStartCommandFailed", err)
	}
	assertWorktreeAbsent(t, repo, filepath.Join(root, "start-failure"), "opencode/start-failure")
}

func TestStartCommandFailureDoesNotLeakCommandOutput(t *testing.T) {
	repo := initRepo(t)
	root := filepath.Join(t.TempDir(), "managed")
	manager := newTestManager(t, repo, root, pathx.CaseSensitive, nil)
	secret := "worktree-fixture-secret-never-log"

	_, err := manager.Create(context.Background(), CreateInput{
		Name: "secret output", StartCommand: "printf '" + secret + "' >&2; exit 29",
	})
	if !errors.Is(err, ErrStartCommandFailed) {
		t.Fatalf("create error = %v, want ErrStartCommandFailed", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("start command error leaked output: %v", err)
	}
	assertWorktreeAbsent(t, repo, filepath.Join(root, "secret-output"), "opencode/secret-output")
}

func TestCreateCancellationImmediatelyAfterAddStillCompensates(t *testing.T) {
	repo := initRepo(t)
	root := filepath.Join(t.TempDir(), "managed")
	ctx, cancel := context.WithCancel(context.Background())
	runner := RunnerFunc(func(runCtx context.Context, spec runtimeprocess.Spec) (runtimeprocess.Result, error) {
		result, err := runtimeprocess.Run(runCtx, spec)
		if err == nil && isGitCommandPrefix(spec, "worktree", "add") {
			cancel()
		}
		return result, err
	})
	manager := newTestManager(t, repo, root, pathx.CaseSensitive, runner)

	_, err := manager.Create(ctx, CreateInput{Name: "cancel after add"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("create error = %v, want context.Canceled", err)
	}
	assertWorktreeAbsent(t, repo, filepath.Join(root, "cancel-after-add"), "opencode/cancel-after-add")
}

func TestRemoveContinuesWhenGitExitsNonzeroAfterDetaching(t *testing.T) {
	repo := initRepo(t)
	root := filepath.Join(t.TempDir(), "managed")
	manager := newTestManager(t, repo, root, pathx.CaseSensitive, nil)
	info, err := manager.Create(context.Background(), CreateInput{Name: "detach regression"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	manager.runner = RunnerFunc(func(ctx context.Context, spec runtimeprocess.Spec) (runtimeprocess.Result, error) {
		if isGitCommandPrefix(spec, "worktree", "remove", "--force") {
			result, runErr := runtimeprocess.Run(ctx, spec)
			if runErr != nil {
				return result, runErr
			}
			result.ExitCode = 72
			return result, &runtimeprocess.Error{
				Kind: runtimeprocess.ExitFailure, Executable: spec.Argv[0], ExitCode: 72,
				Cause: errors.New("fixture failed after detach"),
			}
		}
		return runtimeprocess.Run(ctx, spec)
	})

	if err := manager.Remove(context.Background(), RemoveInput{Directory: info.Directory, Force: true}); err != nil {
		t.Fatalf("remove after detach error: %v", err)
	}
	assertWorktreeAbsent(t, repo, info.Directory, info.Branch)
}

func TestRemoveCancellationImmediatelyAfterDetachStillFinishesCleanup(t *testing.T) {
	repo := initRepo(t)
	root := filepath.Join(t.TempDir(), "managed")
	manager := newTestManager(t, repo, root, pathx.CaseSensitive, nil)
	info, err := manager.Create(context.Background(), CreateInput{Name: "cancel after detach"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager.runner = RunnerFunc(func(runCtx context.Context, spec runtimeprocess.Spec) (runtimeprocess.Result, error) {
		result, runErr := runtimeprocess.Run(runCtx, spec)
		if runErr == nil && isGitCommandPrefix(spec, "worktree", "remove", "--force") {
			cancel()
		}
		return result, runErr
	})

	if err := manager.Remove(ctx, RemoveInput{Directory: info.Directory, Force: true}); err != nil {
		t.Fatalf("remove after detach cancellation: %v", err)
	}
	assertWorktreeAbsent(t, repo, info.Directory, info.Branch)
}

func TestRemoveStopsWhenFailedGitRemoveRemainsAttached(t *testing.T) {
	repo := initRepo(t)
	root := filepath.Join(t.TempDir(), "managed")
	manager := newTestManager(t, repo, root, pathx.CaseSensitive, nil)
	info, err := manager.Create(context.Background(), CreateInput{Name: "still attached"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	manager.runner = RunnerFunc(func(ctx context.Context, spec runtimeprocess.Spec) (runtimeprocess.Result, error) {
		if isGitCommandPrefix(spec, "worktree", "remove", "--force") {
			return fixtureProcessFailure(spec, 73)
		}
		return runtimeprocess.Run(ctx, spec)
	})

	err = manager.Remove(context.Background(), RemoveInput{Directory: info.Directory, Force: true})
	if !errors.Is(err, ErrRemoveFailed) {
		t.Fatalf("remove error = %v, want ErrRemoveFailed", err)
	}
	if _, statErr := os.Stat(info.Directory); statErr != nil {
		t.Fatalf("attached worktree was changed: %v", statErr)
	}
	if _, refErr := runGit(repo, "show-ref", "--verify", "--quiet", "refs/heads/"+info.Branch); refErr != nil {
		t.Fatalf("attached branch was changed: %v", refErr)
	}

	manager.runner = processRunner{}
	if err := manager.Remove(context.Background(), RemoveInput{Directory: info.Directory, Force: true}); err != nil {
		t.Fatalf("cleanup remove: %v", err)
	}
}

func TestRemoveCanRetryBranchDeletionAfterListRefresh(t *testing.T) {
	repo := initRepo(t)
	root := filepath.Join(t.TempDir(), "managed")
	manager := newTestManager(t, repo, root, pathx.CaseSensitive, nil)
	info, err := manager.Create(context.Background(), CreateInput{Name: "branch retry"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	manager.runner = RunnerFunc(func(ctx context.Context, spec runtimeprocess.Spec) (runtimeprocess.Result, error) {
		if isGitCommand(spec, "branch", "-D", info.Branch) {
			return fixtureProcessFailure(spec, 74)
		}
		return runtimeprocess.Run(ctx, spec)
	})

	err = manager.Remove(context.Background(), RemoveInput{Directory: info.Directory, Force: true})
	if !errors.Is(err, ErrRemoveFailed) {
		t.Fatalf("remove error = %v, want ErrRemoveFailed", err)
	}
	if _, statErr := os.Lstat(info.Directory); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("detached directory stat error = %v, want not exist", statErr)
	}
	list, listErr := manager.List(context.Background())
	if listErr != nil || len(list) != 0 {
		t.Fatalf("list after detach = %+v, error=%v", list, listErr)
	}
	manager.runner = processRunner{}
	if err := manager.Remove(context.Background(), RemoveInput{Directory: info.Directory, Force: true}); err != nil {
		t.Fatalf("retry branch cleanup: %v", err)
	}
	assertWorktreeAbsent(t, repo, info.Directory, info.Branch)
}

func TestResetFailureIsTypedAndKeepsWorktreeAttached(t *testing.T) {
	repo := initRepo(t)
	root := filepath.Join(t.TempDir(), "managed")
	manager := newTestManager(t, repo, root, pathx.CaseSensitive, nil)
	info, err := manager.Create(context.Background(), CreateInput{Name: "reset fault"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	manager.runner = RunnerFunc(func(ctx context.Context, spec runtimeprocess.Spec) (runtimeprocess.Result, error) {
		if isGitCommand(spec, "clean", "-ffdx") {
			return fixtureProcessFailure(spec, 76)
		}
		return runtimeprocess.Run(ctx, spec)
	})

	err = manager.Reset(context.Background(), ResetInput{Directory: info.Directory})
	if !errors.Is(err, ErrResetFailed) {
		t.Fatalf("reset error = %v, want ErrResetFailed", err)
	}
	if _, statErr := os.Stat(info.Directory); statErr != nil {
		t.Fatalf("failed reset removed worktree: %v", statErr)
	}
	manager.runner = processRunner{}
	if err := manager.Remove(context.Background(), RemoveInput{Directory: info.Directory, Force: true}); err != nil {
		t.Fatalf("cleanup remove: %v", err)
	}
}

func TestResetCancellationPreservesDomainAndContextErrors(t *testing.T) {
	repo := initRepo(t)
	root := filepath.Join(t.TempDir(), "managed")
	manager := newTestManager(t, repo, root, pathx.CaseSensitive, nil)
	info, err := manager.Create(context.Background(), CreateInput{Name: "reset cancel"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager.runner = RunnerFunc(func(runCtx context.Context, spec runtimeprocess.Spec) (runtimeprocess.Result, error) {
		result, runErr := runtimeprocess.Run(runCtx, spec)
		if runErr == nil && isGitCommandPrefix(spec, "reset", "--hard") {
			cancel()
		}
		return result, runErr
	})

	err = manager.Reset(ctx, ResetInput{Directory: info.Directory})
	if !errors.Is(err, ErrResetFailed) || !errors.Is(err, context.Canceled) {
		t.Fatalf("reset error = %v, want ErrResetFailed and context.Canceled", err)
	}
	if _, statErr := os.Stat(info.Directory); statErr != nil {
		t.Fatalf("canceled reset removed worktree: %v", statErr)
	}
	manager.runner = processRunner{}
	if err := manager.Remove(context.Background(), RemoveInput{Directory: info.Directory, Force: true}); err != nil {
		t.Fatalf("cleanup remove: %v", err)
	}
}

func TestCreateReportsPrimaryAndCompensationFailuresWithoutUnsafeCleanup(t *testing.T) {
	repo := initRepo(t)
	root := filepath.Join(t.TempDir(), "managed")
	runner := RunnerFunc(func(ctx context.Context, spec runtimeprocess.Spec) (runtimeprocess.Result, error) {
		if isGitCommand(spec, "reset", "--hard") || isGitCommandPrefix(spec, "worktree", "remove", "--force") {
			return fixtureProcessFailure(spec, 75)
		}
		return runtimeprocess.Run(ctx, spec)
	})
	manager := newTestManager(t, repo, root, pathx.CaseSensitive, runner)

	_, err := manager.Create(context.Background(), CreateInput{Name: "double fault"})
	if !errors.Is(err, ErrCreateFailed) || !errors.Is(err, ErrCompensationFailed) {
		t.Fatalf("create error = %v, want joined create and compensation failures", err)
	}
	directory := filepath.Join(root, "double-fault")
	if _, statErr := os.Stat(directory); statErr != nil {
		t.Fatalf("still-attached worktree was deleted: %v", statErr)
	}
	manager.runner = processRunner{}
	if err := manager.Remove(context.Background(), RemoveInput{Directory: directory, Force: true}); err != nil {
		t.Fatalf("cleanup remove: %v", err)
	}
}

func TestRemoveRejectsPrimaryEscapedAndSymlinkSwappedPaths(t *testing.T) {
	repo := initRepo(t)
	container := t.TempDir()
	root := filepath.Join(container, "managed")
	manager := newTestManager(t, repo, root, pathx.CaseSensitive, nil)

	if err := manager.Remove(context.Background(), RemoveInput{Directory: repo, Force: true}); !errors.Is(err, ErrPrimary) {
		t.Fatalf("primary remove error = %v, want ErrPrimary", err)
	}
	escaped := filepath.Join(container, "outside")
	if err := os.MkdirAll(escaped, 0o755); err != nil {
		t.Fatalf("make escaped fixture: %v", err)
	}
	if err := manager.Remove(context.Background(), RemoveInput{Directory: escaped, Force: true}); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("escaped remove error = %v, want ErrNotOwned", err)
	}

	info, err := manager.Create(context.Background(), CreateInput{Name: "symlink swap"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if output, detachErr := runGit(repo, "worktree", "remove", "--force", info.Directory); detachErr != nil {
		t.Fatalf("detach fixture: %v: %s", detachErr, output)
	}
	target := filepath.Join(container, "external target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("make symlink target: %v", err)
	}
	marker := filepath.Join(target, "marker")
	if err := os.WriteFile(marker, []byte("preserve\n"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if err := os.Symlink(target, info.Directory); err != nil {
		t.Fatalf("replace worktree with symlink: %v", err)
	}
	if err := manager.Remove(context.Background(), RemoveInput{Directory: info.Directory, Force: true}); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("symlink-swapped remove error = %v, want ErrNotOwned", err)
	}
	if content, readErr := os.ReadFile(marker); readErr != nil || string(content) != "preserve\n" {
		t.Fatalf("external marker changed: content=%q error=%v", content, readErr)
	}
	_, _ = runGit(repo, "branch", "-D", info.Branch)
}

func TestCaseModeControlsCaseOnlyRemoval(t *testing.T) {
	t.Run("sensitive rejects", func(t *testing.T) {
		repo := initRepo(t)
		manager := newTestManager(t, repo, filepath.Join(t.TempDir(), "managed"), pathx.CaseSensitive, nil)
		info, err := manager.Create(context.Background(), CreateInput{Name: "case fixture"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := manager.Remove(context.Background(), RemoveInput{Directory: strings.ToUpper(info.Directory), Force: true}); !errors.Is(err, ErrNotOwned) {
			t.Fatalf("case-only remove error = %v, want ErrNotOwned", err)
		}
		if err := manager.Remove(context.Background(), RemoveInput{Directory: info.Directory, Force: true}); err != nil {
			t.Fatalf("cleanup remove: %v", err)
		}
	})

	t.Run("insensitive matches", func(t *testing.T) {
		repo := initRepo(t)
		manager := newTestManager(t, repo, filepath.Join(t.TempDir(), "managed"), pathx.CaseInsensitive, nil)
		info, err := manager.Create(context.Background(), CreateInput{Name: "case fixture"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := manager.Remove(context.Background(), RemoveInput{Directory: strings.ToUpper(info.Directory), Force: true}); err != nil {
			t.Fatalf("case-insensitive remove: %v", err)
		}
	})
}

func TestListReverifiesOwnershipAfterManagerRestart(t *testing.T) {
	repo := initRepo(t)
	root := filepath.Join(t.TempDir(), "managed")
	first := newTestManager(t, repo, root, pathx.CaseSensitive, nil)
	created, err := first.Create(context.Background(), CreateInput{Name: "restart fixture"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	second := newTestManager(t, repo, root, pathx.CaseSensitive, nil)
	list, err := second.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Name != created.Name || list[0].Branch != created.Branch {
		t.Fatalf("list = %+v, want verified restart worktree", list)
	}
	if err := second.Remove(context.Background(), RemoveInput{Directory: list[0].Directory, Force: true}); err != nil {
		t.Fatalf("remove after restart: %v", err)
	}
}

func TestCreateGeneratesNamesAndAvoidsExistingCandidate(t *testing.T) {
	repo := initRepo(t)
	root := filepath.Join(t.TempDir(), "managed")
	manager := newTestManager(t, repo, root, pathx.CaseSensitive, nil)

	automatic, err := manager.Create(context.Background(), CreateInput{})
	if err != nil {
		t.Fatalf("create automatic name: %v", err)
	}
	first, err := manager.Create(context.Background(), CreateInput{Name: "same name"})
	if err != nil {
		t.Fatalf("create first named worktree: %v", err)
	}
	second, err := manager.Create(context.Background(), CreateInput{Name: "same name"})
	if err != nil {
		t.Fatalf("create colliding named worktree: %v", err)
	}
	if automatic.Name == "" || first.Name != "same-name" || second.Name == first.Name ||
		!strings.HasPrefix(second.Name, "same-name-") {
		t.Fatalf("generated names = automatic:%q first:%q second:%q", automatic.Name, first.Name, second.Name)
	}
	for _, info := range []Info{automatic, first, second} {
		if err := manager.Remove(context.Background(), RemoveInput{Directory: info.Directory, Force: true}); err != nil {
			t.Fatalf("remove %q: %v", info.Name, err)
		}
	}
}

func TestNewRejectsIncompleteOptionsAndPrimaryRootAlias(t *testing.T) {
	if _, err := New(Options{}); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("empty options error = %v, want ErrInvalidOptions", err)
	}
	repo := initRepo(t)
	if _, err := New(Options{
		GitPath: testGitPath(t), Environment: testEnvironment(t),
		Primary: repo, Root: repo, ProjectID: "fixture-project",
	}); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("primary/root alias error = %v, want ErrInvalidOptions", err)
	}
	for name, root := range map[string]string{
		"root contains primary": filepath.Dir(repo),
		"primary contains root": filepath.Join(repo, "managed"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(Options{
				GitPath: testGitPath(t), Environment: testEnvironment(t),
				Primary: repo, Root: root, ProjectID: "fixture-project",
			}); !errors.Is(err, ErrInvalidOptions) {
				t.Fatalf("overlapping root error = %v, want ErrInvalidOptions", err)
			}
		})
	}
	invalidModeRoot := filepath.Join(t.TempDir(), "must not be created")
	if _, err := New(Options{
		GitPath: testGitPath(t), Environment: testEnvironment(t),
		Primary: repo, Root: invalidModeRoot, ProjectID: "fixture-project", CaseMode: pathx.CaseMode(99),
	}); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("invalid case mode error = %v, want ErrInvalidOptions", err)
	}
	if _, err := os.Stat(invalidModeRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid mode root stat error = %v, want not exist", err)
	}
}

func TestConcurrentCreateAndRemoveAreSerializedWithoutResidue(t *testing.T) {
	repo := initRepo(t)
	root := filepath.Join(t.TempDir(), "managed")
	manager := newTestManager(t, repo, root, pathx.CaseSensitive, nil)

	const workers = 8
	created := make([]Info, workers)
	errorsByWorker := make([]error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			created[index], errorsByWorker[index] = manager.Create(
				context.Background(),
				CreateInput{Name: fmt.Sprintf("worker-%02d", index)},
			)
		}(index)
	}
	wait.Wait()
	for index, err := range errorsByWorker {
		if err != nil {
			t.Fatalf("create worker %d: %v", index, err)
		}
	}
	for index := range created {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			errorsByWorker[index] = manager.Remove(
				context.Background(),
				RemoveInput{Directory: created[index].Directory, Force: true},
			)
		}(index)
	}
	wait.Wait()
	for index, err := range errorsByWorker {
		if err != nil {
			t.Fatalf("remove worker %d: %v", index, err)
		}
	}
	list, err := manager.List(context.Background())
	if err != nil {
		t.Fatalf("final list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("final list = %+v, want empty", list)
	}
}

func TestRepeatedCreateFaultsLeaveNoGitRunnerOrFDResidue(t *testing.T) {
	repo := initRepo(t)
	root := filepath.Join(t.TempDir(), "managed")
	runtime.GC()
	beforeGoroutines := runtime.NumGoroutine()
	beforeFDs := countOpenFDs()
	var active atomic.Int64
	runner := RunnerFunc(func(ctx context.Context, spec runtimeprocess.Spec) (runtimeprocess.Result, error) {
		active.Add(1)
		defer active.Add(-1)
		if isGitCommand(spec, "reset", "--hard") {
			return fixtureProcessFailure(spec, 77)
		}
		return runtimeprocess.Run(ctx, spec)
	})
	manager := newTestManager(t, repo, root, pathx.CaseSensitive, runner)

	for index := 0; index < 12; index++ {
		_, err := manager.Create(context.Background(), CreateInput{Name: fmt.Sprintf("fault-%02d", index)})
		if !errors.Is(err, ErrCreateFailed) {
			t.Fatalf("create fault %d error = %v, want ErrCreateFailed", index, err)
		}
	}
	if got := active.Load(); got != 0 {
		t.Fatalf("active runner calls = %d, want 0", got)
	}
	list, err := runGit(repo, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}
	if strings.Contains(list, root) || strings.Contains(list, filepath.Base(root)+string(filepath.Separator)+"fault-") {
		t.Fatalf("managed worktree residue: %s", list)
	}
	branches, err := runGit(repo, "for-each-ref", "--format=%(refname)", "refs/heads/opencode/")
	if err != nil {
		t.Fatalf("list managed branches: %v", err)
	}
	if branches != "" {
		t.Fatalf("managed branch residue: %s", branches)
	}
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	afterGoroutines := runtime.NumGoroutine()
	if delta := afterGoroutines - beforeGoroutines; delta > 3 {
		t.Fatalf("goroutine delta = %d (before=%d after=%d)", delta, beforeGoroutines, afterGoroutines)
	}
	afterFDs := countOpenFDs()
	if beforeFDs >= 0 && afterFDs > beforeFDs+3 {
		t.Fatalf("open FDs grew from %d to %d", beforeFDs, afterFDs)
	}
}

func TestParseWorktreeListPreservesSpacesAndRejectsMalformedInput(t *testing.T) {
	entries, err := parseWorktreeList("worktree /tmp/managed path\x00HEAD deadbeef\x00branch refs/heads/opencode/space\x00\x00")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(entries) != 1 || entries[0].Directory != "/tmp/managed path" || entries[0].Branch != "opencode/space" {
		t.Fatalf("entries = %+v", entries)
	}
	if _, err := parseWorktreeList("branch refs/heads/opencode/orphan\n"); err == nil {
		t.Fatal("malformed input was accepted")
	}
}

func newTestManager(t *testing.T, repo, root string, mode pathx.CaseMode, runner Runner) *Manager {
	t.Helper()
	manager, err := New(Options{
		GitPath: testGitPath(t), Environment: testEnvironment(t),
		Primary: repo, Root: root, ProjectID: "fixture-project", CaseMode: mode, Runner: runner,
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return manager
}

func isGitCommand(spec runtimeprocess.Spec, arguments ...string) bool {
	if len(spec.Argv) != len(arguments)+1 {
		return false
	}
	for index, argument := range arguments {
		if spec.Argv[index+1] != argument {
			return false
		}
	}
	return true
}

func isGitCommandPrefix(spec runtimeprocess.Spec, arguments ...string) bool {
	if len(spec.Argv) < len(arguments)+1 {
		return false
	}
	for index, argument := range arguments {
		if spec.Argv[index+1] != argument {
			return false
		}
	}
	return true
}

func fixtureProcessFailure(spec runtimeprocess.Spec, exitCode int) (runtimeprocess.Result, error) {
	result := runtimeprocess.Result{Command: append([]string(nil), spec.Argv...), ExitCode: exitCode}
	return result, &runtimeprocess.Error{
		Kind: runtimeprocess.ExitFailure, Executable: spec.Argv[0], ExitCode: exitCode,
		Cause: errors.New("injected fixture failure"),
	}
}

func assertWorktreeAbsent(t *testing.T, repo, directory, branch string) {
	t.Helper()
	if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("directory stat error = %v, want not exist", err)
	}
	list, err := runGit(repo, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}
	if strings.Contains(list, directory) || strings.Contains(list, filepath.Base(directory)) {
		t.Fatalf("worktree remains listed: %s", list)
	}
	if _, err := runGit(repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		t.Fatalf("branch %q still exists", branch)
	}
}

func countOpenFDs() int {
	for _, directory := range []string{"/proc/self/fd", "/dev/fd"} {
		entries, err := os.ReadDir(directory)
		if err == nil {
			return len(entries)
		}
	}
	return -1
}
