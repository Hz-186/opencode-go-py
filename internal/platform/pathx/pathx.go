// Package pathx centralizes host and portable path identity rules.
package pathx

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"syscall"
)

var ErrSymlink = errors.New("path symlink resolution failed")

type CaseMode uint8

const (
	CaseSensitive CaseMode = iota
	CaseInsensitive
)

type Platform string

const (
	Darwin  Platform = "darwin"
	Linux   Platform = "linux"
	Windows Platform = "windows"
)

// Canonical returns an absolute, symlink-resolved host path identity.
func Canonical(input string, mode CaseMode) (string, error) {
	absolute, err := filepath.Abs(input)
	if err != nil {
		return "", fmt.Errorf("make path absolute: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) || strings.Contains(strings.ToLower(err.Error()), "too many links") {
			return "", fmt.Errorf("%w: %v", ErrSymlink, err)
		}
		return "", fmt.Errorf("resolve path symlinks: %w", err)
	}
	result := filepath.Clean(resolved)
	if mode == CaseInsensitive {
		result = strings.ToLower(result)
	}
	return result, nil
}

// Contains checks a lexical path boundary without prefix confusion.
func Contains(base, target string, mode CaseMode) bool {
	base = filepath.Clean(base)
	target = filepath.Clean(target)
	if mode == CaseInsensitive {
		base = strings.ToLower(base)
		target = strings.ToLower(target)
	}
	relative, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(relative))
}

// PortableKey normalizes a path fixture independently of the host OS.
func PortableKey(platform Platform, input string) string {
	value := input
	unc := false
	if platform == Windows {
		value = strings.ReplaceAll(value, "\\", "/")
		unc = strings.HasPrefix(value, "//")
	}
	value = path.Clean(value)
	if unc && !strings.HasPrefix(value, "//") {
		value = "/" + value
	}
	if platform == Darwin || platform == Windows {
		value = strings.ToLower(value)
	}
	return value
}
