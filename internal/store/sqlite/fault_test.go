package sqlite

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hz-186/opencode-go-py/internal/event"
)

const (
	sqliteCrashHelperFlag = "OPENCODE_SQLITE_CRASH_HELPER"
	sqliteCrashHelperPath = "OPENCODE_SQLITE_CRASH_PATH"
	sqliteCrashReady      = "sqlite-wal-crash-ready"
)

func TestRealStoreDiskFullIsTypedAndAtomic(t *testing.T) {
	t.Parallel()

	store := openRealTestStore(t, nil)
	var pageCount int64
	if err := store.writer.QueryRowContext(context.Background(), "PRAGMA page_count").Scan(&pageCount); err != nil {
		t.Fatalf("read page count: %v", err)
	}
	var maximum int64
	if err := store.writer.QueryRowContext(context.Background(),
		fmt.Sprintf("PRAGMA max_page_count = %d", pageCount)).Scan(&maximum); err != nil {
		t.Fatalf("limit maximum page count: %v", err)
	}
	if maximum != pageCount {
		t.Fatalf("maximum page count = %d, want %d", maximum, pageCount)
	}
	service, err := event.NewService(store, storeFixedEventIDs("evt_full", "evt_after_full"))
	if err != nil {
		t.Fatalf("new event service: %v", err)
	}
	_, err = service.Publish(context.Background(), realStoreDefinition(),
		realStoreData("fixture-full", strings.Repeat("x", 2<<20)), event.PublishOptions{})
	if !errors.Is(err, ErrStorageFull) || !errors.Is(err, ErrStorage) {
		t.Fatalf("disk-full publish error = %v, want typed ErrStorageFull", err)
	}
	history, historyErr := store.History(context.Background(), "fixture-full", -1, 10)
	if historyErr != nil || len(history) != 0 {
		t.Fatalf("disk-full publish leaked history/error = %+v/%v", history, historyErr)
	}
	if err := store.writer.QueryRowContext(context.Background(),
		"PRAGMA max_page_count = 1073741823").Scan(&maximum); err != nil {
		t.Fatalf("restore maximum page count: %v", err)
	}
	committed, err := service.Publish(context.Background(), realStoreDefinition(),
		realStoreData("fixture-full", "recovered"), event.PublishOptions{})
	if err != nil || committed.Durable == nil || committed.Durable.Sequence != 0 {
		t.Fatalf("publish after disk-full event/error = %+v/%v", committed, err)
	}
}

func TestRealStoreReadOnlyIsTypedAndAtomic(t *testing.T) {
	t.Parallel()

	store := openRealTestStore(t, nil)
	if _, err := store.writer.ExecContext(context.Background(), "PRAGMA query_only = ON"); err != nil {
		t.Fatalf("enable query-only mode: %v", err)
	}
	service, err := event.NewService(store, storeFixedEventIDs("evt_read_only", "evt_after_read_only"))
	if err != nil {
		t.Fatalf("new event service: %v", err)
	}
	_, err = service.Publish(context.Background(), realStoreDefinition(),
		realStoreData("fixture-read-only", "blocked"), event.PublishOptions{})
	if !errors.Is(err, ErrStorageReadOnly) || !errors.Is(err, ErrStorage) {
		t.Fatalf("read-only publish error = %v, want typed ErrStorageReadOnly", err)
	}
	history, historyErr := store.History(context.Background(), "fixture-read-only", -1, 10)
	if historyErr != nil || len(history) != 0 {
		t.Fatalf("read-only publish leaked history/error = %+v/%v", history, historyErr)
	}
	if _, err := store.writer.ExecContext(context.Background(), "PRAGMA query_only = OFF"); err != nil {
		t.Fatalf("disable query-only mode: %v", err)
	}
	committed, err := service.Publish(context.Background(), realStoreDefinition(),
		realStoreData("fixture-read-only", "recovered"), event.PublishOptions{})
	if err != nil || committed.Durable == nil || committed.Durable.Sequence != 0 {
		t.Fatalf("publish after read-only event/error = %+v/%v", committed, err)
	}
}

func TestRealStoreRejectsNotADatabaseAsTypedCorruption(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "not-a-database.sqlite")
	if err := os.WriteFile(path, []byte("this is not a SQLite database\n"), 0o600); err != nil {
		t.Fatalf("write corrupt database: %v", err)
	}
	store, err := Open(context.Background(), DefaultOpenOptions(path))
	if store != nil {
		_ = store.Close()
	}
	var storageErr *StorageError
	if !errors.Is(err, ErrStorageCorrupt) || !errors.Is(err, ErrStorage) ||
		!errors.As(err, &storageErr) {
		t.Fatalf("not-a-database error = %#v, want typed ErrStorageCorrupt", err)
	}
}

func TestRealStoreCancellationRollsBackAndSurvivesReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cancel-reopen.sqlite")
	options := DefaultOpenOptions(path)
	store, err := Open(context.Background(), options)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Write(context.Background(), func(ctx context.Context, tx event.Transaction) error {
		_, err := tx.(SQLExecutor).ExecContext(ctx,
			"CREATE TABLE cancel_projection (id TEXT PRIMARY KEY)")
		return err
	}); err != nil {
		t.Fatalf("create cancellation projection: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	err = store.Write(ctx, func(txCtx context.Context, tx event.Transaction) error {
		if _, err := tx.(SQLExecutor).ExecContext(txCtx,
			"INSERT INTO cancel_projection(id) VALUES (?)", "rolled-back"); err != nil {
			return err
		}
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled write error = %v, want context.Canceled", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close canceled store: %v", err)
	}
	reopened, err := Open(context.Background(), options)
	if err != nil {
		t.Fatalf("reopen canceled store: %v", err)
	}
	defer reopened.Close()
	var rows int
	if err := reopened.readerDB.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM cancel_projection").Scan(&rows); err != nil {
		t.Fatalf("read cancellation projection: %v", err)
	}
	if rows != 0 {
		t.Fatalf("canceled transaction retained %d projection rows", rows)
	}
}

func TestRealStoreRecoversCommittedWALAfterProcessKill(t *testing.T) {
	if os.Getenv(sqliteCrashHelperFlag) == "1" {
		runSQLiteCrashHelper(t)
		return
	}
	t.Parallel()

	path := filepath.Join(t.TempDir(), "wal-crash.sqlite")
	command := exec.Command(os.Args[0], "-test.run=^TestRealStoreRecoversCommittedWALAfterProcessKill$")
	command.Env = append(os.Environ(), sqliteCrashHelperFlag+"=1", sqliteCrashHelperPath+"="+path)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("open crash helper stdout: %v", err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start crash helper: %v", err)
	}
	ready := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if scanner.Text() == sqliteCrashReady {
				ready <- nil
				return
			}
		}
		if err := scanner.Err(); err != nil {
			ready <- err
			return
		}
		ready <- errors.New("crash helper exited before readiness marker")
	}()
	select {
	case err := <-ready:
		if err != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
			t.Fatalf("wait for crash helper: %v; stderr=%s", err, stderr.String())
		}
	case <-time.After(10 * time.Second):
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("crash helper readiness timeout; stderr=%s", stderr.String())
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatalf("kill crash helper: %v", err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("killed crash helper exited successfully")
	}

	reopened, err := Open(context.Background(), DefaultOpenOptions(path))
	if err != nil {
		t.Fatalf("reopen WAL after process kill: %v; helper stderr=%s", err, stderr.String())
	}
	defer reopened.Close()
	history, err := reopened.History(context.Background(), "fixture-wal-crash", -1, 10)
	if err != nil {
		t.Fatalf("read recovered WAL history: %v", err)
	}
	if len(history) != 1 || history[0].ID != "evt_wal_crash" || history[0].Sequence != 0 {
		t.Fatalf("recovered WAL history = %+v", history)
	}
}

func runSQLiteCrashHelper(t *testing.T) {
	t.Helper()
	path := os.Getenv(sqliteCrashHelperPath)
	store, err := Open(context.Background(), DefaultOpenOptions(path))
	if err != nil {
		t.Fatalf("crash helper open store: %v", err)
	}
	if _, err := store.writer.ExecContext(context.Background(), "PRAGMA wal_autocheckpoint = 0"); err != nil {
		t.Fatalf("crash helper disable WAL auto-checkpoint: %v", err)
	}
	service, err := event.NewService(store, storeFixedEventIDs("evt_wal_crash"))
	if err != nil {
		t.Fatalf("crash helper new event service: %v", err)
	}
	if _, err := service.Publish(context.Background(), realStoreDefinition(),
		realStoreData("fixture-wal-crash", "committed before kill"), event.PublishOptions{}); err != nil {
		t.Fatalf("crash helper publish: %v", err)
	}
	fmt.Fprintln(os.Stdout, sqliteCrashReady)
	select {}
}
