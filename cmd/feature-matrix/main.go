package main

import (
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
	flags := flag.NewFlagSet("feature-matrix", flag.ContinueOnError)
	flags.SetOutput(stderr)
	plan := flags.String("plan", "", "path to the Markdown master plan")
	source := flags.String("source", "doc/OPENCODE_REPLICA_MASTER_PLAN.md", "stable source label stored in the mirror")
	output := flags.String("output", "", "destination feature matrix JSON path")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if *output == "" {
		fmt.Fprintln(stderr, "feature-matrix: -output is required")
		return 2
	}

	result, err := baseline.GenerateFeatureMatrix(baseline.FeatureMatrixOptions{
		PlanPath:   *plan,
		SourcePath: *source,
	})
	if err != nil {
		fmt.Fprintf(stderr, "feature-matrix: %v\n", err)
		return 1
	}
	if err := baseline.WriteSnapshot(*output, baseline.Result{JSON: result.JSON, SHA256: result.SHA256}); err != nil {
		fmt.Fprintf(stderr, "feature-matrix: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s  %s\n", result.SHA256, *output)
	return 0
}
