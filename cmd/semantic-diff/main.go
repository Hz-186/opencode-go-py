package main

import (
	"bytes"
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
	flags := flag.NewFlagSet("semantic-diff", flag.ContinueOnError)
	flags.SetOutput(stderr)
	fromPath := flags.String("from", "", "path to the older semantic inventory")
	toPath := flags.String("to", "", "path to the newer semantic inventory")
	output := flags.String("output", "", "destination semantic diff JSON path")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if *output == "" {
		fmt.Fprintln(stderr, "semantic-diff: -output is required")
		return 2
	}
	from, err := readInventory(*fromPath)
	if err != nil {
		fmt.Fprintf(stderr, "semantic-diff: read from inventory: %v\n", err)
		return 1
	}
	to, err := readInventory(*toPath)
	if err != nil {
		fmt.Fprintf(stderr, "semantic-diff: read to inventory: %v\n", err)
		return 1
	}
	result, err := baseline.DiffSemanticInventories(from, to)
	if err != nil {
		fmt.Fprintf(stderr, "semantic-diff: %v\n", err)
		return 1
	}
	if err := baseline.WriteSnapshot(*output, baseline.Result{JSON: result.JSON, SHA256: result.SHA256}); err != nil {
		fmt.Fprintf(stderr, "semantic-diff: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s  %s\n", result.SHA256, *output)
	return 0
}

func readInventory(name string) (baseline.SemanticInventory, error) {
	content, err := os.ReadFile(name)
	if err != nil {
		return baseline.SemanticInventory{}, err
	}
	var inventory baseline.SemanticInventory
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&inventory); err != nil {
		return baseline.SemanticInventory{}, err
	}
	if inventory.SchemaVersion != 1 {
		return baseline.SemanticInventory{}, fmt.Errorf("unsupported semantic inventory schema version %d", inventory.SchemaVersion)
	}
	return inventory, nil
}
