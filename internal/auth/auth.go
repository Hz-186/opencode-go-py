// Package auth defines the canonical credential reference and lease boundary.
package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

var (
	ErrInvalidRef   = errors.New("invalid secret reference")
	ErrInvalid      = errors.New("invalid credential material")
	ErrNotFound     = errors.New("credential not found")
	ErrExpired      = errors.New("credential expired")
	ErrLeaseClosed  = errors.New("credential lease is closed")
	ErrSecretAbsent = errors.New("credential secret is absent")
)

// SecretRef is a safe, non-secret lookup identity.
type SecretRef struct {
	provider string
	slot     string
}

// NewSecretRef validates and normalizes a provider credential identity.
func NewSecretRef(provider, slot string) (SecretRef, error) {
	provider = strings.TrimRight(strings.TrimSpace(provider), "/")
	slot = strings.TrimSpace(slot)
	if provider == "" || slot == "" ||
		strings.IndexByte(provider, 0) >= 0 || strings.IndexByte(slot, 0) >= 0 ||
		strings.Contains(slot, "/") {
		return SecretRef{}, ErrInvalidRef
	}
	return SecretRef{provider: provider, slot: slot}, nil
}

func (r SecretRef) Provider() string {
	return r.provider
}

func (r SecretRef) Slot() string {
	return r.slot
}

func (r SecretRef) String() string {
	return "secret://" + r.provider + "/" + r.slot
}

// CredentialKind identifies the material shape without exposing its values.
type CredentialKind string

const (
	APIKey    CredentialKind = "api_key"
	OAuth     CredentialKind = "oauth"
	WellKnown CredentialKind = "well_known"
)

// Material is accepted only at the write boundary. Its renderers never expose
// secret values.
type Material struct {
	Kind      CredentialKind
	Secrets   map[string][]byte
	Metadata  map[string]string
	ExpiresAt time.Time
}

func (Material) String() string {
	return "[REDACTED credential material]"
}

func (Material) GoString() string {
	return "[REDACTED credential material]"
}

func (Material) MarshalJSON() ([]byte, error) {
	return []byte(`"[REDACTED]"`), nil
}

type storedMaterial struct {
	kind      CredentialKind
	secrets   map[string][]byte
	metadata  map[string]string
	expiresAt time.Time
}

// Store is the backend contract exposed to canonical Core.
type Store interface {
	Put(context.Context, SecretRef, Material) error
	Lease(context.Context, SecretRef) (*CredentialLease, error)
	Delete(context.Context, SecretRef) error
	Refs(context.Context) ([]SecretRef, error)
}

// MemoryStore is a concurrency-safe ephemeral credential backend.
type MemoryStore struct {
	mu      sync.RWMutex
	entries map[SecretRef]storedMaterial
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{entries: make(map[SecretRef]storedMaterial)}
}

func (s *MemoryStore) Put(ctx context.Context, ref SecretRef, material Material) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := NewSecretRef(ref.provider, ref.slot); err != nil {
		return err
	}
	if !validKind(material.Kind) || len(material.Secrets) == 0 {
		return ErrInvalid
	}
	for name, secret := range material.Secrets {
		if strings.TrimSpace(name) == "" || strings.IndexByte(name, 0) >= 0 || len(secret) == 0 {
			return ErrInvalid
		}
	}

	next := storedMaterial{
		kind:      material.Kind,
		secrets:   cloneSecrets(material.Secrets),
		metadata:  cloneMetadata(material.Metadata),
		expiresAt: material.ExpiresAt,
	}
	s.mu.Lock()
	if previous, ok := s.entries[ref]; ok {
		zeroSecrets(previous.secrets)
	}
	s.entries[ref] = next
	s.mu.Unlock()
	return nil
}

func (s *MemoryStore) Lease(ctx context.Context, ref SecretRef) (*CredentialLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	material, ok := s.entries[ref]
	if !ok {
		s.mu.RUnlock()
		return nil, ErrNotFound
	}
	lease := &CredentialLease{
		ref:       ref,
		kind:      material.kind,
		secrets:   cloneSecrets(material.secrets),
		metadata:  cloneMetadata(material.metadata),
		expiresAt: material.expiresAt,
	}
	s.mu.RUnlock()
	if !lease.expiresAt.IsZero() && !lease.expiresAt.After(time.Now()) {
		_ = lease.Close(context.Background())
		return nil, ErrExpired
	}
	return lease, nil
}

func (s *MemoryStore) Delete(ctx context.Context, ref SecretRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if material, ok := s.entries[ref]; ok {
		zeroSecrets(material.secrets)
		delete(s.entries, ref)
	}
	s.mu.Unlock()
	return nil
}

func (s *MemoryStore) Refs(ctx context.Context) ([]SecretRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	refs := make([]SecretRef, 0, len(s.entries))
	for ref := range s.entries {
		refs = append(refs, ref)
	}
	s.mu.RUnlock()
	slices.SortFunc(refs, func(left, right SecretRef) int {
		return strings.Compare(left.String(), right.String())
	})
	return refs, nil
}

// CredentialLease is the only canonical read view of credential material.
type CredentialLease struct {
	mu        sync.Mutex
	ref       SecretRef
	kind      CredentialKind
	secrets   map[string][]byte
	metadata  map[string]string
	expiresAt time.Time
	closed    bool
}

func (l *CredentialLease) Ref() SecretRef {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ref
}

func (l *CredentialLease) Kind() CredentialKind {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.kind
}

func (l *CredentialLease) Metadata() map[string]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return cloneMetadata(l.metadata)
}

func (l *CredentialLease) ExpiresAt() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.expiresAt
}

func (l *CredentialLease) Secret(name string) ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, ErrLeaseClosed
	}
	value, ok := l.secrets[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrSecretAbsent, name)
	}
	return append([]byte(nil), value...), nil
}

// Close zeroes leased secret bytes. It intentionally runs even if ctx is
// already canceled.
func (l *CredentialLease) Close(context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	zeroSecrets(l.secrets)
	l.secrets = nil
	l.closed = true
	return nil
}

func (*CredentialLease) String() string {
	return "[REDACTED credential lease]"
}

func (*CredentialLease) GoString() string {
	return "[REDACTED credential lease]"
}

func (*CredentialLease) MarshalJSON() ([]byte, error) {
	return []byte(`"[REDACTED]"`), nil
}

func validKind(kind CredentialKind) bool {
	return kind == APIKey || kind == OAuth || kind == WellKnown
}

func cloneSecrets(source map[string][]byte) map[string][]byte {
	result := make(map[string][]byte, len(source))
	for key, value := range source {
		result[key] = append([]byte(nil), value...)
	}
	return result
}

func cloneMetadata(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func zeroSecrets(secrets map[string][]byte) {
	for _, secret := range secrets {
		clear(secret)
	}
}
