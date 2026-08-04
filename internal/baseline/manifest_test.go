package baseline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateProducesDeterministicManifestFromFrozenTree(t *testing.T) {
	repository, commit := newFixtureRepository(t)
	options := Options{
		Repository:  repository,
		Commit:      commit,
		Branch:      "dev",
		VersionPath: "packages/opencode/package.json",
		LicensePath: "LICENSE",
	}

	first, err := Generate(context.Background(), options)
	if err != nil {
		t.Fatalf("generate first manifest: %v", err)
	}
	second, err := Generate(context.Background(), options)
	if err != nil {
		t.Fatalf("generate second manifest: %v", err)
	}

	if string(first.JSON) != string(second.JSON) {
		t.Fatalf("manifest output is not deterministic\nfirst:\n%s\nsecond:\n%s", first.JSON, second.JSON)
	}
	if first.SHA256 != second.SHA256 {
		t.Fatalf("manifest digest changed: %q != %q", first.SHA256, second.SHA256)
	}
	if !strings.HasSuffix(string(first.JSON), "\n") {
		t.Fatal("manifest JSON must end with one LF")
	}

	var manifest Manifest
	if err := json.Unmarshal(first.JSON, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.SchemaVersion != 1 {
		t.Fatalf("schema version = %d, want 1", manifest.SchemaVersion)
	}
	if manifest.ScopePolicy != "ADR-0002/v1" {
		t.Fatalf("scope policy = %q, want ADR-0002/v1", manifest.ScopePolicy)
	}
	if manifest.Repository.Commit != commit {
		t.Fatalf("commit = %q, want %q", manifest.Repository.Commit, commit)
	}
	if manifest.Repository.Branch != "dev" {
		t.Fatalf("branch = %q, want dev", manifest.Repository.Branch)
	}
	if manifest.Repository.Version != "1.18.11" {
		t.Fatalf("version = %q, want 1.18.11", manifest.Repository.Version)
	}
	if manifest.License.SPDX != "MIT" {
		t.Fatalf("license SPDX = %q, want MIT", manifest.License.SPDX)
	}
	if manifest.Counts.TrackedFiles != 12 {
		t.Fatalf("tracked files = %d, want 12", manifest.Counts.TrackedFiles)
	}
	if manifest.Counts.SourceFiles != 3 || manifest.Counts.SourceLines != 4 {
		t.Fatalf("source counts = %d files/%d lines, want 3 files/4 lines", manifest.Counts.SourceFiles, manifest.Counts.SourceLines)
	}
	if manifest.Counts.TestFiles != 1 {
		t.Fatalf("test files = %d, want 1", manifest.Counts.TestFiles)
	}
	if manifest.Counts.PackageManifests != 3 || manifest.Counts.WorkspacePackages != 2 {
		t.Fatalf("package counts = %d manifests/%d workspaces, want 3/2", manifest.Counts.PackageManifests, manifest.Counts.WorkspacePackages)
	}
	if len(manifest.Artifacts) != 2 || manifest.Artifacts[0].Path != "packages/docs/openapi.json" || manifest.Artifacts[1].Path != "packages/schema/src/schema.gen.ts" {
		t.Fatalf("generated artifacts = %#v", manifest.Artifacts)
	}
	if len(manifest.Packages) != 3 || manifest.Packages[0].Path != "package.json" || manifest.Packages[1].Path != "packages/opencode/package.json" || manifest.Packages[2].Path != "packages/schema/package.json" {
		t.Fatalf("package order = %#v", manifest.Packages)
	}
	if manifest.Packages[0].Workspace || !manifest.Packages[1].Workspace || !manifest.Packages[2].Workspace {
		t.Fatalf("workspace classification = %#v", manifest.Packages)
	}
	if len(manifest.Packages[1].Dependencies) != 1 || !manifest.Packages[1].Dependencies[0].Workspace {
		t.Fatalf("workspace dependency classification = %#v", manifest.Packages[1].Dependencies)
	}
	if len(manifest.Tests) != 1 || manifest.Tests[0].Path != "packages/opencode/test/session.test.ts" {
		t.Fatalf("test inventory = %#v", manifest.Tests)
	}
	if len(manifest.Files) != 12 || manifest.Files[0].Path != "LICENSE" {
		t.Fatalf("file inventory = %#v", manifest.Files)
	}
	files := make(map[string]FileRecord, len(manifest.Files))
	for _, file := range manifest.Files {
		files[file.Path] = file
	}
	if files["packages/schema/src/schema.gen.ts"].Kind != FileGenerated || files["packages/schema/src/schema.gen.ts"].Classification != ScopeCanonicalV2 {
		t.Fatalf("generated schema classification = %#v", files["packages/schema/src/schema.gen.ts"])
	}
	if files["packages/opencode/src/session/processor.ts"].Classification != ScopeV1Archaeology {
		t.Fatalf("V1 processor classification = %#v", files["packages/opencode/src/session/processor.ts"])
	}
	if files["packages/opencode/test/session.test.ts"].Kind != FileTest {
		t.Fatalf("test file kind = %#v", files["packages/opencode/test/session.test.ts"])
	}
}

func TestGenerateRejectsDirtyWorktree(t *testing.T) {
	repository, commit := newFixtureRepository(t)
	writeFixtureFile(t, repository, "packages/opencode/src/index.ts", "export const dirty = true\n")

	_, err := Generate(context.Background(), Options{
		Repository:  repository,
		Commit:      commit,
		Branch:      "dev",
		VersionPath: "packages/opencode/package.json",
		LicensePath: "LICENSE",
	})
	if !errors.Is(err, ErrDirtyWorktree) {
		t.Fatalf("error = %v, want ErrDirtyWorktree", err)
	}
}

func TestGenerateRejectsDifferentHead(t *testing.T) {
	repository, commit := newFixtureRepository(t)
	writeFixtureFile(t, repository, "README.md", "next\n")
	runGit(t, repository, "add", "README.md")
	runGit(t, repository, "commit", "-m", "next")

	_, err := Generate(context.Background(), Options{
		Repository:  repository,
		Commit:      commit,
		Branch:      "dev",
		VersionPath: "packages/opencode/package.json",
		LicensePath: "LICENSE",
	})
	if !errors.Is(err, ErrHeadMismatch) {
		t.Fatalf("error = %v, want ErrHeadMismatch", err)
	}
}

func TestGenerateRejectsGitlink(t *testing.T) {
	repository, commit := newFixtureRepository(t)
	other, otherCommit := newFixtureRepository(t)
	runGit(t, repository, "update-index", "--add", "--cacheinfo", "160000,"+otherCommit+",vendor/dependency")
	runGit(t, repository, "commit", "-m", "add gitlink")
	commit = strings.TrimSpace(runGit(t, repository, "rev-parse", "HEAD"))

	_, err := Generate(context.Background(), Options{
		Repository:  repository,
		Commit:      commit,
		Branch:      "dev",
		VersionPath: "packages/opencode/package.json",
		LicensePath: "LICENSE",
	})
	if !errors.Is(err, ErrGitlink) {
		t.Fatalf("error = %v, want ErrGitlink (other repo %s)", err, other)
	}
}

func newFixtureRepository(t *testing.T) (string, string) {
	t.Helper()
	repository := t.TempDir()
	runGit(t, repository, "init", "-b", "dev")
	runGit(t, repository, "config", "user.name", "Baseline Test")
	runGit(t, repository, "config", "user.email", "baseline@example.invalid")

	files := map[string]string{
		"LICENSE":                                     "MIT License\nfixture\n",
		"package.json":                                `{"name":"fixture","private":true,"license":"MIT","workspaces":{"packages":["packages/*"]}}` + "\n",
		"packages/opencode/package.json":              `{"name":"opencode","version":"1.18.11","dependencies":{"@fixture/schema":"workspace:*"}}` + "\n",
		"packages/opencode/src/index.ts":              "export const main = true\nexport const value = 1\n",
		"packages/opencode/src/session/processor.ts":  "export const legacy = true\n",
		"packages/opencode/test/session.test.ts":      "import { test } from 'bun:test'\ntest('fixture', () => {})\n",
		"packages/opencode/test/session.test.ts.snap": "snapshot data\n",
		"packages/schema/package.json":                `{"name":"@fixture/schema","version":"1.0.0"}` + "\n",
		"packages/schema/src/schema.gen.ts":           "export const generated = true\n",
		"docs/notes.md":                               "not source\n",
		"assets/icon.svg":                             "<svg></svg>\n",
	}
	for path, content := range files {
		writeFixtureFile(t, repository, path, content)
	}
	if err := os.MkdirAll(filepath.Join(repository, "packages", "docs"), 0o755); err != nil {
		t.Fatalf("create symlink fixture directory: %v", err)
	}
	if err := os.Symlink("../schema/src/schema.gen.ts", filepath.Join(repository, "packages", "docs", "openapi.json")); err != nil {
		t.Fatalf("create generated symlink fixture: %v", err)
	}
	runGit(t, repository, "add", ".")
	runGit(t, repository, "commit", "-m", "fixture")
	return repository, strings.TrimSpace(runGit(t, repository, "rev-parse", "HEAD"))
}

func TestClassifyWorkspacesPreservesExplicitExternalOverrides(t *testing.T) {
	inventory := archiveInventory{
		workspaces: []string{"packages/*"},
		packages: []PackageRecord{
			{Path: "packages/client/package.json", Name: "@fixture/client"},
			{
				Path: "packages/app/package.json", Name: "@fixture/app",
				Dependencies: []DependencyRecord{{Name: "@fixture/client", Constraint: "file:vendor/client.tgz"}},
			},
			{
				Path: "packages/server/package.json", Name: "@fixture/server",
				Dependencies: []DependencyRecord{{Name: "@fixture/client", Constraint: "workspace:*"}},
			},
		},
	}

	if err := classifyWorkspaces(&inventory); err != nil {
		t.Fatalf("classify workspaces: %v", err)
	}
	if inventory.packages[1].Dependencies[0].Workspace {
		t.Fatalf("file override classified as workspace: %#v", inventory.packages[1].Dependencies[0])
	}
	if !inventory.packages[2].Dependencies[0].Workspace {
		t.Fatalf("workspace protocol dependency classified as external: %#v", inventory.packages[2].Dependencies[0])
	}
}

func writeFixtureFile(t *testing.T, repository string, path string, content string) {
	t.Helper()
	fullPath := filepath.Join(repository, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
}

func runGit(t *testing.T, repository string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repository}, args...)...)
	command.Env = append(os.Environ(), "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
