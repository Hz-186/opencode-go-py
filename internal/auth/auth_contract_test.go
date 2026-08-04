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

func TestCredentialMetadataGettersAndAllRenderersAreSafe(t *testing.T) {
	t.Parallel()

	ref, err := NewSecretRef(" provider.example/ ", " primary ")
	if err != nil {
		t.Fatalf("new ref: %v", err)
	}
	if ref.Provider() != "provider.example" || ref.Slot() != "primary" {
		t.Fatalf("ref getters = %q/%q", ref.Provider(), ref.Slot())
	}
	secret := "renderer-secret-never-log"
	expires := time.Now().Add(time.Hour).Round(0)
	material := Material{
		Kind: OAuth, Secrets: map[string][]byte{"access": []byte(secret)},
		Metadata: map[string]string{"account": "fixture"}, ExpiresAt: expires,
	}
	encodedMaterial, err := json.Marshal(material)
	if err != nil {
		t.Fatalf("marshal material: %v", err)
	}
	for _, rendered := range []string{material.String(), material.GoString(), fmt.Sprint(material), fmt.Sprintf("%#v", material), string(encodedMaterial)} {
		if strings.Contains(rendered, secret) || !strings.Contains(rendered, "REDACTED") {
			t.Fatalf("unsafe material renderer: %s", rendered)
		}
	}

	store := NewMemoryStore()
	if err := store.Put(context.Background(), ref, material); err != nil {
		t.Fatalf("put: %v", err)
	}
	lease, err := store.Lease(context.Background(), ref)
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	if lease.Ref() != ref || lease.Kind() != OAuth || !lease.ExpiresAt().Equal(expires) {
		t.Fatalf("lease getters = ref:%v kind:%s expiry:%v", lease.Ref(), lease.Kind(), lease.ExpiresAt())
	}
	metadata := lease.Metadata()
	metadata["account"] = "mutated"
	if lease.Metadata()["account"] != "fixture" {
		t.Fatal("lease metadata was not copy-isolated")
	}
	if _, err := lease.Secret("missing"); !errors.Is(err, ErrSecretAbsent) {
		t.Fatalf("missing secret error = %v, want ErrSecretAbsent", err)
	}
	encodedLease, err := json.Marshal(lease)
	if err != nil {
		t.Fatalf("marshal lease: %v", err)
	}
	for _, rendered := range []string{lease.String(), lease.GoString(), fmt.Sprint(lease), fmt.Sprintf("%#v", lease), string(encodedLease)} {
		if strings.Contains(rendered, secret) || !strings.Contains(rendered, "REDACTED") {
			t.Fatalf("unsafe lease renderer: %s", rendered)
		}
	}
	backing := lease.secrets["access"]
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := lease.Close(canceled); err != nil {
		t.Fatalf("close with canceled context: %v", err)
	}
	assertZeroed(t, backing)
	if err := lease.Close(context.Background()); err != nil {
		t.Fatalf("idempotent close: %v", err)
	}
}

func TestStoreReplacementAndDeleteZeroOwnedSecretBytes(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	ref, _ := NewSecretRef("provider", "primary")
	input := []byte("first-secret")
	if err := store.Put(context.Background(), ref, Material{Kind: APIKey, Secrets: map[string][]byte{"key": input}}); err != nil {
		t.Fatalf("put first: %v", err)
	}
	input[0] = 'X'
	store.mu.RLock()
	firstBacking := store.entries[ref].secrets["key"]
	store.mu.RUnlock()
	if string(firstBacking) != "first-secret" {
		t.Fatalf("stored secret was not input-isolated: %q", firstBacking)
	}
	if err := store.Put(context.Background(), ref, Material{
		Kind: WellKnown, Secrets: map[string][]byte{"token": []byte("second-secret")},
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	assertZeroed(t, firstBacking)
	store.mu.RLock()
	secondBacking := store.entries[ref].secrets["token"]
	store.mu.RUnlock()
	if err := store.Delete(context.Background(), ref); err != nil {
		t.Fatalf("delete: %v", err)
	}
	assertZeroed(t, secondBacking)
	if err := store.Delete(context.Background(), ref); err != nil {
		t.Fatalf("idempotent delete: %v", err)
	}
}

func TestStoreRejectsInvalidMaterialsAndPropagatesCanceledContexts(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	ref, _ := NewSecretRef("provider", "primary")
	invalid := []Material{
		{},
		{Kind: CredentialKind("future"), Secrets: map[string][]byte{"key": []byte("secret")}},
		{Kind: APIKey, Secrets: map[string][]byte{}},
		{Kind: APIKey, Secrets: map[string][]byte{"": []byte("secret")}},
		{Kind: APIKey, Secrets: map[string][]byte{"bad\x00name": []byte("secret")}},
		{Kind: APIKey, Secrets: map[string][]byte{"key": {}}},
	}
	for index, material := range invalid {
		if err := store.Put(context.Background(), ref, material); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid material %d error = %v, want ErrInvalid", index, err)
		}
	}
	if err := store.Put(context.Background(), SecretRef{}, Material{
		Kind: APIKey, Secrets: map[string][]byte{"key": []byte("secret")},
	}); !errors.Is(err, ErrInvalidRef) {
		t.Fatalf("invalid ref put error = %v, want ErrInvalidRef", err)
	}
	for _, kind := range []CredentialKind{APIKey, OAuth, WellKnown} {
		kindRef, _ := NewSecretRef("provider", string(kind))
		if err := store.Put(context.Background(), kindRef, Material{
			Kind: kind, Secrets: map[string][]byte{"key": []byte("secret")},
		}); err != nil {
			t.Fatalf("put kind %s: %v", kind, err)
		}
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Put(canceled, ref, Material{Kind: APIKey, Secrets: map[string][]byte{"key": []byte("secret")}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled put error = %v", err)
	}
	if _, err := store.Lease(canceled, ref); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lease error = %v", err)
	}
	if err := store.Delete(canceled, ref); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled delete error = %v", err)
	}
	if _, err := store.Refs(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled refs error = %v", err)
	}
}

func assertZeroed(t *testing.T, value []byte) {
	t.Helper()
	for index, item := range value {
		if item != 0 {
			t.Fatalf("secret byte %d = %d, want zero", index, item)
		}
	}
}
