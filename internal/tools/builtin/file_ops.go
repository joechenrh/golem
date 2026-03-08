package builtin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/joechenrh/golem/internal/fs"
	"github.com/joechenrh/golem/internal/tools"
)

const (
	maxReadChars   = 50_000
	maxListEntries = 200
	maxSearchHits  = 50
)

// ignoredDirs are skipped during listing and searching.
var ignoredDirs = map[string]bool{
	".git": true, "node_modules": true, "target": true,
	"vendor": true, "__pycache__": true, ".venv": true,
}

// isBinaryExt returns true for known binary file extensions.
func isBinaryExt(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".bmp", ".ico", ".webp",
		".pdf", ".zip", ".tar", ".gz", ".bz2", ".xz", ".7z",
		".exe", ".dll", ".so", ".dylib", ".bin",
		".mp3", ".mp4", ".avi", ".mov", ".wav", ".flac",
		".woff", ".woff2", ".ttf", ".eot",
		".o", ".a", ".pyc", ".class":
		return true
	}
	return false
}

// ── read_file ────────────────────────────────────────────────────

var readFileParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path":   {"type": "string", "description": "File path to read"},
		"offset": {"type": "integer", "description": "Line offset to start reading from (0-based, optional)"},
		"limit":  {"type": "integer", "description": "Maximum number of lines to read (optional)"}
	},
	"required": ["path"]
}`)

// ReadFileTool reads file contents within a sandboxed filesystem.
type ReadFileTool struct {
	filesystem fs.FS
}

func NewReadFileTool(filesystem fs.FS) *ReadFileTool {
	return &ReadFileTool{filesystem: filesystem}
}

func (t *ReadFileTool) Name() string        { return "read_file" }
func (t *ReadFileTool) Description() string { return "Read a file's contents" }
func (t *ReadFileTool) FullDescription() string {
	return "Read a file's contents with line numbers. " +
		"Use offset (0-based line number) and limit (line count) to read portions of large files. " +
		"Output is truncated at 50K chars. Binary files are detected and skipped."
}
func (t *ReadFileTool) Parameters() json.RawMessage { return readFileParams }

func (t *ReadFileTool) Execute(
	_ context.Context, args string,
) (string, error) {
	var params struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if errMsg := tools.ParseArgs(args, &params); errMsg != "" {
		return errMsg, nil
	}
	if params.Path == "" {
		return "Error: 'path' is required", nil
	}

	if isBinaryExt(params.Path) {
		return "[binary file]", nil
	}

	data, err := t.filesystem.ReadFile(params.Path)
	if err != nil {
		return "Error: " + err.Error(), nil
	}

	content := string(data)

	// Apply offset/limit by lines.
	if params.Offset > 0 || params.Limit > 0 {
		lines := strings.Split(content, "\n")
		start := params.Offset
		if start >= len(lines) {
			return fmt.Sprintf("[offset %d beyond end of file (%d lines)]", start, len(lines)), nil
		}
		end := len(lines)
		if params.Limit > 0 && start+params.Limit < end {
			end = start + params.Limit
		}
		content = strings.Join(lines[start:end], "\n")
	}

	if len(content) > maxReadChars {
		content = content[:maxReadChars] + fmt.Sprintf("\n[...truncated, showing %d of %d chars]", maxReadChars, len(data))
	}

	return content, nil
}

// ── write_file ───────────────────────────────────────────────────

var writeFileParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path":    {"type": "string", "description": "File path to write"},
		"content": {"type": "string", "description": "Content to write to the file"}
	},
	"required": ["path", "content"]
}`)

// WriteFileTool writes content to a file within a sandboxed filesystem.
type WriteFileTool struct {
	filesystem fs.FS
}

func NewWriteFileTool(filesystem fs.FS) *WriteFileTool {
	return &WriteFileTool{filesystem: filesystem}
}

func (t *WriteFileTool) Name() string        { return "write_file" }
func (t *WriteFileTool) Description() string { return "Write content to a file" }
func (t *WriteFileTool) FullDescription() string {
	return "Write content to a file, creating parent directories as needed. " +
		"WARNING: Overwrites the entire file if it already exists. " +
		"For partial changes, prefer edit_file instead."
}
func (t *WriteFileTool) Parameters() json.RawMessage { return writeFileParams }

func (t *WriteFileTool) Execute(
	_ context.Context, args string,
) (string, error) {
	var params struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if errMsg := tools.ParseArgs(args, &params); errMsg != "" {
		return errMsg, nil
	}
	if params.Path == "" {
		return "Error: 'path' is required", nil
	}

	data := []byte(params.Content)
	if err := t.filesystem.WriteFile(params.Path, data, 0o644); err != nil {
		return "Error: " + err.Error(), nil
	}

	return fmt.Sprintf("Wrote %d bytes to %s", len(data), params.Path), nil
}

// ── edit_file ────────────────────────────────────────────────────

var editFileParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path":     {"type": "string", "description": "File path to edit"},
		"old_text": {"type": "string", "description": "Exact text to find and replace"},
		"new_text": {"type": "string", "description": "Replacement text"}
	},
	"required": ["path", "old_text", "new_text"]
}`)

// EditFileTool performs targeted string replacement in a file.
type EditFileTool struct {
	filesystem fs.FS
}

func NewEditFileTool(filesystem fs.FS) *EditFileTool {
	return &EditFileTool{filesystem: filesystem}
}

func (t *EditFileTool) Name() string        { return "edit_file" }
func (t *EditFileTool) Description() string { return "Edit a file by replacing text" }
func (t *EditFileTool) FullDescription() string {
	return "Edit a file by finding and replacing an exact text match. " +
		"Only the FIRST occurrence of old_text is replaced. " +
		"Include enough surrounding context in old_text to make the match unique. " +
		"Use read_file first to see the exact text to match."
}
func (t *EditFileTool) Parameters() json.RawMessage { return editFileParams }

func (t *EditFileTool) Execute(
	_ context.Context, args string,
) (string, error) {
	var params struct {
		Path    string `json:"path"`
		OldText string `json:"old_text"`
		NewText string `json:"new_text"`
	}
	if errMsg := tools.ParseArgs(args, &params); errMsg != "" {
		return errMsg, nil
	}
	if params.Path == "" || params.OldText == "" {
		return "Error: 'path' and 'old_text' are required", nil
	}

	data, err := t.filesystem.ReadFile(params.Path)
	if err != nil {
		return "Error: " + err.Error(), nil
	}

	content := string(data)
	idx := strings.Index(content, params.OldText)
	if idx == -1 {
		return "Error: old_text not found in file", nil
	}

	// Find line numbers for context.
	startLine := strings.Count(content[:idx], "\n") + 1
	endLine := startLine + strings.Count(params.OldText, "\n")

	newContent := content[:idx] + params.NewText + content[idx+len(params.OldText):]
	if err := t.filesystem.WriteFile(params.Path, []byte(newContent), 0o644); err != nil {
		return "Error: " + err.Error(), nil
	}

	return fmt.Sprintf("Edited %s (lines %d-%d)", params.Path, startLine, endLine), nil
}

// ── list_directory ───────────────────────────────────────────────

var listDirParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path": {"type": "string", "description": "Directory path to list"}
	},
	"required": ["path"]
}`)

// ListDirectoryTool lists directory contents with type indicators.
type ListDirectoryTool struct {
	filesystem fs.FS
}

func NewListDirectoryTool(
	filesystem fs.FS,
) *ListDirectoryTool {
	return &ListDirectoryTool{filesystem: filesystem}
}

func (t *ListDirectoryTool) Name() string        { return "list_directory" }
func (t *ListDirectoryTool) Description() string { return "List directory contents" }
func (t *ListDirectoryTool) FullDescription() string {
	return "List directory contents with type indicators (file/dir) and sizes. " +
		"Shows up to 200 entries. Skips .git, node_modules, vendor, __pycache__, .venv, and target."
}
func (t *ListDirectoryTool) Parameters() json.RawMessage { return listDirParams }

func (t *ListDirectoryTool) Execute(
	_ context.Context, args string,
) (string, error) {
	var params struct {
		Path string `json:"path"`
	}
	if errMsg := tools.ParseArgs(args, &params); errMsg != "" {
		return errMsg, nil
	}
	if params.Path == "" {
		params.Path = "."
	}

	entries, err := t.filesystem.ReadDir(params.Path)
	if err != nil {
		return "Error: " + err.Error(), nil
	}

	var b strings.Builder
	count := 0
	for _, entry := range entries {
		if ignoredDirs[entry.Name()] {
			continue
		}
		if count >= maxListEntries {
			fmt.Fprintf(&b, "... and %d more entries\n", len(entries)-count)
			break
		}
		if entry.IsDir() {
			fmt.Fprintf(&b, "[dir]  %s/\n", entry.Name())
		} else {
			info, err := entry.Info()
			if err != nil {
				fmt.Fprintf(&b, "[file] %s\n", entry.Name())
			} else {
				fmt.Fprintf(&b, "[file] %s (%s)\n", entry.Name(), formatSize(info.Size()))
			}
		}
		count++
	}

	if b.Len() == 0 {
		return "[empty directory]", nil
	}
	return b.String(), nil
}

func formatSize(bytes int64) string {
	switch {
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// ── search_files ─────────────────────────────────────────────────

var searchFilesParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path":      {"type": "string", "description": "Directory path to search in"},
		"pattern":   {"type": "string", "description": "Text pattern to search for (case-insensitive)"},
		"file_glob": {"type": "string", "description": "Optional file glob filter (e.g. '*.go')"}
	},
	"required": ["path", "pattern"]
}`)

// SearchFilesTool searches file contents recursively.
type SearchFilesTool struct {
	filesystem fs.FS
}

func NewSearchFilesTool(
	filesystem fs.FS,
) *SearchFilesTool {
	return &SearchFilesTool{filesystem: filesystem}
}

func (t *SearchFilesTool) Name() string        { return "search_files" }
func (t *SearchFilesTool) Description() string { return "Search for text in files" }
func (t *SearchFilesTool) FullDescription() string {
	return "Search for text in files recursively. Returns matching lines with file paths and line numbers. " +
		"Case-insensitive. Shows up to 50 matches. Use file_glob (e.g. '*.go') to narrow results."
}
func (t *SearchFilesTool) Parameters() json.RawMessage { return searchFilesParams }

func (t *SearchFilesTool) Execute(
	_ context.Context, args string,
) (string, error) {
	var params struct {
		Path     string `json:"path"`
		Pattern  string `json:"pattern"`
		FileGlob string `json:"file_glob"`
	}
	if errMsg := tools.ParseArgs(args, &params); errMsg != "" {
		return errMsg, nil
	}
	if params.Path == "" || params.Pattern == "" {
		return "Error: 'path' and 'pattern' are required", nil
	}

	var results []string
	t.searchDir(params.Path, strings.ToLower(params.Pattern), params.FileGlob, &results)

	if len(results) == 0 {
		return "No matches found.", nil
	}
	return strings.Join(results, "\n"), nil
}

func (t *SearchFilesTool) searchDir(
	dir, pattern, glob string, results *[]string,
) {
	if len(*results) >= maxSearchHits {
		return
	}

	entries, err := t.filesystem.ReadDir(dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if len(*results) >= maxSearchHits {
			return
		}

		name := entry.Name()
		path := filepath.Join(dir, name)

		if entry.IsDir() {
			if ignoredDirs[name] {
				continue
			}
			t.searchDir(path, pattern, glob, results)
			continue
		}

		if isBinaryExt(name) {
			continue
		}

		// Apply glob filter.
		if glob != "" {
			matched, _ := filepath.Match(glob, name)
			if !matched {
				continue
			}
		}

		t.searchFile(path, pattern, results)
	}
}

func (t *SearchFilesTool) searchFile(
	path, pattern string, results *[]string,
) {
	data, err := t.filesystem.ReadFile(path)
	if err != nil {
		return
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if len(*results) >= maxSearchHits {
			return
		}
		line := scanner.Text()
		if strings.Contains(strings.ToLower(line), pattern) {
			*results = append(*results, fmt.Sprintf("%s:%d: %s", path, lineNum, line))
		}
	}
}
