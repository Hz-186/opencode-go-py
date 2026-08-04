package baseline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const bunLockParserPolicy = "bun-lock-jsonc/v1"

type DependencySourceKind string

const (
	DependencySourceFile      DependencySourceKind = "file"
	DependencySourceGit       DependencySourceKind = "git"
	DependencySourceRegistry  DependencySourceKind = "registry"
	DependencySourceUnknown   DependencySourceKind = "unknown"
	DependencySourceURL       DependencySourceKind = "url"
	DependencySourceWorkspace DependencySourceKind = "workspace"
)

type BunLockInventoryOptions struct {
	LockJSONC  []byte
	SourcePath string
}

type FrozenBunLockOptions struct {
	Repository string
	Commit     string
	LockPath   string
}

type BunLockInventoryResult struct {
	Inventory BunLockInventory
	JSON      []byte
	SHA256    string
}

type BunLockInventory struct {
	SchemaVersion   int             `json:"schema_version"`
	ParserPolicy    string          `json:"parser_policy"`
	Source          SourceDocument  `json:"source"`
	LockfileVersion int             `json:"lockfile_version"`
	Packages        []LockedPackage `json:"packages"`
}

type LockedPackage struct {
	Key        string               `json:"key"`
	Name       string               `json:"name"`
	Locator    string               `json:"locator"`
	Version    string               `json:"version,omitempty"`
	Integrity  string               `json:"integrity,omitempty"`
	SourceKind DependencySourceKind `json:"source_kind"`
}

type bunLockDocument struct {
	LockfileVersion int                          `json:"lockfileVersion"`
	Packages        map[string][]json.RawMessage `json:"packages"`
}

func LoadFrozenBunLock(ctx context.Context, options FrozenBunLockOptions) (BunLockInventoryResult, error) {
	if options.Repository == "" {
		return BunLockInventoryResult{}, errors.New("repository path is required")
	}
	if options.Commit == "" {
		return BunLockInventoryResult{}, errors.New("frozen commit is required")
	}
	lockPath := path.Clean(strings.ReplaceAll(options.LockPath, "\\", "/"))
	if lockPath == "." || path.IsAbs(lockPath) || lockPath == ".." || strings.HasPrefix(lockPath, "../") {
		return BunLockInventoryResult{}, fmt.Errorf("invalid bun lock path %q", options.LockPath)
	}
	commit, err := gitText(ctx, options.Repository, "rev-parse", "--verify", "--end-of-options", options.Commit+"^{commit}")
	if err != nil {
		return BunLockInventoryResult{}, fmt.Errorf("resolve frozen commit %q: %w", options.Commit, err)
	}
	content, err := gitBytes(ctx, options.Repository, "show", commit+":"+lockPath)
	if err != nil {
		return BunLockInventoryResult{}, fmt.Errorf("read %s from frozen tree: %w", lockPath, err)
	}
	return ParseBunLockInventory(BunLockInventoryOptions{LockJSONC: content, SourcePath: lockPath})
}

func ParseBunLockInventory(options BunLockInventoryOptions) (BunLockInventoryResult, error) {
	if len(options.LockJSONC) == 0 {
		return BunLockInventoryResult{}, errors.New("bun lock content is required")
	}
	if options.SourcePath == "" {
		return BunLockInventoryResult{}, errors.New("bun lock source label is required")
	}
	if !utf8.Valid(options.LockJSONC) {
		return BunLockInventoryResult{}, errors.New("bun lock is not valid UTF-8")
	}
	normalized, err := normalizeJSONC(options.LockJSONC)
	if err != nil {
		return BunLockInventoryResult{}, fmt.Errorf("normalize bun lock JSONC: %w", err)
	}
	var document bunLockDocument
	if err := json.Unmarshal(normalized, &document); err != nil {
		return BunLockInventoryResult{}, fmt.Errorf("decode bun lock: %w", err)
	}
	if document.LockfileVersion <= 0 {
		return BunLockInventoryResult{}, fmt.Errorf("unsupported bun lockfile version %d", document.LockfileVersion)
	}
	if document.Packages == nil {
		return BunLockInventoryResult{}, errors.New("bun lock packages are missing")
	}

	packages := make([]LockedPackage, 0, len(document.Packages))
	for key, fields := range document.Packages {
		locked, err := decodeLockedPackage(key, fields)
		if err != nil {
			return BunLockInventoryResult{}, err
		}
		packages = append(packages, locked)
	}
	slices.SortFunc(packages, func(a, b LockedPackage) int {
		if compared := strings.Compare(a.Name, b.Name); compared != 0 {
			return compared
		}
		if compared := strings.Compare(a.Locator, b.Locator); compared != 0 {
			return compared
		}
		return strings.Compare(a.Key, b.Key)
	})

	sourceDigest := sha256.Sum256(options.LockJSONC)
	inventory := BunLockInventory{
		SchemaVersion: 1,
		ParserPolicy:  bunLockParserPolicy,
		Source: SourceDocument{
			Path:   filepath.ToSlash(options.SourcePath),
			SHA256: hex.EncodeToString(sourceDigest[:]),
			Bytes:  int64(len(options.LockJSONC)),
		},
		LockfileVersion: document.LockfileVersion,
		Packages:        packages,
	}
	encoded, err := json.MarshalIndent(inventory, "", "  ")
	if err != nil {
		return BunLockInventoryResult{}, fmt.Errorf("encode bun lock inventory: %w", err)
	}
	encoded = append(encoded, '\n')
	return BunLockInventoryResult{Inventory: inventory, JSON: encoded, SHA256: digestBytes(encoded)}, nil
}

func decodeLockedPackage(key string, fields []json.RawMessage) (LockedPackage, error) {
	if key == "" {
		return LockedPackage{}, errors.New("bun lock package key is empty")
	}
	if len(fields) == 0 {
		return LockedPackage{}, fmt.Errorf("bun lock package %q has no locator", key)
	}
	var locator string
	if err := json.Unmarshal(fields[0], &locator); err != nil || locator == "" {
		return LockedPackage{}, fmt.Errorf("decode bun lock package %q locator", key)
	}
	name, version, sourceKind, err := classifyLockedLocator(locator)
	if err != nil {
		return LockedPackage{}, fmt.Errorf("decode bun lock package %q: %w", key, err)
	}
	integrity := ""
	for _, field := range fields[1:] {
		var candidate string
		if err := json.Unmarshal(field, &candidate); err == nil && isIntegrity(candidate) {
			integrity = candidate
		}
	}
	if sourceKind == DependencySourceRegistry && integrity == "" {
		return LockedPackage{}, fmt.Errorf("bun lock registry package %q has no integrity", key)
	}
	return LockedPackage{
		Key: key, Name: name, Locator: locator, Version: version,
		Integrity: integrity, SourceKind: sourceKind,
	}, nil
}

func classifyLockedLocator(locator string) (string, string, DependencySourceKind, error) {
	separator := packageLocatorSeparator(locator)
	if separator <= 0 || separator == len(locator)-1 {
		return "", "", DependencySourceUnknown, fmt.Errorf("invalid package locator %q", locator)
	}
	name := locator[:separator]
	resolution := locator[separator+1:]
	if strings.HasPrefix(name, "@") && !strings.Contains(name, "/") {
		return "", "", DependencySourceUnknown, fmt.Errorf("invalid scoped package locator %q", locator)
	}
	switch {
	case strings.HasPrefix(resolution, "workspace:"):
		return name, "", DependencySourceWorkspace, nil
	case isGitResolution(resolution):
		return name, "", DependencySourceGit, nil
	case strings.HasPrefix(resolution, "https:") || strings.HasPrefix(resolution, "http:"):
		return name, "", DependencySourceURL, nil
	case isFileResolution(resolution):
		return name, "", DependencySourceFile, nil
	case isExactRegistryVersion(resolution):
		return name, resolution, DependencySourceRegistry, nil
	default:
		return name, "", DependencySourceUnknown, nil
	}
}

func packageLocatorSeparator(locator string) int {
	if !strings.HasPrefix(locator, "@") {
		return strings.Index(locator, "@")
	}
	slash := strings.Index(locator, "/")
	if slash <= 1 {
		return -1
	}
	relative := strings.Index(locator[slash+1:], "@")
	if relative < 0 {
		return -1
	}
	return slash + 1 + relative
}

func isIntegrity(value string) bool {
	algorithm, encoded, found := strings.Cut(value, "-")
	if !found || encoded == "" {
		return false
	}
	switch strings.ToLower(algorithm) {
	case "sha1", "sha256", "sha384", "sha512":
		return true
	default:
		return false
	}
}

func isGitResolution(value string) bool {
	prefixes := []string{"git:", "git+", "github:", "gitlab:", "bitbucket:", "ssh:"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return strings.HasPrefix(value, "https://github.com/") || strings.HasPrefix(value, "http://github.com/")
}

func isFileResolution(value string) bool {
	if strings.HasPrefix(value, "file:") || strings.HasPrefix(value, "link:") || strings.HasPrefix(value, "portal:") {
		return true
	}
	return strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") || strings.Contains(value, "/") || strings.HasSuffix(strings.ToLower(value), ".tgz")
}

func isExactRegistryVersion(value string) bool {
	if value == "" || !unicode.IsDigit(rune(value[0])) {
		return false
	}
	return !strings.ContainsAny(value, " \\/:#*^~<>=|&")
}

func normalizeJSONC(content []byte) ([]byte, error) {
	withoutComments := append([]byte(nil), content...)
	inString := false
	escaped := false
	for index := 0; index < len(withoutComments); index++ {
		current := withoutComments[index]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if current == '\\' {
				escaped = true
				continue
			}
			if current == '"' {
				inString = false
			}
			continue
		}
		if current == '"' {
			inString = true
			continue
		}
		if current != '/' || index+1 >= len(withoutComments) {
			continue
		}
		switch withoutComments[index+1] {
		case '/':
			withoutComments[index] = ' '
			withoutComments[index+1] = ' '
			index += 2
			for ; index < len(withoutComments) && withoutComments[index] != '\n'; index++ {
				withoutComments[index] = ' '
			}
			index--
		case '*':
			withoutComments[index] = ' '
			withoutComments[index+1] = ' '
			index += 2
			closed := false
			for ; index < len(withoutComments); index++ {
				if index+1 < len(withoutComments) && withoutComments[index] == '*' && withoutComments[index+1] == '/' {
					withoutComments[index] = ' '
					withoutComments[index+1] = ' '
					index++
					closed = true
					break
				}
				if withoutComments[index] != '\n' && withoutComments[index] != '\r' {
					withoutComments[index] = ' '
				}
			}
			if !closed {
				return nil, errors.New("unterminated block comment")
			}
		}
	}
	if inString {
		return nil, errors.New("unterminated string")
	}

	normalized := make([]byte, 0, len(withoutComments))
	for index, current := range withoutComments {
		if current == ',' {
			next := index + 1
			for next < len(withoutComments) && unicode.IsSpace(rune(withoutComments[next])) {
				next++
			}
			if next < len(withoutComments) && (withoutComments[next] == '}' || withoutComments[next] == ']') {
				continue
			}
		}
		normalized = append(normalized, current)
	}
	return normalized, nil
}
