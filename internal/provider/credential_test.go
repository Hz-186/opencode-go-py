package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/Hz-186/opencode-go-py/internal/auth"
)

func TestSecretStoreCredentialIsOneAttemptLeaseAndClearsCopy(t *testing.T) {
	store := auth.NewMemoryStore()
	ref, err := auth.NewSecretRef("fixture", "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), ref, auth.Material{Kind: auth.APIKey, Secrets: map[string][]byte{"api_key": []byte("secret")}}); err != nil {
		t.Fatal(err)
	}
	secret, release, err := SecretStoreCredential(store, ref, "api_key").Acquire(context.Background())
	if err != nil || string(secret) != "secret" || release == nil {
		t.Fatalf("acquire = %q release=%v err=%v", secret, release != nil, err)
	}
	release()
	for index, value := range secret {
		if value != 0 {
			t.Fatalf("secret byte %d was not cleared", index)
		}
	}
}

func TestSecretStoreCredentialDoesNotExposeMissingField(t *testing.T) {
	store := auth.NewMemoryStore()
	ref, _ := auth.NewSecretRef("fixture", "default")
	_ = store.Put(context.Background(), ref, auth.Material{Kind: auth.APIKey, Secrets: map[string][]byte{"api_key": []byte("secret")}})
	_, _, err := SecretStoreCredential(store, ref, "missing").Acquire(context.Background())
	if !errors.Is(err, ErrCredential) {
		t.Fatalf("missing credential error = %v", err)
	}
}
