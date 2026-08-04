package sink

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestConcurrentProducersPreserveEveryValueExactlyOnce(t *testing.T) {
	const (
		producers   = 8
		perProducer = 250
		total       = producers * perProducer
	)
	s, err := New[int](7)
	if err != nil {
		t.Fatalf("new sink: %v", err)
	}

	received := make(chan []int, 1)
	go func() {
		values := make([]int, 0, total)
		for range total {
			value, receiveErr := s.Receive(context.Background())
			if receiveErr != nil {
				received <- nil
				return
			}
			values = append(values, value)
		}
		received <- values
	}()

	var wg sync.WaitGroup
	wg.Add(producers)
	for producer := range producers {
		go func(producer int) {
			defer wg.Done()
			for offset := range perProducer {
				if sendErr := s.Send(context.Background(), producer*perProducer+offset); sendErr != nil {
					t.Errorf("producer %d send %d: %v", producer, offset, sendErr)
					return
				}
			}
		}(producer)
	}
	wg.Wait()
	values := <-received
	if len(values) != total {
		t.Fatalf("received %d values, want %d", len(values), total)
	}
	counts := make([]int, total)
	for _, value := range values {
		if value < 0 || value >= total {
			t.Fatalf("received out-of-range value %d", value)
		}
		counts[value]++
	}
	for value, count := range counts {
		if count != 1 {
			t.Fatalf("value %d received %d times, want 1", value, count)
		}
	}
}

func TestReceiveHonorsCancellationAndClose(t *testing.T) {
	t.Parallel()

	s, err := New[int](1)
	if err != nil {
		t.Fatalf("new sink: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := s.Receive(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("receive error = %v, want context.DeadlineExceeded", err)
	}

	done := make(chan error, 1)
	go func() {
		_, receiveErr := s.Receive(context.Background())
		done <- receiveErr
	}()
	s.Close()
	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("receive after close error = %v, want ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not unblock receiver")
	}
}
