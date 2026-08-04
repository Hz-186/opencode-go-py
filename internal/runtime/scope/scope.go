// Package scope owns hierarchical runtime lifetimes and their resources.
package scope

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var (
	// ErrClosed reports an operation attempted after shutdown started.
	ErrClosed = errors.New("runtime scope is closed")
	// ErrInvalidHierarchy reports a child kind that cannot belong to its parent.
	ErrInvalidHierarchy = errors.New("invalid runtime scope hierarchy")
	// ErrNilResource reports an attempt to register a nil resource.
	ErrNilResource = errors.New("runtime scope resource is nil")
)

// Kind identifies a node in the canonical runtime lifetime tree.
type Kind string

const (
	Root    Kind = "root"
	Project Kind = "project"
	Session Kind = "session"
	Turn    Kind = "turn"
)

// Resource is released when its owning scope closes. Implementations must
// honor cancellation and deadlines in ctx.
type Resource interface {
	Close(context.Context) error
}

// ResourceFunc adapts a function into a Resource.
type ResourceFunc func(context.Context) error

// Close calls f.
func (f ResourceFunc) Close(ctx context.Context) error {
	if f == nil {
		return nil
	}
	return f(ctx)
}

type scopeState uint8

const (
	scopeOpen scopeState = iota
	scopeClosing
	scopeClosed
)

// Scope owns a context and a last-in-first-out stack of resources.
// Scope is safe for concurrent registration and closure.
type Scope struct {
	kind   Kind
	name   string
	ctx    context.Context
	cancel context.CancelCauseFunc

	mu        sync.Mutex
	state     scopeState
	resources []Resource
	done      chan struct{}
	closeErr  error
}

// NewRoot creates the root of a runtime lifetime tree.
func NewRoot(parent context.Context, name string) *Scope {
	ctx, cancel := context.WithCancelCause(parent)
	return &Scope{
		kind:   Root,
		name:   name,
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}
}

// Context returns the scope context. It is canceled when an ancestor is
// canceled or this scope starts closing.
func (s *Scope) Context() context.Context {
	return s.ctx
}

// Kind returns the node kind.
func (s *Scope) Kind() Kind {
	return s.kind
}

// Name returns the node name supplied by its owner.
func (s *Scope) Name() string {
	return s.name
}

// Child creates and registers the next valid node in the canonical tree.
func (s *Scope) Child(kind Kind, name string) (*Scope, error) {
	if !validChild(s.kind, kind) {
		return nil, fmt.Errorf("%w: %s cannot contain %s", ErrInvalidHierarchy, s.kind, kind)
	}

	ctx, cancel := context.WithCancelCause(s.ctx)
	child := &Scope{
		kind:   kind,
		name:   name,
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	if err := s.Register(context.Background(), child); err != nil {
		return nil, fmt.Errorf("register %s scope %q: %w", kind, name, err)
	}
	return child, nil
}

// Register transfers ownership of resource to the scope. If shutdown has
// already started, Register closes only resource with ctx and returns
// ErrClosed. Previously registered resources are never closed by rollback.
func (s *Scope) Register(ctx context.Context, resource Resource) error {
	if resource == nil {
		return ErrNilResource
	}

	s.mu.Lock()
	if s.state == scopeOpen {
		s.resources = append(s.resources, resource)
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	return errors.Join(ErrClosed, resource.Close(ctx))
}

// Close cancels the scope and closes owned resources in reverse registration
// order. Exactly one caller performs cleanup; concurrent callers observe the
// same result or their own waiting-context cancellation.
func (s *Scope) Close(ctx context.Context) error {
	s.mu.Lock()
	switch s.state {
	case scopeClosing, scopeClosed:
		done := s.done
		s.mu.Unlock()
		select {
		case <-done:
			s.mu.Lock()
			err := s.closeErr
			s.mu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	case scopeOpen:
		s.state = scopeClosing
		resources := s.resources
		s.resources = nil
		s.cancel(ErrClosed)
		s.mu.Unlock()

		err := closeReverse(ctx, resources)

		s.mu.Lock()
		s.closeErr = err
		s.state = scopeClosed
		close(s.done)
		s.mu.Unlock()
		return err
	default:
		s.mu.Unlock()
		panic("unreachable runtime scope state")
	}
}

func validChild(parent, child Kind) bool {
	switch parent {
	case Root:
		return child == Project
	case Project:
		return child == Session
	case Session:
		return child == Turn
	default:
		return false
	}
}

func closeReverse(ctx context.Context, resources []Resource) error {
	errs := make([]error, 0, len(resources))
	for i := len(resources) - 1; i >= 0; i-- {
		if err := resources[i].Close(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
