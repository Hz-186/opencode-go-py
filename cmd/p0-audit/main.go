package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/Hz-186/opencode-go-py/internal/baseline"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	exitCode := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

func run(ctx context.Context, arguments []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("p0-audit", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repository := flags.String("repo", "", "path to the frozen upstream Git worktree")
	commit := flags.String("commit", "", "frozen upstream commit")
	branch := flags.String("branch", "dev", "expected upstream branch")
	plan := flags.String("plan", "", "path to the Markdown master plan")
	planSource := flags.String("plan-source", "doc/OPENCODE_REPLICA_MASTER_PLAN.md", "stable master plan label")
	documentRoot := flags.String("document-root", "", "root directory containing Markdown plans")
	outputDirectory := flags.String("output-dir", "", "destination directory for P0 audit artifacts")
	outputLabel := flags.String("output-label", "testdata/baseline", "stable output directory label stored in generated artifacts")
	lockPath := flags.String("lock", "bun.lock", "bun lock path in the frozen Git tree")
	licenseEvidence := flags.String("license-evidence", "", "path to canonical license evidence JSON")
	licenseEvidenceSource := flags.String("license-evidence-source", "testdata/baseline/license-evidence.json", "stable evidence label stored in generated artifacts")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if *outputDirectory == "" {
		fmt.Fprintln(stderr, "p0-audit: -output-dir is required")
		return 2
	}
	if *licenseEvidence == "" {
		fmt.Fprintln(stderr, "p0-audit: -license-evidence is required")
		return 2
	}

	manifest, err := baseline.Generate(ctx, baseline.Options{
		Repository: *repository, Commit: *commit, Branch: *branch,
		VersionPath: "packages/opencode/package.json", LicensePath: "LICENSE",
	})
	if err != nil {
		fmt.Fprintf(stderr, "p0-audit: generate baseline: %v\n", err)
		return 1
	}
	semantics, err := baseline.GenerateSemanticInventory(ctx, baseline.SemanticInventoryOptions{
		Repository: *repository, Commit: *commit, Branch: *branch,
	})
	if err != nil {
		fmt.Fprintf(stderr, "p0-audit: generate semantic inventory: %v\n", err)
		return 1
	}
	semanticDiff, err := baseline.DiffSemanticInventories(semantics.Inventory, semantics.Inventory)
	if err != nil {
		fmt.Fprintf(stderr, "p0-audit: generate semantic self-diff: %v\n", err)
		return 1
	}
	features, err := baseline.GenerateFeatureMatrix(baseline.FeatureMatrixOptions{
		PlanPath: *plan, SourcePath: *planSource,
	})
	if err != nil {
		fmt.Fprintf(stderr, "p0-audit: generate feature matrix: %v\n", err)
		return 1
	}
	shortCommit := manifest.Manifest.Repository.Commit
	if len(shortCommit) > 8 {
		shortCommit = shortCommit[:8]
	}
	manifestName := "upstream-" + shortCommit + ".json"
	lock, err := baseline.LoadFrozenBunLock(ctx, baseline.FrozenBunLockOptions{
		Repository: *repository, Commit: *commit, LockPath: *lockPath,
	})
	if err != nil {
		fmt.Fprintf(stderr, "p0-audit: load frozen bun lock: %v\n", err)
		return 1
	}
	evidenceJSON, err := os.ReadFile(*licenseEvidence)
	if err != nil {
		fmt.Fprintf(stderr, "p0-audit: read license evidence: %v\n", err)
		return 1
	}
	licenses, err := baseline.GenerateLicenseLedger(baseline.LicenseLedgerOptions{
		ManifestJSON: manifest.JSON, SourcePath: filepath.ToSlash(filepath.Join(*outputLabel, manifestName)),
		LockInventory: lock.Inventory, EvidenceJSON: evidenceJSON, EvidenceSourcePath: *licenseEvidenceSource,
	})
	if err != nil {
		fmt.Fprintf(stderr, "p0-audit: generate license ledger: %v\n", err)
		return 1
	}
	documents, err := baseline.DiscoverMarkdownDocuments(*documentRoot)
	if err != nil {
		fmt.Fprintf(stderr, "p0-audit: discover documents: %v\n", err)
		return 1
	}
	links, err := baseline.AuditSourceLinks(ctx, baseline.LinkAuditOptions{
		Repository: *repository, Commit: *commit, Branch: *branch,
		DocumentRoot: *documentRoot, Documents: documents,
	})
	if err != nil {
		fmt.Fprintf(stderr, "p0-audit: audit links: %v\n", err)
		return 1
	}
	if !links.Report.Valid {
		fmt.Fprintf(stderr, "p0-audit: %d invalid source links; run link-audit for the structured report\n", len(links.Report.Issues))
		return 1
	}

	artifacts := []struct {
		name   string
		result baseline.Result
	}{
		{name: manifestName, result: manifest},
		{name: "semantic-inventory.json", result: baseline.Result{JSON: semantics.JSON, SHA256: semantics.SHA256}},
		{name: "semantic-self-diff.json", result: baseline.Result{JSON: semanticDiff.JSON, SHA256: semanticDiff.SHA256}},
		{name: "feature-matrix.json", result: baseline.Result{JSON: features.JSON, SHA256: features.SHA256}},
		{name: "bun-lock-inventory.json", result: baseline.Result{JSON: lock.JSON, SHA256: lock.SHA256}},
		{name: "license-evidence.json", result: baseline.Result{JSON: evidenceJSON, SHA256: licenses.Ledger.EvidenceSource.SHA256}},
		{name: "license-ledger.json", result: baseline.Result{JSON: licenses.JSON, SHA256: licenses.SHA256}},
		{name: "source-link-audit.json", result: baseline.Result{JSON: links.JSON, SHA256: links.SHA256}},
	}
	bundleInputs := make([]baseline.BundleInput, 0, len(artifacts))
	for _, artifact := range artifacts {
		bundleInputs = append(bundleInputs, baseline.BundleInput{
			Path: artifact.name, JSON: artifact.result.JSON, SHA256: artifact.result.SHA256,
		})
	}
	bundle, err := baseline.BuildP0Bundle(manifest.Manifest.Repository.Commit, bundleInputs)
	if err != nil {
		fmt.Fprintf(stderr, "p0-audit: build bundle: %v\n", err)
		return 1
	}
	for _, artifact := range artifacts {
		if err := baseline.WriteSnapshot(filepath.Join(*outputDirectory, artifact.name), artifact.result); err != nil {
			fmt.Fprintf(stderr, "p0-audit: write %s: %v\n", artifact.name, err)
			return 1
		}
	}
	if err := baseline.WriteSnapshot(filepath.Join(*outputDirectory, "p0-bundle.json"), baseline.Result{JSON: bundle.JSON, SHA256: bundle.SHA256}); err != nil {
		fmt.Fprintf(stderr, "p0-audit: write bundle: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s  %s\n", bundle.SHA256, filepath.Join(*outputDirectory, "p0-bundle.json"))
	return 0
}
