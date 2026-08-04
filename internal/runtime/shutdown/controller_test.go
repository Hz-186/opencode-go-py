package shutdown

import (
	"sync"
	"testing"
	"time"
)

func TestControllerEscalatesFirstAndSecondSignal(t *testing.T) {
	t.Parallel()

	controller := NewController()
	if got := controller.State(); got != Running {
		t.Fatalf("initial state = %v, want Running", got)
	}
	if got := controller.Observe(); got != StartShutdown {
		t.Fatalf("first action = %v, want StartShutdown", got)
	}
	if got := controller.State(); got != ShutdownRequested {
		t.Fatalf("state after first signal = %v, want ShutdownRequested", got)
	}
	select {
	case <-controller.Shutdown():
	case <-time.After(time.Second):
		t.Fatal("shutdown channel was not closed")
	}
	select {
	case <-controller.Force():
		t.Fatal("force channel closed before second signal")
	default:
	}

	if got := controller.Observe(); got != ForceShutdown {
		t.Fatalf("second action = %v, want ForceShutdown", got)
	}
	if got := controller.State(); got != ForceRequested {
		t.Fatalf("state after second signal = %v, want ForceRequested", got)
	}
	select {
	case <-controller.Force():
	case <-time.After(time.Second):
		t.Fatal("force channel was not closed")
	}
	if got := controller.Observe(); got != ForceShutdown {
		t.Fatalf("third action = %v, want ForceShutdown", got)
	}
}

func TestControllerConcurrentSignalsHaveSingleShutdownWinner(t *testing.T) {
	t.Parallel()

	controller := NewController()
	const callers = 64
	actions := make(chan Action, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			actions <- controller.Observe()
		}()
	}
	wg.Wait()
	close(actions)

	shutdowns := 0
	forces := 0
	for action := range actions {
		switch action {
		case StartShutdown:
			shutdowns++
		case ForceShutdown:
			forces++
		default:
			t.Fatalf("unexpected action %v", action)
		}
	}
	if shutdowns != 1 {
		t.Fatalf("StartShutdown actions = %d, want 1", shutdowns)
	}
	if forces != callers-1 {
		t.Fatalf("ForceShutdown actions = %d, want %d", forces, callers-1)
	}
	if got := controller.State(); got != ForceRequested {
		t.Fatalf("final state = %v, want ForceRequested", got)
	}
}
