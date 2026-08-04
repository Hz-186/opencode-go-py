package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestStoreReturnsOpaqueCredentialLease(t *testing.T) {
	t.Parallel()

	ref, err := NewSecretRef("https://provider.example///", "primary")
	if err != nil {
		t.Fatalf("new secret ref: %v", err)
	}
	if got, want := ref.String(), "secret://https://provider.example/primary"; got != want {
		t.Fatalf("secret ref = %q, want %q", got, want)
	}
	store := NewMemoryStore()
	secret := []byte("fixture-api-key")
	if err := store.Put(context.Background(), ref, Material{
		Kind:     APIKey,
		Secrets:  map[string][]byte{"key": secret},
		Metadata: map[string]string{"account": "fixture"},
	}); err != nil {
		t.Fatalf("put credential: %v", err)
	}
	secret[0] = 'X'

	lease, err := store.Lease(context.Background(), ref)
	if err != nil {
		t.Fatalf("lease credential: %v", err)
	}
	value, err := lease.Secret("key")
	if err != nil {
		t.Fatalf("read leased secret: %v", err)
	}
	if got := string(value); got != "fixture-api-key" {
		t.Fatalf("leased secret = %q, want fixture-api-key", got)
	}
	value[0] = 'X'
	again, err := lease.Secret("key")
	if err != nil || string(again) != "fixture-api-key" {
		t.Fatalf("lease did not return an isolated copy: %q, %v", again, err)
	}

	encoded, err := json.Marshal(lease)
	if err != nil {
		t.Fatalf("marshal lease: %v", err)
	}
	for _, rendered := range []string{string(encoded), fmt.Sprint(lease)} {
		if strings.Contains(rendered, "fixture-api-key") {
			t.Fatalf("rendered lease leaked secret: %s", rendered)
		}
	}
	if err := lease.Close(context.Background()); err != nil {
		t.Fatalf("close lease: %v", err)
	}
	if _, err := lease.Secret("key"); !errors.Is(err, ErrLeaseClosed) {
		t.Fatalf("secret after close error = %v, want ErrLeaseClosed", err)
	}
}

func TestStoreListsOnlyRefsAndRejectsExpiredCredential(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	active, _ := NewSecretRef("active", "api")
	expired, _ := NewSecretRef("expired", "oauth")
	if err := store.Put(context.Background(), active, Material{
		Kind: APIKey, Secrets: map[string][]byte{"key": []byte("active-secret")},
	}); err != nil {
		t.Fatalf("put active: %v", err)
	}
	if err := store.Put(context.Background(), expired, Material{
		Kind: OAuth, Secrets: map[string][]byte{"access": []byte("expired-secret")},
		ExpiresAt: time.Now().Add(-time.Second),
	}); err != nil {
		t.Fatalf("put expired: %v", err)
	}

	refs, err := store.Refs(context.Background())
	if err != nil {
		t.Fatalf("list refs: %v", err)
	}
	if len(refs) != 2 || refs[0].String() != active.String() || refs[1].String() != expired.String() {
		t.Fatalf("refs = %v, want active then expired", refs)
	}
	if _, err := store.Lease(context.Background(), expired); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired lease error = %v, want ErrExpired", err)
	}
	if err := store.Delete(context.Background(), active); err != nil {
		t.Fatalf("delete active: %v", err)
	}
	if _, err := store.Lease(context.Background(), active); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted lease error = %v, want ErrNotFound", err)
	}
}

func TestSecretRefValidationAndConcurrentLeases(t *testing.T) {
	t.Parallel()

	for _, input := range [][2]string{{"", "key"}, {"provider", ""}, {"bad\x00provider", "key"}, {"provider", "bad/slot"}} {
		if _, err := NewSecretRef(input[0], input[1]); !errors.Is(err, ErrInvalidRef) {
			t.Fatalf("NewSecretRef(%q, %q) error = %v, want ErrInvalidRef", input[0], input[1], err)
		}
	}

	store := NewMemoryStore()
	ref, _ := NewSecretRef("provider", "primary")
	if err := store.Put(context.Background(), ref, Material{
		Kind: APIKey, Secrets: map[string][]byte{"key": []byte("secret")},
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	const readers = 64
	errs := make(chan error, readers)
	for range readers {
		go func() {
			lease, err := store.Lease(context.Background(), ref)
			if err == nil {
				_, err = lease.Secret("key")
				_ = lease.Close(context.Background())
			}
			errs <- err
		}()
	}
	for range readers {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent lease: %v", err)
		}
	}
}
