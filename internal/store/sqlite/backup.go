package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	modernsqlite "modernc.org/sqlite"
)

const (
	BackupFormatV1            = "opencode-sqlite-backup-v1"
	DefaultBackupPagesPerStep = int32(256)
	MaximumBackupPagesPerStep = int32(65_536)
)

var ErrBackup = errors.New("sqlite backup failed")

type BackupOptions struct {
	Path               string
	ApplicationVersion string
	PagesPerStep       int32
	Clock              func() time.Time
}

func DefaultBackupOptions(path, applicationVersion string) BackupOptions {
	return BackupOptions{
		Path: path, ApplicationVersion: applicationVersion,
		PagesPerStep: DefaultBackupPagesPerStep, Clock: time.Now,
	}
}

type BackupMetadata struct {
	Format             string `json:"format"`
	Database           string `json:"database"`
	ApplicationVersion string `json:"application_version"`
	MigrationID        string `json:"migration_id"`
	MigrationChecksum  string `json:"migration_checksum"`
	SHA256             string `json:"sha256"`
	Size               int64  `json:"size"`
	CreatedAt          int64  `json:"created_at"`
}

func BackupManifestPath(databasePath string) string {
	return databasePath + ".metadata.json"
}

// Backup creates an online SQLite snapshot and a checksummed manifest. It
// fences the sole writer, publishes both files without overwriting an existing
// target, and removes operation-owned temporary artifacts on every failure.
func (store *Store) Backup(ctx context.Context, options BackupOptions) (_ BackupMetadata, returnErr error) {
	if err := ctx.Err(); err != nil {
		return BackupMetadata{}, err
	}
	if store == nil || store.closed.Load() {
		return BackupMetadata{}, ErrStoreClosed
	}
	if store.broken.Load() {
		return BackupMetadata{}, ErrStoreBroken
	}
	validated, destinationParent, err := validateBackupOptions(store.path, options)
	if err != nil {
		return BackupMetadata{}, err
	}
	select {
	case <-ctx.Done():
		return BackupMetadata{}, ctx.Err()
	case <-store.closeCh:
		return BackupMetadata{}, ErrStoreClosed
	case <-store.writeToken:
	}
	defer func() { store.writeToken <- struct{}{} }()
	if store.closed.Load() {
		return BackupMetadata{}, ErrStoreClosed
	}

	temporary, err := os.CreateTemp(destinationParent, ".opencode-sqlite-backup-*.sqlite")
	if err != nil {
		return BackupMetadata{}, fmt.Errorf("%w: create destination temporary file: %w", ErrBackup, err)
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return BackupMetadata{}, fmt.Errorf("%w: close destination temporary file: %w", ErrBackup, err)
	}
	defer func() {
		_ = os.Remove(temporaryPath)
		_ = os.Remove(temporaryPath + "-wal")
		_ = os.Remove(temporaryPath + "-shm")
	}()

	if err := copyOnlineBackup(ctx, store.writer, temporaryPath, validated.PagesPerStep); err != nil {
		return BackupMetadata{}, err
	}
	migration, err := verifyBackupFile(ctx, temporaryPath, store.rollbackTimeout)
	if err != nil {
		return BackupMetadata{}, err
	}
	digest, size, err := hashBackupFile(temporaryPath)
	if err != nil {
		return BackupMetadata{}, err
	}
	createdAt := validated.Clock().UnixMilli()
	if createdAt < 0 {
		return BackupMetadata{}, fmt.Errorf("%w: backup clock is before Unix epoch", ErrInvalidStoreInput)
	}
	metadata := BackupMetadata{
		Format: BackupFormatV1, Database: filepath.Base(validated.Path),
		ApplicationVersion: validated.ApplicationVersion,
		MigrationID:        migration.ID, MigrationChecksum: migration.Checksum,
		SHA256: digest, Size: size, CreatedAt: createdAt,
	}
	manifestTemporary, err := writeBackupManifestTemporary(destinationParent, metadata)
	if err != nil {
		return BackupMetadata{}, err
	}
	defer func() { _ = os.Remove(manifestTemporary) }()

	if err := ensureBackupTargetsAbsent(validated.Path); err != nil {
		return BackupMetadata{}, err
	}
	databasePublished := false
	manifestPublished := false
	defer func() {
		if returnErr == nil {
			return
		}
		if manifestPublished {
			_ = os.Remove(BackupManifestPath(validated.Path))
		}
		if databasePublished {
			_ = os.Remove(validated.Path)
		}
	}()
	if err := os.Link(temporaryPath, validated.Path); err != nil {
		return BackupMetadata{}, fmt.Errorf("%w: publish database snapshot: %w", ErrBackup, err)
	}
	databasePublished = true
	if err := os.Link(manifestTemporary, BackupManifestPath(validated.Path)); err != nil {
		return BackupMetadata{}, fmt.Errorf("%w: publish backup manifest: %w", ErrBackup, err)
	}
	manifestPublished = true
	if err := syncDirectory(destinationParent); err != nil {
		return BackupMetadata{}, fmt.Errorf("%w: sync backup directory: %w", ErrBackup, err)
	}
	return metadata, nil
}

func validateBackupOptions(source string, options BackupOptions) (BackupOptions, string, error) {
	if strings.TrimSpace(options.Path) == "" || options.Path != strings.TrimSpace(options.Path) ||
		strings.IndexByte(options.Path, 0) >= 0 || !filepath.IsAbs(options.Path) {
		return BackupOptions{}, "", fmt.Errorf("%w: backup path must be absolute and non-empty without whitespace or NUL",
			ErrInvalidStoreInput)
	}
	options.Path = filepath.Clean(options.Path)
	if err := validateStoreIdentity("application version", options.ApplicationVersion); err != nil {
		return BackupOptions{}, "", err
	}
	if options.PagesPerStep <= 0 || options.PagesPerStep > MaximumBackupPagesPerStep {
		return BackupOptions{}, "", fmt.Errorf("%w: backup pages per step must be in [1,%d]",
			ErrInvalidStoreInput, MaximumBackupPagesPerStep)
	}
	if options.Clock == nil {
		return BackupOptions{}, "", fmt.Errorf("%w: backup clock is nil", ErrInvalidStoreInput)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(options.Path))
	if err != nil {
		return BackupOptions{}, "", fmt.Errorf("%w: resolve backup parent: %v", ErrInvalidStoreInput, err)
	}
	parentInfo, err := os.Stat(parent)
	if err != nil || !parentInfo.IsDir() {
		return BackupOptions{}, "", fmt.Errorf("%w: backup parent must be an existing directory",
			ErrInvalidStoreInput)
	}
	options.Path = filepath.Join(parent, filepath.Base(options.Path))
	sourcePath, err := filepath.EvalSymlinks(source)
	if err != nil {
		return BackupOptions{}, "", fmt.Errorf("%w: resolve source database: %w", ErrBackup, err)
	}
	if options.Path == sourcePath {
		return BackupOptions{}, "", fmt.Errorf("%w: backup destination equals source database",
			ErrInvalidStoreInput)
	}
	if err := ensureBackupTargetsAbsent(options.Path); err != nil {
		return BackupOptions{}, "", err
	}
	return options, parent, nil
}

func ensureBackupTargetsAbsent(path string) error {
	for _, candidate := range []string{path, BackupManifestPath(path)} {
		if _, err := os.Lstat(candidate); err == nil {
			return fmt.Errorf("%w: backup target already exists: %s", ErrInvalidStoreInput, candidate)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: inspect backup target %q: %v", ErrInvalidStoreInput, candidate, err)
		}
	}
	return nil
}

type onlineBackupConnection interface {
	NewBackup(string) (*modernsqlite.Backup, error)
}

func copyOnlineBackup(ctx context.Context, source *sql.Conn, destination string, pages int32) error {
	uri := &url.URL{Scheme: "file", Path: destination}
	query := uri.Query()
	query.Set("mode", "rwc")
	uri.RawQuery = query.Encode()
	err := source.Raw(func(driverConnection any) (returnErr error) {
		creator, ok := driverConnection.(onlineBackupConnection)
		if !ok {
			return fmt.Errorf("SQLite driver connection does not support online backup")
		}
		backup, err := creator.NewBackup(uri.String())
		if err != nil {
			return err
		}
		finished := false
		defer func() {
			if finished {
				return
			}
			if err := backup.Finish(); err != nil {
				if returnErr == nil {
					returnErr = err
				} else {
					returnErr = errors.Join(returnErr, err)
				}
			}
		}()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			more, err := backup.Step(pages)
			if err != nil {
				return err
			}
			if !more {
				break
			}
		}
		if err := backup.Finish(); err != nil {
			return err
		}
		finished = true
		return nil
	})
	if err != nil {
		return fmt.Errorf("%w: copy online database: %w", ErrBackup,
			wrapStorage(ctx, "run SQLite online backup", err))
	}
	return nil
}

func verifyBackupFile(ctx context.Context, path string, rollbackTimeout time.Duration) (AppliedMigration, error) {
	uri := &url.URL{Scheme: "file", Path: path}
	query := uri.Query()
	query.Set("mode", "ro")
	query.Set("_foreign_keys", "on")
	query.Set("_query_only", "on")
	query.Set("_dqs", "0")
	query.Set("_error_rc", "1")
	uri.RawQuery = query.Encode()
	database, err := sql.Open(sqliteDriverName, uri.String())
	if err != nil {
		return AppliedMigration{}, fmt.Errorf("%w: open backup verification database: %w", ErrBackup, err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	defer database.Close()
	if err := database.PingContext(ctx); err != nil {
		return AppliedMigration{}, fmt.Errorf("%w: verify backup connection: %w", ErrBackup,
			wrapStorage(ctx, "ping backup database", err))
	}
	backend, err := NewSQLMigrationBackend(database, rollbackTimeout)
	if err != nil {
		return AppliedMigration{}, fmt.Errorf("%w: %w", ErrBackup, err)
	}
	applied, err := backend.AppliedMigrations(ctx)
	if err != nil {
		return AppliedMigration{}, fmt.Errorf("%w: read backup migrations: %w", ErrBackup, err)
	}
	catalog, err := MigrationCatalog()
	if err != nil {
		return AppliedMigration{}, fmt.Errorf("%w: load migration catalog: %w", ErrBackup, err)
	}
	pending, err := PlanMigrations(catalog, applied)
	if err != nil {
		return AppliedMigration{}, fmt.Errorf("%w: verify backup schema: %w", ErrBackup, err)
	}
	if len(pending) != 0 || len(applied) == 0 {
		return AppliedMigration{}, fmt.Errorf("%w: backup schema is not current", ErrBackup)
	}
	connection, err := database.Conn(ctx)
	if err != nil {
		return AppliedMigration{}, fmt.Errorf("%w: acquire backup verification connection: %w", ErrBackup, err)
	}
	defer connection.Close()
	if _, err := checkSQLiteIntegrity(ctx, connection, rollbackTimeout); err != nil {
		return AppliedMigration{}, fmt.Errorf("%w: verify backup integrity: %w", ErrBackup, err)
	}
	return applied[len(applied)-1], nil
}

func hashBackupFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("%w: open backup for hashing: %w", ErrBackup, err)
	}
	defer file.Close()
	digest := sha256.New()
	size, err := io.Copy(digest, file)
	if err != nil {
		return "", 0, fmt.Errorf("%w: hash backup: %w", ErrBackup, err)
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), size, nil
}

func writeBackupManifestTemporary(parent string, metadata BackupMetadata) (string, error) {
	file, err := os.CreateTemp(parent, ".opencode-sqlite-backup-*.metadata.json")
	if err != nil {
		return "", fmt.Errorf("%w: create backup manifest temporary file: %w", ErrBackup, err)
	}
	path := file.Name()
	succeeded := false
	defer func() {
		if !succeeded {
			_ = os.Remove(path)
		}
	}()
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(metadata); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("%w: encode backup manifest: %w", ErrBackup, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("%w: sync backup manifest: %w", ErrBackup, err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("%w: close backup manifest: %w", ErrBackup, err)
	}
	succeeded = true
	return path, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
