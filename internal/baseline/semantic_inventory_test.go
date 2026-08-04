package baseline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateSemanticInventoryExtractsFrozenTypeScriptSurface(t *testing.T) {
	repository, commit := newSemanticFixtureRepository(t)
	options := SemanticInventoryOptions{Repository: repository, Commit: commit, Branch: "dev"}

	first, err := GenerateSemanticInventory(context.Background(), options)
	if err != nil {
		t.Fatalf("generate semantic inventory: %v", err)
	}
	second, err := GenerateSemanticInventory(context.Background(), options)
	if err != nil {
		t.Fatalf("repeat semantic inventory: %v", err)
	}

	if string(first.JSON) != string(second.JSON) || first.SHA256 != second.SHA256 {
		t.Fatal("semantic inventory is not deterministic")
	}
	if !strings.HasSuffix(string(first.JSON), "\n") {
		t.Fatal("semantic inventory JSON must end with one LF")
	}
	if first.Inventory.Repository.Commit != commit || first.Inventory.Repository.Branch != "dev" {
		t.Fatalf("repository = %#v", first.Inventory.Repository)
	}
	if len(first.Inventory.Routes) != 3 {
		t.Fatalf("routes = %#v, want three real endpoints and no comment/string decoys", first.Inventory.Routes)
	}
	assertRoute(t, first.Inventory.Routes, "project.list", "GET", "Paths.list", "/api/project/:projectID", PathResolved, "v2.project.list")
	assertRoute(t, first.Inventory.Routes, "project.refresh", "POST", "Paths.refresh", "/api/project/:projectID/refresh", PathResolved, "v2.project.refresh")
	assertRoute(t, first.Inventory.Routes, "project.dynamic", "PUT", "Paths.dynamic", "", PathUnresolved, "v2.project.dynamic")

	prompt := requireEvent(t, first.Inventory.Events, "session.next.prompt.admitted")
	if prompt.Symbol != "SessionEvent.PromptAdmitted" || !prompt.Durable || prompt.DurableVersion != 1 || prompt.DurableAggregate != "sessionID" {
		t.Fatalf("prompt event = %#v", prompt)
	}
	for _, membership := range []string{"all", "durable", "server", "session-durable"} {
		if !containsString(prompt.Manifests, membership) {
			t.Fatalf("prompt manifests = %#v, missing %q", prompt.Manifests, membership)
		}
	}
	delta := requireEvent(t, first.Inventory.Events, "session.next.text.delta")
	if delta.Symbol != "SessionEvent.Text.Delta" || delta.Durable || !containsString(delta.Manifests, "all") || containsString(delta.Manifests, "durable") {
		t.Fatalf("delta event = %#v", delta)
	}
	legacy := requireEvent(t, first.Inventory.Events, "message.updated")
	if legacy.Symbol != "SessionV1.Event.MessageUpdated" || legacy.Classification != ScopeV1Archaeology ||
		!legacy.Durable || !containsString(legacy.Manifests, "all") || !containsString(legacy.Manifests, "server") ||
		!containsString(legacy.Manifests, "durable") || containsString(legacy.Manifests, "session-durable") {
		t.Fatalf("legacy object event = %#v", legacy)
	}

	if !hasSchemaExport(first.Inventory.Schemas, "Session", "packages/schema/src/session.ts", ScopeCanonicalV2) {
		t.Fatalf("schema exports = %#v, missing Session", first.Inventory.Schemas)
	}
	if !hasSchemaExport(first.Inventory.Schemas, "PositiveInt", "packages/schema/src/schema.ts", ScopeCanonicalV2) {
		t.Fatalf("schema exports = %#v, missing expanded star export", first.Inventory.Schemas)
	}
	if !hasPublicSymbol(first.Inventory.Symbols, "@opencode-ai/schema", "./session", "Info", "const", ScopeCanonicalV2) {
		t.Fatalf("public symbols = %#v, missing wildcard entrypoint value", first.Inventory.Symbols)
	}
	if !hasPublicSymbol(first.Inventory.Symbols, "@opencode-ai/schema", "./permission-v1", "Request", "const", ScopeV1Archaeology) {
		t.Fatalf("public symbols = %#v, missing V1 entrypoint classification", first.Inventory.Symbols)
	}
}

func TestGenerateSemanticInventoryFromConfiguredFrozenUpstream(t *testing.T) {
	repository := os.Getenv("OPENCODE_UPSTREAM_REPO")
	if repository == "" {
		t.Skip("OPENCODE_UPSTREAM_REPO is not configured")
	}
	result, err := GenerateSemanticInventory(context.Background(), SemanticInventoryOptions{
		Repository: repository,
		Commit:     "89130db6b0060a345548d870c51132ee71d6a828",
		Branch:     "dev",
	})
	if err != nil {
		t.Fatalf("generate frozen upstream semantic inventory: %v", err)
	}
	t.Logf("counts=%+v sha256=%s", result.Inventory.Counts, result.SHA256)
	for _, conflict := range result.Inventory.Conflicts {
		t.Logf("conflict=%+v", conflict)
	}
	for _, route := range result.Inventory.Routes {
		if route.PathStatus == PathUnresolved {
			t.Logf("unresolved route=%s expression=%s source=%s:%d", route.OperationID, route.PathExpression, route.SourcePath, route.Line)
		}
	}
	if result.Inventory.Counts.Routes < 50 || result.Inventory.Counts.Events < 50 || result.Inventory.Counts.Schemas < 20 || result.Inventory.Counts.Symbols < 200 {
		t.Fatalf("semantic counts unexpectedly small: %#v", result.Inventory.Counts)
	}
	assertRoute(t, result.Inventory.Routes, "session.list", "GET", `"/api/session"`, "/api/session", PathResolved, "v2.session.list")
	prompt := requireEvent(t, result.Inventory.Events, "session.next.prompt.admitted")
	if !prompt.Durable || !containsString(prompt.Manifests, "session-durable") {
		t.Fatalf("frozen prompt event = %#v", prompt)
	}
	legacyMessage := requireEvent(t, result.Inventory.Events, "message.updated")
	if legacyMessage.Classification != ScopeV1Archaeology || !legacyMessage.Durable ||
		!containsString(legacyMessage.Manifests, "all") || !containsString(legacyMessage.Manifests, "server") ||
		!containsString(legacyMessage.Manifests, "durable") || containsString(legacyMessage.Manifests, "session-durable") {
		t.Fatalf("frozen V1 message event = %#v", legacyMessage)
	}
	for _, record := range result.Inventory.Schemas {
		if record.Classification == ScopeV1Archaeology {
			t.Fatalf("V1 entrypoint leaked into current schema root contract: %#v", record)
		}
	}
}

func TestGenerateSemanticInventoryReportsDuplicateKeys(t *testing.T) {
	repository, _ := newSemanticFixtureRepository(t)
	writeFixtureFile(t, repository, "packages/protocol/src/groups/duplicate.ts", `
import { HttpApiEndpoint } from "effect/unstable/httpapi"
export const duplicate = HttpApiEndpoint.get("project.list", "/api/duplicate", {})
`)
	runGit(t, repository, "add", ".")
	runGit(t, repository, "commit", "-m", "duplicate route")
	commit := strings.TrimSpace(runGit(t, repository, "rev-parse", "HEAD"))

	result, err := GenerateSemanticInventory(context.Background(), SemanticInventoryOptions{
		Repository: repository, Commit: commit, Branch: "dev",
	})
	if err != nil {
		t.Fatalf("generate semantic inventory with conflict report: %v", err)
	}
	if !hasConflict(result.Inventory.Conflicts, "route", "project.list") {
		t.Fatalf("conflicts = %#v, want duplicate route conflict", result.Inventory.Conflicts)
	}
}

func TestGenerateSemanticInventoryRejectsInvalidUTF8(t *testing.T) {
	repository, _ := newSemanticFixtureRepository(t)
	invalidPath := filepath.Join(repository, "packages", "schema", "src", "invalid.ts")
	if err := os.WriteFile(invalidPath, []byte{'e', 'x', 'p', 'o', 'r', 't', ' ', 0xff, '\n'}, 0o644); err != nil {
		t.Fatalf("write invalid UTF-8 fixture: %v", err)
	}
	runGit(t, repository, "add", ".")
	runGit(t, repository, "commit", "-m", "invalid utf8")
	commit := strings.TrimSpace(runGit(t, repository, "rev-parse", "HEAD"))

	_, err := GenerateSemanticInventory(context.Background(), SemanticInventoryOptions{
		Repository: repository, Commit: commit, Branch: "dev",
	})
	if err == nil || !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Fatalf("error = %v, want invalid UTF-8 rejection", err)
	}
}

func TestDiffSemanticInventoriesClassifiesBreakingDrift(t *testing.T) {
	base := SemanticInventory{
		Repository: SemanticRepository{Commit: "base"},
		Routes: []RouteRecord{
			{OperationID: "session.get", Method: "GET", Path: "/api/session/:id", PathStatus: PathResolved},
			{OperationID: "session.old", Method: "POST", Path: "/api/session/old", PathStatus: PathResolved},
		},
		Events:  []EventRecord{{Type: "session.created", Symbol: "Session.Created", Durable: true}},
		Schemas: []SchemaExportRecord{{Symbol: "Session", Entrypoint: ".", SourcePath: "session.ts"}},
		Symbols: []PublicSymbolRecord{{Package: "schema", Entrypoint: "./session", Symbol: "Info", Kind: "const"}},
	}
	target := SemanticInventory{
		Repository: SemanticRepository{Commit: "target"},
		Routes: []RouteRecord{
			{OperationID: "session.get", Method: "GET", Path: "/api/session/:sessionID", PathStatus: PathResolved},
			{OperationID: "session.new", Method: "GET", Path: "/api/session/new", PathStatus: PathResolved},
		},
	}

	result, err := DiffSemanticInventories(base, target)
	if err != nil {
		t.Fatalf("diff semantic inventories: %v", err)
	}
	if result.Diff.Counts.Added != 1 || result.Diff.Counts.Removed != 4 || result.Diff.Counts.Changed != 1 || result.Diff.Counts.Breaking != 5 {
		t.Fatalf("diff counts = %#v", result.Diff.Counts)
	}
	if !hasChange(result.Diff.Changes, "route", "session.get", ChangeChanged, true) ||
		!hasChange(result.Diff.Changes, "route", "session.old", ChangeRemoved, true) ||
		!hasChange(result.Diff.Changes, "route", "session.new", ChangeAdded, false) ||
		!hasChange(result.Diff.Changes, "event", "session.created", ChangeRemoved, true) {
		t.Fatalf("changes = %#v", result.Diff.Changes)
	}
	if !strings.HasSuffix(string(result.JSON), "\n") || result.SHA256 == "" {
		t.Fatalf("diff encoding/hash invalid: hash=%q json=%q", result.SHA256, result.JSON)
	}
}

func TestDiffSemanticInventoriesDoesNotCollapseDuplicateNaturalKeys(t *testing.T) {
	base := SemanticInventory{Routes: []RouteRecord{
		{OperationID: "list", SourcePath: "a.ts", Line: 1, Method: "GET", Path: "/a", PathStatus: PathResolved},
		{OperationID: "list", SourcePath: "b.ts", Line: 1, Method: "GET", Path: "/b", PathStatus: PathResolved},
	}}
	target := SemanticInventory{Routes: []RouteRecord{
		{OperationID: "list", SourcePath: "b.ts", Line: 1, Method: "GET", Path: "/b", PathStatus: PathResolved},
	}}

	result, err := DiffSemanticInventories(base, target)
	if err != nil {
		t.Fatalf("diff duplicate semantic keys: %v", err)
	}
	if result.Diff.Counts.Removed != 1 || result.Diff.Counts.Breaking != 1 ||
		!hasChange(result.Diff.Changes, "route", "list@a.ts:1", ChangeRemoved, true) {
		t.Fatalf("duplicate-key changes = %#v counts=%#v", result.Diff.Changes, result.Diff.Counts)
	}
	self, err := DiffSemanticInventories(base, base)
	if err != nil {
		t.Fatalf("semantic self-diff: %v", err)
	}
	if len(self.Diff.Changes) != 0 || self.Diff.Counts != (SemanticDiffCounts{}) {
		t.Fatalf("semantic self-diff = %#v", self.Diff)
	}
}

func newSemanticFixtureRepository(t *testing.T) (string, string) {
	t.Helper()
	repository, _ := newFixtureRepository(t)
	files := map[string]string{
		"packages/schema/package.json": `{
  "name": "@opencode-ai/schema",
  "exports": {".": "./src/index.ts", "./*": "./src/*.ts"}
}` + "\n",
		"packages/schema/src/index.ts": `
export { Session } from "./session"
export * from "./schema"
`,
		"packages/schema/src/session.ts": `
export * as Session from "./session"
export const Info = Schema.Struct({})
export interface Info {}
`,
		"packages/schema/src/schema.ts": `
export const PositiveInt = Schema.Int
`,
		"packages/schema/src/permission-v1.ts": `
export * as PermissionV1 from "./permission-v1"
export const Request = Schema.Struct({})
`,
		"packages/schema/src/event.ts": `
export * as Event from "./event"
export function define(input: unknown) { return input }
export function inventory(...input: unknown[]) { return input }
export function durable(input: unknown) { return input }
`,
		"packages/schema/src/session-event.ts": `
export * as SessionEvent from "./session-event"
import { Event } from "./event"
const options = { durable: { aggregate: "sessionID", version: 1 } } as const
export const PromptAdmitted = Event.define({
  type: "session.next.prompt.admitted",
  ...options,
  schema: {},
})
export namespace Text {
  // Event.define({ type: "fake.comment", schema: {} })
  export const Delta = Event.define({ type: "session.next.text.delta", schema: {} })
}
export const DurableDefinitions = Event.inventory(PromptAdmitted)
export const Definitions = Event.inventory(PromptAdmitted, Text.Delta)
`,
		"packages/schema/src/session-v1.ts": `
export * from "./v1/session"
`,
		"packages/schema/src/v1/session.ts": `
export * as SessionV1 from "./session"
import { define, inventory } from "../event"
const options = { durable: { aggregate: "sessionID", version: 1 } } as const
const events = {
  MessageUpdated: define({ type: "message.updated", ...options, schema: {} }),
}
export const PartDelta = define({ type: "message.part.delta", schema: {} })
export const Event = {
  ...events,
  PartDelta,
  Definitions: inventory(events.MessageUpdated, PartDelta),
}
`,
		"packages/schema/src/event-manifest.ts": `
import { Event } from "./event"
import { SessionEvent } from "./session-event"
import { SessionV1 } from "./session-v1"
const sessionV1DurableDefinitions = SessionV1.Event.Definitions.filter((definition) => definition.durable !== undefined)
const sessionV1LiveDefinitions = SessionV1.Event.Definitions.filter((definition) => definition.durable === undefined)
const coreDefinitions = Event.inventory(...sessionV1DurableDefinitions, ...SessionEvent.Definitions)
export const ServerDefinitions = Event.inventory(...coreDefinitions)
export const Definitions = Event.inventory(...coreDefinitions, ...sessionV1LiveDefinitions)
`,
		"packages/schema/src/durable-event-manifest.ts": `
import { Event } from "./event"
import { SessionEvent } from "./session-event"
import { SessionV1 } from "./session-v1"
export const SessionDurable = { definitions: Event.durable(SessionEvent.DurableDefinitions) }
export const Durable = Event.durable([
  ...SessionV1.Event.Definitions.filter((definition) => definition.durable !== undefined),
  ...SessionEvent.DurableDefinitions,
])
`,
		"packages/protocol/src/groups/project.ts": `
import { HttpApiEndpoint } from "effect/unstable/httpapi"
const root = "/api/project/:projectID"
const Paths = {
  list: root,
  refresh: ` + "`${root}/refresh`" + `,
  dynamic: makePath(),
}
const decoy = "HttpApiEndpoint.get(\\\"fake.string\\\", \\\"/fake\\\", {})"
// HttpApiEndpoint.get("fake.comment", "/fake", {})
export const list = HttpApiEndpoint.get("project.list", Paths.list, {}).annotateMerge(
  OpenApi.annotations({ identifier: "v2.project.list" }),
)
export const refresh = HttpApiEndpoint.post(
  "project.refresh",
  Paths.refresh,
  {},
).annotateMerge(OpenApi.annotations({ identifier: "v2.project.refresh" }))
export const dynamic = HttpApiEndpoint.put("project.dynamic", Paths.dynamic, {}).annotateMerge(
  OpenApi.annotations({ identifier: "v2.project.dynamic" }),
)
`,
	}
	for path, content := range files {
		writeFixtureFile(t, repository, path, content)
	}
	runGit(t, repository, "add", ".")
	runGit(t, repository, "commit", "-m", "semantic fixture")
	return repository, strings.TrimSpace(runGit(t, repository, "rev-parse", "HEAD"))
}

func assertRoute(t *testing.T, routes []RouteRecord, operation string, method string, expression string, path string, status PathStatus, identifier string) {
	t.Helper()
	for _, route := range routes {
		if route.OperationID != operation {
			continue
		}
		if route.Method != method || route.PathExpression != expression || route.Path != path || route.PathStatus != status || route.OpenAPIIdentifier != identifier {
			t.Fatalf("route %s = %#v", operation, route)
		}
		if route.Line < 1 || route.SourcePath == "" || route.Classification != ScopeCanonicalV2 {
			t.Fatalf("route source metadata = %#v", route)
		}
		return
	}
	t.Fatalf("route %q not found in %#v", operation, routes)
}

func requireEvent(t *testing.T, events []EventRecord, eventType string) EventRecord {
	t.Helper()
	for _, event := range events {
		if event.Type == eventType {
			return event
		}
	}
	t.Fatalf("event %q not found in %#v", eventType, events)
	return EventRecord{}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func hasSchemaExport(exports []SchemaExportRecord, symbol string, source string, scope ScopeClassification) bool {
	for _, record := range exports {
		if record.Symbol == symbol && record.SourcePath == source && record.Classification == scope {
			return true
		}
	}
	return false
}

func hasPublicSymbol(symbols []PublicSymbolRecord, packageName string, entrypoint string, symbol string, kind string, scope ScopeClassification) bool {
	for _, record := range symbols {
		if record.Package == packageName && record.Entrypoint == entrypoint && record.Symbol == symbol && record.Kind == kind && record.Classification == scope {
			return true
		}
	}
	return false
}

func hasConflict(conflicts []SemanticConflict, entity string, key string) bool {
	for _, conflict := range conflicts {
		if conflict.Entity == entity && conflict.Key == key {
			return true
		}
	}
	return false
}

func hasChange(changes []SemanticChange, entity string, key string, kind ChangeKind, breaking bool) bool {
	for _, change := range changes {
		if change.Entity == entity && change.Key == key && change.Kind == kind && change.Breaking == breaking {
			return true
		}
	}
	return false
}
