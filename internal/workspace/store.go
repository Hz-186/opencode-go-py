// Package workspace owns directory-scoped runtime instances.
package workspace

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Hz-186/opencode-go-py/internal/config"
	"github.com/Hz-186/opencode-go-py/internal/domain"
	"github.com/Hz-186/opencode-go-py/internal/platform/pathx"
	"github.com/Hz-186/opencode-go-py/internal/project"
	"github.com/Hz-186/opencode-go-py/internal/runtime/scope"
)

var ErrClosed = errors.New("workspace instance store is closed")

type Snapshot struct {
	Directory   string
	Worktree    string
	Project     project.Resolved
	WorkspaceID *domain.WorkspaceID
	Config      config.ResolvedConfig
}

type BootInput struct {
	Directory  string
	Generation uint64
	Scope      *scope.Scope
}

type BootFunc func(context.Context, BootInput) (Snapshot, error)

type Instance struct {
	generation uint64
	snapshot   Snapshot
	scope      *scope.Scope
}

func (i *Instance) Generation() uint64 {
	return i.generation
}

func (i *Instance) Snapshot() Snapshot {
	return cloneSnapshot(i.snapshot)
}

func (i *Instance) Scope() *scope.Scope {
	return i.scope
}

type entry struct {
	generation uint64
	ready      chan struct{}
	instance   *Instance
	err        error
}

type Store struct {
	root     *scope.Scope
	boot     BootFunc
	caseMode pathx.CaseMode

	mu          sync.Mutex
	entries     map[string]*entry
	generations map[string]uint64
	closed      bool
}

func NewStore(root *scope.Scope, boot BootFunc) (*Store, error) {
	if root == nil || root.Kind() != scope.Root || boot == nil {
		return nil, errors.New("workspace store requires root scope and boot function")
	}
	store := &Store{
		root:        root,
		boot:        boot,
		entries:     make(map[string]*entry),
		generations: make(map[string]uint64),
	}
	if err := root.Register(context.Background(), store); err != nil {
		return nil, fmt.Errorf("register workspace store: %w", err)
	}
	return store, nil
}

func (s *Store) Load(ctx context.Context, directory string) (*Instance, error) {
	key, err := pathx.Canonical(directory, s.caseMode)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	current := s.entries[key]
	if current == nil {
		current = s.newEntryLocked(key)
		go s.complete(key, current)
	}
	s.mu.Unlock()
	return waitEntry(ctx, current)
}

func (s *Store) Reload(ctx context.Context, directory string) (*Instance, error) {
	key, err := pathx.Canonical(directory, s.caseMode)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	previous := s.entries[key]
	next := s.newEntryLocked(key)
	s.mu.Unlock()

	go func() {
		if previous != nil {
			<-previous.ready
			if previous.instance != nil {
				_ = previous.instance.scope.Close(context.Background())
			}
		}
		s.complete(key, next)
	}()
	return waitEntry(ctx, next)
}

func (s *Store) Dispose(ctx context.Context, instance *Instance) error {
	if instance == nil {
		return nil
	}
	key := instance.snapshot.Directory
	s.mu.Lock()
	current := s.entries[key]
	if current == nil || current.instance != instance {
		s.mu.Unlock()
		return nil
	}
	delete(s.entries, key)
	s.mu.Unlock()
	return instance.scope.Close(ctx)
}

func (s *Store) DisposeDirectory(ctx context.Context, directory string) error {
	key, err := pathx.Canonical(directory, s.caseMode)
	if err != nil {
		return err
	}
	s.mu.Lock()
	current := s.entries[key]
	s.mu.Unlock()
	if current == nil {
		return nil
	}
	instance, err := waitEntry(ctx, current)
	if err != nil {
		return err
	}
	return s.Dispose(ctx, instance)
}

func (s *Store) DisposeAll(ctx context.Context) error {
	s.mu.Lock()
	entries := make([]*entry, 0, len(s.entries))
	for key, current := range s.entries {
		entries = append(entries, current)
		delete(s.entries, key)
	}
	s.mu.Unlock()

	var errs []error
	for _, current := range entries {
		instance, err := waitEntry(ctx, current)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := instance.scope.Close(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Store) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return s.DisposeAll(ctx)
}

func (s *Store) newEntryLocked(key string) *entry {
	s.generations[key]++
	current := &entry{generation: s.generations[key], ready: make(chan struct{})}
	s.entries[key] = current
	return current
}

func (s *Store) complete(key string, current *entry) {
	projectScope, err := s.root.Child(scope.Project, key)
	if err == nil {
		var snapshot Snapshot
		snapshot, err = s.boot(projectScope.Context(), BootInput{
			Directory: key, Generation: current.generation, Scope: projectScope,
		})
		if err == nil {
			snapshot.Directory = key
			if snapshot.Worktree == "" {
				snapshot.Worktree = key
			}
			current.instance = &Instance{
				generation: current.generation,
				snapshot:   cloneSnapshot(snapshot),
				scope:      projectScope,
			}
		} else {
			_ = projectScope.Close(context.Background())
		}
	}
	current.err = err
	if err != nil {
		s.mu.Lock()
		if s.entries[key] == current {
			delete(s.entries, key)
		}
		s.mu.Unlock()
	}
	close(current.ready)
}

func waitEntry(ctx context.Context, current *entry) (*Instance, error) {
	select {
	case <-current.ready:
		return current.instance, current.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	result := snapshot
	result.Config = snapshot.Config.Clone()
	if snapshot.WorkspaceID != nil {
		value := *snapshot.WorkspaceID
		result.WorkspaceID = &value
	}
	if snapshot.Project.Previous != nil {
		value := *snapshot.Project.Previous
		result.Project.Previous = &value
	}
	if snapshot.Project.VCS != nil {
		value := *snapshot.Project.VCS
		result.Project.VCS = &value
	}
	return result
}
