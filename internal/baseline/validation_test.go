package baseline

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateRejectsIncompleteOptions(t *testing.T) {
	valid := Options{
		Repository: "repo", Commit: "commit", Branch: "dev",
		VersionPath: "package.json", LicensePath: "LICENSE",
	}
	tests := []struct {
		name   string
		mutate func(*Options)
	}{
		{name: "repository", mutate: func(options *Options) { options.Repository = "" }},
		{name: "commit", mutate: func(options *Options) { options.Commit = "" }},
		{name: "branch", mutate: func(options *Options) { options.Branch = "" }},
		{name: "version path", mutate: func(options *Options) { options.VersionPath = "" }},
		{name: "license path", mutate: func(options *Options) { options.LicensePath = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := valid
			test.mutate(&options)
			if _, err := Generate(context.Background(), options); err == nil {
				t.Fatal("incomplete options unexpectedly succeeded")
			}
		})
	}
}

func TestGenerateFeatureMatrixRejectsInvalidDocuments(t *testing.T) {
	directory := t.TempDir()
	invalidUTF8 := filepath.Join(directory, "invalid.md")
	if err := os.WriteFile(invalidUTF8, []byte{0xff}, 0o644); err != nil {
		t.Fatalf("write invalid UTF-8 fixture: %v", err)
	}
	missingHeader := filepath.Join(directory, "missing-header.md")
	if err := os.WriteFile(missingHeader, []byte("# no table\n"), 0o644); err != nil {
		t.Fatalf("write missing header fixture: %v", err)
	}
	badSeparator := filepath.Join(directory, "bad-separator.md")
	if err := os.WriteFile(badSeparator, []byte("| 功能 | OpenCode 源码位置 | 当前行为 | 依赖 | Go/Python 归属 | 阶段 | 测试依据 | 难度 | 状态 |\n| x | x | x | x | x | x | x | x | x |\n"), 0o644); err != nil {
		t.Fatalf("write separator fixture: %v", err)
	}
	badDifficulty := writeMatrixFixture(t, "pending", "pending", "One", "Two")
	content, err := os.ReadFile(badDifficulty)
	if err != nil {
		t.Fatalf("read difficulty fixture: %v", err)
	}
	content = append(content, []byte("| Three | source | behavior | dependency | Go | P0 | test | EXTREME | pending |\n")...)
	if err := os.WriteFile(badDifficulty, content, 0o644); err != nil {
		t.Fatalf("write difficulty fixture: %v", err)
	}

	tests := []FeatureMatrixOptions{
		{},
		{PlanPath: "plan.md"},
		{PlanPath: invalidUTF8, SourcePath: "invalid.md"},
		{PlanPath: missingHeader, SourcePath: "missing-header.md"},
		{PlanPath: badSeparator, SourcePath: "bad-separator.md"},
		{PlanPath: badDifficulty, SourcePath: "bad-difficulty.md"},
	}
	for index, options := range tests {
		if _, err := GenerateFeatureMatrix(options); err == nil {
			t.Fatalf("invalid feature matrix case %d unexpectedly succeeded", index)
		}
	}
}

func TestGenerateLicenseLedgerRejectsInvalidManifestMetadata(t *testing.T) {
	tests := []LicenseLedgerOptions{
		{},
		{ManifestJSON: []byte(`{}`)},
		{ManifestJSON: []byte(`not-json`), SourcePath: "manifest.json"},
		{ManifestJSON: []byte(`{"schema_version":2}`), SourcePath: "manifest.json"},
		{ManifestJSON: []byte(`{"schema_version":1,"license":{"spdx":"MIT"}}`), SourcePath: "manifest.json"},
		{ManifestJSON: []byte(`{"schema_version":1,"repository":{"commit":"abc"},"license":{}}`), SourcePath: "manifest.json"},
	}
	for index, options := range tests {
		if _, err := GenerateLicenseLedger(options); err == nil {
			t.Fatalf("invalid license ledger case %d unexpectedly succeeded", index)
		}
	}
}

func TestDiscoverMarkdownDocumentsReturnsSortedMarkdownOnly(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, root, "b.MD", "# b\n")
	writeFixtureFile(t, root, "nested/a.md", "# a\n")
	writeFixtureFile(t, root, "nested/ignored.txt", "ignored\n")

	documents, err := DiscoverMarkdownDocuments(root)
	if err != nil {
		t.Fatalf("discover Markdown: %v", err)
	}
	if len(documents) != 2 || filepath.Base(documents[0]) != "b.MD" || filepath.Base(documents[1]) != "a.md" {
		t.Fatalf("documents = %#v", documents)
	}
}

func TestDiffManifestsRejectsInvalidAndDuplicateRecords(t *testing.T) {
	valid := Manifest{SchemaVersion: 1, Repository: Repository{Commit: "commit"}}
	tests := [][2]Manifest{
		{{}, valid},
		{{SchemaVersion: 1}, valid},
		{withFiles(valid, []FileRecord{{Path: "same"}, {Path: "same"}}), valid},
		{withPackages(valid, []PackageRecord{{Path: "same"}, {Path: "same"}}), valid},
	}
	for index, manifests := range tests {
		if _, err := DiffManifests(manifests[0], manifests[1]); err == nil {
			t.Fatalf("invalid manifest diff case %d unexpectedly succeeded", index)
		}
	}
}

func TestBuildP0BundleRejectsInvalidPathsAndDuplicates(t *testing.T) {
	content := []byte("valid\n")
	digest := digestBytes(content)
	tests := []struct {
		commit string
		inputs []BundleInput
	}{
		{commit: "", inputs: []BundleInput{{Path: "a", JSON: content, SHA256: digest}}},
		{commit: "commit"},
		{commit: "commit", inputs: []BundleInput{{Path: "../escape", JSON: content, SHA256: digest}}},
		{commit: "commit", inputs: []BundleInput{{Path: "same", JSON: content, SHA256: digest}, {Path: "same", JSON: content, SHA256: digest}}},
	}
	for index, test := range tests {
		if _, err := BuildP0Bundle(test.commit, test.inputs); err == nil {
			t.Fatalf("invalid bundle case %d unexpectedly succeeded", index)
		}
	}
}

func withFiles(manifest Manifest, files []FileRecord) Manifest {
	manifest.Files = files
	return manifest
}

func withPackages(manifest Manifest, packages []PackageRecord) Manifest {
	manifest.Packages = packages
	return manifest
}
