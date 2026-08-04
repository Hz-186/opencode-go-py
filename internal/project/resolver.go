// Package project resolves stable project identity from filesystem and Git.
package project

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Hz-186/opencode-go-py/internal/domain"
	"github.com/Hz-186/opencode-go-py/internal/platform/pathx"
	runtimeprocess "github.com/Hz-186/opencode-go-py/internal/runtime/process"
)

const GlobalID domain.ProjectID = "global"

type VCS struct {
	Type  string
	Store string
}

type Resolved struct {
	Previous  *domain.ProjectID
	ID        domain.ProjectID
	Directory string
	VCS       *VCS
}

type Resolver struct {
	GitPath     string
	Environment []string
	CaseMode    pathx.CaseMode
}

func (r Resolver) Resolve(ctx context.Context, directory string) (Resolved, error) {
	if r.GitPath == "" || r.Environment == nil {
		return Resolved{}, errors.New("project resolver requires explicit git path and environment")
	}
	input, err := pathx.Canonical(directory, r.CaseMode)
	if err != nil {
		return Resolved{}, err
	}
	worktreeText, err := r.git(ctx, input, "rev-parse", "--show-toplevel")
	if err != nil {
		if isGitExitFailure(err) {
			return Resolved{
				ID:        GlobalID,
				Directory: filepath.VolumeName(input) + string(filepath.Separator),
			}, nil
		}
		return Resolved{}, fmt.Errorf("discover git worktree: %w", err)
	}
	worktree, err := pathx.Canonical(firstLine(worktreeText), r.CaseMode)
	if err != nil {
		return Resolved{}, fmt.Errorf("canonicalize git worktree: %w", err)
	}

	commonText, err := r.git(ctx, input, "rev-parse", "--git-common-dir")
	if err != nil {
		return Resolved{}, fmt.Errorf("resolve git common directory: %w", err)
	}
	common := firstLine(commonText)
	if !filepath.IsAbs(common) {
		common = filepath.Join(input, common)
	}
	common, err = pathx.Canonical(common, r.CaseMode)
	if err != nil {
		return Resolved{}, fmt.Errorf("canonicalize git common directory: %w", err)
	}

	previous := cachedID(filepath.Join(common, "opencode"))
	id := domain.ProjectID("")
	remoteText, remoteErr := r.git(ctx, input, "config", "--get", "remote.origin.url")
	if remoteErr != nil && !isGitExitFailure(remoteErr) {
		return Resolved{}, fmt.Errorf("resolve git remote: %w", remoteErr)
	}
	if remoteErr == nil {
		if normalized := NormalizeRemote(firstLine(remoteText)); normalized != "" {
			hash := sha1.Sum([]byte("git-remote:" + normalized))
			id = domain.ProjectID(fmt.Sprintf("%x", hash))
		}
	}
	if id == "" && previous != nil {
		id = *previous
	}
	if id == "" {
		rootText, rootErr := r.git(ctx, input, "rev-list", "--max-parents=0", "HEAD")
		if rootErr != nil && !isGitExitFailure(rootErr) {
			return Resolved{}, fmt.Errorf("resolve git root commit: %w", rootErr)
		}
		if rootErr == nil {
			id = domain.ProjectID(firstLine(rootText))
		}
	}
	if id == "" {
		id = GlobalID
	}
	return Resolved{
		Previous:  previous,
		ID:        id,
		Directory: worktree,
		VCS:       &VCS{Type: "git", Store: common},
	}, nil
}

func isGitExitFailure(err error) bool {
	var processErr *runtimeprocess.Error
	return errors.As(err, &processErr) && processErr.Kind == runtimeprocess.ExitFailure
}

func (r Resolver) Commit(resolved Resolved) error {
	if resolved.VCS == nil || resolved.ID == GlobalID {
		return nil
	}
	file, err := os.CreateTemp(resolved.VCS.Store, ".opencode-*")
	if err != nil {
		return fmt.Errorf("create project id temp file: %w", err)
	}
	temp := file.Name()
	defer os.Remove(temp)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("chmod project id temp file: %w", err)
	}
	if _, err := file.WriteString(string(resolved.ID)); err != nil {
		_ = file.Close()
		return fmt.Errorf("write project id temp file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close project id temp file: %w", err)
	}
	if err := os.Rename(temp, filepath.Join(resolved.VCS.Store, "opencode")); err != nil {
		return fmt.Errorf("commit project id: %w", err)
	}
	return nil
}

func (r Resolver) git(ctx context.Context, cwd string, arguments ...string) (string, error) {
	spec := runtimeprocess.Spec{
		Argv:        append([]string{r.GitPath}, arguments...),
		Env:         append([]string(nil), r.Environment...),
		CWD:         cwd,
		StdoutLimit: 256 * 1024,
		StderrLimit: 256 * 1024,
		GracePeriod: 250 * time.Millisecond,
	}
	result, err := runtimeprocess.Run(ctx, spec)
	return string(result.Stdout), err
}

var scpRemote = regexp.MustCompile("^([^@/:]+@)?([^/:]+):(.+)$")

func NormalizeRemote(input string) string {
	value := strings.TrimSpace(input)
	if value == "" {
		return ""
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" {
		if parsed.Scheme == "file" {
			return ""
		}
		return remoteParts(parsed.Hostname(), parsed.Path)
	}
	match := scpRemote.FindStringSubmatch(value)
	if len(match) != 4 {
		return ""
	}
	return remoteParts(match[2], match[3])
}

func remoteParts(host, name string) string {
	name = strings.TrimLeft(name, "/")
	name = strings.TrimSuffix(name, "/")
	name = strings.TrimSuffix(name, ".git")
	name = strings.TrimSuffix(name, "/")
	if host == "" || name == "" {
		return ""
	}
	return strings.ToLower(host) + "/" + name
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.IndexByte(value, '\n'); index >= 0 {
		return strings.TrimSpace(value[:index])
	}
	return value
}

func cachedID(path string) *domain.ProjectID {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	value := strings.TrimSpace(string(content))
	if value == "" {
		return nil
	}
	id := domain.ProjectID(value)
	return &id
}
