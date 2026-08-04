package baseline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestParseBunLockInventoryExtractsDeterministicResolvedPackages(t *testing.T) {
	lockJSONC := []byte(`{
	  "lockfileVersion": 1,
	  "packages": {
	    "alpha": ["alpha@1.2.3", "", {}, "sha512-alpha"],
	    "consumer/alpha": ["alpha@2.0.0-beta.1", "", {}, "sha512-alpha-beta"],
	    "gitpkg": ["gitpkg@github:owner/repository#0123456", {}, "owner-repository-0123456", "sha512-git"],
	    "filepkg": ["filepkg@vendor/filepkg-1.0.0.tgz", {}, "sha512-file"],
	    "@fixture/workspace": ["@fixture/workspace@workspace:packages/workspace"],
	    "@solidjs/start": ["@solidjs/start@https://pkg.pr.new/@solidjs/start@dfb2020", {}, "sha512-url"],
	  },
	}`)

	first, err := ParseBunLockInventory(BunLockInventoryOptions{
		LockJSONC:  lockJSONC,
		SourcePath: "bun.lock",
	})
	if err != nil {
		t.Fatalf("parse bun lock inventory: %v", err)
	}
	second, err := ParseBunLockInventory(BunLockInventoryOptions{
		LockJSONC:  lockJSONC,
		SourcePath: "bun.lock",
	})
	if err != nil {
		t.Fatalf("repeat bun lock inventory: %v", err)
	}
	if string(first.JSON) != string(second.JSON) || first.SHA256 != second.SHA256 {
		t.Fatal("bun lock inventory is not deterministic")
	}
	if len(first.Inventory.Packages) != 6 {
		t.Fatalf("packages = %#v", first.Inventory.Packages)
	}
	want := []LockedPackage{
		{Key: "@fixture/workspace", Name: "@fixture/workspace", Locator: "@fixture/workspace@workspace:packages/workspace", SourceKind: DependencySourceWorkspace},
		{Key: "@solidjs/start", Name: "@solidjs/start", Locator: "@solidjs/start@https://pkg.pr.new/@solidjs/start@dfb2020", Integrity: "sha512-url", SourceKind: DependencySourceURL},
		{Key: "alpha", Name: "alpha", Locator: "alpha@1.2.3", Version: "1.2.3", Integrity: "sha512-alpha", SourceKind: DependencySourceRegistry},
		{Key: "consumer/alpha", Name: "alpha", Locator: "alpha@2.0.0-beta.1", Version: "2.0.0-beta.1", Integrity: "sha512-alpha-beta", SourceKind: DependencySourceRegistry},
		{Key: "filepkg", Name: "filepkg", Locator: "filepkg@vendor/filepkg-1.0.0.tgz", Integrity: "sha512-file", SourceKind: DependencySourceFile},
		{Key: "gitpkg", Name: "gitpkg", Locator: "gitpkg@github:owner/repository#0123456", Integrity: "sha512-git", SourceKind: DependencySourceGit},
	}
	for index := range want {
		if first.Inventory.Packages[index] != want[index] {
			t.Fatalf("package[%d] = %#v, want %#v", index, first.Inventory.Packages[index], want[index])
		}
	}
	digest := sha256.Sum256(lockJSONC)
	if first.Inventory.Source.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("source digest = %q", first.Inventory.Source.SHA256)
	}
	if first.Inventory.Source.Bytes != int64(len(lockJSONC)) {
		t.Fatalf("source bytes = %d", first.Inventory.Source.Bytes)
	}
}

func TestLoadFrozenBunLockReadsCommitInsteadOfRollingWorktree(t *testing.T) {
	repository, _ := newFixtureRepository(t)
	writeFixtureFile(t, repository, "bun.lock", `{"lockfileVersion":1,"packages":{"alpha":["alpha@1.2.3","",{},"sha512-frozen"]}}`)
	runGit(t, repository, "add", "bun.lock")
	runGit(t, repository, "commit", "-m", "add bun lock")
	commit := strings.TrimSpace(runGit(t, repository, "rev-parse", "HEAD"))
	writeFixtureFile(t, repository, "bun.lock", `{"lockfileVersion":1,"packages":{"alpha":["alpha@9.9.9","",{},"sha512-dirty"]}}`)

	result, err := LoadFrozenBunLock(context.Background(), FrozenBunLockOptions{
		Repository: repository,
		Commit:     commit,
		LockPath:   "bun.lock",
	})
	if err != nil {
		t.Fatalf("load frozen bun lock: %v", err)
	}
	if len(result.Inventory.Packages) != 1 || result.Inventory.Packages[0].Version != "1.2.3" {
		t.Fatalf("frozen packages = %#v", result.Inventory.Packages)
	}
}

func TestGenerateLicenseEvidenceVerifiesRegistryMetadataAgainstLockIntegrity(t *testing.T) {
	manifestJSON := licenseManifestFixture(t, []DependencyRecord{
		{Name: "alpha", Constraint: "^1.0.0", Kind: "runtime"},
		{Name: "filepkg", Constraint: "file:vendor/filepkg.tgz", Kind: "runtime"},
		{Name: "gitpkg", Constraint: "github:owner/repository#0123456", Kind: "development"},
		{Name: "@fixture/workspace", Constraint: "workspace:*", Kind: "runtime", Workspace: true},
	})
	lock := BunLockInventory{
		SchemaVersion:   1,
		ParserPolicy:    bunLockParserPolicy,
		Source:          SourceDocument{Path: "bun.lock", SHA256: "lock-sha256", Bytes: 100},
		LockfileVersion: 1,
		Packages: []LockedPackage{
			{Key: "alpha", Name: "alpha", Locator: "alpha@1.2.3", Version: "1.2.3", Integrity: "sha512-alpha", SourceKind: DependencySourceRegistry},
			{Key: "consumer/alpha", Name: "alpha", Locator: "alpha@2.0.0", Version: "2.0.0", Integrity: "sha512-alpha-v2", SourceKind: DependencySourceRegistry},
			{Key: "filepkg", Name: "filepkg", Locator: "filepkg@vendor/filepkg.tgz", Integrity: "sha512-file", SourceKind: DependencySourceFile},
			{Key: "gitpkg", Name: "gitpkg", Locator: "gitpkg@github:owner/repository#0123456", Integrity: "sha512-git", SourceKind: DependencySourceGit},
			{Key: "@fixture/workspace", Name: "@fixture/workspace", Locator: "@fixture/workspace@workspace:packages/workspace", SourceKind: DependencySourceWorkspace},
		},
	}
	fetched := make([]string, 0, 2)
	metadata := map[string]string{
		"alpha@1.2.3": `{"name":"alpha","version":"1.2.3","license":"MIT","dist":{"integrity":"sha512-alpha"}}`,
		"alpha@2.0.0": `{"name":"alpha","version":"2.0.0","license":"Apache-2.0","dist":{"integrity":"sha512-alpha-v2"}}`,
	}
	result, err := GenerateLicenseEvidence(context.Background(), LicenseEvidenceOptions{
		ManifestJSON:       manifestJSON,
		ManifestSourcePath: "baseline.json",
		LockInventory:      lock,
		RegistryURL:        "https://registry.npmjs.org",
		Concurrency:        1,
		FetchPackageVersion: func(_ context.Context, name string, version string) ([]byte, error) {
			key := name + "@" + version
			fetched = append(fetched, key)
			value, ok := metadata[key]
			if !ok {
				return nil, fmt.Errorf("unexpected registry request %s", key)
			}
			return []byte(value), nil
		},
	})
	if err != nil {
		t.Fatalf("generate license evidence: %v", err)
	}
	if !slices.Equal(fetched, []string{"alpha@1.2.3", "alpha@2.0.0"}) {
		t.Fatalf("registry requests = %#v", fetched)
	}
	if len(result.Evidence.Packages) != 4 {
		t.Fatalf("evidence packages = %#v", result.Evidence.Packages)
	}
	alphaV1 := result.Evidence.Packages[0]
	if alphaV1.Name != "alpha" || alphaV1.Version != "1.2.3" || alphaV1.License != "MIT" || alphaV1.LicenseStatus != LicenseVerified {
		t.Fatalf("alpha v1 evidence = %#v", alphaV1)
	}
	if alphaV1.RegistryIntegrity != alphaV1.LockIntegrity || alphaV1.SourceURL != "https://registry.npmjs.org/alpha/1.2.3" {
		t.Fatalf("alpha v1 source evidence = %#v", alphaV1)
	}
	if result.Evidence.Packages[2].SourceKind != DependencySourceFile || result.Evidence.Packages[2].LicenseStatus != LicenseUnresolved {
		t.Fatalf("file evidence = %#v", result.Evidence.Packages[2])
	}
	if result.Evidence.Packages[3].SourceKind != DependencySourceGit || result.Evidence.Packages[3].LicenseStatus != LicenseUnresolved {
		t.Fatalf("git evidence = %#v", result.Evidence.Packages[3])
	}
	if result.Evidence.ManifestSource.Path != "baseline.json" || result.Evidence.LockSource != lock.Source {
		t.Fatalf("evidence sources = %#v / %#v", result.Evidence.ManifestSource, result.Evidence.LockSource)
	}
}

func TestGenerateLicenseEvidenceRejectsRegistryIntegrityMismatch(t *testing.T) {
	manifestJSON := licenseManifestFixture(t, []DependencyRecord{{Name: "alpha", Constraint: "1.2.3", Kind: "runtime"}})
	_, err := GenerateLicenseEvidence(context.Background(), LicenseEvidenceOptions{
		ManifestJSON:       manifestJSON,
		ManifestSourcePath: "baseline.json",
		LockInventory: BunLockInventory{
			SchemaVersion: 1, ParserPolicy: bunLockParserPolicy,
			Source: SourceDocument{Path: "bun.lock", SHA256: "lock-sha256", Bytes: 100}, LockfileVersion: 1,
			Packages: []LockedPackage{{Key: "alpha", Name: "alpha", Locator: "alpha@1.2.3", Version: "1.2.3", Integrity: "sha512-lock", SourceKind: DependencySourceRegistry}},
		},
		RegistryURL: "https://registry.npmjs.org",
		FetchPackageVersion: func(context.Context, string, string) ([]byte, error) {
			return []byte(`{"name":"alpha","version":"1.2.3","license":"MIT","dist":{"integrity":"sha512-registry"}}`), nil
		},
	})
	if err == nil {
		t.Fatal("registry integrity mismatch unexpectedly succeeded")
	}
}

func TestGenerateLicenseEvidenceIsDeterministicWithConcurrentFetches(t *testing.T) {
	manifestJSON := licenseManifestFixture(t, []DependencyRecord{
		{Name: "alpha", Constraint: "1.2.3", Kind: "runtime"},
		{Name: "beta", Constraint: "2.0.0", Kind: "runtime"},
	})
	lock := BunLockInventory{
		SchemaVersion: 1, ParserPolicy: bunLockParserPolicy,
		Source: SourceDocument{Path: "bun.lock", SHA256: "lock-sha256", Bytes: 100}, LockfileVersion: 1,
		Packages: []LockedPackage{
			{Key: "alpha", Name: "alpha", Locator: "alpha@1.2.3", Version: "1.2.3", Integrity: "sha512-alpha", SourceKind: DependencySourceRegistry},
			{Key: "beta", Name: "beta", Locator: "beta@2.0.0", Version: "2.0.0", Integrity: "sha512-beta", SourceKind: DependencySourceRegistry},
		},
	}
	metadata := map[string][]byte{
		"alpha@1.2.3": []byte(`{"name":"alpha","version":"1.2.3","license":"MIT","dist":{"integrity":"sha512-alpha"}}`),
		"beta@2.0.0":  []byte(`{"name":"beta","version":"2.0.0","license":"ISC","dist":{"integrity":"sha512-beta"}}`),
	}
	options := LicenseEvidenceOptions{
		ManifestJSON: manifestJSON, ManifestSourcePath: "baseline.json", LockInventory: lock,
		RegistryURL: "https://registry.npmjs.org", Concurrency: 2,
		FetchPackageVersion: func(_ context.Context, name string, version string) ([]byte, error) {
			return metadata[name+"@"+version], nil
		},
	}
	first, err := GenerateLicenseEvidence(context.Background(), options)
	if err != nil {
		t.Fatalf("generate first evidence: %v", err)
	}
	second, err := GenerateLicenseEvidence(context.Background(), options)
	if err != nil {
		t.Fatalf("generate second evidence: %v", err)
	}
	if string(first.JSON) != string(second.JSON) || first.SHA256 != second.SHA256 {
		t.Fatal("concurrent license evidence output is not deterministic")
	}
}

func licenseManifestFixture(t *testing.T, dependencies []DependencyRecord) []byte {
	t.Helper()
	manifest := Manifest{
		SchemaVersion: 1,
		Repository:    Repository{Commit: "0123456789abcdef", Version: "1.0.0"},
		License:       LicenseRecord{Path: "LICENSE", SPDX: "MIT", SHA256: "license-hash", Bytes: 10},
		Packages:      []PackageRecord{{Path: "package.json", Name: "root", License: "MIT", Dependencies: dependencies}},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode manifest fixture: %v", err)
	}
	return encoded
}

func TestGenerateLicenseLedgerSeparatesWorkspaceAndAppliesVersionedEvidence(t *testing.T) {
	manifest := Manifest{
		SchemaVersion: 1,
		Repository:    Repository{Commit: "0123456789abcdef", Version: "1.0.0"},
		License:       LicenseRecord{Path: "LICENSE", SPDX: "MIT", SHA256: "license-hash", Bytes: 10},
		Packages: []PackageRecord{
			{
				Path: "package.json", Name: "root", License: "MIT",
				Dependencies: []DependencyRecord{
					{Name: "external", Constraint: "1.2.3", Kind: "runtime"},
					{Name: "git-external", Constraint: "github:owner/repository#0123456", Kind: "runtime"},
				},
			},
			{
				Path: "packages/core/package.json", Name: "@fixture/core", Workspace: true,
				Dependencies: []DependencyRecord{
					{Name: "@fixture/schema", Constraint: "workspace:*", Kind: "runtime", Workspace: true},
					{Name: "external", Constraint: "^1.0.0", Kind: "development"},
				},
			},
			{Path: "packages/schema/package.json", Name: "@fixture/schema", Workspace: true, License: "Apache-2.0"},
		},
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode manifest fixture: %v", err)
	}
	lock := BunLockInventory{
		SchemaVersion: 1, ParserPolicy: bunLockParserPolicy,
		Source: SourceDocument{Path: "bun.lock", SHA256: "lock-sha256", Bytes: 100}, LockfileVersion: 1,
		Packages: []LockedPackage{
			{Key: "external", Name: "external", Locator: "external@1.2.3", Version: "1.2.3", Integrity: "sha512-external", SourceKind: DependencySourceRegistry},
			{Key: "git-external", Name: "git-external", Locator: "git-external@github:owner/repository#0123456", Integrity: "sha512-git", SourceKind: DependencySourceGit},
		},
	}
	evidence, err := GenerateLicenseEvidence(context.Background(), LicenseEvidenceOptions{
		ManifestJSON: manifestJSON, ManifestSourcePath: "testdata/baseline/upstream.json",
		LockInventory: lock, RegistryURL: "https://registry.npmjs.org",
		FetchPackageVersion: func(context.Context, string, string) ([]byte, error) {
			return []byte(`{"name":"external","version":"1.2.3","license":"BSD-3-Clause","dist":{"integrity":"sha512-external"}}`), nil
		},
	})
	if err != nil {
		t.Fatalf("generate evidence fixture: %v", err)
	}

	result, err := GenerateLicenseLedger(LicenseLedgerOptions{
		ManifestJSON:       manifestJSON,
		SourcePath:         "testdata/baseline/upstream.json",
		LockInventory:      lock,
		EvidenceJSON:       evidence.JSON,
		EvidenceSourcePath: "testdata/baseline/license-evidence.json",
	})
	if err != nil {
		t.Fatalf("generate license ledger: %v", err)
	}
	if result.Ledger.BaselineCommit != manifest.Repository.Commit {
		t.Fatalf("baseline commit = %q", result.Ledger.BaselineCommit)
	}
	if len(result.Ledger.Packages) != 3 {
		t.Fatalf("packages = %#v", result.Ledger.Packages)
	}
	if result.Ledger.Packages[1].EffectiveLicense != "MIT" || result.Ledger.Packages[1].LicenseSource != "repository-root" {
		t.Fatalf("inherited workspace license = %#v", result.Ledger.Packages[1])
	}
	if result.Ledger.Packages[2].EffectiveLicense != "Apache-2.0" || result.Ledger.Packages[2].LicenseSource != "package.json" {
		t.Fatalf("declared workspace license = %#v", result.Ledger.Packages[2])
	}
	if len(result.Ledger.ExternalDependencies) != 2 {
		t.Fatalf("external dependencies = %#v", result.Ledger.ExternalDependencies)
	}
	external := result.Ledger.ExternalDependencies[0]
	if external.Name != "external" || external.LicenseStatus != LicenseVerified || external.License != "BSD-3-Clause" {
		t.Fatalf("external dependency = %#v", external)
	}
	if len(external.Constraints) != 2 || external.Constraints[0] != "1.2.3" || external.Constraints[1] != "^1.0.0" {
		t.Fatalf("external constraints = %#v", external.Constraints)
	}
	if len(external.Consumers) != 2 || external.Consumers[0] != "@fixture/core" || external.Consumers[1] != "root" {
		t.Fatalf("external consumers = %#v", external.Consumers)
	}
	if len(external.Kinds) != 2 || external.Kinds[0] != "development" || external.Kinds[1] != "runtime" {
		t.Fatalf("external kinds = %#v", external.Kinds)
	}
	if len(external.Resolutions) != 1 || external.Resolutions[0].Version != "1.2.3" || external.Resolutions[0].LockIntegrity != "sha512-external" {
		t.Fatalf("external resolutions = %#v", external.Resolutions)
	}
	gitExternal := result.Ledger.ExternalDependencies[1]
	if gitExternal.Name != "git-external" || gitExternal.LicenseStatus != LicenseUnresolved || len(gitExternal.Resolutions) != 1 {
		t.Fatalf("git dependency = %#v", gitExternal)
	}
	if result.Ledger.LockSource != lock.Source || result.Ledger.EvidenceSource.Path != "testdata/baseline/license-evidence.json" {
		t.Fatalf("ledger evidence sources = %#v / %#v", result.Ledger.LockSource, result.Ledger.EvidenceSource)
	}
}

func TestGenerateLicenseLedgerIsDeterministic(t *testing.T) {
	manifestJSON := []byte(`{"schema_version":1,"repository":{"commit":"abc","tree":"tree","branch":"dev","version":"1"},"license":{"path":"LICENSE","spdx":"MIT","sha256":"hash","bytes":1},"counts":{},"sources":[],"packages":[],"tests":[],"artifacts":[]}`)
	lock := BunLockInventory{
		SchemaVersion: 1, ParserPolicy: bunLockParserPolicy,
		Source: SourceDocument{Path: "bun.lock", SHA256: "lock-sha256", Bytes: 1}, LockfileVersion: 1,
		Packages: []LockedPackage{},
	}
	evidence, err := GenerateLicenseEvidence(context.Background(), LicenseEvidenceOptions{
		ManifestJSON: manifestJSON, ManifestSourcePath: "baseline.json", LockInventory: lock,
		RegistryURL: "https://registry.npmjs.org",
		FetchPackageVersion: func(context.Context, string, string) ([]byte, error) {
			return nil, errors.New("unexpected registry request")
		},
	})
	if err != nil {
		t.Fatalf("generate empty evidence: %v", err)
	}

	options := LicenseLedgerOptions{
		ManifestJSON: manifestJSON, SourcePath: "baseline.json", LockInventory: lock,
		EvidenceJSON: evidence.JSON, EvidenceSourcePath: "license-evidence.json",
	}
	first, err := GenerateLicenseLedger(options)
	if err != nil {
		t.Fatalf("generate first ledger: %v", err)
	}
	second, err := GenerateLicenseLedger(options)
	if err != nil {
		t.Fatalf("generate second ledger: %v", err)
	}
	if string(first.JSON) != string(second.JSON) || first.SHA256 != second.SHA256 {
		t.Fatal("license ledger output is not deterministic")
	}
}

func TestGenerateLicenseLedgerRejectsEvidenceForDifferentLock(t *testing.T) {
	manifestJSON := licenseManifestFixture(t, []DependencyRecord{{Name: "alpha", Constraint: "1.2.3", Kind: "runtime"}})
	lock := BunLockInventory{
		SchemaVersion: 1, ParserPolicy: bunLockParserPolicy,
		Source: SourceDocument{Path: "bun.lock", SHA256: "lock-sha256", Bytes: 100}, LockfileVersion: 1,
		Packages: []LockedPackage{{Key: "alpha", Name: "alpha", Locator: "alpha@1.2.3", Version: "1.2.3", Integrity: "sha512-alpha", SourceKind: DependencySourceRegistry}},
	}
	evidence, err := GenerateLicenseEvidence(context.Background(), LicenseEvidenceOptions{
		ManifestJSON: manifestJSON, ManifestSourcePath: "baseline.json", LockInventory: lock,
		RegistryURL: "https://registry.npmjs.org",
		FetchPackageVersion: func(context.Context, string, string) ([]byte, error) {
			return []byte(`{"name":"alpha","version":"1.2.3","license":"MIT","dist":{"integrity":"sha512-alpha"}}`), nil
		},
	})
	if err != nil {
		t.Fatalf("generate evidence fixture: %v", err)
	}
	evidence.Evidence.LockSource.SHA256 = "different-lock"
	staleJSON, err := json.MarshalIndent(evidence.Evidence, "", "  ")
	if err != nil {
		t.Fatalf("encode stale evidence: %v", err)
	}
	staleJSON = append(staleJSON, '\n')

	_, err = GenerateLicenseLedger(LicenseLedgerOptions{
		ManifestJSON: manifestJSON, SourcePath: "baseline.json", LockInventory: lock,
		EvidenceJSON: staleJSON, EvidenceSourcePath: "license-evidence.json",
	})
	if err == nil {
		t.Fatal("stale lock evidence unexpectedly succeeded")
	}
}
