package fs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// resolvedTempDir returns a temp dir with symlinks resolved (macOS /var -> /private/var).
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolving temp dir: %v", err)
	}
	return resolved
}

func TestNewLocalFS_AbsoluteRoot(t *testing.T) {
	dir := resolvedTempDir(t)
	lfs, err := NewLocalFS(dir)
	if err != nil {
		t.Fatalf("NewLocalFS(%q): %v", dir, err)
	}
	if lfs.Root() != dir {
		t.Errorf("Root() = %q, want %q", lfs.Root(), dir)
	}
}

func TestReadFile_InSandbox(t *testing.T) {
	dir := resolvedTempDir(t)
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("world"), 0o644)

	lfs, _ := NewLocalFS(dir)
	data, err := lfs.ReadFile("hello.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "world" {
		t.Errorf("ReadFile = %q, want %q", data, "world")
	}
}

func TestReadFile_AbsolutePathInSandbox(t *testing.T) {
	dir := resolvedTempDir(t)
	os.WriteFile(filepath.Join(dir, "abs.txt"), []byte("ok"), 0o644)

	lfs, _ := NewLocalFS(dir)
	data, err := lfs.ReadFile(filepath.Join(dir, "abs.txt"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "ok" {
		t.Errorf("ReadFile = %q, want %q", data, "ok")
	}
}

func TestReadFile_PathTraversal(t *testing.T) {
	dir := resolvedTempDir(t)
	lfs, _ := NewLocalFS(dir)

	_, err := lfs.ReadFile("../../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
	var se *SandboxError
	if !errors.As(err, &se) {
		t.Fatalf("expected SandboxError, got %T: %v", err, err)
	}
}

func TestReadFile_AbsolutePathOutside(t *testing.T) {
	dir := resolvedTempDir(t)
	lfs, _ := NewLocalFS(dir)

	_, err := lfs.ReadFile("/etc/passwd")
	if err == nil {
		t.Fatal("expected error for absolute path outside root")
	}
	var se *SandboxError
	if !errors.As(err, &se) {
		t.Fatalf("expected SandboxError, got %T: %v", err, err)
	}
}

func TestReadFile_SymlinkEscape(t *testing.T) {
	dir := resolvedTempDir(t)

	// Create a symlink pointing outside the sandbox.
	outside := resolvedTempDir(t)
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644)
	os.Symlink(outside, filepath.Join(dir, "escape"))

	lfs, _ := NewLocalFS(dir)
	_, err := lfs.ReadFile("escape/secret.txt")
	if err == nil {
		t.Fatal("expected error for symlink escape")
	}
	var se *SandboxError
	if !errors.As(err, &se) {
		t.Fatalf("expected SandboxError, got %T: %v", err, err)
	}
}

func TestWriteFile_CreatesParentDirs(t *testing.T) {
	dir := resolvedTempDir(t)
	lfs, _ := NewLocalFS(dir)

	err := lfs.WriteFile("sub/dir/file.txt", []byte("data"), 0o644)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, _ := os.ReadFile(filepath.Join(dir, "sub/dir/file.txt"))
	if string(got) != "data" {
		t.Errorf("file contents = %q, want %q", got, "data")
	}
}

func TestWriteFile_OutsideSandbox(t *testing.T) {
	dir := resolvedTempDir(t)
	lfs, _ := NewLocalFS(dir)

	err := lfs.WriteFile("../../tmp/evil.txt", []byte("data"), 0o644)
	if err == nil {
		t.Fatal("expected error for write outside sandbox")
	}
	var se *SandboxError
	if !errors.As(err, &se) {
		t.Fatalf("expected SandboxError, got %T: %v", err, err)
	}
}

func TestStat(t *testing.T) {
	dir := resolvedTempDir(t)
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("x"), 0o644)

	lfs, _ := NewLocalFS(dir)
	info, err := lfs.Stat("test.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Name() != "test.txt" {
		t.Errorf("Name = %q, want %q", info.Name(), "test.txt")
	}
}

func TestReadDir(t *testing.T) {
	dir := resolvedTempDir(t)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644)

	lfs, _ := NewLocalFS(dir)
	entries, err := lfs.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("got %d entries, want 2", len(entries))
	}
}

func TestMkdirAll(t *testing.T) {
	dir := resolvedTempDir(t)
	lfs, _ := NewLocalFS(dir)

	err := lfs.MkdirAll("deep/nested/dir", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "deep/nested/dir"))
	if err != nil {
		t.Fatalf("directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected a directory")
	}
}

func TestAbs(t *testing.T) {
	dir := resolvedTempDir(t)
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0o644)

	lfs, _ := NewLocalFS(dir)
	abs, err := lfs.Abs("file.txt")
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	if abs != filepath.Join(dir, "file.txt") {
		t.Errorf("Abs = %q, want %q", abs, filepath.Join(dir, "file.txt"))
	}
}

func TestSandboxError_Message(t *testing.T) {
	e := &SandboxError{Path: "../secret", Root: "/workspace"}
	want := `path "../secret" is outside workspace root "/workspace"`
	if e.Error() != want {
		t.Errorf("Error() = %q, want %q", e.Error(), want)
	}
}
