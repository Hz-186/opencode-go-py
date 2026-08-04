package baseline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

type LicenseLedgerOptions struct {
	ManifestJSON       []byte
	SourcePath         string
	LockInventory      BunLockInventory
	EvidenceJSON       []byte
	EvidenceSourcePath string
}

type LicenseLedgerResult struct {
	Ledger LicenseLedger
	JSON   []byte
	SHA256 string
}

type LicenseLedger struct {
	SchemaVersion        int                         `json:"schema_version"`
	Source               SourceDocument              `json:"source"`
	BaselineCommit       string                      `json:"baseline_commit"`
	BaselineVersion      string                      `json:"baseline_version"`
	RootLicense          LicenseRecord               `json:"root_license"`
	LockSource           SourceDocument              `json:"lock_source"`
	EvidenceSource       SourceDocument              `json:"evidence_source"`
	Packages             []PackageLicenseRecord      `json:"packages"`
	ExternalDependencies []ExternalDependencyLicense `json:"external_dependencies"`
}

type PackageLicenseRecord struct {
	Path             string `json:"path"`
	Name             string `json:"name"`
	Workspace        bool   `json:"workspace"`
	DeclaredLicense  string `json:"declared_license,omitempty"`
	EffectiveLicense string `json:"effective_license"`
	LicenseSource    string `json:"license_source"`
}

type LicenseStatus string

const (
	LicenseUnresolved LicenseStatus = "unresolved"
	LicenseVerified   LicenseStatus = "verified"
)

type ExternalDependencyLicense struct {
	Name          string                   `json:"name"`
	Constraints   []string                 `json:"constraints"`
	Kinds         []string                 `json:"kinds"`
	Consumers     []string                 `json:"consumers"`
	License       string                   `json:"license,omitempty"`
	LicenseStatus LicenseStatus            `json:"license_status"`
	Resolutions   []PackageLicenseEvidence `json:"resolutions"`
}

type dependencyEvidence struct {
	constraints map[string]struct{}
	kinds       map[string]struct{}
	consumers   map[string]struct{}
}

func GenerateLicenseLedger(options LicenseLedgerOptions) (LicenseLedgerResult, error) {
	if options.SourcePath == "" {
		return LicenseLedgerResult{}, errors.New("baseline manifest source label is required")
	}
	if options.EvidenceSourcePath == "" {
		return LicenseLedgerResult{}, errors.New("license evidence source label is required")
	}
	manifest, err := decodeLicenseManifest(options.ManifestJSON)
	if err != nil {
		return LicenseLedgerResult{}, err
	}
	if err := validateBunLockInventory(options.LockInventory); err != nil {
		return LicenseLedgerResult{}, err
	}
	evidence, err := validateLicenseEvidence(options.EvidenceJSON, options.ManifestJSON, options.SourcePath, options.LockInventory)
	if err != nil {
		return LicenseLedgerResult{}, err
	}

	packages := make([]PackageLicenseRecord, 0, len(manifest.Packages))
	external := make(map[string]*dependencyEvidence)
	for _, packageRecord := range manifest.Packages {
		effective := packageRecord.License
		source := "package.json"
		if effective == "" {
			effective = manifest.License.SPDX
			source = "repository-root"
		}
		packages = append(packages, PackageLicenseRecord{
			Path:             packageRecord.Path,
			Name:             packageRecord.Name,
			Workspace:        packageRecord.Workspace,
			DeclaredLicense:  packageRecord.License,
			EffectiveLicense: effective,
			LicenseSource:    source,
		})
		for _, dependency := range packageRecord.Dependencies {
			if dependency.Workspace {
				continue
			}
			evidence := external[dependency.Name]
			if evidence == nil {
				evidence = &dependencyEvidence{
					constraints: make(map[string]struct{}),
					kinds:       make(map[string]struct{}),
					consumers:   make(map[string]struct{}),
				}
				external[dependency.Name] = evidence
			}
			evidence.constraints[dependency.Constraint] = struct{}{}
			evidence.kinds[dependency.Kind] = struct{}{}
			evidence.consumers[packageRecord.Name] = struct{}{}
		}
	}
	slices.SortFunc(packages, func(a, b PackageLicenseRecord) int { return strings.Compare(a.Path, b.Path) })
	dependencies := make([]ExternalDependencyLicense, 0, len(external))
	evidenceByName := make(map[string][]PackageLicenseEvidence)
	for _, record := range evidence.Packages {
		evidenceByName[record.Name] = append(evidenceByName[record.Name], record)
	}
	for name, evidence := range external {
		resolutions := evidenceByName[name]
		status := LicenseVerified
		licenses := make(map[string]struct{})
		for _, resolution := range resolutions {
			if resolution.LicenseStatus != LicenseVerified {
				status = LicenseUnresolved
			}
			if resolution.License != "" {
				licenses[resolution.License] = struct{}{}
			}
		}
		license := ""
		if len(licenses) == 1 {
			for value := range licenses {
				license = value
			}
		}
		dependencies = append(dependencies, ExternalDependencyLicense{
			Name:          name,
			Constraints:   sortedSet(evidence.constraints),
			Kinds:         sortedSet(evidence.kinds),
			Consumers:     sortedSet(evidence.consumers),
			License:       license,
			LicenseStatus: status,
			Resolutions:   resolutions,
		})
	}
	slices.SortFunc(dependencies, func(a, b ExternalDependencyLicense) int { return strings.Compare(a.Name, b.Name) })
	sourceDigest := sha256.Sum256(options.ManifestJSON)
	ledger := LicenseLedger{
		SchemaVersion: 2,
		Source: SourceDocument{
			Path:   filepath.ToSlash(options.SourcePath),
			SHA256: hex.EncodeToString(sourceDigest[:]),
			Bytes:  int64(len(options.ManifestJSON)),
		},
		BaselineCommit:  manifest.Repository.Commit,
		BaselineVersion: manifest.Repository.Version,
		RootLicense:     manifest.License,
		LockSource:      options.LockInventory.Source,
		EvidenceSource: SourceDocument{
			Path: filepath.ToSlash(options.EvidenceSourcePath), SHA256: digestBytes(options.EvidenceJSON), Bytes: int64(len(options.EvidenceJSON)),
		},
		Packages:             packages,
		ExternalDependencies: dependencies,
	}
	encoded, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return LicenseLedgerResult{}, fmt.Errorf("encode license ledger: %w", err)
	}
	encoded = append(encoded, '\n')
	digest := sha256.Sum256(encoded)
	return LicenseLedgerResult{
		Ledger: ledger,
		JSON:   encoded,
		SHA256: hex.EncodeToString(digest[:]),
	}, nil
}

func sortedSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	slices.Sort(result)
	return result
}
