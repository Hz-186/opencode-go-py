package baseline

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

type ChangeKind string

const (
	ChangeAdded   ChangeKind = "added"
	ChangeRemoved ChangeKind = "removed"
	ChangeChanged ChangeKind = "changed"
)

type SemanticDiffResult struct {
	Diff   SemanticDiff
	JSON   []byte
	SHA256 string
}

type SemanticDiff struct {
	SchemaVersion int                `json:"schema_version"`
	Base          SemanticRepository `json:"base"`
	Target        SemanticRepository `json:"target"`
	Counts        SemanticDiffCounts `json:"counts"`
	Changes       []SemanticChange   `json:"changes"`
}

type SemanticDiffCounts struct {
	Added    int `json:"added"`
	Removed  int `json:"removed"`
	Changed  int `json:"changed"`
	Breaking int `json:"breaking"`
}

type SemanticChange struct {
	Entity   string          `json:"entity"`
	Key      string          `json:"key"`
	Kind     ChangeKind      `json:"kind"`
	Breaking bool            `json:"breaking"`
	Before   json.RawMessage `json:"before,omitempty"`
	After    json.RawMessage `json:"after,omitempty"`
}

func DiffSemanticInventories(base SemanticInventory, target SemanticInventory) (SemanticDiffResult, error) {
	changes := make([]SemanticChange, 0)
	routeDuplicates := duplicateNaturalKeys(base.Routes, target.Routes, func(record RouteRecord) string { return record.OperationID })
	if err := diffRecords("route", keyedRoutes(base.Routes, routeDuplicates), keyedRoutes(target.Routes, routeDuplicates), routeBreaking, &changes); err != nil {
		return SemanticDiffResult{}, err
	}
	eventDuplicates := duplicateNaturalKeys(base.Events, target.Events, func(record EventRecord) string { return record.Type })
	if err := diffRecords("event", keyedEvents(base.Events, eventDuplicates), keyedEvents(target.Events, eventDuplicates), eventBreaking, &changes); err != nil {
		return SemanticDiffResult{}, err
	}
	schemaDuplicates := duplicateNaturalKeys(base.Schemas, target.Schemas, func(record SchemaExportRecord) string { return record.Entrypoint + ":" + record.Symbol })
	if err := diffRecords("schema", keyedSchemas(base.Schemas, schemaDuplicates), keyedSchemas(target.Schemas, schemaDuplicates), schemaBreaking, &changes); err != nil {
		return SemanticDiffResult{}, err
	}
	symbolDuplicates := duplicateNaturalKeys(base.Symbols, target.Symbols, func(record PublicSymbolRecord) string {
		return record.Package + ":" + record.Entrypoint + ":" + record.Symbol + ":" + record.Kind
	})
	if err := diffRecords("symbol", keyedSymbols(base.Symbols, symbolDuplicates), keyedSymbols(target.Symbols, symbolDuplicates), symbolBreaking, &changes); err != nil {
		return SemanticDiffResult{}, err
	}
	slices.SortFunc(changes, func(a, b SemanticChange) int {
		if compared := strings.Compare(a.Entity, b.Entity); compared != 0 {
			return compared
		}
		if compared := strings.Compare(a.Key, b.Key); compared != 0 {
			return compared
		}
		return strings.Compare(string(a.Kind), string(b.Kind))
	})
	diff := SemanticDiff{
		SchemaVersion: 1,
		Base:          base.Repository,
		Target:        target.Repository,
		Changes:       changes,
	}
	for _, change := range changes {
		switch change.Kind {
		case ChangeAdded:
			diff.Counts.Added++
		case ChangeRemoved:
			diff.Counts.Removed++
		case ChangeChanged:
			diff.Counts.Changed++
		}
		if change.Breaking {
			diff.Counts.Breaking++
		}
	}
	if diff.Changes == nil {
		diff.Changes = []SemanticChange{}
	}
	encoded, err := json.MarshalIndent(diff, "", "  ")
	if err != nil {
		return SemanticDiffResult{}, fmt.Errorf("encode semantic diff: %w", err)
	}
	encoded = append(encoded, '\n')
	return SemanticDiffResult{Diff: diff, JSON: encoded, SHA256: digestBytes(encoded)}, nil
}

func diffRecords[T any](entity string, base map[string]T, target map[string]T, breaking func(T, T, ChangeKind) bool, changes *[]SemanticChange) error {
	keys := make([]string, 0, len(base)+len(target))
	seen := make(map[string]struct{}, len(base)+len(target))
	for key := range base {
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for key := range target {
		if _, exists := seen[key]; exists {
			continue
		}
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		before, beforeExists := base[key]
		after, afterExists := target[key]
		switch {
		case !beforeExists:
			encoded, err := json.Marshal(after)
			if err != nil {
				return fmt.Errorf("encode added %s %s: %w", entity, key, err)
			}
			*changes = append(*changes, SemanticChange{Entity: entity, Key: key, Kind: ChangeAdded, Breaking: breaking(before, after, ChangeAdded), After: encoded})
		case !afterExists:
			encoded, err := json.Marshal(before)
			if err != nil {
				return fmt.Errorf("encode removed %s %s: %w", entity, key, err)
			}
			*changes = append(*changes, SemanticChange{Entity: entity, Key: key, Kind: ChangeRemoved, Breaking: breaking(before, after, ChangeRemoved), Before: encoded})
		case !reflect.DeepEqual(before, after):
			beforeJSON, err := json.Marshal(before)
			if err != nil {
				return fmt.Errorf("encode changed %s %s base: %w", entity, key, err)
			}
			afterJSON, err := json.Marshal(after)
			if err != nil {
				return fmt.Errorf("encode changed %s %s target: %w", entity, key, err)
			}
			*changes = append(*changes, SemanticChange{
				Entity: entity, Key: key, Kind: ChangeChanged,
				Breaking: breaking(before, after, ChangeChanged), Before: beforeJSON, After: afterJSON,
			})
		}
	}
	return nil
}

func duplicateNaturalKeys[T any](base []T, target []T, natural func(T) string) map[string]bool {
	counts := make(map[string]int, len(base)+len(target))
	for _, records := range [][]T{base, target} {
		seen := make(map[string]int)
		for _, record := range records {
			seen[natural(record)]++
		}
		for key, count := range seen {
			if count > counts[key] {
				counts[key] = count
			}
		}
	}
	duplicates := make(map[string]bool)
	for key, count := range counts {
		if count > 1 {
			duplicates[key] = true
		}
	}
	return duplicates
}

func keyedRoutes(records []RouteRecord, duplicates map[string]bool) map[string]RouteRecord {
	result := make(map[string]RouteRecord, len(records))
	for _, record := range records {
		key := record.OperationID
		if duplicates[key] {
			key += "@" + record.SourcePath + ":" + strconv.Itoa(record.Line)
		}
		insertUniqueRecord(result, key, record)
	}
	return result
}

func keyedEvents(records []EventRecord, duplicates map[string]bool) map[string]EventRecord {
	result := make(map[string]EventRecord, len(records))
	for _, record := range records {
		key := record.Type
		if duplicates[key] {
			key += "@" + record.Symbol + ":v" + strconv.Itoa(record.DurableVersion)
		}
		insertUniqueRecord(result, key, record)
	}
	return result
}

func keyedSchemas(records []SchemaExportRecord, duplicates map[string]bool) map[string]SchemaExportRecord {
	result := make(map[string]SchemaExportRecord, len(records))
	for _, record := range records {
		key := record.Entrypoint + ":" + record.Symbol
		if duplicates[key] {
			key += "@" + record.SourcePath
		}
		insertUniqueRecord(result, key, record)
	}
	return result
}

func keyedSymbols(records []PublicSymbolRecord, duplicates map[string]bool) map[string]PublicSymbolRecord {
	result := make(map[string]PublicSymbolRecord, len(records))
	for _, record := range records {
		key := record.Package + ":" + record.Entrypoint + ":" + record.Symbol + ":" + record.Kind
		if duplicates[key] {
			key += "@" + record.SourcePath + ":" + strconv.Itoa(record.Line)
		}
		insertUniqueRecord(result, key, record)
	}
	return result
}

func insertUniqueRecord[T any](records map[string]T, key string, record T) {
	if _, exists := records[key]; !exists {
		records[key] = record
		return
	}
	for suffix := 2; ; suffix++ {
		candidate := key + "#" + strconv.Itoa(suffix)
		if _, exists := records[candidate]; exists {
			continue
		}
		records[candidate] = record
		return
	}
}

func routeBreaking(before RouteRecord, after RouteRecord, kind ChangeKind) bool {
	if kind == ChangeRemoved {
		return true
	}
	if kind == ChangeAdded {
		return false
	}
	return before.Method != after.Method || before.Path != after.Path || before.PathStatus != after.PathStatus || before.OpenAPIIdentifier != after.OpenAPIIdentifier
}

func eventBreaking(before EventRecord, after EventRecord, kind ChangeKind) bool {
	if kind == ChangeRemoved {
		return true
	}
	if kind == ChangeAdded {
		return false
	}
	return before.Type != after.Type || before.Durable != after.Durable || before.DurableAggregate != after.DurableAggregate || before.DurableVersion != after.DurableVersion
}

func schemaBreaking(before SchemaExportRecord, after SchemaExportRecord, kind ChangeKind) bool {
	if kind == ChangeRemoved {
		return true
	}
	if kind == ChangeAdded {
		return false
	}
	return before.Symbol != after.Symbol || before.Entrypoint != after.Entrypoint || before.Classification != after.Classification
}

func symbolBreaking(before PublicSymbolRecord, after PublicSymbolRecord, kind ChangeKind) bool {
	if kind == ChangeRemoved {
		return true
	}
	if kind == ChangeAdded {
		return false
	}
	return before.Package != after.Package || before.Entrypoint != after.Entrypoint || before.Symbol != after.Symbol || before.Kind != after.Kind || before.Classification != after.Classification
}
