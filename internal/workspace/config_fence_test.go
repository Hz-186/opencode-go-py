package workspace

import (
	"context"
	"testing"

	"github.com/Hz-186/opencode-go-py/internal/config"
	"github.com/Hz-186/opencode-go-py/internal/domain"
	"github.com/Hz-186/opencode-go-py/internal/runtime/scope"
)

func TestReloadCapturesNewConfigGenerationWithoutMutatingOldSnapshot(t *testing.T) {
	t.Parallel()

	configManager := config.NewManager(config.Resolver{})
	if _, err := configManager.Reload(context.Background(), []config.Source{{
		ID: "one", Kind: config.Project, Content: []byte(`{"model":"provider/one"}`),
	}}); err != nil {
		t.Fatalf("load config one: %v", err)
	}
	root := scope.NewRoot(context.Background(), "app")
	store, err := NewStore(root, func(_ context.Context, input BootInput) (Snapshot, error) {
		current, ok := configManager.Current()
		if !ok {
			t.Fatal("boot has no config")
		}
		return Snapshot{Directory: input.Directory, Worktree: input.Directory, Config: current}, nil
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	directory := t.TempDir()
	first, err := store.Load(context.Background(), directory)
	if err != nil {
		t.Fatalf("load first: %v", err)
	}

	if _, err := configManager.Reload(context.Background(), []config.Source{{
		ID: "two", Kind: config.Project, Content: []byte(`{"model":"provider/two"}`),
	}}); err != nil {
		t.Fatalf("load config two: %v", err)
	}
	second, err := store.Reload(context.Background(), directory)
	if err != nil {
		t.Fatalf("reload instance: %v", err)
	}
	firstSnapshot := first.Snapshot()
	secondSnapshot := second.Snapshot()
	if firstSnapshot.Config.Generation != 1 || secondSnapshot.Config.Generation != 2 {
		t.Fatalf("config generations = first:%d second:%d", firstSnapshot.Config.Generation, secondSnapshot.Config.Generation)
	}
	if got := firstSnapshot.Config.Value.Object["model"].String; got != "provider/one" {
		t.Fatalf("old snapshot model = %q, want provider/one", got)
	}
	firstSnapshot.Config.Value.Object["model"] = domain.JSONString("mutated")
	if got := first.Snapshot().Config.Value.Object["model"].String; got != "provider/one" {
		t.Fatalf("cached old snapshot mutated to %q", got)
	}
	if got := secondSnapshot.Config.Value.Object["model"].String; got != "provider/two" {
		t.Fatalf("new snapshot model = %q, want provider/two", got)
	}
	_ = store.DisposeAll(context.Background())
}
