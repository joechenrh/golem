package fs

import (
	"fmt"
	"os"
)

// FS abstracts filesystem operations for tool implementations.
type FS interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, perm os.FileMode) error
	Stat(path string) (os.FileInfo, error)
	ReadDir(path string) ([]os.DirEntry, error)
	MkdirAll(path string, perm os.FileMode) error
	Abs(path string) (string, error)
}

// SandboxError indicates a path access outside the allowed root.
type SandboxError struct {
	Path string
	Root string
}

func (e *SandboxError) Error() string {
	return fmt.Sprintf("path %q is outside workspace root %q", e.Path, e.Root)
}
