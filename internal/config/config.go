// Package config resolves canonical configuration snapshots with provenance.
package config

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/Hz-186/opencode-go-py/internal/domain"
)

type SourceKind string

const (
	Remote            SourceKind = "remote"
	Global            SourceKind = "global"
	Custom            SourceKind = "custom"
	Project           SourceKind = "project"
	Directory         SourceKind = "directory"
	Inline            SourceKind = "inline"
	Organization      SourceKind = "organization"
	Managed           SourceKind = "managed"
	ManagedPreference SourceKind = "managed_preference"
)

type Stage string

const (
	Read       Stage = "read"
	Substitute Stage = "substitute"
	Parse      Stage = "parse"
	Validate   Stage = "validate"
)

type Error struct {
	Stage  Stage
	Source string
	Field  string
	Cause  error
}

func (e *Error) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("config %s failed for %q at %s: %v", e.Stage, e.Source, e.Field, e.Cause)
	}
	return fmt.Sprintf("config %s failed for %q: %v", e.Stage, e.Source, e.Cause)
}

func (e *Error) Unwrap() error {
	return e.Cause
}

type Source struct {
	ID       string
	Kind     SourceKind
	Path     string
	BaseDir  string
	Content  []byte
	Optional bool
}

type SourceRef struct {
	ID     string
	Kind   SourceKind
	Path   string
	Digest string
}

type ResolvedConfig struct {
	Value      domain.JSONValue
	Sources    []SourceRef
	Origins    map[string]SourceRef
	Generation uint64
}

// Clone returns an isolated immutable-by-copy snapshot.
func (c ResolvedConfig) Clone() ResolvedConfig {
	return cloneResolved(c)
}

// Resolver contains only explicit inputs; it never scans ambient integration
// configuration or environment variables.
type Resolver struct {
	Environment map[string]string
	Home        string
	ReadFile    func(string) ([]byte, error)
}

var envToken = regexp.MustCompile("\\{env:([^}]+)\\}")

func (r Resolver) Resolve(ctx context.Context, sources []Source, generation uint64) (ResolvedConfig, error) {
	ordered := append([]Source(nil), sources...)
	slices.SortStableFunc(ordered, func(left, right Source) int {
		return precedence(left.Kind) - precedence(right.Kind)
	})

	result := domain.JSONObject(map[string]domain.JSONValue{})
	origins := make(map[string]SourceRef)
	refs := make([]SourceRef, 0, len(ordered))
	seen := make(map[string]struct{}, len(ordered))
	for _, source := range ordered {
		if err := ctx.Err(); err != nil {
			return ResolvedConfig{}, err
		}
		if source.ID == "" || precedence(source.Kind) < 0 {
			return ResolvedConfig{}, &Error{
				Stage: Validate, Source: source.ID,
				Cause: errors.New("source id and kind must be valid"),
			}
		}
		if _, duplicate := seen[source.ID]; duplicate {
			return ResolvedConfig{}, &Error{
				Stage: Validate, Source: source.ID,
				Cause: errors.New("duplicate config source id"),
			}
		}
		seen[source.ID] = struct{}{}

		content, missing, err := r.read(source)
		if err != nil {
			return ResolvedConfig{}, &Error{Stage: Read, Source: source.ID, Cause: err}
		}
		if missing {
			continue
		}
		ref := SourceRef{
			ID:     source.ID,
			Kind:   source.Kind,
			Path:   source.Path,
			Digest: fmt.Sprintf("%x", sha256.Sum256(content)),
		}
		baseDir := source.BaseDir
		if baseDir == "" && source.Path != "" {
			baseDir = filepath.Dir(source.Path)
		}
		expanded, err := r.substitute(ctx, string(content), baseDir)
		if err != nil {
			return ResolvedConfig{}, &Error{Stage: Substitute, Source: source.ID, Cause: err}
		}
		parsed, err := parseJSONC([]byte(expanded))
		if err != nil {
			return ResolvedConfig{}, &Error{Stage: Parse, Source: source.ID, Cause: err}
		}
		if err := validateTopLevel(parsed); err != nil {
			var fieldErr *fieldError
			if errors.As(err, &fieldErr) {
				return ResolvedConfig{}, &Error{
					Stage: Validate, Source: source.ID, Field: fieldErr.field, Cause: fieldErr.cause,
				}
			}
			return ResolvedConfig{}, &Error{Stage: Validate, Source: source.ID, Cause: err}
		}
		delete(parsed.Object, "theme")
		delete(parsed.Object, "keybinds")
		delete(parsed.Object, "tui")
		result = mergeValues("", result, parsed, ref, origins)
		refs = append(refs, ref)
	}

	return cloneResolved(ResolvedConfig{
		Value: result, Sources: refs, Origins: origins, Generation: generation,
	}), nil
}

func (r Resolver) read(source Source) ([]byte, bool, error) {
	if source.Content != nil {
		return append([]byte(nil), source.Content...), false, nil
	}
	if source.Path == "" {
		return nil, false, errors.New("source must provide content or path")
	}
	read := r.ReadFile
	if read == nil {
		read = os.ReadFile
	}
	content, err := read(source.Path)
	if err != nil {
		if source.Optional && errors.Is(err, os.ErrNotExist) {
			return nil, true, nil
		}
		return nil, false, err
	}
	return content, false, nil
}

func (r Resolver) substitute(ctx context.Context, text, baseDir string) (string, error) {
	environment := make(map[string]string, len(r.Environment))
	for key, value := range r.Environment {
		environment[key] = value
	}
	text = envToken.ReplaceAllStringFunc(text, func(token string) string {
		match := envToken.FindStringSubmatch(token)
		return environment[match[1]]
	})

	var output strings.Builder
	cursor := 0
	for {
		relative := strings.Index(text[cursor:], "{file:")
		if relative < 0 {
			output.WriteString(text[cursor:])
			return output.String(), nil
		}
		index := cursor + relative
		endRelative := strings.IndexByte(text[index:], '}')
		if endRelative < 0 {
			output.WriteString(text[cursor:])
			return output.String(), nil
		}
		end := index + endRelative + 1
		token := text[index:end]
		output.WriteString(text[cursor:index])

		lineStart := strings.LastIndexByte(text[:index], '\n') + 1
		if strings.HasPrefix(strings.TrimLeft(text[lineStart:index], " \t\r"), "//") {
			output.WriteString(token)
			cursor = end
			continue
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		name := strings.TrimSuffix(strings.TrimPrefix(token, "{file:"), "}")
		path := name
		if strings.HasPrefix(path, "~/") {
			if r.Home == "" {
				return "", fmt.Errorf("file reference %q needs an explicit home directory", token)
			}
			path = filepath.Join(r.Home, strings.TrimPrefix(path, "~/"))
		} else if !filepath.IsAbs(path) {
			if baseDir == "" {
				return "", fmt.Errorf("file reference %q needs an explicit base directory", token)
			}
			path = filepath.Join(baseDir, path)
		}
		read := r.ReadFile
		if read == nil {
			read = os.ReadFile
		}
		content, err := read(filepath.Clean(path))
		if err != nil {
			return "", fmt.Errorf("bad file reference %q: %w", token, err)
		}
		quoted := strconv.Quote(strings.TrimSpace(string(content)))
		output.WriteString(quoted[1 : len(quoted)-1])
		cursor = end
	}
}

type Manager struct {
	resolver Resolver
	reloadMu sync.Mutex
	mu       sync.RWMutex
	current  ResolvedConfig
	hasValue bool
}

func NewManager(resolver Resolver) *Manager {
	return &Manager{resolver: resolver}
}

// Reload advances generation only after full resolution. Failure returns the
// last valid snapshot together with the error.
func (m *Manager) Reload(ctx context.Context, sources []Source) (ResolvedConfig, error) {
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()

	m.mu.RLock()
	generation := m.current.Generation + 1
	m.mu.RUnlock()
	next, err := m.resolver.Resolve(ctx, sources, generation)
	if err != nil {
		current, _ := m.Current()
		return current, err
	}
	m.mu.Lock()
	m.current = cloneResolved(next)
	m.hasValue = true
	m.mu.Unlock()
	return cloneResolved(next), nil
}

func (m *Manager) Current() (ResolvedConfig, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.hasValue {
		return ResolvedConfig{}, false
	}
	return cloneResolved(m.current), true
}

func precedence(kind SourceKind) int {
	switch kind {
	case Remote:
		return 0
	case Global:
		return 10
	case Custom:
		return 20
	case Project:
		return 30
	case Directory:
		return 40
	case Inline:
		return 50
	case Organization:
		return 60
	case Managed:
		return 70
	case ManagedPreference:
		return 80
	default:
		return -1
	}
}
