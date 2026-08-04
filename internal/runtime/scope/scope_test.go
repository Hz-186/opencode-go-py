package scope

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestScopeTreeClosesChildrenAndResourcesInReverseOrder(t *testing.T) {
	t.Parallel()

	var (
		mu    sync.Mutex
		order []string
	)
	record := func(name string) Resource {
		return ResourceFunc(func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			order = append(order, name)
			return nil
		})
	}

	root := NewRoot(context.Background(), "app")
	if err := root.Register(context.Background(), record("root")); err != nil {
		t.Fatalf("register root resource: %v", err)
	}
	project, err := root.Child(Project, "project-1")
	if err != nil {
		t.Fatalf("create project scope: %v", err)
	}
	if err := project.Register(context.Background(), record("project")); err != nil {
		t.Fatalf("register project resource: %v", err)
	}
	session, err := project.Child(Session, "session-1")
	if err != nil {
		t.Fatalf("create session scope: %v", err)
	}
	if err := session.Register(context.Background(), record("session")); err != nil {
		t.Fatalf("register session resource: %v", err)
	}
	turn, err := session.Child(Turn, "turn-1")
	if err != nil {
		t.Fatalf("create turn scope: %v", err)
	}
	if err := turn.Register(context.Background(), record("turn")); err != nil {
		t.Fatalf("register turn resource: %v", err)
	}

	if got, want := turn.Kind(), Turn; got != want {
		t.Fatalf("turn kind = %q, want %q", got, want)
	}
	if got, want := turn.Name(), "turn-1"; got != want {
		t.Fatalf("turn name = %q, want %q", got, want)
	}
	if err := root.Close(context.Background()); err != nil {
		t.Fatalf("close root: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"turn", "session", "project", "root"}
	if len(order) != len(want) {
		t.Fatalf("close order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("close order = %v, want %v", order, want)
		}
	}
}

func TestScopePropagatesParentCancellation(t *testing.T) {
	t.Parallel()

	parent, cancel := context.WithCancel(context.Background())
	root := NewRoot(parent, "app")
	project, err := root.Child(Project, "project-1")
	if err != nil {
		t.Fatalf("create project scope: %v", err)
	}
	session, err := project.Child(Session, "session-1")
	if err != nil {
		t.Fatalf("create session scope: %v", err)
	}
	turn, err := session.Child(Turn, "turn-1")
	if err != nil {
		t.Fatalf("create turn scope: %v", err)
	}

	cancel()
	select {
	case <-turn.Context().Done():
		if !errors.Is(context.Cause(turn.Context()), context.Canceled) {
			t.Fatalf("turn cancellation cause = %v, want context.Canceled", context.Cause(turn.Context()))
		}
	case <-time.After(time.Second):
		t.Fatal("turn context was not canceled")
	}

	if err := root.Close(context.Background()); err != nil {
		t.Fatalf("close canceled tree: %v", err)
	}
}

func TestScopeCloseIsConcurrentAndIdempotent(t *testing.T) {
	t.Parallel()

	root := NewRoot(context.Background(), "app")
	var closes atomic.Int64
	if err := root.Register(context.Background(), ResourceFunc(func(context.Context) error {
		closes.Add(1)
		time.Sleep(5 * time.Millisecond)
		return nil
	})); err != nil {
		t.Fatalf("register resource: %v", err)
	}

	const callers = 64
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			errs <- root.Close(context.Background())
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent close: %v", err)
		}
	}
	if got := closes.Load(); got != 1 {
		t.Fatalf("resource closes = %d, want 1", got)
	}
}

func TestScopeCloseHonorsDeadlineAndContinuesCleanup(t *testing.T) {
	t.Parallel()

	root := NewRoot(context.Background(), "app")
	var earlierClosed atomic.Bool
	if err := root.Register(context.Background(), ResourceFunc(func(context.Context) error {
		earlierClosed.Store(true)
		return nil
	})); err != nil {
		t.Fatalf("register earlier resource: %v", err)
	}
	if err := root.Register(context.Background(), ResourceFunc(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})); err != nil {
		t.Fatalf("register hanging resource: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := root.Close(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("close elapsed = %s, want <= 500ms", elapsed)
	}
	if !earlierClosed.Load() {
		t.Fatal("resource before hanging resource was not closed")
	}
}

func TestRegisterAfterCloseRollsBackOnlyNewResource(t *testing.T) {
	t.Parallel()

	root := NewRoot(context.Background(), "app")
	var existingCloses atomic.Int64
	if err := root.Register(context.Background(), ResourceFunc(func(context.Context) error {
		existingCloses.Add(1)
		return nil
	})); err != nil {
		t.Fatalf("register existing resource: %v", err)
	}
	if err := root.Close(context.Background()); err != nil {
		t.Fatalf("close root: %v", err)
	}

	var rejectedCloses atomic.Int64
	err := root.Register(context.Background(), ResourceFunc(func(context.Context) error {
		rejectedCloses.Add(1)
		return nil
	}))
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("register error = %v, want ErrClosed", err)
	}
	if got := existingCloses.Load(); got != 1 {
		t.Fatalf("existing resource closes = %d, want 1", got)
	}
	if got := rejectedCloses.Load(); got != 1 {
		t.Fatalf("rejected resource closes = %d, want 1", got)
	}
}

func TestScopeRejectsInvalidHierarchy(t *testing.T) {
	t.Parallel()

	root := NewRoot(context.Background(), "app")
	if _, err := root.Child(Session, "session-1"); !errors.Is(err, ErrInvalidHierarchy) {
		t.Fatalf("root to session error = %v, want ErrInvalidHierarchy", err)
	}
	project, err := root.Child(Project, "project-1")
	if err != nil {
		t.Fatalf("create project scope: %v", err)
	}
	if _, err := project.Child(Turn, "turn-1"); !errors.Is(err, ErrInvalidHierarchy) {
		t.Fatalf("project to turn error = %v, want ErrInvalidHierarchy", err)
	}
	if err := root.Close(context.Background()); err != nil {
		t.Fatalf("close root: %v", err)
	}
}

func TestScopeTenThousandCreateCloseCycles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping scope stress test in short mode")
	}

	runtime.GC()
	before := runtime.NumGoroutine()
	for i := 0; i < 10_000; i++ {
		root := NewRoot(context.Background(), "app")
		project, err := root.Child(Project, "project")
		if err != nil {
			t.Fatalf("cycle %d create project: %v", i, err)
		}
		session, err := project.Child(Session, "session")
		if err != nil {
			t.Fatalf("cycle %d create session: %v", i, err)
		}
		turn, err := session.Child(Turn, "turn")
		if err != nil {
			t.Fatalf("cycle %d create turn: %v", i, err)
		}
		if err := turn.Register(context.Background(), ResourceFunc(func(context.Context) error { return nil })); err != nil {
			t.Fatalf("cycle %d register: %v", i, err)
		}
		if err := root.Close(context.Background()); err != nil {
			t.Fatalf("cycle %d close: %v", i, err)
		}
	}
	runtime.GC()
	time.Sleep(20 * time.Millisecond)
	after := runtime.NumGoroutine()
	if delta := after - before; delta > 4 {
		t.Fatalf("goroutine delta after 10,000 cycles = %d (before=%d after=%d)", delta, before, after)
	}
}
