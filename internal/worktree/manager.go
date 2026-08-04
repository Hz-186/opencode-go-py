// Package worktree manages isolated Git worktrees inside a verified root.
package worktree

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Hz-186/opencode-go-py/internal/platform/pathx"
	runtimeprocess "github.com/Hz-186/opencode-go-py/internal/runtime/process"
)

const (
	branchPrefix       = "opencode/"
	gitOutputLimit     = 256 * 1024
	gitGracePeriod     = 250 * time.Millisecond
	compensationWindow = 10 * time.Second
	maxNameAttempts    = 26
)

var (
	ErrInvalidOptions     = errors.New("invalid worktree manager options")
	ErrInvalidName        = errors.New("invalid worktree name")
	ErrConflict           = errors.New("worktree name or branch conflicts")
	ErrNotOwned           = errors.New("worktree is not owned by this manager")
	ErrPrimary            = errors.New("primary worktree cannot be changed")
	ErrDirty              = errors.New("worktree has local changes")
	ErrCreateFailed       = errors.New("worktree creation failed")
	ErrListFailed         = errors.New("worktree listing failed")
	ErrRemoveFailed       = errors.New("worktree removal failed")
	ErrResetFailed        = errors.New("worktree reset failed")
	ErrStartCommandFailed = errors.New("worktree start command failed")
	ErrCompensationFailed = errors.New("worktree compensation failed")
)

// Info is the stable public identity of a managed worktree.
type Info struct {
	Name      string
	Branch    string
	Directory string
}

// CreateInput describes one isolated worktree creation.
type CreateInput struct {
	Name         string
	StartCommand string
}

// RemoveInput describes one worktree removal. Dirty worktrees require Force.
type RemoveInput struct {
	Directory string
	Force     bool
}

// ResetInput identifies a managed worktree to restore to the primary ref.
type ResetInput struct {
	Directory string
}

// Runner launches explicit bounded child-process specs. It is injectable so
// failure and compensation paths can be tested without process-global hooks.
type Runner interface {
	Run(context.Context, runtimeprocess.Spec) (runtimeprocess.Result, error)
}

// RunnerFunc adapts a function to Runner.
type RunnerFunc func(context.Context, runtimeprocess.Spec) (runtimeprocess.Result, error)

// Run implements Runner.
func (f RunnerFunc) Run(ctx context.Context, spec runtimeprocess.Spec) (runtimeprocess.Result, error) {
	return f(ctx, spec)
}

// Options are explicit manager dependencies and path-identity rules.
type Options struct {
	GitPath     string
	Environment []string
	Primary     string
	Root        string
	ProjectID   string
	CaseMode    pathx.CaseMode
	Runner      Runner
	ShellPath   string
}

// Manager serializes Git mutations and remembers only verified ownership.
type Manager struct {
	gitPath     string
	environment []string
	primary     string
	root        string
	rootDisplay string
	caseMode    pathx.CaseMode
	runner      Runner
	shellPath   string

	mu    sync.Mutex
	owned map[string]Info
}

type processRunner struct{}

func (processRunner) Run(ctx context.Context, spec runtimeprocess.Spec) (runtimeprocess.Result, error) {
	return runtimeprocess.Run(ctx, spec)
}

type gitEntry struct {
	Directory string
	Branch    string
}

// New validates and canonicalizes the primary worktree and managed root.
func New(options Options) (*Manager, error) {
	if strings.TrimSpace(options.GitPath) == "" || options.Environment == nil ||
		strings.TrimSpace(options.Primary) == "" || strings.TrimSpace(options.Root) == "" ||
		strings.TrimSpace(options.ProjectID) == "" {
		return nil, fmt.Errorf("%w: git path, environment, primary, root, and project ID are required", ErrInvalidOptions)
	}
	if options.CaseMode != pathx.CaseSensitive && options.CaseMode != pathx.CaseInsensitive {
		return nil, fmt.Errorf("%w: unsupported path case mode", ErrInvalidOptions)
	}
	primary, err := pathx.Canonical(options.Primary, options.CaseMode)
	if err != nil {
		return nil, fmt.Errorf("%w: canonicalize primary: %w", ErrInvalidOptions, err)
	}
	rootDisplay, err := filepath.Abs(options.Root)
	if err != nil {
		return nil, fmt.Errorf("%w: make managed root absolute: %w", ErrInvalidOptions, err)
	}
	rootDisplay = filepath.Clean(rootDisplay)
	if err := os.MkdirAll(rootDisplay, 0o700); err != nil {
		return nil, fmt.Errorf("%w: create managed root: %w", ErrInvalidOptions, err)
	}
	root, err := pathx.Canonical(rootDisplay, options.CaseMode)
	if err != nil {
		return nil, fmt.Errorf("%w: canonicalize managed root: %w", ErrInvalidOptions, err)
	}
	if pathx.Contains(root, primary, options.CaseMode) || pathx.Contains(primary, root, options.CaseMode) {
		return nil, fmt.Errorf("%w: managed root and primary worktree must be disjoint", ErrInvalidOptions)
	}
	runner := options.Runner
	if runner == nil {
		runner = processRunner{}
	}
	shellPath := strings.TrimSpace(options.ShellPath)
	if shellPath == "" {
		shellPath = "/bin/sh"
	}
	return &Manager{
		gitPath:     options.GitPath,
		environment: append([]string(nil), options.Environment...),
		primary:     primary,
		root:        root,
		rootDisplay: rootDisplay,
		caseMode:    options.CaseMode,
		runner:      runner,
		shellPath:   shellPath,
		owned:       make(map[string]Info),
	}, nil
}

// Create creates, verifies, populates, and optionally boots one worktree.
func (m *Manager) Create(ctx context.Context, input CreateInput) (Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}

	info, err := m.candidate(ctx, input.Name)
	if err != nil {
		return Info{}, err
	}
	created, err := m.git(ctx, m.primary, "worktree", "add", "--no-checkout", "-b", info.Branch, info.Directory)
	if err != nil {
		return Info{}, fmt.Errorf("%w: git add: %w", ErrCreateFailed, safeProcessCause(created, err))
	}

	verificationCtx, cancelVerification := context.WithTimeout(context.Background(), compensationWindow)
	entries, err := m.entries(verificationCtx)
	cancelVerification()
	if err != nil {
		return Info{}, fmt.Errorf("%w: verify created path: %w", ErrCreateFailed, err)
	}
	key, err := m.pathKey(info.Directory)
	if err != nil {
		return Info{}, fmt.Errorf("%w: canonicalize created path: %w", ErrCreateFailed, err)
	}
	entry, ok := entries[key]
	if !ok || entry.Branch != info.Branch || !m.isManagedKey(key) {
		return Info{}, fmt.Errorf("%w: git did not report the expected managed worktree", ErrCreateFailed)
	}
	m.owned[key] = info
	if err := ctx.Err(); err != nil {
		return Info{}, m.compensateCreate(info, key, err)
	}

	if result, resetErr := m.git(ctx, info.Directory, "reset", "--hard"); resetErr != nil {
		primary := fmt.Errorf("%w: populate worktree: %w", ErrCreateFailed, safeProcessCause(result, resetErr))
		return Info{}, m.compensateCreate(info, key, primary)
	}
	if command := strings.TrimSpace(input.StartCommand); command != "" {
		result, commandErr := m.runner.Run(ctx, runtimeprocess.Spec{
			Argv:        []string{m.shellPath, "-lc", command},
			Env:         append([]string(nil), m.environment...),
			CWD:         info.Directory,
			StdoutLimit: gitOutputLimit,
			StderrLimit: gitOutputLimit,
			GracePeriod: gitGracePeriod,
		})
		if commandErr != nil {
			primary := fmt.Errorf("%w: %w", ErrStartCommandFailed, safeProcessCause(result, commandErr))
			return Info{}, m.compensateCreate(info, key, primary)
		}
	}
	return info, nil
}

// List returns only worktrees whose path and branch prove managed ownership.
func (m *Manager) List(ctx context.Context) ([]Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entries, err := m.entries(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Info, 0, len(entries))
	for key, entry := range entries {
		info, ok := m.managedInfo(key, entry)
		if !ok {
			continue
		}
		m.owned[key] = info
		result = append(result, info)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Directory < result[j].Directory
	})
	return result, nil
}

// Remove removes a verified managed worktree. It never recursively removes an
// unowned, primary, escaped, or still-attached path.
func (m *Manager) Remove(ctx context.Context, input RemoveInput) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := m.pathKey(input.Directory)
	if err != nil {
		return fmt.Errorf("%w: canonicalize requested path: %w", ErrNotOwned, err)
	}
	if key == m.primary {
		return ErrPrimary
	}

	entries, err := m.entries(ctx)
	if err != nil {
		return err
	}
	info, owned := m.owned[key]
	if entry, exists := entries[key]; exists {
		verified, managed := m.managedInfo(key, entry)
		if !managed {
			return ErrNotOwned
		}
		info, owned = verified, true
		m.owned[key] = verified
	}
	if !owned || !m.isManagedKey(key) {
		return ErrNotOwned
	}

	entry, attached := entries[key]
	cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), compensationWindow)
	defer cancelCleanup()
	if attached {
		if !input.Force {
			status, statusErr := m.git(ctx, entry.Directory, "-c", "core.fsmonitor=false", "status", "--porcelain=v1", "--untracked-files=all")
			if statusErr != nil {
				return fmt.Errorf("%w: inspect dirty state: %w", ErrRemoveFailed, safeProcessCause(status, statusErr))
			}
			if len(bytesTrimSpace(status.Stdout)) != 0 {
				return ErrDirty
			}
		}
		arguments := []string{"worktree", "remove"}
		if input.Force {
			arguments = append(arguments, "--force")
		}
		arguments = append(arguments, entry.Directory)
		removed, removeErr := m.git(ctx, m.primary, arguments...)
		next, listErr := m.entries(cleanupCtx)
		if listErr != nil {
			if removeErr != nil {
				return errors.Join(
					fmt.Errorf("%w: git remove: %w", ErrRemoveFailed, safeProcessCause(removed, removeErr)),
					fmt.Errorf("%w: verify remove: %w", ErrRemoveFailed, listErr),
				)
			}
			return fmt.Errorf("%w: verify remove: %w", ErrRemoveFailed, listErr)
		}
		if _, stillAttached := next[key]; stillAttached {
			if removeErr != nil {
				return fmt.Errorf("%w: git remove: %w", ErrRemoveFailed, safeProcessCause(removed, removeErr))
			}
			return fmt.Errorf("%w: git reported success but worktree remains attached", ErrRemoveFailed)
		}
	}

	if err := m.removeDirectory(info, key); err != nil {
		return err
	}
	if err := m.deleteBranch(cleanupCtx, info.Branch); err != nil {
		return err
	}
	delete(m.owned, key)
	return nil
}

// Reset discards tracked, untracked, ignored, and submodule changes in a
// verified managed worktree, then proves that the resulting status is clean.
func (m *Manager) Reset(ctx context.Context, input ResetInput) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := m.pathKey(input.Directory)
	if err != nil {
		return fmt.Errorf("%w: canonicalize requested path: %w", ErrNotOwned, err)
	}
	if key == m.primary {
		return ErrPrimary
	}
	entries, err := m.entries(ctx)
	if err != nil {
		return err
	}
	entry, attached := entries[key]
	if !attached {
		return ErrNotOwned
	}
	info, managed := m.managedInfo(key, entry)
	if !managed {
		return ErrNotOwned
	}
	m.owned[key] = info

	target, err := m.primaryRef(ctx)
	if err != nil {
		return err
	}
	steps := []struct {
		name      string
		arguments []string
	}{
		{name: "reset tracked files", arguments: []string{"reset", "--hard", target}},
		{name: "clean untracked files", arguments: []string{"clean", "-ffdx"}},
		{name: "initialize submodules", arguments: []string{"submodule", "update", "--init", "--recursive", "--force"}},
		{name: "reset submodules", arguments: []string{"submodule", "foreach", "--recursive", "git", "reset", "--hard"}},
		{name: "clean submodules", arguments: []string{"submodule", "foreach", "--recursive", "git", "clean", "-fdx"}},
	}
	for _, step := range steps {
		result, stepErr := m.git(ctx, entry.Directory, step.arguments...)
		if stepErr != nil {
			return fmt.Errorf("%w: %s: %w", ErrResetFailed, step.name, safeProcessCause(result, stepErr))
		}
	}
	status, statusErr := m.git(ctx, entry.Directory, "-c", "core.fsmonitor=false", "status", "--porcelain=v1", "--untracked-files=all")
	if statusErr != nil {
		return fmt.Errorf("%w: verify clean status: %w", ErrResetFailed, safeProcessCause(status, statusErr))
	}
	if len(bytesTrimSpace(status.Stdout)) != 0 {
		return fmt.Errorf("%w: local changes remain after reset", ErrResetFailed)
	}
	return nil
}

func (m *Manager) primaryRef(ctx context.Context) (string, error) {
	commands := [][]string{
		{"symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"},
		{"symbolic-ref", "--quiet", "--short", "HEAD"},
		{"rev-parse", "HEAD"},
	}
	for index, arguments := range commands {
		result, err := m.git(ctx, m.primary, arguments...)
		if err == nil {
			value := strings.TrimSpace(string(result.Stdout))
			if value == "" || strings.ContainsAny(value, "\r\n") {
				return "", fmt.Errorf("%w: primary ref output is empty or multiline", ErrResetFailed)
			}
			return value, nil
		}
		if index < len(commands)-1 && result.ExitCode == 1 {
			continue
		}
		return "", fmt.Errorf("%w: resolve primary ref: %w", ErrResetFailed, safeProcessCause(result, err))
	}
	return "", fmt.Errorf("%w: primary ref is unavailable", ErrResetFailed)
}

func (m *Manager) candidate(ctx context.Context, input string) (Info, error) {
	base := slugify(input)
	if input != "" && base == "" {
		base = randomName()
	}
	if base == "" {
		base = randomName()
	}
	if base == "" {
		return Info{}, ErrInvalidName
	}
	for attempt := 0; attempt < maxNameAttempts; attempt++ {
		name := base
		if attempt > 0 {
			name = base + "-" + randomName()
		}
		branch := branchPrefix + name
		directory := filepath.Join(m.rootDisplay, name)
		key, err := m.pathKey(directory)
		if err != nil {
			return Info{}, fmt.Errorf("%w: candidate path: %w", ErrInvalidName, err)
		}
		if !m.isManagedKey(key) {
			return Info{}, fmt.Errorf("%w: candidate escaped managed root", ErrInvalidName)
		}
		if _, err := os.Lstat(directory); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return Info{}, fmt.Errorf("%w: inspect candidate: %w", ErrCreateFailed, err)
		}
		result, branchErr := m.git(ctx, m.primary, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
		if branchErr == nil {
			continue
		}
		if result.ExitCode != 1 {
			return Info{}, fmt.Errorf("%w: inspect branch: %w", ErrCreateFailed, safeProcessCause(result, branchErr))
		}
		return Info{Name: name, Branch: branch, Directory: directory}, nil
	}
	return Info{}, ErrConflict
}

func (m *Manager) entries(ctx context.Context) (map[string]gitEntry, error) {
	result, err := m.git(ctx, m.primary, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrListFailed, safeProcessCause(result, err))
	}
	parsed, err := parseWorktreeList(string(result.Stdout))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrListFailed, err)
	}
	entries := make(map[string]gitEntry, len(parsed))
	for _, entry := range parsed {
		key, keyErr := m.pathKey(entry.Directory)
		if keyErr != nil {
			return nil, fmt.Errorf("%w: canonicalize listed path: %w", ErrListFailed, keyErr)
		}
		if _, duplicate := entries[key]; duplicate {
			return nil, fmt.Errorf("%w: duplicate worktree path", ErrListFailed)
		}
		entries[key] = entry
	}
	return entries, nil
}

func parseWorktreeList(output string) ([]gitEntry, error) {
	var fields []string
	if strings.IndexByte(output, 0) >= 0 {
		fields = strings.Split(output, "\x00")
	} else {
		fields = strings.Split(output, "\n")
	}
	entries := make([]gitEntry, 0)
	for _, raw := range fields {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "worktree ") {
			directory := strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
			if directory == "" {
				return nil, errors.New("empty worktree path")
			}
			entries = append(entries, gitEntry{Directory: directory})
			continue
		}
		if len(entries) == 0 {
			return nil, errors.New("worktree attribute appeared before a path")
		}
		if strings.HasPrefix(line, "branch ") {
			branch := strings.TrimSpace(strings.TrimPrefix(line, "branch "))
			entries[len(entries)-1].Branch = strings.TrimPrefix(branch, "refs/heads/")
		}
	}
	return entries, nil
}

func (m *Manager) managedInfo(key string, entry gitEntry) (Info, bool) {
	if !m.isManagedKey(key) {
		return Info{}, false
	}
	name := filepath.Base(key)
	if name == "." || name == string(filepath.Separator) || slugify(name) != name {
		return Info{}, false
	}
	if entry.Branch != branchPrefix+name {
		return Info{}, false
	}
	if info, ok := m.owned[key]; ok && info.Name == name && info.Branch == entry.Branch {
		return info, true
	}
	return Info{Name: name, Branch: entry.Branch, Directory: entry.Directory}, true
}

func (m *Manager) isManagedKey(key string) bool {
	return key != m.root && key != m.primary && filepath.Dir(key) == m.root &&
		pathx.Contains(m.root, key, m.caseMode)
}

func (m *Manager) compensateCreate(info Info, key string, primary error) error {
	ctx, cancel := context.WithTimeout(context.Background(), compensationWindow)
	defer cancel()
	compensationErr := m.compensate(ctx, info, key)
	if compensationErr == nil {
		return primary
	}
	return errors.Join(primary, compensationErr)
}

func (m *Manager) compensate(ctx context.Context, info Info, key string) error {
	entries, err := m.entries(ctx)
	if err != nil {
		return fmt.Errorf("%w: verify owned path: %w", ErrCompensationFailed, err)
	}
	if entry, attached := entries[key]; attached {
		verified, ok := m.managedInfo(key, entry)
		if !ok || verified.Branch != info.Branch {
			return fmt.Errorf("%w: ownership changed before cleanup", ErrCompensationFailed)
		}
		removed, removeErr := m.git(ctx, m.primary, "worktree", "remove", "--force", entry.Directory)
		next, listErr := m.entries(ctx)
		if listErr != nil {
			verificationErr := fmt.Errorf("%w: verify detach: %w", ErrCompensationFailed, listErr)
			if removeErr == nil {
				return verificationErr
			}
			return errors.Join(verificationErr, processFailure(ErrCompensationFailed, "git remove", removed, removeErr))
		}
		if _, stillAttached := next[key]; stillAttached {
			return processFailure(ErrCompensationFailed, "git remove", removed, removeErr)
		}
	}
	if err := m.removeDirectory(info, key); err != nil {
		return fmt.Errorf("%w: %w", ErrCompensationFailed, err)
	}
	if err := m.deleteBranch(ctx, info.Branch); err != nil {
		return fmt.Errorf("%w: %w", ErrCompensationFailed, err)
	}
	delete(m.owned, key)
	return nil
}

func (m *Manager) removeDirectory(info Info, expectedKey string) error {
	if !m.isManagedKey(expectedKey) {
		return ErrNotOwned
	}
	actualKey, err := m.pathKey(info.Directory)
	if err != nil {
		return fmt.Errorf("%w: verify directory: %w", ErrRemoveFailed, err)
	}
	if actualKey != expectedKey || actualKey == m.primary {
		return ErrNotOwned
	}
	if err := os.RemoveAll(info.Directory); err != nil {
		return fmt.Errorf("%w: remove directory: %w", ErrRemoveFailed, err)
	}
	return nil
}

func (m *Manager) deleteBranch(ctx context.Context, branch string) error {
	if !strings.HasPrefix(branch, branchPrefix) || strings.TrimPrefix(branch, branchPrefix) == "" {
		return ErrNotOwned
	}
	check, checkErr := m.git(ctx, m.primary, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if checkErr != nil {
		if check.ExitCode == 1 {
			return nil
		}
		return fmt.Errorf("%w: inspect branch: %w", ErrRemoveFailed, safeProcessCause(check, checkErr))
	}
	deleted, deleteErr := m.git(ctx, m.primary, "branch", "-D", branch)
	if deleteErr != nil {
		return fmt.Errorf("%w: delete branch: %w", ErrRemoveFailed, safeProcessCause(deleted, deleteErr))
	}
	return nil
}

func (m *Manager) git(ctx context.Context, cwd string, arguments ...string) (runtimeprocess.Result, error) {
	return m.runner.Run(ctx, runtimeprocess.Spec{
		Argv:        append([]string{m.gitPath}, arguments...),
		Env:         append([]string(nil), m.environment...),
		CWD:         cwd,
		StdoutLimit: gitOutputLimit,
		StderrLimit: gitOutputLimit,
		GracePeriod: gitGracePeriod,
	})
}

func (m *Manager) pathKey(input string) (string, error) {
	absolute, err := filepath.Abs(input)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	current := absolute
	missing := make([]string, 0)
	for {
		resolved, resolveErr := filepath.EvalSymlinks(current)
		if resolveErr == nil {
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			resolved = filepath.Clean(resolved)
			if m.caseMode == pathx.CaseInsensitive {
				resolved = strings.ToLower(resolved)
			}
			return resolved, nil
		}
		if !errors.Is(resolveErr, os.ErrNotExist) {
			if errors.Is(resolveErr, syscall.ELOOP) || strings.Contains(strings.ToLower(resolveErr.Error()), "too many links") {
				return "", fmt.Errorf("%w: %w", pathx.ErrSymlink, resolveErr)
			}
			return "", resolveErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", resolveErr
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func slugify(input string) string {
	var builder strings.Builder
	separator := false
	for _, value := range strings.ToLower(strings.TrimSpace(input)) {
		if value >= 'a' && value <= 'z' || value >= '0' && value <= '9' {
			if separator && builder.Len() > 0 {
				builder.WriteByte('-')
			}
			builder.WriteRune(value)
			separator = false
			continue
		}
		separator = builder.Len() > 0
	}
	return strings.Trim(builder.String(), "-")
}

func randomName() string {
	var data [6]byte
	if _, err := rand.Read(data[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(data[:])
}

func bytesTrimSpace(input []byte) []byte {
	return []byte(strings.TrimSpace(string(input)))
}

func safeProcessCause(result runtimeprocess.Result, err error) error {
	if err == nil {
		return nil
	}
	var processErr *runtimeprocess.Error
	if errors.As(err, &processErr) {
		return &runtimeprocess.Error{
			Kind:       processErr.Kind,
			Executable: processErr.Executable,
			ExitCode:   result.ExitCode,
			Cause:      processErr.Cause,
		}
	}
	return err
}

func processFailure(kind error, operation string, result runtimeprocess.Result, err error) error {
	if err == nil {
		return fmt.Errorf("%w: %s did not detach the worktree", kind, operation)
	}
	return fmt.Errorf("%w: %s: %w", kind, operation, safeProcessCause(result, err))
}
