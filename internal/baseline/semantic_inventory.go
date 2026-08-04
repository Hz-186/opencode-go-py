package baseline

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

const semanticParserPolicy = "typescript-static-v1"

type PathStatus string

const (
	PathResolved   PathStatus = "resolved"
	PathUnresolved PathStatus = "unresolved"
)

type SemanticInventoryOptions struct {
	Repository string
	Commit     string
	Branch     string
}

type SemanticInventoryResult struct {
	Inventory SemanticInventory
	JSON      []byte
	SHA256    string
}

type SemanticInventory struct {
	SchemaVersion int                  `json:"schema_version"`
	ParserPolicy  string               `json:"parser_policy"`
	Repository    SemanticRepository   `json:"repository"`
	Counts        SemanticCounts       `json:"counts"`
	Routes        []RouteRecord        `json:"routes"`
	Events        []EventRecord        `json:"events"`
	Schemas       []SchemaExportRecord `json:"schemas"`
	Symbols       []PublicSymbolRecord `json:"symbols"`
	Conflicts     []SemanticConflict   `json:"conflicts"`
}

type SemanticRepository struct {
	Commit string `json:"commit"`
	Tree   string `json:"tree"`
	Branch string `json:"branch"`
}

type SemanticCounts struct {
	Routes     int `json:"routes"`
	Events     int `json:"events"`
	Schemas    int `json:"schemas"`
	Symbols    int `json:"symbols"`
	Unresolved int `json:"unresolved_routes"`
	Conflicts  int `json:"conflicts"`
}

type RouteRecord struct {
	OperationID       string              `json:"operation_id"`
	Method            string              `json:"method"`
	PathExpression    string              `json:"path_expression"`
	Path              string              `json:"path,omitempty"`
	PathStatus        PathStatus          `json:"path_status"`
	OpenAPIIdentifier string              `json:"openapi_identifier,omitempty"`
	SourcePath        string              `json:"source_path"`
	Line              int                 `json:"line"`
	Classification    ScopeClassification `json:"classification"`
}

type EventRecord struct {
	Type             string              `json:"type"`
	Symbol           string              `json:"symbol"`
	SourcePath       string              `json:"source_path"`
	Line             int                 `json:"line"`
	Classification   ScopeClassification `json:"classification"`
	Durable          bool                `json:"durable"`
	DurableAggregate string              `json:"durable_aggregate,omitempty"`
	DurableVersion   int                 `json:"durable_version,omitempty"`
	Manifests        []string            `json:"manifests"`
}

type SchemaExportRecord struct {
	Package        string              `json:"package"`
	Entrypoint     string              `json:"entrypoint"`
	Symbol         string              `json:"symbol"`
	Kind           string              `json:"kind"`
	SourcePath     string              `json:"source_path"`
	Line           int                 `json:"line"`
	Classification ScopeClassification `json:"classification"`
}

type PublicSymbolRecord struct {
	Package        string              `json:"package"`
	Entrypoint     string              `json:"entrypoint"`
	Symbol         string              `json:"symbol"`
	Kind           string              `json:"kind"`
	SourcePath     string              `json:"source_path"`
	Line           int                 `json:"line"`
	Classification ScopeClassification `json:"classification"`
}

type SemanticConflict struct {
	Entity  string   `json:"entity"`
	Key     string   `json:"key"`
	Sources []string `json:"sources"`
}

type semanticSource struct {
	path    string
	content []byte
}

func GenerateSemanticInventory(ctx context.Context, options SemanticInventoryOptions) (SemanticInventoryResult, error) {
	if err := validateSemanticOptions(options); err != nil {
		return SemanticInventoryResult{}, err
	}
	repository, err := filepath.Abs(options.Repository)
	if err != nil {
		return SemanticInventoryResult{}, fmt.Errorf("resolve semantic inventory repository: %w", err)
	}
	if err := validateWorktree(ctx, repository, Options{Commit: options.Commit, Branch: options.Branch}); err != nil {
		return SemanticInventoryResult{}, err
	}
	commit, err := gitText(ctx, repository, "rev-parse", "--verify", "--end-of-options", options.Commit+"^{commit}")
	if err != nil {
		return SemanticInventoryResult{}, fmt.Errorf("resolve semantic inventory commit %q: %w", options.Commit, err)
	}
	tree, err := gitText(ctx, repository, "rev-parse", "--verify", "--end-of-options", commit+"^{tree}")
	if err != nil {
		return SemanticInventoryResult{}, fmt.Errorf("resolve semantic inventory tree: %w", err)
	}
	sources, err := readSemanticSources(ctx, repository, commit)
	if err != nil {
		return SemanticInventoryResult{}, err
	}

	routes, err := extractRoutes(sources)
	if err != nil {
		return SemanticInventoryResult{}, err
	}
	events, err := extractEvents(sources)
	if err != nil {
		return SemanticInventoryResult{}, err
	}
	schemas, symbols, err := extractSchemaSurface(sources)
	if err != nil {
		return SemanticInventoryResult{}, err
	}
	sortSemanticRecords(routes, events, schemas, symbols)
	conflicts := semanticConflicts(routes, events, schemas)

	inventory := SemanticInventory{
		SchemaVersion: 1,
		ParserPolicy:  semanticParserPolicy,
		Repository:    SemanticRepository{Commit: commit, Tree: tree, Branch: options.Branch},
		Routes:        nonNilRoutes(routes),
		Events:        nonNilEvents(events),
		Schemas:       nonNilSchemas(schemas),
		Symbols:       nonNilSymbols(symbols),
		Conflicts:     nonNilConflicts(conflicts),
	}
	inventory.Counts = SemanticCounts{
		Routes: len(routes), Events: len(events), Schemas: len(schemas), Symbols: len(symbols), Conflicts: len(conflicts),
	}
	for _, route := range routes {
		if route.PathStatus == PathUnresolved {
			inventory.Counts.Unresolved++
		}
	}
	encoded, err := json.MarshalIndent(inventory, "", "  ")
	if err != nil {
		return SemanticInventoryResult{}, fmt.Errorf("encode semantic inventory: %w", err)
	}
	encoded = append(encoded, '\n')
	return SemanticInventoryResult{Inventory: inventory, JSON: encoded, SHA256: digestBytes(encoded)}, nil
}

func validateSemanticOptions(options SemanticInventoryOptions) error {
	if options.Repository == "" {
		return errors.New("semantic inventory repository path is required")
	}
	if options.Commit == "" {
		return errors.New("semantic inventory frozen commit is required")
	}
	if options.Branch == "" {
		return errors.New("semantic inventory frozen branch is required")
	}
	return nil
}

func readSemanticSources(ctx context.Context, repository string, commit string) ([]semanticSource, error) {
	command := exec.CommandContext(ctx, "git", "-C", repository, "archive", "--format=tar", commit)
	command.Env = utf8Environment()
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open semantic inventory archive: %w", err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start semantic inventory archive: %w", err)
	}

	reader := tar.NewReader(stdout)
	sources := make([]semanticSource, 0)
	for {
		header, readErr := reader.Next()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			command.Process.Kill()
			command.Wait()
			return nil, fmt.Errorf("read semantic inventory archive: %w", readErr)
		}
		name := path.Clean(header.Name)
		if !header.FileInfo().Mode().IsRegular() || !isSemanticSource(name) {
			continue
		}
		content, readErr := io.ReadAll(reader)
		if readErr != nil {
			command.Process.Kill()
			command.Wait()
			return nil, fmt.Errorf("read semantic source %s: %w", name, readErr)
		}
		if !utf8.Valid(content) {
			command.Process.Kill()
			command.Wait()
			return nil, fmt.Errorf("semantic source %s is not valid UTF-8", name)
		}
		sources = append(sources, semanticSource{path: name, content: content})
	}
	if err := command.Wait(); err != nil {
		return nil, fmt.Errorf("git archive semantic sources: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	slices.SortFunc(sources, func(a, b semanticSource) int { return strings.Compare(a.path, b.path) })
	return sources, nil
}

func isSemanticSource(name string) bool {
	if name == "packages/schema/package.json" {
		return true
	}
	if strings.HasPrefix(name, "packages/schema/src/") && strings.HasSuffix(name, ".ts") {
		return true
	}
	return isRouteSource(name)
}

func isRouteSource(name string) bool {
	if !strings.HasSuffix(name, ".ts") {
		return false
	}
	return strings.HasPrefix(name, "packages/protocol/src/groups/") ||
		strings.HasPrefix(name, "packages/opencode/src/server/routes/instance/httpapi/groups/")
}

func semanticScope(name string) ScopeClassification {
	normalized := "/" + strings.ToLower(strings.ReplaceAll(name, "\\", "/"))
	base := strings.ToLower(path.Base(name))
	if strings.Contains(normalized, "/src/v1/") || strings.Contains(normalized, "/v1/") ||
		strings.HasSuffix(base, "-v1.ts") || strings.HasPrefix(base, "legacy-") {
		return ScopeV1Archaeology
	}
	return classifyScope(name)
}

func sortSemanticRecords(routes []RouteRecord, events []EventRecord, schemas []SchemaExportRecord, symbols []PublicSymbolRecord) {
	slices.SortFunc(routes, func(a, b RouteRecord) int {
		if compared := strings.Compare(a.OperationID, b.OperationID); compared != 0 {
			return compared
		}
		if compared := strings.Compare(a.SourcePath, b.SourcePath); compared != 0 {
			return compared
		}
		return a.Line - b.Line
	})
	slices.SortFunc(events, func(a, b EventRecord) int {
		if compared := strings.Compare(a.Type, b.Type); compared != 0 {
			return compared
		}
		if compared := strings.Compare(a.Symbol, b.Symbol); compared != 0 {
			return compared
		}
		return a.DurableVersion - b.DurableVersion
	})
	slices.SortFunc(schemas, func(a, b SchemaExportRecord) int {
		if compared := strings.Compare(a.Entrypoint, b.Entrypoint); compared != 0 {
			return compared
		}
		if compared := strings.Compare(a.Symbol, b.Symbol); compared != 0 {
			return compared
		}
		if compared := strings.Compare(a.Kind, b.Kind); compared != 0 {
			return compared
		}
		return strings.Compare(a.SourcePath, b.SourcePath)
	})
	slices.SortFunc(symbols, func(a, b PublicSymbolRecord) int {
		if compared := strings.Compare(a.Package, b.Package); compared != 0 {
			return compared
		}
		if compared := strings.Compare(a.Entrypoint, b.Entrypoint); compared != 0 {
			return compared
		}
		if compared := strings.Compare(a.Symbol, b.Symbol); compared != 0 {
			return compared
		}
		if compared := strings.Compare(a.Kind, b.Kind); compared != 0 {
			return compared
		}
		if compared := strings.Compare(a.SourcePath, b.SourcePath); compared != 0 {
			return compared
		}
		return a.Line - b.Line
	})
}

func semanticConflicts(routes []RouteRecord, events []EventRecord, schemas []SchemaExportRecord) []SemanticConflict {
	conflicts := make([]SemanticConflict, 0)
	collect := func(entity string, keys map[string][]string) {
		for key, sources := range keys {
			if len(sources) < 2 {
				continue
			}
			slices.Sort(sources)
			conflicts = append(conflicts, SemanticConflict{Entity: entity, Key: key, Sources: slices.Compact(sources)})
		}
	}
	routeKeys := make(map[string][]string)
	for _, record := range routes {
		routeKeys[record.OperationID] = append(routeKeys[record.OperationID], sourceLabel(record.SourcePath, record.Line))
	}
	collect("route", routeKeys)
	eventKeys := make(map[string][]string)
	for _, record := range events {
		eventKeys[record.Type] = append(eventKeys[record.Type], sourceLabel(record.SourcePath, record.Line))
	}
	collect("event", eventKeys)
	schemaKeys := make(map[string][]string)
	for _, record := range schemas {
		if record.Classification != ScopeCanonicalV2 {
			continue
		}
		schemaKeys[record.Entrypoint+":"+record.Symbol] = append(schemaKeys[record.Entrypoint+":"+record.Symbol], sourceLabel(record.SourcePath, record.Line))
	}
	collect("schema", schemaKeys)
	slices.SortFunc(conflicts, func(a, b SemanticConflict) int {
		if compared := strings.Compare(a.Entity, b.Entity); compared != 0 {
			return compared
		}
		return strings.Compare(a.Key, b.Key)
	})
	return conflicts
}

func sourceLabel(name string, line int) string {
	return fmt.Sprintf("%s:%d", name, line)
}

func nonNilRoutes(value []RouteRecord) []RouteRecord {
	if value == nil {
		return []RouteRecord{}
	}
	return value
}

func nonNilEvents(value []EventRecord) []EventRecord {
	if value == nil {
		return []EventRecord{}
	}
	return value
}

func nonNilSchemas(value []SchemaExportRecord) []SchemaExportRecord {
	if value == nil {
		return []SchemaExportRecord{}
	}
	return value
}

func nonNilSymbols(value []PublicSymbolRecord) []PublicSymbolRecord {
	if value == nil {
		return []PublicSymbolRecord{}
	}
	return value
}

func nonNilConflicts(value []SemanticConflict) []SemanticConflict {
	if value == nil {
		return []SemanticConflict{}
	}
	return value
}
