package baseline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var (
	ErrDuplicateFeature  = errors.New("feature matrix contains a duplicate feature")
	ErrFeatureStatus     = errors.New("feature matrix contains an invalid status")
	ErrFeatureDifficulty = errors.New("feature matrix contains an invalid difficulty")
)

type FeatureMatrixOptions struct {
	PlanPath   string
	SourcePath string
}

type FeatureMatrixResult struct {
	Matrix FeatureMatrix
	JSON   []byte
	SHA256 string
}

type FeatureMatrix struct {
	SchemaVersion int             `json:"schema_version"`
	Source        SourceDocument  `json:"source"`
	Features      []FeatureRecord `json:"features"`
}

type SourceDocument struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type FeatureClassification string

const (
	FeatureCanonical        FeatureClassification = "canonical"
	FeatureReplicaExtension FeatureClassification = "replica-extension"
)

type FeatureStatus string

const (
	StatusPending    FeatureStatus = "pending"
	StatusInProgress FeatureStatus = "in_progress"
	StatusBlocked    FeatureStatus = "blocked"
	StatusVerified   FeatureStatus = "verified"
	StatusSuperseded FeatureStatus = "superseded"
)

type FeatureRecord struct {
	ID             string                `json:"id"`
	Name           string                `json:"name"`
	UpstreamSource string                `json:"upstream_source"`
	Behavior       string                `json:"behavior"`
	Dependencies   string                `json:"dependencies"`
	Ownership      string                `json:"ownership"`
	Phase          string                `json:"phase"`
	TestBasis      string                `json:"test_basis"`
	Difficulty     string                `json:"difficulty"`
	Status         FeatureStatus         `json:"status"`
	Classification FeatureClassification `json:"classification"`
}

func GenerateFeatureMatrix(options FeatureMatrixOptions) (FeatureMatrixResult, error) {
	if options.PlanPath == "" {
		return FeatureMatrixResult{}, errors.New("feature matrix plan path is required")
	}
	if options.SourcePath == "" {
		return FeatureMatrixResult{}, errors.New("feature matrix source label is required")
	}
	content, err := os.ReadFile(options.PlanPath)
	if err != nil {
		return FeatureMatrixResult{}, fmt.Errorf("read feature matrix plan: %w", err)
	}
	if !utf8.Valid(content) {
		return FeatureMatrixResult{}, errors.New("feature matrix plan is not valid UTF-8")
	}
	features, err := parseFeatureTable(string(content))
	if err != nil {
		return FeatureMatrixResult{}, err
	}
	sourceDigest := sha256.Sum256(content)
	matrix := FeatureMatrix{
		SchemaVersion: 1,
		Source: SourceDocument{
			Path:   filepath.ToSlash(options.SourcePath),
			SHA256: hex.EncodeToString(sourceDigest[:]),
			Bytes:  int64(len(content)),
		},
		Features: features,
	}
	encoded, err := json.MarshalIndent(matrix, "", "  ")
	if err != nil {
		return FeatureMatrixResult{}, fmt.Errorf("encode feature matrix: %w", err)
	}
	encoded = append(encoded, '\n')
	digest := sha256.Sum256(encoded)
	return FeatureMatrixResult{
		Matrix: matrix,
		JSON:   encoded,
		SHA256: hex.EncodeToString(digest[:]),
	}, nil
}

func parseFeatureTable(document string) ([]FeatureRecord, error) {
	lines := strings.Split(strings.ReplaceAll(document, "\r\n", "\n"), "\n")
	headerIndex := -1
	for index, line := range lines {
		cells, ok := splitMarkdownRow(line)
		if ok && len(cells) == 9 && cells[0] == "功能" && cells[8] == "状态" {
			headerIndex = index
			break
		}
	}
	if headerIndex < 0 || headerIndex+1 >= len(lines) {
		return nil, errors.New("feature matrix table header is missing")
	}
	separator, ok := splitMarkdownRow(lines[headerIndex+1])
	if !ok || len(separator) != 9 || !isMarkdownSeparator(separator) {
		return nil, errors.New("feature matrix table separator is invalid")
	}

	features := make([]FeatureRecord, 0)
	names := make(map[string]struct{})
	ids := make(map[string]string)
	for lineIndex := headerIndex + 2; lineIndex < len(lines); lineIndex++ {
		cells, row := splitMarkdownRow(lines[lineIndex])
		if !row {
			break
		}
		if len(cells) != 9 {
			return nil, fmt.Errorf("feature matrix line %d has %d columns, want 9", lineIndex+1, len(cells))
		}
		feature, err := featureFromCells(cells)
		if err != nil {
			return nil, fmt.Errorf("feature matrix line %d: %w", lineIndex+1, err)
		}
		if _, duplicate := names[feature.Name]; duplicate {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateFeature, feature.Name)
		}
		if existing, collision := ids[feature.ID]; collision {
			return nil, fmt.Errorf("feature ID collision %s between %q and %q", feature.ID, existing, feature.Name)
		}
		names[feature.Name] = struct{}{}
		ids[feature.ID] = feature.Name
		features = append(features, feature)
	}
	if len(features) == 0 {
		return nil, errors.New("feature matrix table has no features")
	}
	return features, nil
}

func featureFromCells(cells []string) (FeatureRecord, error) {
	if cells[0] == "" {
		return FeatureRecord{}, errors.New("feature name is empty")
	}
	status := FeatureStatus(cells[8])
	switch status {
	case StatusPending, StatusInProgress, StatusBlocked, StatusVerified, StatusSuperseded:
	default:
		return FeatureRecord{}, fmt.Errorf("%w: %s", ErrFeatureStatus, status)
	}
	switch cells[7] {
	case "M", "H", "VH":
	default:
		return FeatureRecord{}, fmt.Errorf("%w: %s", ErrFeatureDifficulty, cells[7])
	}
	classification := FeatureCanonical
	if strings.Contains(cells[2], "[replica-extension]") {
		classification = FeatureReplicaExtension
	}
	digest := sha256.Sum256([]byte(cells[0]))
	return FeatureRecord{
		ID:             "FM-" + strings.ToUpper(hex.EncodeToString(digest[:6])),
		Name:           cells[0],
		UpstreamSource: cells[1],
		Behavior:       cells[2],
		Dependencies:   cells[3],
		Ownership:      cells[4],
		Phase:          cells[5],
		TestBasis:      cells[6],
		Difficulty:     cells[7],
		Status:         status,
		Classification: classification,
	}, nil
}

func splitMarkdownRow(line string) ([]string, bool) {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) < 2 || trimmed[0] != '|' || trimmed[len(trimmed)-1] != '|' {
		return nil, false
	}
	trimmed = trimmed[1 : len(trimmed)-1]
	cells := make([]string, 0, 9)
	var cell strings.Builder
	inCode := false
	for index := 0; index < len(trimmed); index++ {
		character := trimmed[index]
		if character == '\\' && index+1 < len(trimmed) && trimmed[index+1] == '|' {
			cell.WriteByte('|')
			index++
			continue
		}
		if character == '`' {
			inCode = !inCode
			cell.WriteByte(character)
			continue
		}
		if character == '|' && !inCode {
			cells = append(cells, strings.TrimSpace(cell.String()))
			cell.Reset()
			continue
		}
		cell.WriteByte(character)
	}
	if inCode {
		return nil, false
	}
	cells = append(cells, strings.TrimSpace(cell.String()))
	return cells, true
}

func isMarkdownSeparator(cells []string) bool {
	for _, cell := range cells {
		separator := strings.Trim(cell, ":")
		if len(separator) < 3 || len(strings.Trim(separator, "-")) != 0 {
			return false
		}
	}
	return true
}
