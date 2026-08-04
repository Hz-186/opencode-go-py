package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestResolverAppliesCanonicalPrecedenceDeepMergeAndProvenance(t *testing.T) {
	t.Parallel()

	resolver := Resolver{}
	sources := []Source{
		{ID: "managed", Kind: Managed, Content: []byte(`{"model":"managed/model","provider":{"openai":{"options":{"region":"managed"}}}}`)},
		{ID: "project", Kind: Project, Content: []byte(`{"model":"project/model","provider":{"openai":{"options":{"timeout":30}}},"instructions":["project.md","shared.md"]}`)},
		{ID: "global", Kind: Global, Content: []byte(`{"model":"global/model","provider":{"openai":{"api":"responses","options":{"timeout":10}}},"instructions":["global.md","shared.md"]}`)},
		{ID: "inline", Kind: Inline, Content: []byte(`{"model":"inline/model","enabled_providers":["openai"]}`)},
		{ID: "custom", Kind: Custom, Content: []byte(`{"small_model":"custom/small","enabled_providers":["anthropic"]}`)},
	}

	resolved, err := resolver.Resolve(context.Background(), sources, 7)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Generation != 7 {
		t.Fatalf("generation = %d, want 7", resolved.Generation)
	}
	assertStringField(t, resolved.Value.Object, "model", "managed/model")
	assertStringField(t, resolved.Value.Object, "small_model", "custom/small")
	providers := objectField(t, resolved.Value.Object, "provider")
	openai := objectField(t, providers, "openai")
	assertStringField(t, openai, "api", "responses")
	options := objectField(t, openai, "options")
	assertNumberField(t, options, "timeout", "30")
	assertStringField(t, options, "region", "managed")
	assertStringArrayField(t, resolved.Value.Object, "enabled_providers", []string{"openai"})
	assertStringArrayField(t, resolved.Value.Object, "instructions", []string{"global.md", "shared.md", "project.md"})

	if got := resolved.Origins["/model"].ID; got != "managed" {
		t.Fatalf("model origin = %q, want managed", got)
	}
	if got := resolved.Origins["/provider/openai/api"].ID; got != "global" {
		t.Fatalf("provider api origin = %q, want global", got)
	}
	if got := resolved.Origins["/provider/openai/options/timeout"].ID; got != "project" {
		t.Fatalf("timeout origin = %q, want project", got)
	}
	wantOrder := []string{"global", "custom", "project", "inline", "managed"}
	if len(resolved.Sources) != len(wantOrder) {
		t.Fatalf("source count = %d, want %d", len(resolved.Sources), len(wantOrder))
	}
	for i, want := range wantOrder {
		if resolved.Sources[i].ID != want {
			t.Fatalf("source order = %v, want %v", sourceIDs(resolved.Sources), wantOrder)
		}
		if resolved.Sources[i].Digest == "" {
			t.Fatalf("source %q has empty digest", want)
		}
	}
}

func TestResolverParsesJSONCAndSubstitutesEnvironmentAndFiles(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "中文 config")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatalf("make config directory: %v", err)
	}
	secretPath := filepath.Join(directory, "prompt.txt")
	if err := os.WriteFile(secretPath, []byte("line one\n\"line two\"\n"), 0o600); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	content := []byte(`{
  // {file:ignored.txt} remains a comment
  "model": "{env:MODEL_ID}",
  "username": "prefix-{env:MISSING}-suffix",
  "instructions": ["{file:prompt.txt}",],
}`)
	resolver := Resolver{
		Environment: map[string]string{"MODEL_ID": "provider/model"},
		Home:        directory,
	}

	resolved, err := resolver.Resolve(context.Background(), []Source{{
		ID:      "project-jsonc",
		Kind:    Project,
		BaseDir: directory,
		Content: content,
	}}, 1)
	if err != nil {
		t.Fatalf("resolve JSONC: %v", err)
	}
	assertStringField(t, resolved.Value.Object, "model", "provider/model")
	assertStringField(t, resolved.Value.Object, "username", "prefix--suffix")
	assertStringArrayField(t, resolved.Value.Object, "instructions", []string{"line one\n\"line two\""})
}

func TestResolverReportsSourceStageAndField(t *testing.T) {
	t.Parallel()

	resolver := Resolver{}
	_, err := resolver.Resolve(context.Background(), []Source{{
		ID: "bad-json", Kind: Project, Content: []byte(`{"model":`),
	}}, 1)
	var configErr *Error
	if !errors.As(err, &configErr) {
		t.Fatalf("parse error type = %T, want *Error", err)
	}
	if configErr.Source != "bad-json" || configErr.Stage != Parse {
		t.Fatalf("parse error = %+v, want source bad-json and stage parse", configErr)
	}

	_, err = resolver.Resolve(context.Background(), []Source{{
		ID: "unknown-field", Kind: Project, Content: []byte(`{"not_a_config_key":true}`),
	}}, 1)
	if !errors.As(err, &configErr) {
		t.Fatalf("validation error type = %T, want *Error", err)
	}
	if configErr.Source != "unknown-field" || configErr.Stage != Validate || configErr.Field != "/not_a_config_key" {
		t.Fatalf("validation error = %+v, want source/stage/field", configErr)
	}
}

func TestManagerKeepsLastValidGenerationAndImmutableSnapshots(t *testing.T) {
	t.Parallel()

	manager := NewManager(Resolver{})
	first, err := manager.Reload(context.Background(), []Source{{
		ID: "first", Kind: Project,
		Content: []byte(`{"model":"provider/one","provider":{"p":{"options":{"timeout":10}}}}`),
	}})
	if err != nil {
		t.Fatalf("first reload: %v", err)
	}
	if first.Generation != 1 {
		t.Fatalf("first generation = %d, want 1", first.Generation)
	}

	failed, err := manager.Reload(context.Background(), []Source{{
		ID: "broken", Kind: Project, Content: []byte(`{"unknown":true}`),
	}})
	if err == nil {
		t.Fatal("broken reload unexpectedly succeeded")
	}
	if failed.Generation != 1 {
		t.Fatalf("failed reload generation = %d, want last valid 1", failed.Generation)
	}
	assertStringField(t, failed.Value.Object, "model", "provider/one")

	first.Value.Object["model"] = jsonString("mutated")
	current, ok := manager.Current()
	if !ok {
		t.Fatal("manager has no current snapshot")
	}
	assertStringField(t, current.Value.Object, "model", "provider/one")

	second, err := manager.Reload(context.Background(), []Source{{
		ID: "second", Kind: Project, Content: []byte(`{"model":"provider/two"}`),
	}})
	if err != nil {
		t.Fatalf("second reload: %v", err)
	}
	if second.Generation != 2 {
		t.Fatalf("second generation = %d, want 2", second.Generation)
	}
}

func TestManagerSerializesConcurrentReloadGenerations(t *testing.T) {
	manager := NewManager(Resolver{})
	const reloads = 64
	var wg sync.WaitGroup
	errs := make(chan error, reloads)
	wg.Add(reloads)
	for range reloads {
		go func() {
			defer wg.Done()
			_, err := manager.Reload(context.Background(), []Source{{
				ID: "concurrent", Kind: Inline, Content: []byte(`{"model":"provider/model"}`),
			}})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent reload: %v", err)
		}
	}
	current, ok := manager.Current()
	if !ok {
		t.Fatal("manager has no current snapshot")
	}
	if current.Generation != reloads {
		t.Fatalf("generation = %d, want %d", current.Generation, reloads)
	}
}

func sourceIDs(sources []SourceRef) []string {
	result := make([]string, len(sources))
	for i := range sources {
		result[i] = sources[i].ID
	}
	return result
}
