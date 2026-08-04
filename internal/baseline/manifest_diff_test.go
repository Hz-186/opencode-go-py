package baseline

import "testing"

func TestDiffManifestsReportsDeterministicFileAndPackageChanges(t *testing.T) {
	from := Manifest{
		SchemaVersion: 1,
		Repository:    Repository{Commit: "from"},
		Files: []FileRecord{
			{Path: "deleted.ts", ObjectID: "old-deleted", Kind: FileSource, Classification: ScopeShared},
			{Path: "modified.ts", ObjectID: "old", Kind: FileSource, Classification: ScopeCanonicalV2},
		},
		Packages: []PackageRecord{
			{Path: "packages/old/package.json", Name: "old", Workspace: true},
			{Path: "packages/shared/package.json", Name: "shared", Workspace: true, Dependencies: []DependencyRecord{{Name: "a", Constraint: "1"}}},
		},
	}
	to := Manifest{
		SchemaVersion: 1,
		Repository:    Repository{Commit: "to"},
		Files: []FileRecord{
			{Path: "added.ts", ObjectID: "new-added", Kind: FileSource, Classification: ScopeShared},
			{Path: "modified.ts", ObjectID: "new", Kind: FileSource, Classification: ScopeCanonicalV2},
		},
		Packages: []PackageRecord{
			{Path: "packages/new/package.json", Name: "new", Workspace: true},
			{Path: "packages/shared/package.json", Name: "shared", Workspace: true, Dependencies: []DependencyRecord{{Name: "a", Constraint: "2"}}},
		},
	}

	first, err := DiffManifests(from, to)
	if err != nil {
		t.Fatalf("diff manifests: %v", err)
	}
	second, err := DiffManifests(from, to)
	if err != nil {
		t.Fatalf("repeat manifest diff: %v", err)
	}
	if string(first.JSON) != string(second.JSON) || first.SHA256 != second.SHA256 {
		t.Fatal("manifest diff is not deterministic")
	}
	if first.Diff.Summary.FilesAdded != 1 || first.Diff.Summary.FilesDeleted != 1 || first.Diff.Summary.FilesModified != 1 {
		t.Fatalf("file summary = %#v", first.Diff.Summary)
	}
	if first.Diff.Files.Added[0].Path != "added.ts" || first.Diff.Files.Deleted[0].Path != "deleted.ts" || first.Diff.Files.Modified[0].Path != "modified.ts" {
		t.Fatalf("file changes = %#v", first.Diff.Files)
	}
	if first.Diff.Summary.PackagesAdded != 1 || first.Diff.Summary.PackagesDeleted != 1 || first.Diff.Summary.PackagesModified != 1 {
		t.Fatalf("package summary = %#v", first.Diff.Summary)
	}
	if first.Diff.Packages.Modified[0].Before.Dependencies[0].Constraint != "1" || first.Diff.Packages.Modified[0].After.Dependencies[0].Constraint != "2" {
		t.Fatalf("package modification = %#v", first.Diff.Packages.Modified[0])
	}
}

func TestDiffManifestsSelfDiffIsEmpty(t *testing.T) {
	manifest := Manifest{
		SchemaVersion: 1,
		Repository:    Repository{Commit: "same"},
		Files:         []FileRecord{{Path: "file.ts", ObjectID: "object"}},
		Packages:      []PackageRecord{{Path: "package.json", Name: "root"}},
	}

	result, err := DiffManifests(manifest, manifest)
	if err != nil {
		t.Fatalf("self diff: %v", err)
	}
	if result.Diff.Summary != (ManifestDiffSummary{}) {
		t.Fatalf("self diff summary = %#v", result.Diff.Summary)
	}
}
