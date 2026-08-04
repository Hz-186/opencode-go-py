package scope

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestRegisterCloseRaceClosesEveryResourceExactlyOnce(t *testing.T) {
	root := NewRoot(context.Background(), "app")
	const resources = 2_000
	counts := make([]atomic.Int64, resources)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(resources)
	for i := range resources {
		go func(index int) {
			defer wg.Done()
			<-start
			_ = root.Register(context.Background(), ResourceFunc(func(context.Context) error {
				counts[index].Add(1)
				return nil
			}))
		}(i)
	}
	close(start)
	if err := root.Close(context.Background()); err != nil {
		t.Fatalf("close root: %v", err)
	}
	wg.Wait()
	if err := root.Close(context.Background()); err != nil {
		t.Fatalf("repeat close root: %v", err)
	}
	for i := range counts {
		if got := counts[i].Load(); got != 1 {
			t.Fatalf("resource %d close count = %d, want 1", i, got)
		}
	}
}

func TestScopeCloseJoinsErrorsAndContinuesReverseCleanup(t *testing.T) {
	t.Parallel()

	firstErr := errors.New("first fixture failure")
	secondErr := errors.New("second fixture failure")
	root := NewRoot(context.Background(), "app")
	if err := root.Register(context.Background(), ResourceFunc(func(context.Context) error {
		return firstErr
	})); err != nil {
		t.Fatalf("register first: %v", err)
	}
	if err := root.Register(context.Background(), ResourceFunc(func(context.Context) error {
		return secondErr
	})); err != nil {
		t.Fatalf("register second: %v", err)
	}

	err := root.Close(context.Background())
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("close error = %v, want both fixture failures", err)
	}
}
