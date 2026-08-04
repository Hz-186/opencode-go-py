package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hz-186/opencode-go-py/internal/domain"
)

func TestResolverReadsExplicitPathsAndAppliesEveryPrecedenceLayer(t *testing.T) {
	directory := t.TempDir()
	remotePath := filepath.Join(directory, "remote.jsonc")
	if err := os.WriteFile(remotePath, []byte("{\"model\":\"remote/model\"}\n"), 0o600); err != nil {
		t.Fatalf("write remote config: %v", err)
	}
	sources := []Source{
		{ID: "managed-preference", Kind: ManagedPreference, Content: []byte(`{"model":"managed-preference/model"}`)},
		{ID: "organization", Kind: Organization, Content: []byte(`{"model":"organization/model"}`)},
		{ID: "directory", Kind: Directory, Content: []byte(`{"model":"directory/model"}`)},
		{ID: "custom", Kind: Custom, Content: []byte(`{"model":"custom/model"}`)},
		{ID: "remote", Kind: Remote, Path: remotePath},
		{ID: "managed", Kind: Managed, Content: []byte(`{"model":"managed/model"}`)},
		{ID: "project", Kind: Project, Content: []byte(`{"model":"project/model"}`)},
		{ID: "inline", Kind: Inline, Content: []byte(`{"model":"inline/model"}`)},
		{ID: "global", Kind: Global, Content: []byte(`{"model":"global/model"}`)},
	}

	resolved, err := (Resolver{}).Resolve(context.Background(), sources, 11)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	assertStringField(t, resolved.Value.Object, "model", "managed-preference/model")
	want := []string{"remote", "global", "custom", "project", "directory", "inline", "organization", "managed", "managed-preference"}
	if got := sourceIDs(resolved.Sources); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("source order = %v, want %v", got, want)
	}
	if resolved.Sources[0].Path != remotePath || resolved.Sources[0].Digest == "" || resolved.Generation != 11 {
		t.Fatalf("remote source/generation = %+v/%d", resolved.Sources[0], resolved.Generation)
	}
}

func TestResolverPathOptionalReadErrorsAndExplicitEnvironment(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("OPENCODE_CONFIG_AMBIENT_FIXTURE", "must-not-be-read")
	resolver := Resolver{Environment: map[string]string{"EXPLICIT": "explicit-value"}}
	resolved, err := resolver.Resolve(context.Background(), []Source{
		{ID: "optional", Kind: Remote, Path: filepath.Join(directory, "missing.json"), Optional: true},
		{ID: "content-wins", Kind: Global, Path: filepath.Join(directory, "also-missing.json"), Content: []byte(`{"model":"{env:EXPLICIT}","username":"{env:OPENCODE_CONFIG_AMBIENT_FIXTURE}"}`)},
	}, 1)
	if err != nil {
		t.Fatalf("resolve optional/content: %v", err)
	}
	if len(resolved.Sources) != 1 || resolved.Sources[0].ID != "content-wins" {
		t.Fatalf("resolved sources = %+v, want content-wins only", resolved.Sources)
	}
	assertStringField(t, resolved.Value.Object, "model", "explicit-value")
	assertStringField(t, resolved.Value.Object, "username", "")

	permissionResolver := Resolver{ReadFile: func(string) ([]byte, error) { return nil, os.ErrPermission }}
	_, err = permissionResolver.Resolve(context.Background(), []Source{{ID: "permission", Kind: Project, Path: "fixture.json"}}, 1)
	assertConfigError(t, err, Read, "permission", "", os.ErrPermission)
	_, err = (Resolver{}).Resolve(context.Background(), []Source{{ID: "missing-input", Kind: Project}}, 1)
	assertConfigError(t, err, Read, "missing-input", "", nil)
}

func TestResolverSubstitutionContracts(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "home.txt"), []byte(" home value \n"), 0o600); err != nil {
		t.Fatalf("write home fixture: %v", err)
	}
	resolved, err := (Resolver{Home: directory}).Resolve(context.Background(), []Source{{
		ID: "home", Kind: Project, BaseDir: directory,
		Content: []byte(`{"instructions":["{file:~/home.txt}"]}`),
	}}, 1)
	if err != nil {
		t.Fatalf("resolve home file: %v", err)
	}
	assertStringArrayField(t, resolved.Value.Object, "instructions", []string{"home value"})

	_, err = (Resolver{}).Resolve(context.Background(), []Source{{
		ID: "no-home", Kind: Project, Content: []byte(`{"instructions":["{file:~/missing}"]}`),
	}}, 1)
	assertConfigError(t, err, Substitute, "no-home", "", nil)
	_, err = (Resolver{}).Resolve(context.Background(), []Source{{
		ID: "no-base", Kind: Project, Content: []byte(`{"instructions":["{file:relative.txt}"]}`),
	}}, 1)
	assertConfigError(t, err, Substitute, "no-base", "", nil)
}

func TestTopLevelConfigTypeMatrix(t *testing.T) {
	valid := `{
  "$schema":"schema", "shell":"/bin/sh", "logLevel":"DEBUG",
  "snapshot":true, "autoshare":false, "share":"manual", "autoupdate":"notify",
  "model":"provider/model", "small_model":"provider/small", "default_agent":"build", "username":"fixture",
  "subagent_depth":0, "disabled_providers":["disabled"], "enabled_providers":["enabled"],
  "instructions":["one"], "plugin":[], "layout":"auto",
  "server":{}, "command":{}, "skills":{}, "references":{}, "reference":{}, "watcher":{},
  "mode":{}, "agent":{}, "provider":{}, "mcp":{}, "permission":{}, "tools":{},
  "attachment":{}, "enterprise":{}, "tool_output":{}, "compaction":{}, "experimental":{},
  "formatter":false, "lsp":{}, "theme":{}, "keybinds":{}, "tui":{}
}`
	resolved, err := (Resolver{}).Resolve(context.Background(), []Source{{ID: "valid-matrix", Kind: Project, Content: []byte(valid)}}, 1)
	if err != nil {
		t.Fatalf("resolve valid matrix: %v", err)
	}
	for _, removed := range []string{"theme", "keybinds", "tui"} {
		if _, ok := resolved.Value.Object[removed]; ok {
			t.Fatalf("legacy UI field %q was retained", removed)
		}
	}

	tests := map[string]string{
		"string":            `{"model":true}`,
		"log level":         `{"logLevel":"TRACE"}`,
		"boolean":           `{"snapshot":"true"}`,
		"share enum":        `{"share":"sometimes"}`,
		"negative depth":    `{"subagent_depth":-1}`,
		"fractional depth":  `{"subagent_depth":1.5}`,
		"string array":      `{"instructions":[1]}`,
		"plugin array":      `{"plugin":{}}`,
		"object":            `{"server":[]}`,
		"object or boolean": `{"formatter":[]}`,
		"autoupdate":        `{"autoupdate":"always"}`,
		"layout type":       `{"layout":true}`,
		"layout enum":       `{"layout":"wide"}`,
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := (Resolver{}).Resolve(context.Background(), []Source{{ID: name, Kind: Project, Content: []byte(content)}}, 1)
			var configErr *Error
			if !errors.As(err, &configErr) || configErr.Stage != Validate || configErr.Field == "" {
				t.Fatalf("validation error = %v, want typed field error", err)
			}
		})
	}
}

func TestConfigParseSourceAndCloneFailureContracts(t *testing.T) {
	tests := map[string][]byte{
		"invalid UTF-8":        {0xff},
		"array root":           []byte(`[]`),
		"unterminated comment": []byte(`{/* broken`),
		"duplicate key":        []byte(`{"model":"one","model":"two"}`),
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := (Resolver{}).Resolve(context.Background(), []Source{{ID: name, Kind: Project, Content: content}}, 1)
			assertConfigError(t, err, Parse, name, "", nil)
		})
	}
	_, err := (Resolver{}).Resolve(context.Background(), []Source{
		{ID: "duplicate", Kind: Global, Content: []byte(`{}`)},
		{ID: "duplicate", Kind: Project, Content: []byte(`{}`)},
	}, 1)
	assertConfigError(t, err, Validate, "duplicate", "", nil)
	_, err = (Resolver{}).Resolve(context.Background(), []Source{{ID: "bad-kind", Kind: SourceKind("future"), Content: []byte(`{}`)}}, 1)
	assertConfigError(t, err, Validate, "bad-kind", "", nil)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Resolver{}).Resolve(canceled, []Source{{ID: "canceled", Kind: Project, Content: []byte(`{}`)}}, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled resolve error = %v, want context.Canceled", err)
	}

	original := ResolvedConfig{
		Value: domain.JSONObject(map[string]domain.JSONValue{
			"nested": domain.JSONObject(map[string]domain.JSONValue{"value": domain.JSONString("original")}),
		}),
		Sources:    []SourceRef{{ID: "source", Kind: Project}},
		Origins:    map[string]SourceRef{"/nested/value": {ID: "source", Kind: Project}},
		Generation: 4,
	}
	clone := original.Clone()
	nested := clone.Value.Object["nested"]
	nested.Object["value"] = domain.JSONString("mutated")
	clone.Value.Object["nested"] = nested
	clone.Sources[0].ID = "mutated"
	clone.Origins["/nested/value"] = SourceRef{ID: "mutated"}
	if original.Value.Object["nested"].Object["value"].String != "original" || original.Sources[0].ID != "source" ||
		original.Origins["/nested/value"].ID != "source" {
		t.Fatalf("original config was mutated: %+v", original)
	}
}

func assertConfigError(t *testing.T, err error, stage Stage, source, field string, cause error) {
	t.Helper()
	var configErr *Error
	if !errors.As(err, &configErr) {
		t.Fatalf("error type = %T, want *Error: %v", err, err)
	}
	if configErr.Stage != stage || configErr.Source != source || configErr.Field != field {
		t.Fatalf("config error = %+v, want stage=%s source=%q field=%q", configErr, stage, source, field)
	}
	if cause != nil && !errors.Is(err, cause) {
		t.Fatalf("config error = %v, want cause %v", err, cause)
	}
	if configErr.Error() == "" {
		t.Fatal("config error rendered empty text")
	}
}
