package baseline

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func WriteSnapshot(output string, result Result) error {
	if output == "" {
		return errors.New("snapshot output path is required")
	}
	digest := sha256.Sum256(result.JSON)
	actual := hex.EncodeToString(digest[:])
	if !strings.EqualFold(actual, result.SHA256) {
		return fmt.Errorf("manifest digest mismatch: computed=%s provided=%s", actual, result.SHA256)
	}

	directory := filepath.Dir(output)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create snapshot directory: %w", err)
	}
	manifestTemp, err := prepareAtomicFile(directory, filepath.Base(output), result.JSON)
	if err != nil {
		return err
	}
	defer os.Remove(manifestTemp)
	digestContent := []byte(fmt.Sprintf("%s  %s\n", actual, filepath.Base(output)))
	digestTemp, err := prepareAtomicFile(directory, filepath.Base(output)+".sha256", digestContent)
	if err != nil {
		return err
	}
	defer os.Remove(digestTemp)

	if err := os.Rename(manifestTemp, output); err != nil {
		return fmt.Errorf("replace manifest snapshot: %w", err)
	}
	if err := os.Rename(digestTemp, output+".sha256"); err != nil {
		return fmt.Errorf("replace manifest digest: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return err
	}
	return nil
}

func prepareAtomicFile(directory string, name string, content []byte) (string, error) {
	file, err := os.CreateTemp(directory, "."+name+".tmp-")
	if err != nil {
		return "", fmt.Errorf("create temporary snapshot: %w", err)
	}
	temporary := file.Name()
	remove := true
	defer func() {
		file.Close()
		if remove {
			os.Remove(temporary)
		}
	}()
	if err := file.Chmod(0o644); err != nil {
		return "", fmt.Errorf("set temporary snapshot mode: %w", err)
	}
	if _, err := file.Write(content); err != nil {
		return "", fmt.Errorf("write temporary snapshot: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("sync temporary snapshot: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close temporary snapshot: %w", err)
	}
	remove = false
	return temporary, nil
}

func syncDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open snapshot directory: %w", err)
	}
	defer handle.Close()
	if err := handle.Sync(); err != nil {
		return fmt.Errorf("sync snapshot directory: %w", err)
	}
	return nil
}
