package baseline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

type PackageVersionFetcher func(ctx context.Context, name string, version string) ([]byte, error)

type LicenseEvidenceOptions struct {
	ManifestJSON        []byte
	ManifestSourcePath  string
	LockInventory       BunLockInventory
	RegistryURL         string
	FetchPackageVersion PackageVersionFetcher
	Concurrency         int
}

type LicenseEvidenceResult struct {
	Evidence LicenseEvidence
	JSON     []byte
	SHA256   string
}

type LicenseEvidence struct {
	SchemaVersion  int                      `json:"schema_version"`
	RegistryURL    string                   `json:"registry_url"`
	ManifestSource SourceDocument           `json:"manifest_source"`
	LockSource     SourceDocument           `json:"lock_source"`
	Packages       []PackageLicenseEvidence `json:"packages"`
}

type PackageLicenseEvidence struct {
	Name              string               `json:"name"`
	Version           string               `json:"version,omitempty"`
	Locator           string               `json:"locator,omitempty"`
	SourceKind        DependencySourceKind `json:"source_kind"`
	LockKeys          []string             `json:"lock_keys,omitempty"`
	LockIntegrity     string               `json:"lock_integrity,omitempty"`
	RegistryIntegrity string               `json:"registry_integrity,omitempty"`
	MetadataSHA256    string               `json:"registry_metadata_sha256,omitempty"`
	License           string               `json:"license,omitempty"`
	LicenseStatus     LicenseStatus        `json:"license_status"`
	SourceURL         string               `json:"source_url,omitempty"`
	UnresolvedReason  string               `json:"unresolved_reason,omitempty"`
}

type registryPackageVersion struct {
	Name    string          `json:"name"`
	Version string          `json:"version"`
	License json.RawMessage `json:"license"`
	Dist    struct {
		Integrity string `json:"integrity"`
	} `json:"dist"`
}

type packageEvidenceBuilder struct {
	record PackageLicenseEvidence
	keys   map[string]struct{}
}

func GenerateLicenseEvidence(ctx context.Context, options LicenseEvidenceOptions) (LicenseEvidenceResult, error) {
	manifest, err := decodeLicenseManifest(options.ManifestJSON)
	if err != nil {
		return LicenseEvidenceResult{}, err
	}
	if options.ManifestSourcePath == "" {
		return LicenseEvidenceResult{}, errors.New("baseline manifest source label is required")
	}
	if err := validateBunLockInventory(options.LockInventory); err != nil {
		return LicenseEvidenceResult{}, err
	}
	registryURL, err := normalizeRegistryURL(options.RegistryURL)
	if err != nil {
		return LicenseEvidenceResult{}, err
	}
	if options.FetchPackageVersion == nil {
		return LicenseEvidenceResult{}, errors.New("registry package version fetcher is required")
	}

	externalNames := externalDependencyNames(manifest)
	builders := make(map[string]*packageEvidenceBuilder)
	resolvedNames := make(map[string]struct{}, len(externalNames))
	for _, locked := range options.LockInventory.Packages {
		if _, external := externalNames[locked.Name]; !external || locked.SourceKind == DependencySourceWorkspace {
			continue
		}
		identity := strings.Join([]string{locked.Name, locked.Locator, locked.Integrity}, "\x00")
		builder := builders[identity]
		if builder == nil {
			builder = &packageEvidenceBuilder{
				record: PackageLicenseEvidence{
					Name: locked.Name, Version: locked.Version, Locator: locked.Locator,
					SourceKind: locked.SourceKind, LockIntegrity: locked.Integrity,
					LicenseStatus: LicenseUnresolved,
				},
				keys: make(map[string]struct{}),
			}
			builders[identity] = builder
		}
		builder.keys[locked.Key] = struct{}{}
		resolvedNames[locked.Name] = struct{}{}
	}
	for name := range externalNames {
		if _, resolved := resolvedNames[name]; resolved {
			continue
		}
		identity := name + "\x00"
		builders[identity] = &packageEvidenceBuilder{
			record: PackageLicenseEvidence{
				Name: name, SourceKind: DependencySourceUnknown,
				LicenseStatus: LicenseUnresolved, UnresolvedReason: "missing-lock-resolution",
			},
			keys: make(map[string]struct{}),
		}
	}

	packages := make([]PackageLicenseEvidence, 0, len(builders))
	for _, builder := range builders {
		builder.record.LockKeys = sortedSet(builder.keys)
		packages = append(packages, builder.record)
	}
	sortPackageLicenseEvidence(packages)
	if err := resolvePackageLicenses(ctx, registryURL, options.FetchPackageVersion, packages, options.Concurrency); err != nil {
		return LicenseEvidenceResult{}, err
	}

	manifestDigest := sha256.Sum256(options.ManifestJSON)
	evidence := LicenseEvidence{
		SchemaVersion: 1,
		RegistryURL:   registryURL,
		ManifestSource: SourceDocument{
			Path:   filepath.ToSlash(options.ManifestSourcePath),
			SHA256: hex.EncodeToString(manifestDigest[:]),
			Bytes:  int64(len(options.ManifestJSON)),
		},
		LockSource: options.LockInventory.Source,
		Packages:   packages,
	}
	encoded, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return LicenseEvidenceResult{}, fmt.Errorf("encode license evidence: %w", err)
	}
	encoded = append(encoded, '\n')
	return LicenseEvidenceResult{Evidence: evidence, JSON: encoded, SHA256: digestBytes(encoded)}, nil
}

func resolvePackageLicenses(ctx context.Context, registryURL string, fetch PackageVersionFetcher, packages []PackageLicenseEvidence, concurrency int) error {
	if concurrency <= 0 {
		concurrency = 8
	}
	if concurrency > len(packages) {
		concurrency = len(packages)
	}
	if concurrency == 0 {
		return nil
	}
	jobs := make(chan int)
	errorsByIndex := make([]error, len(packages))
	var workers sync.WaitGroup
	workers.Add(concurrency)
	for range concurrency {
		go func() {
			defer workers.Done()
			for index := range jobs {
				errorsByIndex[index] = resolvePackageLicense(ctx, registryURL, fetch, &packages[index])
			}
		}()
	}
	for index := range packages {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	for _, err := range errorsByIndex {
		if err != nil {
			return err
		}
	}
	return nil
}

func validateLicenseEvidence(content []byte, manifestJSON []byte, manifestSourcePath string, lock BunLockInventory) (LicenseEvidence, error) {
	if len(content) == 0 {
		return LicenseEvidence{}, errors.New("license evidence JSON is required")
	}
	var evidence LicenseEvidence
	if err := json.Unmarshal(content, &evidence); err != nil {
		return LicenseEvidence{}, fmt.Errorf("decode license evidence: %w", err)
	}
	if evidence.SchemaVersion != 1 {
		return LicenseEvidence{}, fmt.Errorf("unsupported license evidence schema version %d", evidence.SchemaVersion)
	}
	registryURL, err := normalizeRegistryURL(evidence.RegistryURL)
	if err != nil {
		return LicenseEvidence{}, err
	}
	if registryURL != evidence.RegistryURL {
		return LicenseEvidence{}, errors.New("license evidence registry URL is not canonical")
	}
	manifestDigest := sha256.Sum256(manifestJSON)
	wantManifestSource := SourceDocument{
		Path:   filepath.ToSlash(manifestSourcePath),
		SHA256: hex.EncodeToString(manifestDigest[:]),
		Bytes:  int64(len(manifestJSON)),
	}
	if evidence.ManifestSource != wantManifestSource {
		return LicenseEvidence{}, fmt.Errorf("license evidence manifest source mismatch: got=%#v want=%#v", evidence.ManifestSource, wantManifestSource)
	}
	if evidence.LockSource != lock.Source {
		return LicenseEvidence{}, fmt.Errorf("license evidence lock source mismatch: got=%#v want=%#v", evidence.LockSource, lock.Source)
	}

	manifest, err := decodeLicenseManifest(manifestJSON)
	if err != nil {
		return LicenseEvidence{}, err
	}
	externalNames := externalDependencyNames(manifest)
	type expectedResolution struct {
		keys map[string]struct{}
	}
	expected := make(map[string]*expectedResolution)
	expectedByName := make(map[string]int)
	for _, locked := range lock.Packages {
		if _, external := externalNames[locked.Name]; !external || locked.SourceKind == DependencySourceWorkspace {
			continue
		}
		identity := licenseEvidenceIdentity(locked.Name, locked.Locator, locked.Integrity)
		resolution := expected[identity]
		if resolution == nil {
			resolution = &expectedResolution{keys: make(map[string]struct{})}
			expected[identity] = resolution
			expectedByName[locked.Name]++
		}
		resolution.keys[locked.Key] = struct{}{}
	}

	covered := make(map[string]struct{}, len(evidence.Packages))
	coveredNames := make(map[string]int, len(externalNames))
	for index, record := range evidence.Packages {
		if index > 0 && comparePackageLicenseEvidence(evidence.Packages[index-1], record) >= 0 {
			return LicenseEvidence{}, fmt.Errorf("license evidence packages are not strictly sorted at index %d", index)
		}
		if _, external := externalNames[record.Name]; !external {
			return LicenseEvidence{}, fmt.Errorf("license evidence contains non-external package %q", record.Name)
		}
		if record.LicenseStatus != LicenseVerified && record.LicenseStatus != LicenseUnresolved {
			return LicenseEvidence{}, fmt.Errorf("license evidence for %s has invalid status %q", record.Name, record.LicenseStatus)
		}
		identity := licenseEvidenceIdentity(record.Name, record.Locator, record.LockIntegrity)
		if _, duplicate := covered[identity]; duplicate {
			return LicenseEvidence{}, fmt.Errorf("duplicate license evidence for %s", record.Name)
		}
		covered[identity] = struct{}{}
		coveredNames[record.Name]++

		if record.Locator == "" {
			if record.SourceKind != DependencySourceUnknown || record.UnresolvedReason != "missing-lock-resolution" || expectedByName[record.Name] != 0 {
				return LicenseEvidence{}, fmt.Errorf("invalid missing-lock evidence for %s", record.Name)
			}
			if len(record.LockKeys) != 0 || record.LicenseStatus != LicenseUnresolved {
				return LicenseEvidence{}, fmt.Errorf("missing-lock evidence for %s must remain unresolved", record.Name)
			}
			continue
		}

		resolution := expected[identity]
		if resolution == nil {
			return LicenseEvidence{}, fmt.Errorf("license evidence for %s is not present in frozen lock", record.Name)
		}
		if !slices.Equal(record.LockKeys, sortedSet(resolution.keys)) {
			return LicenseEvidence{}, fmt.Errorf("license evidence lock keys mismatch for %s", record.Name)
		}
		switch record.SourceKind {
		case DependencySourceRegistry:
			if record.Version == "" || record.LockIntegrity == "" || record.RegistryIntegrity != record.LockIntegrity || record.MetadataSHA256 == "" {
				return LicenseEvidence{}, fmt.Errorf("registry evidence for %s is incomplete", record.Name)
			}
			if record.SourceURL != registryPackageVersionURL(registryURL, record.Name, record.Version) {
				return LicenseEvidence{}, fmt.Errorf("registry evidence source URL mismatch for %s@%s", record.Name, record.Version)
			}
			if record.LicenseStatus == LicenseVerified && record.License == "" {
				return LicenseEvidence{}, fmt.Errorf("verified registry evidence for %s@%s has no license", record.Name, record.Version)
			}
			if record.LicenseStatus == LicenseUnresolved && record.UnresolvedReason == "" {
				return LicenseEvidence{}, fmt.Errorf("unresolved registry evidence for %s@%s has no reason", record.Name, record.Version)
			}
		case DependencySourceFile, DependencySourceGit, DependencySourceURL, DependencySourceUnknown:
			if record.LicenseStatus != LicenseUnresolved || record.License != "" || record.UnresolvedReason == "" {
				return LicenseEvidence{}, fmt.Errorf("non-registry evidence for %s must remain unresolved", record.Name)
			}
		case DependencySourceWorkspace:
			return LicenseEvidence{}, fmt.Errorf("workspace package %s appears in external license evidence", record.Name)
		default:
			return LicenseEvidence{}, fmt.Errorf("unsupported dependency source kind %q for %s", record.SourceKind, record.Name)
		}
	}
	for name := range externalNames {
		if coveredNames[name] == 0 {
			return LicenseEvidence{}, fmt.Errorf("license evidence is missing external dependency %s", name)
		}
	}
	for identity := range expected {
		if _, ok := covered[identity]; !ok {
			return LicenseEvidence{}, fmt.Errorf("license evidence is missing frozen lock resolution %q", identity)
		}
	}
	canonical, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return LicenseEvidence{}, fmt.Errorf("encode canonical license evidence: %w", err)
	}
	canonical = append(canonical, '\n')
	if !bytes.Equal(content, canonical) {
		return LicenseEvidence{}, errors.New("license evidence JSON is not canonical")
	}
	return evidence, nil
}

func decodeLicenseManifest(content []byte) (Manifest, error) {
	if len(content) == 0 {
		return Manifest{}, errors.New("baseline manifest JSON is required")
	}
	var manifest Manifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode baseline manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 {
		return Manifest{}, fmt.Errorf("unsupported baseline manifest schema version %d", manifest.SchemaVersion)
	}
	if manifest.Repository.Commit == "" {
		return Manifest{}, errors.New("baseline manifest commit is missing")
	}
	if manifest.License.SPDX == "" {
		return Manifest{}, errors.New("baseline root license SPDX is missing")
	}
	return manifest, nil
}

func validateBunLockInventory(inventory BunLockInventory) error {
	if inventory.SchemaVersion != 1 {
		return fmt.Errorf("unsupported bun lock inventory schema version %d", inventory.SchemaVersion)
	}
	if inventory.ParserPolicy != bunLockParserPolicy {
		return fmt.Errorf("unsupported bun lock parser policy %q", inventory.ParserPolicy)
	}
	if inventory.LockfileVersion <= 0 {
		return fmt.Errorf("unsupported bun lockfile version %d", inventory.LockfileVersion)
	}
	if inventory.Source.Path == "" || inventory.Source.SHA256 == "" || inventory.Source.Bytes <= 0 {
		return errors.New("bun lock source evidence is incomplete")
	}
	return nil
}

func normalizeRegistryURL(value string) (string, error) {
	value = strings.TrimSuffix(strings.TrimSpace(value), "/")
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("invalid HTTPS registry URL %q", value)
	}
	return value, nil
}

func externalDependencyNames(manifest Manifest) map[string]struct{} {
	names := make(map[string]struct{})
	for _, packageRecord := range manifest.Packages {
		for _, dependency := range packageRecord.Dependencies {
			if !dependency.Workspace {
				names[dependency.Name] = struct{}{}
			}
		}
	}
	return names
}

func resolvePackageLicense(ctx context.Context, registryURL string, fetch PackageVersionFetcher, evidence *PackageLicenseEvidence) error {
	switch evidence.SourceKind {
	case DependencySourceRegistry:
		if evidence.Version == "" || evidence.LockIntegrity == "" {
			return fmt.Errorf("registry package %s has incomplete lock evidence", evidence.Name)
		}
		content, err := fetch(ctx, evidence.Name, evidence.Version)
		if err != nil {
			return fmt.Errorf("fetch registry metadata for %s@%s: %w", evidence.Name, evidence.Version, err)
		}
		var metadata registryPackageVersion
		if err := json.Unmarshal(content, &metadata); err != nil {
			return fmt.Errorf("decode registry metadata for %s@%s: %w", evidence.Name, evidence.Version, err)
		}
		if metadata.Name != evidence.Name || metadata.Version != evidence.Version {
			return fmt.Errorf("registry identity mismatch for %s@%s: got %s@%s", evidence.Name, evidence.Version, metadata.Name, metadata.Version)
		}
		if metadata.Dist.Integrity != evidence.LockIntegrity {
			return fmt.Errorf("registry integrity mismatch for %s@%s: lock=%s registry=%s", evidence.Name, evidence.Version, evidence.LockIntegrity, metadata.Dist.Integrity)
		}
		metadataDigest := sha256.Sum256(content)
		evidence.RegistryIntegrity = metadata.Dist.Integrity
		evidence.MetadataSHA256 = hex.EncodeToString(metadataDigest[:])
		evidence.SourceURL = registryPackageVersionURL(registryURL, evidence.Name, evidence.Version)
		license := decodeRegistryLicense(metadata.License)
		if license == "" {
			evidence.UnresolvedReason = "registry-license-missing-or-unsupported"
			return nil
		}
		evidence.License = license
		evidence.LicenseStatus = LicenseVerified
		return nil
	case DependencySourceFile, DependencySourceGit, DependencySourceURL, DependencySourceUnknown:
		if evidence.UnresolvedReason == "" {
			evidence.UnresolvedReason = "non-registry-source"
		}
		return nil
	case DependencySourceWorkspace:
		return fmt.Errorf("workspace package %s must not appear in external license evidence", evidence.Name)
	default:
		return fmt.Errorf("unsupported dependency source kind %q for %s", evidence.SourceKind, evidence.Name)
	}
}

func registryPackageVersionURL(registryURL string, name string, version string) string {
	return registryURL + "/" + url.PathEscape(name) + "/" + url.PathEscape(version)
}

func decodeRegistryLicense(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var license string
	if err := json.Unmarshal(raw, &license); err == nil {
		return strings.TrimSpace(license)
	}
	var legacy struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &legacy); err == nil {
		return strings.TrimSpace(legacy.Type)
	}
	return ""
}

func sortPackageLicenseEvidence(packages []PackageLicenseEvidence) {
	slices.SortFunc(packages, comparePackageLicenseEvidence)
}

func comparePackageLicenseEvidence(a PackageLicenseEvidence, b PackageLicenseEvidence) int {
	if compared := strings.Compare(a.Name, b.Name); compared != 0 {
		return compared
	}
	if compared := strings.Compare(a.Version, b.Version); compared != 0 {
		return compared
	}
	if compared := strings.Compare(a.Locator, b.Locator); compared != 0 {
		return compared
	}
	return strings.Compare(a.LockIntegrity, b.LockIntegrity)
}

func licenseEvidenceIdentity(name string, locator string, integrity string) string {
	return strings.Join([]string{name, locator, integrity}, "\x00")
}
