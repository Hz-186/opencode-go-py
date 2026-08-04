package project

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	runtimeprocess "github.com/Hz-186/opencode-go-py/internal/runtime/process"
)

func TestResolveNonGitAndEmptyGit(t *testing.T) {
	t.Parallel()

	resolver := testResolver(t)
	plain := t.TempDir()
	result, err := resolver.Resolve(context.Background(), plain)
	if err != nil {
		t.Fatalf("resolve non-git: %v", err)
	}
	if result.ID != GlobalID || result.VCS != nil {
		t.Fatalf("non-git result = %+v, want global without VCS", result)
	}
	if result.Directory != filepath.VolumeName(plain)+string(filepath.Separator) {
		t.Fatalf("non-git directory = %q, want volume root", result.Directory)
	}

	empty := t.TempDir()
	runGit(t, empty, "init", "--quiet")
	result, err = resolver.Resolve(context.Background(), empty)
	if err != nil {
		t.Fatalf("resolve empty git: %v", err)
	}
	if result.ID != GlobalID || result.VCS == nil || result.VCS.Type != "git" {
		t.Fatalf("empty git result = %+v, want global git project", result)
	}
}

func TestResolveUsesRootCommitThenNormalizedRemoteAcrossWorktrees(t *testing.T) {
	repo := initRepo(t)
	resolver := testResolver(t)
	root := strings.TrimSpace(runGit(t, repo, "rev-list", "--max-parents=0", "HEAD"))

	result, err := resolver.Resolve(context.Background(), repo)
	if err != nil {
		t.Fatalf("resolve root commit: %v", err)
	}
	if string(result.ID) != root {
		t.Fatalf("project ID = %q, want root commit %q", result.ID, root)
	}
	if err := resolver.Commit(result); err != nil {
		t.Fatalf("commit project id: %v", err)
	}

	runGit(t, repo, "remote", "add", "origin", "git@GitHub.COM:Org/Repo.git")
	remoteResult, err := resolver.Resolve(context.Background(), repo)
	if err != nil {
		t.Fatalf("resolve remote: %v", err)
	}
	hash := sha1.Sum([]byte("git-remote:github.com/Org/Repo"))
	wantRemote := fmt.Sprintf("%x", hash)
	if string(remoteResult.ID) != wantRemote {
		t.Fatalf("remote project ID = %q, want %q", remoteResult.ID, wantRemote)
	}
	if remoteResult.Previous == nil || *remoteResult.Previous != result.ID {
		t.Fatalf("previous ID = %v, want %q", remoteResult.Previous, result.ID)
	}

	worktree := filepath.Join(t.TempDir(), "Linked 工作区")
	runGit(t, repo, "worktree", "add", "--quiet", "-b", "fixture-worktree", worktree)
	t.Cleanup(func() { _ = exec.Command(testGitPath(t), "-C", repo, "worktree", "remove", "--force", worktree).Run() })
	linked, err := resolver.Resolve(context.Background(), worktree)
	if err != nil {
		t.Fatalf("resolve linked worktree: %v", err)
	}
	if linked.ID != remoteResult.ID || linked.VCS == nil || linked.VCS.Store != remoteResult.VCS.Store {
		t.Fatalf("linked result = %+v, want shared ID/store with %+v", linked, remoteResult)
	}
}

func TestResolveCanonicalizesSymlinkInput(t *testing.T) {
	t.Parallel()

	repo := initRepo(t)
	link := filepath.Join(t.TempDir(), "repo link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatalf("make symlink: %v", err)
	}
	result, err := testResolver(t).Resolve(context.Background(), link)
	if err != nil {
		t.Fatalf("resolve symlink: %v", err)
	}
	real, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("real path: %v", err)
	}
	if result.Directory != real {
		t.Fatalf("directory = %q, want %q", result.Directory, real)
	}
}

func TestResolveDoesNotDowngradeCanceledOptionalGitProbeToGlobal(t *testing.T) {
	t.Parallel()

	repo := initRepo(t)
	fixtureDirectory := t.TempDir()
	shim := filepath.Join(fixtureDirectory, "git-shim")
	marker := filepath.Join(fixtureDirectory, "optional-probe-started")
	script := `#!/bin/sh
case "$1 $2" in
  "rev-parse --show-toplevel") printf '%s\n' "$FIXTURE_WORKTREE" ;;
  "rev-parse --git-common-dir") printf '%s\n' "$FIXTURE_GIT_DIR" ;;
  "config --get") printf started > "$FIXTURE_MARKER"; trap 'exit 0' TERM; while :; do sleep 1; done ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(shim, []byte(script), 0o700); err != nil {
		t.Fatalf("write git shim: %v", err)
	}
	resolver := Resolver{
		GitPath: shim,
		Environment: []string{
			"PATH=" + os.Getenv("PATH"),
			"LANG=en_US.UTF-8",
			"LC_ALL=en_US.UTF-8",
			"FIXTURE_WORKTREE=" + repo,
			"FIXTURE_GIT_DIR=" + filepath.Join(repo, ".git"),
			"FIXTURE_MARKER=" + marker,
		},
	}
	ctx := newControlledContext(context.DeadlineExceeded)
	defer ctx.cancel()
	resolved := make(chan error, 1)
	go func() {
		_, err := resolver.Resolve(ctx, repo)
		resolved <- err
	}()
	markerDeadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat optional remote probe marker: %v", err)
		}
		if time.Now().After(markerDeadline) {
			t.Fatal("optional remote probe did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx.cancel()
	select {
	case err := <-resolved:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("resolve error = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resolve did not return after deadline")
	}
}

func TestResolveDoesNotDowngradeCanceledRootCommitProbeToGlobal(t *testing.T) {
	t.Parallel()

	repo := initRepo(t)
	fixtureDirectory := t.TempDir()
	shim := filepath.Join(fixtureDirectory, "git-shim")
	marker := filepath.Join(fixtureDirectory, "root-probe-started")
	script := `#!/bin/sh
case "$1 $2" in
  "rev-parse --show-toplevel") printf '%s\n' "$FIXTURE_WORKTREE" ;;
  "rev-parse --git-common-dir") printf '%s\n' "$FIXTURE_GIT_DIR" ;;
  "config --get") exit 1 ;;
  "rev-list --max-parents=0") printf started > "$FIXTURE_MARKER"; trap 'exit 0' TERM; while :; do sleep 1; done ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(shim, []byte(script), 0o700); err != nil {
		t.Fatalf("write git shim: %v", err)
	}
	resolver := Resolver{
		GitPath: shim,
		Environment: []string{
			"PATH=" + os.Getenv("PATH"),
			"LANG=en_US.UTF-8",
			"LC_ALL=en_US.UTF-8",
			"FIXTURE_WORKTREE=" + repo,
			"FIXTURE_GIT_DIR=" + filepath.Join(repo, ".git"),
			"FIXTURE_MARKER=" + marker,
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resolved := make(chan error, 1)
	go func() {
		_, err := resolver.Resolve(ctx, repo)
		resolved <- err
	}()
	markerDeadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat optional root commit probe marker: %v", err)
		}
		if time.Now().After(markerDeadline) {
			t.Fatal("optional root commit probe did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-resolved:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("resolve error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resolve did not return after cancellation")
	}
}

func TestGitExitFailurePolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		kind runtimeprocess.ErrorKind
		want bool
	}{
		{name: "exit", kind: runtimeprocess.ExitFailure, want: true},
		{name: "canceled", kind: runtimeprocess.Canceled},
		{name: "timeout", kind: runtimeprocess.Timeout},
		{name: "start", kind: runtimeprocess.StartFailure},
		{name: "wait", kind: runtimeprocess.WaitFailure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := fmt.Errorf("wrapped probe failure: %w", &runtimeprocess.Error{
				Kind:       test.kind,
				Executable: "git",
				ExitCode:   -1,
				Cause:      errors.New("fixture failure"),
			})
			if got := isGitExitFailure(err); got != test.want {
				t.Fatalf("isGitExitFailure() = %v, want %v", got, test.want)
			}
		})
	}
}

type controlledContext struct {
	context.Context
	done chan struct{}
	err  error
	once sync.Once
}

func newControlledContext(err error) *controlledContext {
	return &controlledContext{
		Context: context.Background(),
		done:    make(chan struct{}),
		err:     err,
	}
}

func (c *controlledContext) Done() <-chan struct{} {
	return c.done
}

func (c *controlledContext) Err() error {
	select {
	case <-c.done:
		return c.err
	default:
		return nil
	}
}

func (c *controlledContext) cancel() {
	c.once.Do(func() { close(c.done) })
}

func initRepo(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	runGit(t, directory, "init", "--quiet")
	runGit(t, directory, "config", "user.email", "fixture@example.com")
	runGit(t, directory, "config", "user.name", "Fixture")
	if err := os.WriteFile(filepath.Join(directory, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	runGit(t, directory, "add", "README.md")
	runGit(t, directory, "commit", "--quiet", "-m", "fixture")
	return directory
}

func testResolver(t *testing.T) Resolver {
	t.Helper()
	return Resolver{
		GitPath:     testGitPath(t),
		Environment: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()},
	}
}

func testGitPath(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	return path
}

func runGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command(testGitPath(t), args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}
