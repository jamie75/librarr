// Package safepath contains small helpers for validating filesystem paths
// before application code opens or writes them.
package safepath

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

// ExistingUnderRoot resolves an existing path and verifies that it remains
// below the configured root after symlinks are resolved.
func ExistingUnderRoot(root, candidate string) (string, error) {
	if invalidComponent(root) || invalidComponent(candidate) {
		return "", fmt.Errorf("path contains invalid control characters")
	}
	root, err := absoluteClean(root)
	if err != nil {
		return "", fmt.Errorf("invalid path root: %w", err)
	}
	candidate, err = absoluteClean(candidate)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve path root: %w", err)
	}
	realCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	realRoot, err = filepath.Abs(filepath.Clean(realRoot))
	if err != nil {
		return "", err
	}
	realCandidate, err = filepath.Abs(filepath.Clean(realCandidate))
	if err != nil {
		return "", err
	}
	if !contained(realRoot, realCandidate) {
		return "", fmt.Errorf("path is outside configured root")
	}
	return realCandidate, nil
}

// UnderRoot validates a path that may not exist yet, resolving the existing
// parent so a symlink cannot redirect a future write outside the root.
func UnderRoot(root, candidate string) (string, error) {
	if invalidComponent(root) || invalidComponent(candidate) {
		return "", fmt.Errorf("path contains invalid control characters")
	}
	root, err := absoluteClean(root)
	if err != nil {
		return "", fmt.Errorf("invalid path root: %w", err)
	}
	candidate, err = absoluteClean(candidate)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve path root: %w", err)
	}
	parent, err := existingParent(filepath.Dir(candidate))
	if err != nil {
		return "", fmt.Errorf("resolve path parent: %w", err)
	}
	realRoot, err = filepath.Abs(filepath.Clean(realRoot))
	if err != nil {
		return "", err
	}
	parent, err = filepath.Abs(filepath.Clean(parent))
	if err != nil {
		return "", err
	}
	if !contained(realRoot, parent) {
		return "", fmt.Errorf("path is outside configured root")
	}
	return candidate, nil
}

func existingParent(path string) (string, error) {
	current := filepath.Clean(path)
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return resolved, nil
		}
		next := filepath.Dir(current)
		if next == current {
			return "", fmt.Errorf("no existing parent")
		}
		current = next
	}
}

func absoluteClean(value string) (string, error) {
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("path must be absolute")
	}
	return filepath.Abs(filepath.Clean(value))
}

func invalidComponent(value string) bool {
	if strings.TrimSpace(value) == "" {
		return true
	}
	for _, r := range value {
		if r == 0 || unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func contained(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}
