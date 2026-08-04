package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Hz-186/opencode-go-py/internal/baseline"
)

func main() {
	exitCode := run(os.Args[1:], os.Stdout, os.Stderr)
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

func run(arguments []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("license-ledger", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repository := flags.String("repo", "", "path to the frozen upstream Git worktree")
	commit := flags.String("commit", "", "frozen upstream commit")
	lockPath := flags.String("lock", "bun.lock", "bun lock path in the frozen Git tree")
	manifest := flags.String("manifest", "", "path to the generated baseline manifest")
	source := flags.String("source", "testdata/baseline/upstream-89130db6.json", "stable manifest label stored in the ledger")
	evidence := flags.String("evidence", "", "path to canonical license evidence JSON")
	evidenceSource := flags.String("evidence-source", "testdata/baseline/license-evidence.json", "stable evidence label stored in the ledger")
	output := flags.String("output", "", "destination license ledger JSON path")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if *output == "" {
		fmt.Fprintln(stderr, "license-ledger: -output is required")
		return 2
	}
	if *evidence == "" {
		fmt.Fprintln(stderr, "license-ledger: -evidence is required")
		return 2
	}
	content, err := os.ReadFile(*manifest)
	if err != nil {
		fmt.Fprintf(stderr, "license-ledger: read manifest: %v\n", err)
		return 1
	}
	lock, err := baseline.LoadFrozenBunLock(context.Background(), baseline.FrozenBunLockOptions{
		Repository: *repository, Commit: *commit, LockPath: *lockPath,
	})
	if err != nil {
		fmt.Fprintf(stderr, "license-ledger: %v\n", err)
		return 1
	}
	evidenceJSON, err := os.ReadFile(*evidence)
	if err != nil {
		fmt.Fprintf(stderr, "license-ledger: read evidence: %v\n", err)
		return 1
	}
	result, err := baseline.GenerateLicenseLedger(baseline.LicenseLedgerOptions{
		ManifestJSON: content, SourcePath: *source, LockInventory: lock.Inventory,
		EvidenceJSON: evidenceJSON, EvidenceSourcePath: *evidenceSource,
	})
	if err != nil {
		fmt.Fprintf(stderr, "license-ledger: %v\n", err)
		return 1
	}
	if err := baseline.WriteSnapshot(*output, baseline.Result{JSON: result.JSON, SHA256: result.SHA256}); err != nil {
		fmt.Fprintf(stderr, "license-ledger: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s  %s\n", result.SHA256, *output)
	return 0
}
