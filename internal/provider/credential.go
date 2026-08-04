package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/Hz-186/opencode-go-py/internal/auth"
)

var ErrCredential = errors.New("provider credential unavailable")

// CredentialSource yields one short-lived copy for a single HTTP attempt.
// Release must clear the returned bytes and close any backing lease.
type CredentialSource interface {
	Acquire(context.Context) ([]byte, func(), error)
}

type CredentialSourceFunc func(context.Context) ([]byte, func(), error)

func (source CredentialSourceFunc) Acquire(ctx context.Context) ([]byte, func(), error) {
	if source == nil {
		return nil, nil, ErrCredential
	}
	return source(ctx)
}

type storeCredential struct {
	store auth.Store
	ref   auth.SecretRef
	name  string
}

// SecretStoreCredential adapts the P3 secret store to the provider boundary.
func SecretStoreCredential(store auth.Store, ref auth.SecretRef, name string) CredentialSource {
	return storeCredential{store: store, ref: ref, name: name}
}

func (source storeCredential) Acquire(ctx context.Context) ([]byte, func(), error) {
	if source.store == nil || source.name == "" {
		return nil, nil, ErrCredential
	}
	lease, err := source.store.Lease(ctx, source.ref)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrCredential, err)
	}
	secret, err := lease.Secret(source.name)
	if err != nil {
		_ = lease.Close(context.Background())
		return nil, nil, fmt.Errorf("%w: secret field unavailable", ErrCredential)
	}
	release := func() {
		clear(secret)
		_ = lease.Close(context.Background())
	}
	return secret, release, nil
}
