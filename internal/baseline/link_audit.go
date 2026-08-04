package baseline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

var (
	markdownLinkPattern = regexp.MustCompile(`\]\(([^)\r\n]+)\)`)
	lineFragmentPattern = regexp.MustCompile(`^L([1-9][0-9]*)(?:-L?([1-9][0-9]*))?$`)
)

type LinkAuditOptions struct {
	Repository   string
	Commit       string
	Branch       string
	DocumentRoot string
	Documents    []string
}

type LinkAuditResult struct {
	Report LinkAuditReport
	JSON   []byte
	SHA256 string
}

type LinkAuditReport struct {
	SchemaVersion  int              `json:"schema_version"`
	BaselineCommit string           `json:"baseline_commit"`
	Valid          bool             `json:"valid"`
	Documents      []SourceDocument `json:"documents"`
	Links          []SourceLink     `json:"links"`
	Issues         []LinkIssue      `json:"issues"`
}

type SourceLink struct {
	Document   string `json:"document"`
	SourcePath string `json:"source_path"`
	StartLine  int    `json:"start_line,omitempty"`
	EndLine    int    `json:"end_line,omitempty"`
}

type LinkIssueKind string

const (
	LinkMissingFile       LinkIssueKind = "missing_file"
	LinkLineOutOfRange    LinkIssueKind = "line_out_of_range"
	LinkMalformedFragment LinkIssueKind = "malformed_fragment"
)

type LinkIssue struct {
	Kind       LinkIssueKind `json:"kind"`
	Document   string        `json:"document"`
	SourcePath string        `json:"source_path"`
	Fragment   string        `json:"fragment,omitempty"`
	Message    string        `json:"message"`
}

type frozenFile struct {
	content []byte
	found   bool
}

func AuditSourceLinks(ctx context.Context, options LinkAuditOptions) (LinkAuditResult, error) {
	if options.DocumentRoot == "" {
		return LinkAuditResult{}, errors.New("document root is required")
	}
	if len(options.Documents) == 0 {
		return LinkAuditResult{}, errors.New("at least one document is required")
	}
	repository, err := filepath.Abs(options.Repository)
	if err != nil {
		return LinkAuditResult{}, fmt.Errorf("resolve repository path: %w", err)
	}
	documentRoot, err := filepath.Abs(options.DocumentRoot)
	if err != nil {
		return LinkAuditResult{}, fmt.Errorf("resolve document root: %w", err)
	}
	if err := validateWorktree(ctx, repository, Options{
		Repository: repository, Commit: options.Commit, Branch: options.Branch,
	}); err != nil {
		return LinkAuditResult{}, err
	}
	commit, err := gitText(ctx, repository, "rev-parse", "--verify", "--end-of-options", options.Commit+"^{commit}")
	if err != nil {
		return LinkAuditResult{}, fmt.Errorf("resolve frozen commit: %w", err)
	}

	documents := slices.Clone(options.Documents)
	slices.SortFunc(documents, func(a, b string) int { return strings.Compare(filepath.ToSlash(a), filepath.ToSlash(b)) })
	report := LinkAuditReport{SchemaVersion: 1, BaselineCommit: commit, Valid: true}
	cache := make(map[string]frozenFile)
	for _, document := range documents {
		if err := auditDocument(ctx, repository, documentRoot, commit, document, cache, &report); err != nil {
			return LinkAuditResult{}, err
		}
	}
	report.Valid = len(report.Issues) == 0
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return LinkAuditResult{}, fmt.Errorf("encode source link report: %w", err)
	}
	encoded = append(encoded, '\n')
	digest := sha256.Sum256(encoded)
	return LinkAuditResult{
		Report: report,
		JSON:   encoded,
		SHA256: hex.EncodeToString(digest[:]),
	}, nil
}

func DiscoverMarkdownDocuments(root string) ([]string, error) {
	documents := make([]string, 0)
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			documents = append(documents, name)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover Markdown documents: %w", err)
	}
	slices.Sort(documents)
	return documents, nil
}

func auditDocument(
	ctx context.Context,
	repository string,
	documentRoot string,
	commit string,
	document string,
	cache map[string]frozenFile,
	report *LinkAuditReport,
) error {
	absoluteDocument, err := filepath.Abs(document)
	if err != nil {
		return fmt.Errorf("resolve document path: %w", err)
	}
	relativeDocument, err := filepath.Rel(documentRoot, absoluteDocument)
	if err != nil || relativeDocument == ".." || strings.HasPrefix(relativeDocument, ".."+string(filepath.Separator)) {
		return fmt.Errorf("document %s is outside document root %s", document, documentRoot)
	}
	content, err := os.ReadFile(absoluteDocument)
	if err != nil {
		return fmt.Errorf("read document %s: %w", relativeDocument, err)
	}
	if !utf8.Valid(content) {
		return fmt.Errorf("document %s is not valid UTF-8", relativeDocument)
	}
	digest := sha256.Sum256(content)
	documentLabel := filepath.ToSlash(relativeDocument)
	report.Documents = append(report.Documents, SourceDocument{
		Path: documentLabel, SHA256: hex.EncodeToString(digest[:]), Bytes: int64(len(content)),
	})
	repositoryPrefix := filepath.ToSlash(repository) + "/"
	for _, match := range markdownLinkPattern.FindAllSubmatch(content, -1) {
		destination := strings.TrimSpace(string(match[1]))
		filePart, fragment, _ := strings.Cut(destination, "#")
		decoded, err := url.PathUnescape(strings.Trim(filePart, "<>"))
		if err != nil || !strings.HasPrefix(decoded, repositoryPrefix) {
			continue
		}
		sourcePath := strings.TrimPrefix(decoded, repositoryPrefix)
		link := SourceLink{Document: documentLabel, SourcePath: sourcePath}
		if fragment != "" {
			lines := lineFragmentPattern.FindStringSubmatch(fragment)
			if lines == nil {
				report.Links = append(report.Links, link)
				report.Issues = append(report.Issues, LinkIssue{
					Kind: LinkMalformedFragment, Document: documentLabel, SourcePath: sourcePath,
					Fragment: fragment, Message: "expected #L<line> or #L<start>-L<end>",
				})
				continue
			}
			link.StartLine, _ = strconv.Atoi(lines[1])
			link.EndLine = link.StartLine
			if lines[2] != "" {
				link.EndLine, _ = strconv.Atoi(lines[2])
			}
		}
		report.Links = append(report.Links, link)
		file, found := cache[sourcePath]
		if !found {
			file.content, file.found = readFrozenFile(ctx, repository, commit, sourcePath)
			cache[sourcePath] = file
		}
		if !file.found {
			report.Issues = append(report.Issues, LinkIssue{
				Kind: LinkMissingFile, Document: documentLabel, SourcePath: sourcePath,
				Message: "source path does not exist in frozen commit",
			})
			continue
		}
		if link.StartLine == 0 {
			continue
		}
		lines := lineCount(file.content)
		if link.StartLine > link.EndLine || link.EndLine > lines {
			report.Issues = append(report.Issues, LinkIssue{
				Kind: LinkLineOutOfRange, Document: documentLabel, SourcePath: sourcePath,
				Fragment: fragment, Message: fmt.Sprintf("source has %d lines", lines),
			})
		}
	}
	return nil
}

func readFrozenFile(ctx context.Context, repository string, commit string, sourcePath string) ([]byte, bool) {
	content, err := gitBytes(ctx, repository, "cat-file", "blob", commit+":"+sourcePath)
	if err != nil {
		return nil, false
	}
	return content, true
}
