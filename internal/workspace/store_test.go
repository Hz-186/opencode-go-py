package workspace

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hz-186/opencode-go-py/internal/config"
	"github.com/Hz-186/opencode-go-py/internal/domain"
	"github.com/Hz-186/opencode-go-py/internal/project"
	"github.com/Hz-186/opencode-go-py/internal/runtime/scope"
)

func TestConcurrentLoadSharesOneInflightInstance(t *testing.T) {
	t.Parallel()

	root := scope.NewRoot(context.Background(), "app")
	var boots atomic.Int64
	store, err := NewStore(root, func(_ context.Context, input BootInput) (Snapshot, error) {
		boots.Add(1)
		time.Sleep(10 * time.Millisecond)
		return Snapshot{Directory: input.Directory, Worktree: input.Directory}, nil
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	directory := t.TempDir()
	const callers = 64
	instances := make(chan *Instance, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			instance, loadErr := store.Load(context.Background(), directory)
			instances <- instance
			errs <- loadErr
		}()
	}
	wg.Wait()
	close(instances)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("load: %v", err)
		}
	}
	var first *Instance
	for instance := range instances {
		if first == nil {
			first = instance
		}
		if instance != first {
			t.Fatal("concurrent load returned different instances")
		}
	}
	if got := boots.Load(); got != 1 {
		t.Fatalf("boot count = %d, want 1", got)
	}
	if first.Generation() != 1 {
		t.Fatalf("generation = %d, want 1", first.Generation())
	}
	if err := store.DisposeAll(context.Background()); err != nil {
		t.Fatalf("dispose all: %v", err)
	}
}

func TestCanceledWaiterDoesNotCancelSharedBoot(t *testing.T) {
	t.Parallel()

	root := scope.NewRoot(context.Background(), "app")
	started := make(chan struct{})
	release := make(chan struct{})
	store, err := NewStore(root, func(_ context.Context, input BootInput) (Snapshot, error) {
		close(started)
		<-release
		return Snapshot{Directory: input.Directory, Worktree: input.Directory}, nil
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	directory := t.TempDir()
	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	firstDone := make(chan error, 1)
	go func() {
		_, loadErr := store.Load(waitCtx, directory)
		firstDone <- loadErr
	}()
	<-started
	if err := <-firstDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first load error = %v, want deadline", err)
	}
	close(release)
	instance, err := store.Load(context.Background(), directory)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if instance == nil {
		t.Fatal("shared boot returned nil instance")
	}
	_ = store.DisposeAll(context.Background())
}

func TestCanceledDisposeDirectoryWaiterDoesNotOrphanSharedBoot(t *testing.T) {
	t.Parallel()

	root := scope.NewRoot(context.Background(), "app")
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	var closed atomic.Int64
	store, err := NewStore(root, func(_ context.Context, input BootInput) (Snapshot, error) {
		startOnce.Do(func() { close(started) })
		<-release
		if err := input.Scope.Register(context.Background(), scope.ResourceFunc(func(context.Context) error {
			closed.Add(1)
			return nil
		})); err != nil {
			return Snapshot{}, err
		}
		return Snapshot{Directory: input.Directory, Worktree: input.Directory}, nil
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	directory := t.TempDir()
	loaded := make(chan *Instance, 1)
	loadErr := make(chan error, 1)
	go func() {
		instance, err := store.Load(context.Background(), directory)
		loaded <- instance
		loadErr <- err
	}()
	<-started

	disposeCtx, cancelDispose := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelDispose()
	if err := store.DisposeDirectory(disposeCtx, directory); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled dispose error = %v, want deadline", err)
	}
	close(release)
	first := <-loaded
	if err := <-loadErr; err != nil {
		t.Fatalf("shared load: %v", err)
	}
	second, err := store.Load(context.Background(), directory)
	if err != nil {
		t.Fatalf("load after canceled dispose: %v", err)
	}
	if second != first || second.Generation() != 1 {
		t.Fatalf("canceled dispose orphaned generation: first=%p/%d second=%p/%d", first, first.Generation(), second, second.Generation())
	}
	if err := store.DisposeDirectory(context.Background(), directory); err != nil {
		t.Fatalf("retry dispose: %v", err)
	}
	if got := closed.Load(); got != 1 {
		t.Fatalf("close count = %d, want 1", got)
	}
}

func TestBootFailureIsEvictedAndRetried(t *testing.T) {
	t.Parallel()

	root := scope.NewRoot(context.Background(), "app")
	fixtureErr := errors.New("fixture boot failure")
	var boots atomic.Int64
	store, err := NewStore(root, func(_ context.Context, input BootInput) (Snapshot, error) {
		if boots.Add(1) == 1 {
			return Snapshot{}, fixtureErr
		}
		return Snapshot{Directory: input.Directory, Worktree: input.Directory}, nil
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	directory := t.TempDir()
	if _, err := store.Load(context.Background(), directory); !errors.Is(err, fixtureErr) {
		t.Fatalf("first load error = %v, want fixture error", err)
	}
	instance, err := store.Load(context.Background(), directory)
	if err != nil {
		t.Fatalf("retry load: %v", err)
	}
	if instance.Generation() != 2 {
		t.Fatalf("retry generation = %d, want 2", instance.Generation())
	}
	_ = store.DisposeAll(context.Background())
}

func TestReloadFencesGenerationAndStaleDisposeCannotCloseNewInstance(t *testing.T) {
	t.Parallel()

	root := scope.NewRoot(context.Background(), "app")
	var closed sync.Map
	store, err := NewStore(root, func(_ context.Context, input BootInput) (Snapshot, error) {
		if err := input.Scope.Register(context.Background(), scope.ResourceFunc(func(context.Context) error {
			counter, _ := closed.LoadOrStore(input.Generation, &atomic.Int64{})
			counter.(*atomic.Int64).Add(1)
			return nil
		})); err != nil {
			return Snapshot{}, err
		}
		return Snapshot{Directory: input.Directory, Worktree: input.Directory}, nil
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	directory := t.TempDir()
	first, err := store.Load(context.Background(), directory)
	if err != nil {
		t.Fatalf("load first: %v", err)
	}
	second, err := store.Reload(context.Background(), directory)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if first.Generation() != 1 || second.Generation() != 2 || first == second {
		t.Fatalf("generations = first:%d second:%d", first.Generation(), second.Generation())
	}
	assertCloseCount(t, &closed, 1, 1)
	if err := store.Dispose(context.Background(), first); err != nil {
		t.Fatalf("dispose stale: %v", err)
	}
	assertCloseCount(t, &closed, 2, 0)
	if err := store.Dispose(context.Background(), second); err != nil {
		t.Fatalf("dispose current: %v", err)
	}
	assertCloseCount(t, &closed, 2, 1)
}

func TestRootCloseFencesStoreAndSnapshotClonesNestedPointers(t *testing.T) {
	t.Parallel()

	root := scope.NewRoot(context.Background(), "app")
	workspaceID := domain.WorkspaceID("workspace-one")
	previous := domain.ProjectID("previous-project")
	store, err := NewStore(root, func(_ context.Context, input BootInput) (Snapshot, error) {
		return Snapshot{
			Directory: input.Directory,
			Worktree:  input.Directory,
			Project: project.Resolved{
				Previous: &previous,
				ID:       domain.ProjectID("project-one"),
				VCS:      &project.VCS{Type: "git", Store: "git-store"},
			},
			WorkspaceID: &workspaceID,
			Config: config.ResolvedConfig{
				Value:      domain.JSONObject(map[string]domain.JSONValue{"model": domain.JSONString("provider/model")}),
				Origins:    map[string]config.SourceRef{"/model": {ID: "fixture", Kind: config.Project}},
				Generation: 9,
			},
		}, nil
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	directory := t.TempDir()
	instance, err := store.Load(context.Background(), directory)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if instance.Scope() == nil || instance.Scope().Kind() != scope.Project {
		t.Fatalf("instance scope = %v, want project scope", instance.Scope())
	}
	first := instance.Snapshot()
	*first.WorkspaceID = domain.WorkspaceID("mutated-workspace")
	*first.Project.Previous = domain.ProjectID("mutated-previous")
	first.Project.VCS.Type = "mutated-vcs"
	first.Config.Value.Object["model"] = domain.JSONString("mutated/model")
	first.Config.Origins["/model"] = config.SourceRef{ID: "mutated"}
	second := instance.Snapshot()
	if *second.WorkspaceID != workspaceID || *second.Project.Previous != previous || second.Project.VCS.Type != "git" ||
		second.Config.Value.Object["model"].String != "provider/model" || second.Config.Origins["/model"].ID != "fixture" {
		t.Fatalf("snapshot clone was mutated: %+v", second)
	}
	if err := root.Close(context.Background()); err != nil {
		t.Fatalf("close root: %v", err)
	}
	if _, err := store.Load(context.Background(), directory); !errors.Is(err, ErrClosed) {
		t.Fatalf("load after close error = %v, want ErrClosed", err)
	}
	if _, err := store.Reload(context.Background(), directory); !errors.Is(err, ErrClosed) {
		t.Fatalf("reload after close error = %v, want ErrClosed", err)
	}
	if err := store.Dispose(context.Background(), nil); err != nil {
		t.Fatalf("dispose nil: %v", err)
	}
}

func TestNewStoreRejectsInvalidScopeAndBootInputs(t *testing.T) {
	t.Parallel()

	root := scope.NewRoot(context.Background(), "app")
	projectScope, err := root.Child(scope.Project, "fixture")
	if err != nil {
		t.Fatalf("project scope: %v", err)
	}
	validBoot := func(context.Context, BootInput) (Snapshot, error) { return Snapshot{}, nil }
	for name, input := range map[string]struct {
		root *scope.Scope
		boot BootFunc
	}{
		"nil root": {boot: validBoot},
		"non-root": {root: projectScope, boot: validBoot},
		"nil boot": {root: root},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewStore(input.root, input.boot); err == nil {
				t.Fatal("invalid store construction succeeded")
			}
		})
	}
	if err := root.Close(context.Background()); err != nil {
		t.Fatalf("close root: %v", err)
	}
	if _, err := NewStore(root, validBoot); err == nil {
		t.Fatal("store registered on a closed root")
	}
}

func assertCloseCount(t *testing.T, values *sync.Map, generation uint64, want int64) {
	t.Helper()
	value, ok := values.Load(generation)
	if !ok {
		if want == 0 {
			return
		}
		t.Fatalf("generation %d has no close counter", generation)
	}
	if got := value.(*atomic.Int64).Load(); got != want {
		t.Fatalf("generation %d close count = %d, want %d", generation, got, want)
	}
}
