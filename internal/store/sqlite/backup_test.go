package sqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Hz-186/opencode-go-py/internal/domain"
	"github.com/Hz-186/opencode-go-py/internal/event"
)

func TestRealStoreOnlineBackupIsConsistentAndSelfDescribing(t *testing.T) {
	t.Parallel()

	store := openRealTestStore(t, nil)
	service, err := event.NewService(store,
		storeFixedEventIDs("evt_backup_before", "evt_backup_after"))
	if err != nil {
		t.Fatalf("new event service: %v", err)
	}
	if _, err := service.Publish(context.Background(), realStoreDefinition(),
		realStoreData("fixture-backup", "before"), event.PublishOptions{}); err != nil {
		t.Fatalf("publish before backup: %v", err)
	}
	destination := filepath.Join(t.TempDir(), "backup 快照.sqlite")
	options := DefaultBackupOptions(destination, "test-app-v1")
	options.Clock = func() time.Time { return time.UnixMilli(1234) }
	options.PagesPerStep = 1
	metadata, err := store.Backup(context.Background(), options)
	if err != nil {
		t.Fatalf("online backup: %v", err)
	}
	if metadata.Format != BackupFormatV1 || metadata.Database != filepath.Base(destination) ||
		metadata.ApplicationVersion != "test-app-v1" || metadata.CreatedAt != 1234 ||
		metadata.MigrationID != "000001_event_store" || len(metadata.MigrationChecksum) != 64 ||
		len(metadata.SHA256) != 64 || metadata.Size <= 0 {
		t.Fatalf("backup metadata = %+v", metadata)
	}
	manifestContent, err := os.ReadFile(BackupManifestPath(destination))
	if err != nil {
		t.Fatalf("read backup manifest: %v", err)
	}
	var manifest BackupMetadata
	if err := json.Unmarshal(manifestContent, &manifest); err != nil {
		t.Fatalf("decode backup manifest: %v", err)
	}
	if !reflect.DeepEqual(manifest, metadata) || len(manifestContent) == 0 || manifestContent[len(manifestContent)-1] != '\n' {
		t.Fatalf("backup manifest/metadata mismatch = %+v / %+v", manifest, metadata)
	}

	if _, err := service.Publish(context.Background(), realStoreDefinition(),
		realStoreData("fixture-backup", "after"), event.PublishOptions{}); err != nil {
		t.Fatalf("publish after backup: %v", err)
	}
	backupStore, err := Open(context.Background(), DefaultOpenOptions(destination))
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer backupStore.Close()
	history, err := backupStore.History(context.Background(), "fixture-backup", -1, 10)
	if err != nil {
		t.Fatalf("read backup history: %v", err)
	}
	if got := backupEventIDs(history); !reflect.DeepEqual(got, []domain.EventID{"evt_backup_before"}) {
		t.Fatalf("backup history IDs = %v", got)
	}
	if report, err := backupStore.CheckIntegrity(context.Background()); err != nil || !report.Healthy() {
		t.Fatalf("backup integrity report/error = %+v/%v", report, err)
	}
}

func TestRealStoreBackupRejectsOverwriteSourceAndCancellation(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "source.sqlite")
	store, err := Open(context.Background(), DefaultOpenOptions(path))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	if _, err := store.Backup(context.Background(), DefaultBackupOptions(path, "test-app")); !errors.Is(err, ErrInvalidStoreInput) {
		t.Fatalf("backup over source error = %v, want ErrInvalidStoreInput", err)
	}

	destination := filepath.Join(t.TempDir(), "existing.sqlite")
	sentinel := []byte("do not overwrite\n")
	if err := os.WriteFile(destination, sentinel, 0o600); err != nil {
		t.Fatalf("write existing destination: %v", err)
	}
	if _, err := store.Backup(context.Background(), DefaultBackupOptions(destination, "test-app")); !errors.Is(err, ErrInvalidStoreInput) {
		t.Fatalf("backup over existing destination error = %v, want ErrInvalidStoreInput", err)
	}
	content, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(content, sentinel) {
		t.Fatalf("existing destination content/error = %q/%v", content, err)
	}

	canceledDestination := filepath.Join(t.TempDir(), "canceled.sqlite")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Backup(ctx, DefaultBackupOptions(canceledDestination, "test-app")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled backup error = %v, want context.Canceled", err)
	}
	for _, candidate := range []string{canceledDestination, BackupManifestPath(canceledDestination)} {
		if _, err := os.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("canceled backup retained %q: %v", candidate, err)
		}
	}
}

func TestRealStoreBackupWriterFenceHonorsCancellation(t *testing.T) {
	t.Parallel()

	store := openRealTestStore(t, nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- store.Write(context.Background(), func(context.Context, event.Transaction) error {
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not acquire fence")
	}
	destination := filepath.Join(t.TempDir(), "queued-backup.sqlite")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := store.Backup(ctx, DefaultBackupOptions(destination, "test-app")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued backup error = %v, want context.DeadlineExceeded", err)
	}
	close(release)
	if err := <-writeDone; err != nil {
		t.Fatalf("release writer: %v", err)
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("queued canceled backup retained destination: %v", err)
	}
}

func backupEventIDs(history []event.StoredEvent) []domain.EventID {
	result := make([]domain.EventID, len(history))
	for index, record := range history {
		result[index] = record.ID
	}
	return result
}
