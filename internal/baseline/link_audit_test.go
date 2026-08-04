package baseline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditSourceLinksValidatesFrozenFilesAndLines(t *testing.T) {
	repository, commit := newFixtureRepository(t)
	documentRoot := t.TempDir()
	document := filepath.Join(documentRoot, "plan.md")
	content := "[source](" + filepath.ToSlash(filepath.Join(repository, "packages", "opencode", "src", "index.ts")) + "#L2)\n"
	if err := os.WriteFile(document, []byte(content), 0o644); err != nil {
		t.Fatalf("write link document: %v", err)
	}
	options := LinkAuditOptions{
		Repository:   repository,
		Commit:       commit,
		Branch:       "dev",
		DocumentRoot: documentRoot,
		Documents:    []string{document},
	}

	first, err := AuditSourceLinks(context.Background(), options)
	if err != nil {
		t.Fatalf("audit source links: %v", err)
	}
	second, err := AuditSourceLinks(context.Background(), options)
	if err != nil {
		t.Fatalf("repeat source link audit: %v", err)
	}
	if string(first.JSON) != string(second.JSON) || first.SHA256 != second.SHA256 {
		t.Fatal("source link audit is not deterministic")
	}
	if !first.Report.Valid || len(first.Report.Issues) != 0 {
		t.Fatalf("valid report = %#v", first.Report)
	}
	if len(first.Report.Links) != 1 {
		t.Fatalf("links = %#v", first.Report.Links)
	}
	link := first.Report.Links[0]
	if link.Document != "plan.md" || link.SourcePath != "packages/opencode/src/index.ts" || link.StartLine != 2 {
		t.Fatalf("link = %#v", link)
	}
}

func TestAuditSourceLinksReportsMissingFileAndOutOfRangeLine(t *testing.T) {
	repository, commit := newFixtureRepository(t)
	documentRoot := t.TempDir()
	document := filepath.Join(documentRoot, "plan.md")
	validPath := filepath.ToSlash(filepath.Join(repository, "packages", "opencode", "src", "index.ts"))
	missingPath := filepath.ToSlash(filepath.Join(repository, "missing.ts"))
	content := strings.Join([]string{
		"[past end](" + validPath + "#L99)",
		"[missing](" + missingPath + "#L1)",
	}, "\n")
	if err := os.WriteFile(document, []byte(content), 0o644); err != nil {
		t.Fatalf("write link document: %v", err)
	}

	result, err := AuditSourceLinks(context.Background(), LinkAuditOptions{
		Repository:   repository,
		Commit:       commit,
		Branch:       "dev",
		DocumentRoot: documentRoot,
		Documents:    []string{document},
	})
	if err != nil {
		t.Fatalf("audit source links: %v", err)
	}
	if result.Report.Valid {
		t.Fatal("invalid links unexpectedly reported valid")
	}
	if len(result.Report.Issues) != 2 {
		t.Fatalf("issues = %#v", result.Report.Issues)
	}
	if result.Report.Issues[0].Kind != LinkLineOutOfRange || result.Report.Issues[1].Kind != LinkMissingFile {
		t.Fatalf("issue order = %#v", result.Report.Issues)
	}
}

func TestAuditSourceLinksRejectsDirtyUpstream(t *testing.T) {
	repository, commit := newFixtureRepository(t)
	writeFixtureFile(t, repository, "README.md", "dirty\n")
	documentRoot := t.TempDir()
	document := filepath.Join(documentRoot, "plan.md")
	if err := os.WriteFile(document, []byte("# no links\n"), 0o644); err != nil {
		t.Fatalf("write link document: %v", err)
	}

	_, err := AuditSourceLinks(context.Background(), LinkAuditOptions{
		Repository: repository, Commit: commit, Branch: "dev",
		DocumentRoot: documentRoot, Documents: []string{document},
	})
	if !errors.Is(err, ErrDirtyWorktree) {
		t.Fatalf("error = %v, want ErrDirtyWorktree", err)
	}
}
