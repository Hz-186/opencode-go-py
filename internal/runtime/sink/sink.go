// Package sink provides a bounded, context-aware backpressure primitive.
package sink

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var (
	// ErrClosed reports an operation on a closed sink.
	ErrClosed = errors.New("bounded sink is closed")
	// ErrInvalidCapacity reports a non-positive bound.
	ErrInvalidCapacity = errors.New("bounded sink capacity must be positive")
)

// Sink is a bounded FIFO. Producers block when capacity is exhausted and all
// blocking operations can be interrupted by a context or Close.
type Sink[T any] struct {
	values chan T
	done   chan struct{}
	once   sync.Once
}

// New constructs a sink with an exact positive capacity.
func New[T any](capacity int) (*Sink[T], error) {
	if capacity <= 0 {
		return nil, fmt.Errorf("%w: %d", ErrInvalidCapacity, capacity)
	}
	return &Sink[T]{
		values: make(chan T, capacity),
		done:   make(chan struct{}),
	}, nil
}

// Send enqueues value, applying backpressure when the sink is full.
func (s *Sink[T]) Send(ctx context.Context, value T) error {
	select {
	case <-s.done:
		return ErrClosed
	default:
	}

	select {
	case <-s.done:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	case s.values <- value:
		return nil
	}
}

// Receive dequeues the oldest value. Values accepted before Close remain
// available; ErrClosed is returned after the buffer is drained.
func (s *Sink[T]) Receive(ctx context.Context) (T, error) {
	select {
	case value := <-s.values:
		return value, nil
	default:
	}

	select {
	case value := <-s.values:
		return value, nil
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	case <-s.done:
		select {
		case value := <-s.values:
			return value, nil
		default:
			var zero T
			return zero, ErrClosed
		}
	}
}

// Close releases blocked producers and consumers. It is idempotent.
func (s *Sink[T]) Close() {
	s.once.Do(func() {
		close(s.done)
	})
}

// Len returns the number of currently buffered values.
func (s *Sink[T]) Len() int {
	return len(s.values)
}

// Cap returns the configured bound.
func (s *Sink[T]) Cap() int {
	return cap(s.values)
}
