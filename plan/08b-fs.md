# Step 8b: Filesystem Abstraction

## Scope

Pluggable filesystem interface for file operation tools. The default implementation enforces workspace sandboxing — tools cannot read or write outside the workspace root.

## Files

- `internal/fs/fs.go` — FS interface
- `internal/fs/local.go` — LocalFS implementation with sandbox enforcement

## Key Points

### FS Interface (`fs.go`)

```go
package fs

// FS abstracts filesystem operations for tool implementations.
type FS interface {
    ReadFile(path string) ([]byte, error)
    WriteFile(path string, data []byte, perm os.FileMode) error
    Stat(path string) (os.FileInfo, error)
    ReadDir(path string) ([]os.DirEntry, error)
    MkdirAll(path string, perm os.FileMode) error
    Abs(path string) (string, error)
}
```

Mirrors the `os` package signatures so tools can be written naturally. The abstraction exists for two reasons:
1. **Sandbox enforcement** — `LocalFS` rejects paths outside the workspace root
2. **Testing** — tools can be tested with an in-memory FS

### LocalFS (`local.go`)

```go
type LocalFS struct {
    root string // workspace root (absolute path)
}

func NewLocalFS(root string) (*LocalFS, error)
```

**Path resolution**:
1. If the path is relative, resolve it against `root`
2. Call `filepath.Abs()` to canonicalize
3. Verify `strings.HasPrefix(abs, root)` — reject if false
4. Symlinks: resolve with `filepath.EvalSymlinks()` and re-check prefix

**Sandbox error**:
```go
type SandboxError struct {
    Path string
    Root string
}

func (e *SandboxError) Error() string {
    return fmt.Sprintf("path %q is outside workspace root %q", e.Path, e.Root)
}
```

### What the sandbox blocks

- `../` traversal beyond root: `ReadFile("../../etc/passwd")` → error
- Absolute paths outside root: `ReadFile("/etc/passwd")` → error
- Symlinks pointing outside root: `ReadFile("link-to-outside")` → error

### What the sandbox allows

- Any path within root: `ReadFile("src/main.go")` → OK
- Subdirectory creation: `MkdirAll("src/new/pkg", 0755)` → OK
- Absolute paths within root: `ReadFile("/workspace/src/main.go")` → OK

## Design Decisions

- Interface mirrors `os` package — no new concepts to learn
- `LocalFS` resolves symlinks to prevent sandbox escape
- `root` must be an absolute path (constructor validates)
- No `Remove` method — file deletion is intentionally excluded from Phase 1-2 for safety
- Future: `MemFS` for testing, `DockerFS` for sandboxed execution

## Done When

- `NewLocalFS(root)` creates a sandboxed FS
- `ReadFile("src/main.go")` reads file relative to root
- `ReadFile("../../etc/passwd")` returns `SandboxError`
- `WriteFile` creates parent dirs and writes within sandbox
- Symlink traversal outside root is blocked
