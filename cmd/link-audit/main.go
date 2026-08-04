package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

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
	flags := flag.NewFlagSet("link-audit", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repository := flags.String("repo", "", "path to the frozen upstream Git worktree")
	commit := flags.String("commit", "", "frozen upstream commit")
	branch := flags.String("branch", "dev", "expected upstream branch")
	documentRoot := flags.String("document-root", "", "root directory containing Markdown plans")
	output := flags.String("output", "", "destination link audit JSON path")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if *output == "" {
		fmt.Fprintln(stderr, "link-audit: -output is required")
		return 2
	}
	documents, err := baseline.DiscoverMarkdownDocuments(*documentRoot)
	if err != nil {
		fmt.Fprintf(stderr, "link-audit: %v\n", err)
		return 1
	}
	result, err := baseline.AuditSourceLinks(ctx, baseline.LinkAuditOptions{
		Repository: *repository, Commit: *commit, Branch: *branch,
		DocumentRoot: *documentRoot, Documents: documents,
	})
	if err != nil {
		fmt.Fprintf(stderr, "link-audit: %v\n", err)
		return 1
	}
	if err := baseline.WriteSnapshot(*output, baseline.Result{JSON: result.JSON, SHA256: result.SHA256}); err != nil {
		fmt.Fprintf(stderr, "link-audit: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s  %s\n", result.SHA256, *output)
	if !result.Report.Valid {
		fmt.Fprintf(stderr, "link-audit: %d invalid source links\n", len(result.Report.Issues))
		return 1
	}
	return 0
}
