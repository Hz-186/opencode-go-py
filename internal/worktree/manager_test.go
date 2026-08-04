package worktree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateListAndRemoveManagedWorktree(t *testing.T) {
	repo := initRepo(t)
	root := filepath.Join(t.TempDir(), "managed worktrees")
	manager, err := New(Options{
		GitPath: testGitPath(t), Environment: testEnvironment(t),
		Primary: repo, Root: root, ProjectID: "fixture-project",
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

	info, err := manager.Create(context.Background(), CreateInput{Name: "Feature 中文"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if info.Name != "feature" || info.Branch != "opencode/feature" {
		t.Fatalf("info = %+v, want feature branch", info)
	}
	if info.Directory != filepath.Join(root, "feature") {
		t.Fatalf("directory = %q, want managed root child", info.Directory)
	}
	list, err := manager.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0] != info {
		t.Fatalf("list = %+v, want %+v", list, info)
	}

	if err := os.WriteFile(filepath.Join(info.Directory, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("write dirty fixture: %v", err)
	}
	if err := manager.Remove(context.Background(), RemoveInput{Directory: info.Directory}); !errors.Is(err, ErrDirty) {
		t.Fatalf("dirty remove error = %v, want ErrDirty", err)
	}
	if _, err := os.Stat(info.Directory); err != nil {
		t.Fatalf("dirty worktree was removed: %v", err)
	}
	if err := manager.Remove(context.Background(), RemoveInput{Directory: info.Directory, Force: true}); err != nil {
		t.Fatalf("force remove: %v", err)
	}
	if _, err := os.Stat(info.Directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed directory stat error = %v, want not exist", err)
	}
	if output, err := runGit(repo, "show-ref", "--verify", "--quiet", "refs/heads/opencode/feature"); err == nil {
		t.Fatalf("branch still exists: %s", output)
	}
}

func TestRemoveRejectsUnownedDirectoryWithoutDeletingIt(t *testing.T) {
	t.Parallel()

	repo := initRepo(t)
	root := filepath.Join(t.TempDir(), "managed")
	manager, err := New(Options{
		GitPath: testGitPath(t), Environment: testEnvironment(t),
		Primary: repo, Root: root, ProjectID: "fixture-project",
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	unowned := filepath.Join(root, "unowned")
	if err := os.MkdirAll(unowned, 0o755); err != nil {
		t.Fatalf("make unowned: %v", err)
	}
	if err := manager.Remove(context.Background(), RemoveInput{Directory: unowned, Force: true}); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("remove error = %v, want ErrNotOwned", err)
	}
	if _, err := os.Stat(unowned); err != nil {
		t.Fatalf("unowned directory was changed: %v", err)
	}
}

func TestResetManagedWorktreeToPrimaryRefAndCleanState(t *testing.T) {
	repo := initRepo(t)
	root := filepath.Join(t.TempDir(), "managed")
	manager, err := New(Options{
		GitPath: testGitPath(t), Environment: testEnvironment(t),
		Primary: repo, Root: root, ProjectID: "fixture-project",
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	info, err := manager.Create(context.Background(), CreateInput{Name: "reset fixture"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("primary next\n"), 0o644); err != nil {
		t.Fatalf("update primary: %v", err)
	}
	if _, err := runGit(repo, "add", "README.md"); err != nil {
		t.Fatalf("add primary update: %v", err)
	}
	if output, err := runGit(repo, "commit", "--quiet", "-m", "primary next"); err != nil {
		t.Fatalf("commit primary update: %v: %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(info.Directory, "README.md"), []byte("dirty tracked\n"), 0o644); err != nil {
		t.Fatalf("dirty tracked file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(info.Directory, "untracked.txt"), []byte("dirty untracked\n"), 0o644); err != nil {
		t.Fatalf("dirty untracked file: %v", err)
	}

	if err := manager.Reset(context.Background(), ResetInput{Directory: info.Directory}); err != nil {
		t.Fatalf("reset: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(info.Directory, "README.md"))
	if err != nil || string(content) != "primary next\n" {
		t.Fatalf("README after reset = %q, error=%v", content, err)
	}
	if _, err := os.Stat(filepath.Join(info.Directory, "untracked.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("untracked file stat error = %v, want not exist", err)
	}
	status, err := runGit(info.Directory, "status", "--porcelain=v1")
	if err != nil || status != "" {
		t.Fatalf("status after reset = %q, error=%v", status, err)
	}
	if err := manager.Remove(context.Background(), RemoveInput{Directory: info.Directory, Force: true}); err != nil {
		t.Fatalf("remove: %v", err)
	}
}

func TestResetRejectsPrimaryAndUnownedDirectory(t *testing.T) {
	repo := initRepo(t)
	root := filepath.Join(t.TempDir(), "managed")
	manager, err := New(Options{
		GitPath: testGitPath(t), Environment: testEnvironment(t),
		Primary: repo, Root: root, ProjectID: "fixture-project",
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if err := manager.Reset(context.Background(), ResetInput{Directory: repo}); !errors.Is(err, ErrPrimary) {
		t.Fatalf("primary reset error = %v, want ErrPrimary", err)
	}
	unowned := filepath.Join(root, "unowned")
	if err := os.MkdirAll(unowned, 0o755); err != nil {
		t.Fatalf("make unowned: %v", err)
	}
	if err := manager.Reset(context.Background(), ResetInput{Directory: unowned}); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("unowned reset error = %v, want ErrNotOwned", err)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if _, err := runGit(directory, "init", "--quiet"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	_, _ = runGit(directory, "config", "user.email", "fixture@example.com")
	_, _ = runGit(directory, "config", "user.name", "Fixture")
	if err := os.WriteFile(filepath.Join(directory, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := runGit(directory, "add", "README.md"); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if output, err := runGit(directory, "commit", "--quiet", "-m", "fixture"); err != nil {
		t.Fatalf("git commit: %v: %s", err, output)
	}
	return directory
}

func testGitPath(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	return path
}

func testEnvironment(t *testing.T) []string {
	t.Helper()
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8"}
}

func runGit(directory string, arguments ...string) (string, error) {
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8")
	output, err := command.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}
