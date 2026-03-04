package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LocalFS implements FS with workspace sandbox enforcement.
// All paths are resolved relative to root, and access outside root is rejected.
type LocalFS struct {
	root string // absolute workspace root
}

// NewLocalFS creates a sandboxed filesystem rooted at the given directory.
// The root must be an absolute path.
func NewLocalFS(root string) (*LocalFS, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolving root: %w", err)
	}
	// Resolve symlinks in root itself so prefix checks work correctly.
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolving root symlinks: %w", err)
	}
	return &LocalFS{root: resolved}, nil
}

// Root returns the absolute workspace root path.
func (f *LocalFS) Root() string { return f.root }

func (f *LocalFS) ReadFile(path string) ([]byte, error) {
	safe, err := f.resolve(path)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(safe)
}

func (f *LocalFS) WriteFile(path string, data []byte, perm os.FileMode) error {
	safe, err := f.resolve(path)
	if err != nil {
		return err
	}
	// Create parent directories if needed.
	if err := os.MkdirAll(filepath.Dir(safe), 0o755); err != nil {
		return err
	}
	return os.WriteFile(safe, data, perm)
}

func (f *LocalFS) Stat(path string) (os.FileInfo, error) {
	safe, err := f.resolve(path)
	if err != nil {
		return nil, err
	}
	return os.Stat(safe)
}

func (f *LocalFS) ReadDir(path string) ([]os.DirEntry, error) {
	safe, err := f.resolve(path)
	if err != nil {
		return nil, err
	}
	return os.ReadDir(safe)
}

func (f *LocalFS) MkdirAll(path string, perm os.FileMode) error {
	safe, err := f.resolve(path)
	if err != nil {
		return err
	}
	return os.MkdirAll(safe, perm)
}

func (f *LocalFS) Abs(path string) (string, error) {
	return f.resolve(path)
}

// inSandbox checks whether the given absolute path is within the workspace root.
// A simple HasPrefix(path, root) is insufficient because "/tmp/sandbox" is a
// prefix of "/tmp/sandboxescape". We require an exact match or a separator after root.
func (f *LocalFS) inSandbox(abs string) bool {
	return abs == f.root || strings.HasPrefix(abs, f.root+string(filepath.Separator))
}

// resolve converts a path to an absolute path within the sandbox.
// It rejects paths that escape the workspace root, including via symlinks.
func (f *LocalFS) resolve(path string) (string, error) {
	var abs string
	if filepath.IsAbs(path) {
		abs = filepath.Clean(path)
	} else {
		abs = filepath.Join(f.root, path)
	}

	// Check the cleaned path first (catches ../../../etc/passwd).
	if !f.inSandbox(abs) {
		return "", &SandboxError{Path: path, Root: f.root}
	}

	// Resolve symlinks and re-check to prevent symlink escape.
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// If the target doesn't exist yet (e.g., WriteFile to new path),
		// resolve the parent and check that.
		if os.IsNotExist(err) {
			return f.resolveNewPath(abs, path)
		}
		return "", err
	}

	if !f.inSandbox(resolved) {
		return "", &SandboxError{Path: path, Root: f.root}
	}
	return resolved, nil
}

// resolveNewPath handles paths where the target doesn't exist yet.
// It resolves the existing parent directory and verifies the sandbox.
func (f *LocalFS) resolveNewPath(abs, original string) (string, error) {
	dir := filepath.Dir(abs)
	base := filepath.Base(abs)

	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		// Parent doesn't exist either — check the cleaned path.
		if !f.inSandbox(abs) {
			return "", &SandboxError{Path: original, Root: f.root}
		}
		return abs, nil
	}

	full := filepath.Join(resolved, base)
	if !f.inSandbox(full) {
		return "", &SandboxError{Path: original, Root: f.root}
	}
	return full, nil
}
