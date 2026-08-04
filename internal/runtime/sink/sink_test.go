package sink

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSinkAppliesBackpressure(t *testing.T) {
	t.Parallel()

	s, err := New[int](1)
	if err != nil {
		t.Fatalf("new sink: %v", err)
	}
	if err := s.Send(context.Background(), 1); err != nil {
		t.Fatalf("send first value: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := s.Send(ctx, 2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked send error = %v, want context.DeadlineExceeded", err)
	}
	if got, want := s.Len(), 1; got != want {
		t.Fatalf("sink length = %d, want %d", got, want)
	}
}

func TestSlowConsumerReleasesBlockedProducer(t *testing.T) {
	t.Parallel()

	s, err := New[string](1)
	if err != nil {
		t.Fatalf("new sink: %v", err)
	}
	if err := s.Send(context.Background(), "first"); err != nil {
		t.Fatalf("send first value: %v", err)
	}

	producer := make(chan error, 1)
	go func() {
		producer <- s.Send(context.Background(), "second")
	}()
	select {
	case err := <-producer:
		t.Fatalf("producer completed without backpressure: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	value, err := s.Receive(context.Background())
	if err != nil {
		t.Fatalf("receive first value: %v", err)
	}
	if value != "first" {
		t.Fatalf("first value = %q, want first", value)
	}
	select {
	case err := <-producer:
		if err != nil {
			t.Fatalf("send second value: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("producer remained blocked after consumer received")
	}

	value, err = s.Receive(context.Background())
	if err != nil {
		t.Fatalf("receive second value: %v", err)
	}
	if value != "second" {
		t.Fatalf("second value = %q, want second", value)
	}
}

func TestCloseUnblocksSendAndDrainsBufferedValues(t *testing.T) {
	t.Parallel()

	s, err := New[int](1)
	if err != nil {
		t.Fatalf("new sink: %v", err)
	}
	if err := s.Send(context.Background(), 1); err != nil {
		t.Fatalf("send first value: %v", err)
	}

	producer := make(chan error, 1)
	go func() {
		producer <- s.Send(context.Background(), 2)
	}()
	time.Sleep(10 * time.Millisecond)
	s.Close()
	s.Close()

	select {
	case err := <-producer:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("blocked send error = %v, want ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked send was not released by close")
	}

	value, err := s.Receive(context.Background())
	if err != nil {
		t.Fatalf("drain buffered value: %v", err)
	}
	if value != 1 {
		t.Fatalf("drained value = %d, want 1", value)
	}
	if _, err := s.Receive(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("receive after drain error = %v, want ErrClosed", err)
	}
	if err := s.Send(context.Background(), 3); !errors.Is(err, ErrClosed) {
		t.Fatalf("send after close error = %v, want ErrClosed", err)
	}
}

func TestSinkRejectsInvalidCapacity(t *testing.T) {
	t.Parallel()

	if _, err := New[int](0); !errors.Is(err, ErrInvalidCapacity) {
		t.Fatalf("zero capacity error = %v, want ErrInvalidCapacity", err)
	}
	if _, err := New[int](-1); !errors.Is(err, ErrInvalidCapacity) {
		t.Fatalf("negative capacity error = %v, want ErrInvalidCapacity", err)
	}
}
