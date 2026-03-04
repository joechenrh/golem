package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joechenrh/golem/internal/executor"
	"github.com/joechenrh/golem/internal/fs"
)

func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolving temp dir: %v", err)
	}
	return resolved
}

func setupFS(t *testing.T) (*fs.LocalFS, string) {
	t.Helper()
	dir := resolvedTempDir(t)
	lfs, err := fs.NewLocalFS(dir)
	if err != nil {
		t.Fatalf("NewLocalFS: %v", err)
	}
	return lfs, dir
}

// ─── ShellTool Tests ─────────────────────────────────────────────

func TestShellTool_Execute(t *testing.T) {
	exec := executor.NewLocal(t.TempDir())
	tool := NewShellTool(exec, 30*time.Second)

	result, err := tool.Execute(context.Background(), `{"command":"echo hello"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "hello") {
		t.Errorf("result = %q, want to contain 'hello'", result)
	}
}

func TestShellTool_CustomTimeout(t *testing.T) {
	exec := executor.NewLocal(t.TempDir())
	tool := NewShellTool(exec, 30*time.Second)

	result, err := tool.Execute(context.Background(), `{"command":"sleep 10","timeout":1}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "timed out") {
		t.Errorf("result = %q, want to contain 'timed out'", result)
	}
}

func TestShellTool_EmptyCommand(t *testing.T) {
	exec := executor.NewLocal(t.TempDir())
	tool := NewShellTool(exec, 30*time.Second)

	result, _ := tool.Execute(context.Background(), `{"command":""}`)
	if !strings.Contains(result, "required") {
		t.Errorf("result = %q, want error about required command", result)
	}
}

func TestShellTool_InvalidJSON(t *testing.T) {
	exec := executor.NewLocal(t.TempDir())
	tool := NewShellTool(exec, 30*time.Second)

	result, _ := tool.Execute(context.Background(), `not json`)
	if !strings.Contains(result, "invalid") {
		t.Errorf("result = %q, want error about invalid arguments", result)
	}
}

func TestShellTool_NoopExecutor(t *testing.T) {
	exec := executor.NewNoop()
	tool := NewShellTool(exec, 30*time.Second)

	result, _ := tool.Execute(context.Background(), `{"command":"rm -rf /"}`)
	if !strings.Contains(result, "disabled") {
		t.Errorf("result = %q, want disabled message", result)
	}
}

func TestShellTool_Metadata(t *testing.T) {
	exec := executor.NewLocal(t.TempDir())
	tool := NewShellTool(exec, 30*time.Second)

	if tool.Name() != "shell_exec" {
		t.Errorf("Name() = %q", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description() is empty")
	}
	if tool.Parameters() == nil {
		t.Error("Parameters() is nil")
	}
}

// ─── ReadFileTool Tests ──────────────────────────────────────────

func TestReadFile_Success(t *testing.T) {
	lfs, dir := setupFS(t)
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello world"), 0o644)

	tool := NewReadFileTool(lfs)
	result, err := tool.Execute(context.Background(), `{"path":"test.txt"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result != "hello world" {
		t.Errorf("result = %q, want %q", result, "hello world")
	}
}

func TestReadFile_WithOffsetLimit(t *testing.T) {
	lfs, dir := setupFS(t)
	os.WriteFile(filepath.Join(dir, "lines.txt"), []byte("line0\nline1\nline2\nline3\nline4"), 0o644)

	tool := NewReadFileTool(lfs)
	result, _ := tool.Execute(context.Background(), `{"path":"lines.txt","offset":1,"limit":2}`)
	if result != "line1\nline2" {
		t.Errorf("result = %q, want %q", result, "line1\nline2")
	}
}

func TestReadFile_BinaryFile(t *testing.T) {
	lfs, _ := setupFS(t)
	tool := NewReadFileTool(lfs)

	result, _ := tool.Execute(context.Background(), `{"path":"image.png"}`)
	if result != "[binary file]" {
		t.Errorf("result = %q, want %q", result, "[binary file]")
	}
}

func TestReadFile_NotFound(t *testing.T) {
	lfs, _ := setupFS(t)
	tool := NewReadFileTool(lfs)

	result, _ := tool.Execute(context.Background(), `{"path":"nonexistent.txt"}`)
	if !strings.Contains(result, "Error") {
		t.Errorf("result = %q, want error", result)
	}
}

func TestReadFile_Truncation(t *testing.T) {
	lfs, dir := setupFS(t)
	bigContent := strings.Repeat("x", maxReadChars+1000)
	os.WriteFile(filepath.Join(dir, "big.txt"), []byte(bigContent), 0o644)

	tool := NewReadFileTool(lfs)
	result, _ := tool.Execute(context.Background(), `{"path":"big.txt"}`)
	if !strings.Contains(result, "truncated") {
		t.Errorf("result should mention truncation")
	}
}

// ─── WriteFileTool Tests ─────────────────────────────────────────

func TestWriteFile_Success(t *testing.T) {
	lfs, dir := setupFS(t)
	tool := NewWriteFileTool(lfs)

	result, err := tool.Execute(context.Background(), `{"path":"new.txt","content":"hello"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "5 bytes") {
		t.Errorf("result = %q, want byte count", result)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "new.txt"))
	if string(data) != "hello" {
		t.Errorf("file contents = %q", data)
	}
}

func TestWriteFile_CreatesParentDirs(t *testing.T) {
	lfs, dir := setupFS(t)
	tool := NewWriteFileTool(lfs)

	result, _ := tool.Execute(context.Background(), `{"path":"a/b/c.txt","content":"deep"}`)
	if !strings.Contains(result, "4 bytes") {
		t.Errorf("result = %q", result)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "a/b/c.txt"))
	if string(data) != "deep" {
		t.Errorf("file contents = %q", data)
	}
}

// ─── EditFileTool Tests ──────────────────────────────────────────

func TestEditFile_Success(t *testing.T) {
	lfs, dir := setupFS(t)
	os.WriteFile(filepath.Join(dir, "edit.txt"), []byte("foo bar baz"), 0o644)

	tool := NewEditFileTool(lfs)
	result, err := tool.Execute(context.Background(), `{"path":"edit.txt","old_text":"bar","new_text":"qux"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "Edited") {
		t.Errorf("result = %q", result)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "edit.txt"))
	if string(data) != "foo qux baz" {
		t.Errorf("file = %q, want %q", data, "foo qux baz")
	}
}

func TestEditFile_NotFound(t *testing.T) {
	lfs, dir := setupFS(t)
	os.WriteFile(filepath.Join(dir, "edit.txt"), []byte("hello"), 0o644)

	tool := NewEditFileTool(lfs)
	result, _ := tool.Execute(context.Background(), `{"path":"edit.txt","old_text":"missing","new_text":"x"}`)
	if !strings.Contains(result, "not found") {
		t.Errorf("result = %q, want not found error", result)
	}
}

// ─── ListDirectoryTool Tests ─────────────────────────────────────

func TestListDirectory_Success(t *testing.T) {
	lfs, dir := setupFS(t)
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(dir, "subdir"), 0o755)

	tool := NewListDirectoryTool(lfs)
	result, err := tool.Execute(context.Background(), `{"path":"."}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "[file] file.txt") {
		t.Errorf("result missing file entry: %s", result)
	}
	if !strings.Contains(result, "[dir]  subdir/") {
		t.Errorf("result missing dir entry: %s", result)
	}
}

func TestListDirectory_SkipsIgnored(t *testing.T) {
	lfs, dir := setupFS(t)
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755)
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)

	tool := NewListDirectoryTool(lfs)
	result, _ := tool.Execute(context.Background(), `{"path":"."}`)
	if strings.Contains(result, ".git") {
		t.Errorf("result should not contain .git: %s", result)
	}
	if strings.Contains(result, "node_modules") {
		t.Errorf("result should not contain node_modules: %s", result)
	}
	if !strings.Contains(result, "src") {
		t.Errorf("result should contain src: %s", result)
	}
}

func TestListDirectory_Empty(t *testing.T) {
	lfs, _ := setupFS(t)
	tool := NewListDirectoryTool(lfs)

	result, _ := tool.Execute(context.Background(), `{"path":"."}`)
	if result != "[empty directory]" {
		t.Errorf("result = %q, want '[empty directory]'", result)
	}
}

// ─── SearchFilesTool Tests ───────────────────────────────────────

func TestSearchFiles_Success(t *testing.T) {
	lfs, dir := setupFS(t)
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("func main() {\n\tfmt.Println(\"hello\")\n}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("no match here\n"), 0o644)

	tool := NewSearchFilesTool(lfs)
	result, err := tool.Execute(context.Background(), `{"path":".","pattern":"hello"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "a.go:2:") {
		t.Errorf("result = %q, want match in a.go line 2", result)
	}
	if strings.Contains(result, "b.txt") {
		t.Errorf("result should not contain b.txt: %s", result)
	}
}

func TestSearchFiles_WithGlob(t *testing.T) {
	lfs, dir := setupFS(t)
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("hello\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("hello\n"), 0o644)

	tool := NewSearchFilesTool(lfs)
	result, _ := tool.Execute(context.Background(), `{"path":".","pattern":"hello","file_glob":"*.go"}`)
	if !strings.Contains(result, "a.go") {
		t.Errorf("result should contain a.go: %s", result)
	}
	if strings.Contains(result, "b.txt") {
		t.Errorf("result should not contain b.txt: %s", result)
	}
}

func TestSearchFiles_NoMatch(t *testing.T) {
	lfs, dir := setupFS(t)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("nothing\n"), 0o644)

	tool := NewSearchFilesTool(lfs)
	result, _ := tool.Execute(context.Background(), `{"path":".","pattern":"nonexistent"}`)
	if result != "No matches found." {
		t.Errorf("result = %q", result)
	}
}

func TestSearchFiles_CaseInsensitive(t *testing.T) {
	lfs, dir := setupFS(t)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("Hello World\n"), 0o644)

	tool := NewSearchFilesTool(lfs)
	result, _ := tool.Execute(context.Background(), `{"path":".","pattern":"hello"}`)
	if !strings.Contains(result, "a.txt:1:") {
		t.Errorf("result = %q, want case-insensitive match", result)
	}
}

// ─── Stub Tests ──────────────────────────────────────────────────

func TestStubs_ReturnNotImplemented(t *testing.T) {
	stubs := WebStubs()
	for _, s := range stubs {
		result, err := s.Execute(context.Background(), `{"input":"test"}`)
		if err != nil {
			t.Errorf("%s Execute error: %v", s.Name(), err)
		}
		if result != "This tool is not yet implemented." {
			t.Errorf("%s result = %q", s.Name(), result)
		}
	}
}

// ─── FormatSize Tests ────────────────────────────────────────────

func TestFormatSize(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
	}
	for _, tt := range tests {
		got := formatSize(tt.bytes)
		if got != tt.want {
			t.Errorf("formatSize(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}
