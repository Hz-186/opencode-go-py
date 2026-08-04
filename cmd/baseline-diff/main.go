package main

import (
	"encoding/json"
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
	flags := flag.NewFlagSet("baseline-diff", flag.ContinueOnError)
	flags.SetOutput(stderr)
	fromPath := flags.String("from", "", "path to the older baseline manifest")
	toPath := flags.String("to", "", "path to the newer baseline manifest")
	output := flags.String("output", "", "destination baseline diff JSON path")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if *output == "" {
		fmt.Fprintln(stderr, "baseline-diff: -output is required")
		return 2
	}
	from, err := readManifest(*fromPath)
	if err != nil {
		fmt.Fprintf(stderr, "baseline-diff: read from manifest: %v\n", err)
		return 1
	}
	to, err := readManifest(*toPath)
	if err != nil {
		fmt.Fprintf(stderr, "baseline-diff: read to manifest: %v\n", err)
		return 1
	}
	result, err := baseline.DiffManifests(from, to)
	if err != nil {
		fmt.Fprintf(stderr, "baseline-diff: %v\n", err)
		return 1
	}
	if err := baseline.WriteSnapshot(*output, baseline.Result{JSON: result.JSON, SHA256: result.SHA256}); err != nil {
		fmt.Fprintf(stderr, "baseline-diff: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s  %s\n", result.SHA256, *output)
	return 0
}

func readManifest(name string) (baseline.Manifest, error) {
	content, err := os.ReadFile(name)
	if err != nil {
		return baseline.Manifest{}, err
	}
	var manifest baseline.Manifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		return baseline.Manifest{}, err
	}
	return manifest, nil
}
