package baseline

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

var (
	ErrDirtyWorktree  = errors.New("baseline repository worktree is dirty")
	ErrHeadMismatch   = errors.New("baseline repository HEAD does not match frozen commit")
	ErrBranchMismatch = errors.New("baseline repository branch does not match frozen branch")
	ErrGitlink        = errors.New("baseline repository contains a gitlink")
)

type Options struct {
	Repository  string
	Commit      string
	Branch      string
	VersionPath string
	LicensePath string
}

type Result struct {
	Manifest Manifest
	JSON     []byte
	SHA256   string
}

type Manifest struct {
	SchemaVersion int              `json:"schema_version"`
	ScopePolicy   string           `json:"scope_policy"`
	Repository    Repository       `json:"repository"`
	License       LicenseRecord    `json:"license"`
	Counts        Counts           `json:"counts"`
	Files         []FileRecord     `json:"files"`
	Sources       []SourceCount    `json:"sources"`
	Packages      []PackageRecord  `json:"packages"`
	Tests         []TestRecord     `json:"tests"`
	Artifacts     []ArtifactRecord `json:"artifacts"`
}

type Repository struct {
	Commit  string `json:"commit"`
	Tree    string `json:"tree"`
	Branch  string `json:"branch"`
	Version string `json:"version"`
}

type LicenseRecord struct {
	Path   string `json:"path"`
	SPDX   string `json:"spdx"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type Counts struct {
	TrackedFiles      int `json:"tracked_files"`
	SourceFiles       int `json:"source_files"`
	SourceLines       int `json:"source_lines"`
	TestFiles         int `json:"test_files"`
	PackageManifests  int `json:"package_manifests"`
	WorkspacePackages int `json:"workspace_packages"`
	Artifacts         int `json:"artifacts"`
}

type SourceCount struct {
	Extension string `json:"extension"`
	Files     int    `json:"files"`
	Lines     int    `json:"lines"`
}

type FileKind string

const (
	FileGenerated FileKind = "generated"
	FileOther     FileKind = "other"
	FilePackage   FileKind = "package"
	FileSource    FileKind = "source"
	FileTest      FileKind = "test"
)

type ScopeClassification string

const (
	ScopeCanonicalV2   ScopeClassification = "canonical-v2"
	ScopeShared        ScopeClassification = "shared"
	ScopeV1Archaeology ScopeClassification = "v1-archaeology"
)

type FileRecord struct {
	Path           string              `json:"path"`
	Mode           string              `json:"mode"`
	ObjectID       string              `json:"object_id"`
	Bytes          int64               `json:"bytes"`
	Kind           FileKind            `json:"kind"`
	Classification ScopeClassification `json:"classification"`
}

type PackageRecord struct {
	Path         string             `json:"path"`
	Name         string             `json:"name"`
	Version      string             `json:"version,omitempty"`
	Private      bool               `json:"private"`
	License      string             `json:"license,omitempty"`
	Workspace    bool               `json:"workspace"`
	Dependencies []DependencyRecord `json:"dependencies"`
}

type DependencyRecord struct {
	Name       string `json:"name"`
	Constraint string `json:"constraint"`
	Kind       string `json:"kind"`
	Workspace  bool   `json:"workspace"`
}

type TestRecord struct {
	Path string `json:"path"`
}

type ArtifactRecord struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type packageDocument struct {
	Name                 string            `json:"name"`
	Version              string            `json:"version"`
	Private              bool              `json:"private"`
	License              string            `json:"license"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	Workspaces           json.RawMessage   `json:"workspaces"`
}

type archiveInventory struct {
	version      string
	license      LicenseRecord
	rootLicense  string
	sourceCounts map[string]SourceCount
	packages     []PackageRecord
	tests        []TestRecord
	artifacts    []ArtifactRecord
	workspaces   []string
}

func Generate(ctx context.Context, options Options) (Result, error) {
	if err := validateOptions(options); err != nil {
		return Result{}, err
	}
	repository, err := filepath.Abs(options.Repository)
	if err != nil {
		return Result{}, fmt.Errorf("resolve repository path: %w", err)
	}
	if err := validateWorktree(ctx, repository, options); err != nil {
		return Result{}, err
	}
	commit, err := gitText(ctx, repository, "rev-parse", "--verify", "--end-of-options", options.Commit+"^{commit}")
	if err != nil {
		return Result{}, fmt.Errorf("resolve frozen commit %q: %w", options.Commit, err)
	}
	tree, err := gitText(ctx, repository, "rev-parse", "--verify", "--end-of-options", commit+"^{tree}")
	if err != nil {
		return Result{}, fmt.Errorf("resolve frozen tree: %w", err)
	}
	files, err := inspectTree(ctx, repository, commit)
	if err != nil {
		return Result{}, err
	}
	inventory, err := inspectArchive(ctx, repository, commit, options)
	if err != nil {
		return Result{}, err
	}
	if inventory.version == "" {
		return Result{}, fmt.Errorf("version is missing from %s", options.VersionPath)
	}
	if inventory.license.SHA256 == "" {
		return Result{}, fmt.Errorf("license file %s is missing from frozen tree", options.LicensePath)
	}
	if inventory.license.SPDX == "" {
		inventory.license.SPDX = inventory.rootLicense
	}

	manifest := Manifest{
		SchemaVersion: 1,
		ScopePolicy:   "ADR-0002/v1",
		Repository: Repository{
			Commit:  commit,
			Tree:    tree,
			Branch:  options.Branch,
			Version: inventory.version,
		},
		License:   inventory.license,
		Files:     files,
		Sources:   sortedSourceCounts(inventory.sourceCounts),
		Packages:  inventory.packages,
		Tests:     inventory.tests,
		Artifacts: inventory.artifacts,
	}
	for _, source := range manifest.Sources {
		manifest.Counts.SourceFiles += source.Files
		manifest.Counts.SourceLines += source.Lines
	}
	manifest.Counts.TrackedFiles = len(files)
	manifest.Counts.TestFiles = len(manifest.Tests)
	manifest.Counts.PackageManifests = len(manifest.Packages)
	for _, packageRecord := range manifest.Packages {
		if packageRecord.Workspace {
			manifest.Counts.WorkspacePackages++
		}
	}
	manifest.Counts.Artifacts = len(manifest.Artifacts)

	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Result{}, fmt.Errorf("encode manifest: %w", err)
	}
	encoded = append(encoded, '\n')
	digest := sha256.Sum256(encoded)
	return Result{Manifest: manifest, JSON: encoded, SHA256: hex.EncodeToString(digest[:])}, nil
}

func validateOptions(options Options) error {
	if options.Repository == "" {
		return errors.New("repository path is required")
	}
	if options.Commit == "" {
		return errors.New("frozen commit is required")
	}
	if options.Branch == "" {
		return errors.New("frozen branch is required")
	}
	if options.VersionPath == "" {
		return errors.New("version package path is required")
	}
	if options.LicensePath == "" {
		return errors.New("license path is required")
	}
	return nil
}

func validateWorktree(ctx context.Context, repository string, options Options) error {
	inside, err := gitText(ctx, repository, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return fmt.Errorf("open baseline repository: %w", err)
	}
	if inside != "true" {
		return fmt.Errorf("%s is not a Git worktree", repository)
	}
	status, err := gitBytes(ctx, repository, "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=all")
	if err != nil {
		return fmt.Errorf("inspect worktree status: %w", err)
	}
	if len(bytes.TrimSpace(status)) > 0 {
		return fmt.Errorf("%w: %s", ErrDirtyWorktree, firstStatusLine(status))
	}
	head, err := gitText(ctx, repository, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return fmt.Errorf("resolve repository HEAD: %w", err)
	}
	commit, err := gitText(ctx, repository, "rev-parse", "--verify", "--end-of-options", options.Commit+"^{commit}")
	if err != nil {
		return fmt.Errorf("resolve requested commit: %w", err)
	}
	if head != commit {
		return fmt.Errorf("%w: HEAD=%s frozen=%s", ErrHeadMismatch, head, commit)
	}
	branch, err := gitText(ctx, repository, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return fmt.Errorf("resolve repository branch: %w", err)
	}
	if branch != options.Branch {
		return fmt.Errorf("%w: current=%s frozen=%s", ErrBranchMismatch, branch, options.Branch)
	}
	return nil
}

func inspectTree(ctx context.Context, repository string, commit string) ([]FileRecord, error) {
	output, err := gitBytes(ctx, repository, "ls-tree", "-r", "-z", "-l", "--full-tree", commit)
	if err != nil {
		return nil, fmt.Errorf("list frozen tree: %w", err)
	}
	files := make([]FileRecord, 0)
	for _, entry := range bytes.Split(output, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		tab := bytes.IndexByte(entry, '\t')
		if tab < 0 {
			return nil, fmt.Errorf("parse ls-tree entry %q", entry)
		}
		fields := bytes.Fields(entry[:tab])
		if len(fields) != 4 {
			return nil, fmt.Errorf("parse ls-tree metadata %q", entry[:tab])
		}
		if string(fields[0]) == "160000" || string(fields[1]) == "commit" {
			return nil, fmt.Errorf("%w: %s", ErrGitlink, entry[tab+1:])
		}
		size, err := strconv.ParseInt(string(fields[3]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse blob size for %s: %w", entry[tab+1:], err)
		}
		name := string(entry[tab+1:])
		files = append(files, FileRecord{
			Path: name, Mode: string(fields[0]), ObjectID: string(fields[2]), Bytes: size,
			Kind: classifyFileKind(name), Classification: classifyScope(name),
		})
	}
	return files, nil
}

func inspectArchive(ctx context.Context, repository string, commit string, options Options) (archiveInventory, error) {
	command := exec.CommandContext(ctx, "git", "-C", repository, "archive", "--format=tar", commit)
	command.Env = utf8Environment()
	stdout, err := command.StdoutPipe()
	if err != nil {
		return archiveInventory{}, fmt.Errorf("open git archive stream: %w", err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return archiveInventory{}, fmt.Errorf("start git archive: %w", err)
	}

	inventory := archiveInventory{sourceCounts: make(map[string]SourceCount)}
	readErr := readArchive(tar.NewReader(stdout), options, &inventory)
	waitErr := command.Wait()
	if readErr != nil {
		return archiveInventory{}, readErr
	}
	if waitErr != nil {
		return archiveInventory{}, fmt.Errorf("git archive: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	if err := classifyWorkspaces(&inventory); err != nil {
		return archiveInventory{}, err
	}
	slices.SortFunc(inventory.packages, func(a, b PackageRecord) int { return strings.Compare(a.Path, b.Path) })
	slices.SortFunc(inventory.tests, func(a, b TestRecord) int { return strings.Compare(a.Path, b.Path) })
	slices.SortFunc(inventory.artifacts, func(a, b ArtifactRecord) int { return strings.Compare(a.Path, b.Path) })
	return inventory, nil
}

func readArchive(reader *tar.Reader, options Options, inventory *archiveInventory) error {
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read git archive: %w", err)
		}
		name := path.Clean(header.Name)
		if header.Typeflag == tar.TypeSymlink {
			if isGeneratedArtifact(name) {
				content := []byte(header.Linkname)
				digest := sha256.Sum256(content)
				inventory.artifacts = append(inventory.artifacts, ArtifactRecord{
					Path: name, SHA256: hex.EncodeToString(digest[:]), Bytes: int64(len(content)),
				})
			}
			continue
		}
		if !header.FileInfo().Mode().IsRegular() {
			continue
		}
		if isTestPath(name) {
			inventory.tests = append(inventory.tests, TestRecord{Path: name})
		}
		extension, source := sourceExtension(name)
		artifact := isGeneratedArtifact(name)
		needsContent := source || artifact || name == options.VersionPath || name == options.LicensePath || path.Base(name) == "package.json"
		if !needsContent {
			continue
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			return fmt.Errorf("read %s from git archive: %w", name, err)
		}
		if source {
			if !utf8.Valid(content) {
				return fmt.Errorf("source file %s is not valid UTF-8", name)
			}
			count := inventory.sourceCounts[extension]
			count.Extension = extension
			count.Files++
			count.Lines += lineCount(content)
			inventory.sourceCounts[extension] = count
		}
		if artifact {
			digest := sha256.Sum256(content)
			inventory.artifacts = append(inventory.artifacts, ArtifactRecord{
				Path: name, SHA256: hex.EncodeToString(digest[:]), Bytes: int64(len(content)),
			})
		}
		if name == options.LicensePath {
			digest := sha256.Sum256(content)
			inventory.license = LicenseRecord{
				Path: name, SHA256: hex.EncodeToString(digest[:]), Bytes: int64(len(content)),
			}
		}
		if path.Base(name) == "package.json" {
			document, err := decodePackage(name, content)
			if err != nil {
				return err
			}
			inventory.packages = append(inventory.packages, packageRecord(name, document))
			if name == "package.json" {
				inventory.rootLicense = document.License
				workspaces, err := decodeWorkspaces(document.Workspaces)
				if err != nil {
					return fmt.Errorf("decode root workspaces: %w", err)
				}
				inventory.workspaces = workspaces
			}
			if name == options.VersionPath {
				inventory.version = document.Version
			}
		}
	}
}

func decodePackage(name string, content []byte) (packageDocument, error) {
	var document packageDocument
	decoder := json.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(&document); err != nil {
		return packageDocument{}, fmt.Errorf("decode %s: %w", name, err)
	}
	if document.Name == "" {
		return packageDocument{}, fmt.Errorf("package %s has no name", name)
	}
	return document, nil
}

func packageRecord(name string, document packageDocument) PackageRecord {
	record := PackageRecord{
		Path: name, Name: document.Name, Version: document.Version,
		Private: document.Private, License: document.License,
	}
	groups := []struct {
		kind   string
		values map[string]string
	}{
		{kind: "runtime", values: document.Dependencies},
		{kind: "development", values: document.DevDependencies},
		{kind: "peer", values: document.PeerDependencies},
		{kind: "optional", values: document.OptionalDependencies},
	}
	for _, group := range groups {
		for dependency, constraint := range group.values {
			record.Dependencies = append(record.Dependencies, DependencyRecord{
				Name: dependency, Constraint: constraint, Kind: group.kind,
			})
		}
	}
	slices.SortFunc(record.Dependencies, func(a, b DependencyRecord) int {
		if compared := strings.Compare(a.Name, b.Name); compared != 0 {
			return compared
		}
		return strings.Compare(a.Kind, b.Kind)
	})
	return record
}

func decodeWorkspaces(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	trimmed := bytes.TrimSpace(raw)
	var patterns []string
	switch trimmed[0] {
	case '[':
		if err := json.Unmarshal(trimmed, &patterns); err != nil {
			return nil, err
		}
	case '{':
		var object struct {
			Packages []string `json:"packages"`
		}
		if err := json.Unmarshal(trimmed, &object); err != nil {
			return nil, err
		}
		patterns = object.Packages
	default:
		return nil, fmt.Errorf("unsupported workspaces value %q", trimmed)
	}
	for index, pattern := range patterns {
		patterns[index] = strings.TrimSuffix(path.Clean(pattern), "/")
		if strings.HasPrefix(patterns[index], "!") {
			return nil, fmt.Errorf("negative workspace pattern %q is unsupported", pattern)
		}
		if _, err := path.Match(patterns[index], "workspace/probe"); err != nil {
			return nil, fmt.Errorf("invalid workspace pattern %q: %w", pattern, err)
		}
	}
	slices.Sort(patterns)
	return patterns, nil
}

func classifyWorkspaces(inventory *archiveInventory) error {
	workspaceNames := make(map[string]string)
	for index := range inventory.packages {
		directory := path.Dir(inventory.packages[index].Path)
		if directory == "." || !matchesWorkspace(directory, inventory.workspaces) {
			continue
		}
		inventory.packages[index].Workspace = true
		if existing, found := workspaceNames[inventory.packages[index].Name]; found {
			return fmt.Errorf("duplicate workspace package name %q in %s and %s", inventory.packages[index].Name, existing, inventory.packages[index].Path)
		}
		workspaceNames[inventory.packages[index].Name] = inventory.packages[index].Path
	}
	for packageIndex := range inventory.packages {
		for dependencyIndex := range inventory.packages[packageIndex].Dependencies {
			dependency := &inventory.packages[packageIndex].Dependencies[dependencyIndex]
			_, workspace := workspaceNames[dependency.Name]
			dependency.Workspace = workspace && !usesExplicitExternalSource(dependency.Constraint)
		}
	}
	return nil
}

func usesExplicitExternalSource(constraint string) bool {
	lower := strings.ToLower(strings.TrimSpace(constraint))
	prefixes := []string{
		"bitbucket:", "file:", "git:", "git+", "github:", "gitlab:",
		"http:", "https:", "link:", "npm:", "portal:", "ssh:",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func matchesWorkspace(directory string, patterns []string) bool {
	for _, pattern := range patterns {
		matched, _ := path.Match(pattern, directory)
		if matched {
			return true
		}
	}
	return false
}

func sortedSourceCounts(counts map[string]SourceCount) []SourceCount {
	result := make([]SourceCount, 0, len(counts))
	for _, count := range counts {
		result = append(result, count)
	}
	slices.SortFunc(result, func(a, b SourceCount) int { return strings.Compare(a.Extension, b.Extension) })
	return result
}

func sourceExtension(name string) (string, bool) {
	if isTestPath(name) {
		return "", false
	}
	extension := strings.ToLower(path.Ext(name))
	switch extension {
	case ".c", ".cc", ".cjs", ".cpp", ".css", ".go", ".h", ".hpp", ".html", ".java", ".js", ".jsx", ".kt", ".kts", ".mjs", ".proto", ".ps1", ".py", ".rs", ".scss", ".sh", ".sql", ".swift", ".ts", ".tsx", ".zsh":
		return extension, true
	default:
		return "", false
	}
}

func isTestPath(name string) bool {
	base := strings.ToLower(path.Base(name))
	if strings.HasSuffix(base, "_test.go") {
		return true
	}
	switch path.Ext(base) {
	case ".cjs", ".js", ".jsx", ".mjs", ".ts", ".tsx":
		return strings.Contains(base, ".test.") || strings.Contains(base, ".spec.")
	default:
		return false
	}
}

func isGeneratedArtifact(name string) bool {
	base := strings.ToLower(path.Base(name))
	return strings.Contains(base, ".gen.") || strings.HasSuffix(base, ".schema.json") || base == "openapi.json" || base == "openapi.yaml" || base == "openapi.yml"
}

func classifyFileKind(name string) FileKind {
	if isGeneratedArtifact(name) {
		return FileGenerated
	}
	if path.Base(name) == "package.json" {
		return FilePackage
	}
	if isTestPath(name) {
		return FileTest
	}
	if _, source := sourceExtension(name); source {
		return FileSource
	}
	return FileOther
}

func classifyScope(name string) ScopeClassification {
	v1Files := map[string]struct{}{
		"packages/opencode/src/session/prompt.ts":    {},
		"packages/opencode/src/session/processor.ts": {},
		"packages/opencode/src/session/tools.ts":     {},
		"packages/opencode/src/skill/index.ts":       {},
		"packages/plugin/src/index.ts":               {},
	}
	if _, v1 := v1Files[name]; v1 {
		return ScopeV1Archaeology
	}
	v1Prefixes := []string{
		"packages/opencode/src/permission/",
		"packages/sdk/",
	}
	for _, prefix := range v1Prefixes {
		if strings.HasPrefix(name, prefix) {
			return ScopeV1Archaeology
		}
	}
	canonicalPrefixes := []string{
		"packages/client/",
		"packages/codemode/",
		"packages/core/",
		"packages/llm/",
		"packages/plugin/src/v2/",
		"packages/protocol/",
		"packages/schema/",
		"packages/sdk-next/",
		"packages/server/",
	}
	for _, prefix := range canonicalPrefixes {
		if strings.HasPrefix(name, prefix) {
			return ScopeCanonicalV2
		}
	}
	return ScopeShared
}

func lineCount(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	lines := bytes.Count(content, []byte{'\n'})
	if content[len(content)-1] != '\n' {
		lines++
	}
	return lines
}

func firstStatusLine(status []byte) string {
	line, _, _ := bytes.Cut(bytes.TrimSpace(status), []byte{'\n'})
	return string(line)
}

func gitText(ctx context.Context, repository string, args ...string) (string, error) {
	output, err := gitBytes(ctx, repository, args...)
	return strings.TrimSpace(string(output)), err
}

func gitBytes(ctx context.Context, repository string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", repository}, args...)...)
	command.Env = utf8Environment()
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func utf8Environment() []string {
	environment := os.Environ()
	environment = append(environment, "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8")
	return environment
}
