package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/Hz-186/opencode-go-py/internal/baseline"
)

const maxRegistryMetadataBytes = 8 << 20

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	exitCode := run(ctx, os.Args[1:], os.Stdout, os.Stderr, &http.Client{Timeout: 30 * time.Second})
	stop()
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

func run(ctx context.Context, arguments []string, stdout io.Writer, stderr io.Writer, client *http.Client) int {
	flags := flag.NewFlagSet("license-evidence", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repository := flags.String("repo", "", "path to the frozen upstream Git worktree")
	commit := flags.String("commit", "", "frozen upstream commit")
	lockPath := flags.String("lock", "bun.lock", "bun lock path in the frozen Git tree")
	manifestPath := flags.String("manifest", "", "path to the generated baseline manifest")
	manifestSource := flags.String("manifest-source", "testdata/baseline/upstream-89130db6.json", "stable manifest label stored in the evidence")
	registry := flags.String("registry", "https://registry.npmjs.org", "HTTPS npm registry base URL")
	workers := flags.Int("workers", 8, "maximum concurrent registry requests (1-64)")
	output := flags.String("output", "", "destination license evidence JSON path")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if *output == "" {
		fmt.Fprintln(stderr, "license-evidence: -output is required")
		return 2
	}
	if *workers < 1 || *workers > 64 {
		fmt.Fprintln(stderr, "license-evidence: -workers must be between 1 and 64")
		return 2
	}
	if client == nil {
		fmt.Fprintln(stderr, "license-evidence: HTTP client is required")
		return 1
	}
	manifestJSON, err := os.ReadFile(*manifestPath)
	if err != nil {
		fmt.Fprintf(stderr, "license-evidence: read manifest: %v\n", err)
		return 1
	}
	lock, err := baseline.LoadFrozenBunLock(ctx, baseline.FrozenBunLockOptions{
		Repository: *repository, Commit: *commit, LockPath: *lockPath,
	})
	if err != nil {
		fmt.Fprintf(stderr, "license-evidence: %v\n", err)
		return 1
	}
	fetch := registryFetcher(client, strings.TrimSuffix(*registry, "/"))
	evidence, err := baseline.GenerateLicenseEvidence(ctx, baseline.LicenseEvidenceOptions{
		ManifestJSON: manifestJSON, ManifestSourcePath: *manifestSource,
		LockInventory: lock.Inventory, RegistryURL: *registry, FetchPackageVersion: fetch, Concurrency: *workers,
	})
	if err != nil {
		fmt.Fprintf(stderr, "license-evidence: %v\n", err)
		return 1
	}
	if err := baseline.WriteSnapshot(*output, baseline.Result{JSON: evidence.JSON, SHA256: evidence.SHA256}); err != nil {
		fmt.Fprintf(stderr, "license-evidence: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s  %s\n", evidence.SHA256, *output)
	return 0
}

func registryFetcher(client *http.Client, registryURL string) baseline.PackageVersionFetcher {
	return func(ctx context.Context, name string, version string) ([]byte, error) {
		endpoint := registryURL + "/" + url.PathEscape(name) + "/" + url.PathEscape(version)
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("User-Agent", "opencode-go-py-p0-license-evidence/1")
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 32<<10))
			return nil, fmt.Errorf("registry returned %s", response.Status)
		}
		limited := io.LimitReader(response.Body, maxRegistryMetadataBytes+1)
		content, err := io.ReadAll(limited)
		if err != nil {
			return nil, err
		}
		if len(content) > maxRegistryMetadataBytes {
			return nil, errors.New("registry metadata exceeds 8 MiB limit")
		}
		return content, nil
	}
}
