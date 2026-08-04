package baseline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

type ManifestDiffResult struct {
	Diff   ManifestDiff
	JSON   []byte
	SHA256 string
}

type ManifestDiff struct {
	SchemaVersion   int                 `json:"schema_version"`
	From            Repository          `json:"from"`
	To              Repository          `json:"to"`
	FromScopePolicy string              `json:"from_scope_policy"`
	ToScopePolicy   string              `json:"to_scope_policy"`
	Summary         ManifestDiffSummary `json:"summary"`
	Files           FileChanges         `json:"files"`
	Packages        PackageChanges      `json:"packages"`
}

type ManifestDiffSummary struct {
	FilesAdded       int `json:"files_added"`
	FilesDeleted     int `json:"files_deleted"`
	FilesModified    int `json:"files_modified"`
	PackagesAdded    int `json:"packages_added"`
	PackagesDeleted  int `json:"packages_deleted"`
	PackagesModified int `json:"packages_modified"`
}

type FileChanges struct {
	Added    []FileRecord       `json:"added"`
	Deleted  []FileRecord       `json:"deleted"`
	Modified []FileModification `json:"modified"`
}

type FileModification struct {
	Path   string     `json:"path"`
	Before FileRecord `json:"before"`
	After  FileRecord `json:"after"`
}

type PackageChanges struct {
	Added    []PackageRecord       `json:"added"`
	Deleted  []PackageRecord       `json:"deleted"`
	Modified []PackageModification `json:"modified"`
}

type PackageModification struct {
	Path   string        `json:"path"`
	Before PackageRecord `json:"before"`
	After  PackageRecord `json:"after"`
}

func DiffManifests(from Manifest, to Manifest) (ManifestDiffResult, error) {
	if from.SchemaVersion != 1 || to.SchemaVersion != 1 {
		return ManifestDiffResult{}, fmt.Errorf("unsupported manifest schema versions from=%d to=%d", from.SchemaVersion, to.SchemaVersion)
	}
	if from.Repository.Commit == "" || to.Repository.Commit == "" {
		return ManifestDiffResult{}, errors.New("both manifests must contain a commit")
	}
	fileChanges, err := diffFiles(from.Files, to.Files)
	if err != nil {
		return ManifestDiffResult{}, err
	}
	packageChanges, err := diffPackages(from.Packages, to.Packages)
	if err != nil {
		return ManifestDiffResult{}, err
	}
	diff := ManifestDiff{
		SchemaVersion:   1,
		From:            from.Repository,
		To:              to.Repository,
		FromScopePolicy: from.ScopePolicy,
		ToScopePolicy:   to.ScopePolicy,
		Files:           fileChanges,
		Packages:        packageChanges,
		Summary: ManifestDiffSummary{
			FilesAdded:       len(fileChanges.Added),
			FilesDeleted:     len(fileChanges.Deleted),
			FilesModified:    len(fileChanges.Modified),
			PackagesAdded:    len(packageChanges.Added),
			PackagesDeleted:  len(packageChanges.Deleted),
			PackagesModified: len(packageChanges.Modified),
		},
	}
	encoded, err := json.MarshalIndent(diff, "", "  ")
	if err != nil {
		return ManifestDiffResult{}, fmt.Errorf("encode manifest diff: %w", err)
	}
	encoded = append(encoded, '\n')
	digest := sha256.Sum256(encoded)
	return ManifestDiffResult{
		Diff: diff, JSON: encoded, SHA256: hex.EncodeToString(digest[:]),
	}, nil
}

func diffFiles(from []FileRecord, to []FileRecord) (FileChanges, error) {
	fromByPath, err := fileRecordsByPath(from)
	if err != nil {
		return FileChanges{}, err
	}
	toByPath, err := fileRecordsByPath(to)
	if err != nil {
		return FileChanges{}, err
	}
	changes := FileChanges{
		Added: make([]FileRecord, 0), Deleted: make([]FileRecord, 0), Modified: make([]FileModification, 0),
	}
	for name, before := range fromByPath {
		after, found := toByPath[name]
		if !found {
			changes.Deleted = append(changes.Deleted, before)
			continue
		}
		if before != after {
			changes.Modified = append(changes.Modified, FileModification{Path: name, Before: before, After: after})
		}
	}
	for name, after := range toByPath {
		if _, found := fromByPath[name]; !found {
			changes.Added = append(changes.Added, after)
		}
	}
	slices.SortFunc(changes.Added, func(a, b FileRecord) int { return strings.Compare(a.Path, b.Path) })
	slices.SortFunc(changes.Deleted, func(a, b FileRecord) int { return strings.Compare(a.Path, b.Path) })
	slices.SortFunc(changes.Modified, func(a, b FileModification) int { return strings.Compare(a.Path, b.Path) })
	return changes, nil
}

func diffPackages(from []PackageRecord, to []PackageRecord) (PackageChanges, error) {
	fromByPath, err := packageRecordsByPath(from)
	if err != nil {
		return PackageChanges{}, err
	}
	toByPath, err := packageRecordsByPath(to)
	if err != nil {
		return PackageChanges{}, err
	}
	changes := PackageChanges{
		Added: make([]PackageRecord, 0), Deleted: make([]PackageRecord, 0), Modified: make([]PackageModification, 0),
	}
	for name, before := range fromByPath {
		after, found := toByPath[name]
		if !found {
			changes.Deleted = append(changes.Deleted, before)
			continue
		}
		if !equalPackageRecord(before, after) {
			changes.Modified = append(changes.Modified, PackageModification{Path: name, Before: before, After: after})
		}
	}
	for name, after := range toByPath {
		if _, found := fromByPath[name]; !found {
			changes.Added = append(changes.Added, after)
		}
	}
	slices.SortFunc(changes.Added, func(a, b PackageRecord) int { return strings.Compare(a.Path, b.Path) })
	slices.SortFunc(changes.Deleted, func(a, b PackageRecord) int { return strings.Compare(a.Path, b.Path) })
	slices.SortFunc(changes.Modified, func(a, b PackageModification) int { return strings.Compare(a.Path, b.Path) })
	return changes, nil
}

func fileRecordsByPath(records []FileRecord) (map[string]FileRecord, error) {
	result := make(map[string]FileRecord, len(records))
	for _, record := range records {
		if _, duplicate := result[record.Path]; duplicate {
			return nil, fmt.Errorf("duplicate file path %q", record.Path)
		}
		result[record.Path] = record
	}
	return result, nil
}

func packageRecordsByPath(records []PackageRecord) (map[string]PackageRecord, error) {
	result := make(map[string]PackageRecord, len(records))
	for _, record := range records {
		if _, duplicate := result[record.Path]; duplicate {
			return nil, fmt.Errorf("duplicate package path %q", record.Path)
		}
		result[record.Path] = record
	}
	return result, nil
}

func equalPackageRecord(left PackageRecord, right PackageRecord) bool {
	return left.Path == right.Path &&
		left.Name == right.Name &&
		left.Version == right.Version &&
		left.Private == right.Private &&
		left.License == right.License &&
		left.Workspace == right.Workspace &&
		slices.Equal(left.Dependencies, right.Dependencies)
}
