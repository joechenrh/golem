package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/joechenrh/golem/internal/config"
)

func newTestPersona(dir string) *config.Persona {
	return &config.Persona{
		SoulPath:   filepath.Join(dir, "SOUL.md"),
		AgentsPath: filepath.Join(dir, "AGENTS.md"),
		MemoryPath: filepath.Join(dir, "MEMORY.md"),
	}
}

func TestPersonaSelfToolReadMemoryEmpty(t *testing.T) {
	dir := resolvedTempDir(t)
	persona := newTestPersona(dir)
	tool := NewPersonaSelfTool(persona)

	result, err := tool.Execute(context.Background(), `{"action":"read","file":"memory"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "does not exist") {
		t.Errorf("result = %q, want 'does not exist'", result)
	}
}

func TestPersonaSelfToolDefaultFileIsMemory(t *testing.T) {
	dir := resolvedTempDir(t)
	persona := newTestPersona(dir)
	tool := NewPersonaSelfTool(persona)

	// No "file" param should default to memory.
	result, err := tool.Execute(context.Background(), `{"action":"read"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "MEMORY.md") {
		t.Errorf("result = %q, want reference to MEMORY.md", result)
	}
}

func TestPersonaSelfToolWriteAndReadMemory(t *testing.T) {
	dir := resolvedTempDir(t)
	persona := newTestPersona(dir)
	tool := NewPersonaSelfTool(persona)

	result, err := tool.Execute(context.Background(), `{"action":"write","content":"# Memory\n\nUser prefers short answers."}`)
	if err != nil {
		t.Fatalf("write Execute: %v", err)
	}
	if !strings.Contains(result, "updated") {
		t.Errorf("write result = %q, want success", result)
	}

	// Verify in-memory update.
	if persona.GetMemory() != "# Memory\n\nUser prefers short answers." {
		t.Errorf("in-memory = %q", persona.GetMemory())
	}

	result, err = tool.Execute(context.Background(), `{"action":"read"}`)
	if err != nil {
		t.Fatalf("read Execute: %v", err)
	}
	if !strings.Contains(result, "User prefers short answers.") {
		t.Errorf("read result = %q, want memory content", result)
	}
}

func TestPersonaSelfToolWriteAndReadSoul(t *testing.T) {
	dir := resolvedTempDir(t)
	persona := newTestPersona(dir)
	tool := NewPersonaSelfTool(persona)

	result, err := tool.Execute(context.Background(), `{"action":"write","file":"soul","content":"You are a helpful agent."}`)
	if err != nil {
		t.Fatalf("write Execute: %v", err)
	}
	if !strings.Contains(result, "SOUL.md updated") {
		t.Errorf("write result = %q", result)
	}
	if persona.GetSoul() != "You are a helpful agent." {
		t.Errorf("in-memory soul = %q", persona.GetSoul())
	}

	result, err = tool.Execute(context.Background(), `{"action":"read","file":"soul"}`)
	if err != nil {
		t.Fatalf("read Execute: %v", err)
	}
	if !strings.Contains(result, "helpful agent") {
		t.Errorf("read result = %q", result)
	}
}

func TestPersonaSelfToolWriteAndReadAgents(t *testing.T) {
	dir := resolvedTempDir(t)
	persona := newTestPersona(dir)
	tool := NewPersonaSelfTool(persona)

	result, err := tool.Execute(context.Background(), `{"action":"write","file":"agents","content":"Always cite sources."}`)
	if err != nil {
		t.Fatalf("write Execute: %v", err)
	}
	if !strings.Contains(result, "AGENTS.md updated") {
		t.Errorf("write result = %q", result)
	}
	if persona.GetAgents() != "Always cite sources." {
		t.Errorf("in-memory agents = %q", persona.GetAgents())
	}

	result, err = tool.Execute(context.Background(), `{"action":"read","file":"agents"}`)
	if err != nil {
		t.Fatalf("read Execute: %v", err)
	}
	if !strings.Contains(result, "cite sources") {
		t.Errorf("read result = %q", result)
	}
}

func TestPersonaSelfToolSoulBackup(t *testing.T) {
	dir := resolvedTempDir(t)
	persona := newTestPersona(dir)
	tool := NewPersonaSelfTool(persona)

	// Write initial content.
	tool.Execute(context.Background(), `{"action":"write","file":"soul","content":"Original soul."}`)

	// Overwrite — should create .bak.
	tool.Execute(context.Background(), `{"action":"write","file":"soul","content":"New soul."}`)

	bakPath := filepath.Join(dir, "SOUL.md.bak")
	data, err := os.ReadFile(bakPath)
	if err != nil {
		t.Fatalf("reading backup: %v", err)
	}
	if string(data) != "Original soul." {
		t.Errorf("backup content = %q, want %q", data, "Original soul.")
	}
}

func TestPersonaSelfToolAgentsBackup(t *testing.T) {
	dir := resolvedTempDir(t)
	persona := newTestPersona(dir)
	tool := NewPersonaSelfTool(persona)

	tool.Execute(context.Background(), `{"action":"write","file":"agents","content":"Original rules."}`)
	tool.Execute(context.Background(), `{"action":"write","file":"agents","content":"New rules."}`)

	bakPath := filepath.Join(dir, "AGENTS.md.bak")
	data, err := os.ReadFile(bakPath)
	if err != nil {
		t.Fatalf("reading backup: %v", err)
	}
	if string(data) != "Original rules." {
		t.Errorf("backup content = %q, want %q", data, "Original rules.")
	}
}

func TestPersonaSelfToolMemoryNoBackup(t *testing.T) {
	dir := resolvedTempDir(t)
	persona := newTestPersona(dir)
	tool := NewPersonaSelfTool(persona)

	tool.Execute(context.Background(), `{"action":"write","content":"First."}`)
	tool.Execute(context.Background(), `{"action":"write","content":"Second."}`)

	bakPath := filepath.Join(dir, "MEMORY.md.bak")
	if _, err := os.Stat(bakPath); err == nil {
		t.Error("MEMORY.md.bak should not be created")
	}
}

func TestPersonaSelfToolLineLimitSoul(t *testing.T) {
	dir := resolvedTempDir(t)
	persona := newTestPersona(dir)
	tool := NewPersonaSelfTool(persona)

	// 101 lines should fail.
	content := strings.Repeat("line\n", 100) + "last"
	args, _ := json.Marshal(map[string]string{"action": "write", "file": "soul", "content": content})
	result, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(result, "Error:") || !strings.Contains(result, "100 line limit") {
		t.Errorf("result = %q, want Error with line limit message", result)
	}
}

func TestPersonaSelfToolLineLimitAgents(t *testing.T) {
	dir := resolvedTempDir(t)
	persona := newTestPersona(dir)
	tool := NewPersonaSelfTool(persona)

	content := strings.Repeat("line\n", 150) + "last"
	args, _ := json.Marshal(map[string]string{"action": "write", "file": "agents", "content": content})
	result, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(result, "Error:") || !strings.Contains(result, "150 line limit") {
		t.Errorf("result = %q, want Error with line limit message", result)
	}
}

func TestPersonaSelfToolLineLimitMemory(t *testing.T) {
	dir := resolvedTempDir(t)
	persona := newTestPersona(dir)
	tool := NewPersonaSelfTool(persona)

	content := strings.Repeat("line\n", 200) + "last"
	args, _ := json.Marshal(map[string]string{"action": "write", "content": content})
	result, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(result, "Error:") || !strings.Contains(result, "200 line limit") {
		t.Errorf("result = %q, want Error with line limit message", result)
	}
}

func TestPersonaSelfToolWriteEmptyContent(t *testing.T) {
	dir := resolvedTempDir(t)
	persona := newTestPersona(dir)
	tool := NewPersonaSelfTool(persona)

	result, err := tool.Execute(context.Background(), `{"action":"write","content":""}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(result, "Error:") {
		t.Fatalf("expected Error result for empty content, got %q", result)
	}
}

func TestPersonaSelfToolInvalidAction(t *testing.T) {
	dir := resolvedTempDir(t)
	persona := newTestPersona(dir)
	tool := NewPersonaSelfTool(persona)

	result, err := tool.Execute(context.Background(), `{"action":"delete"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(result, "Error:") {
		t.Fatalf("expected Error result for invalid action, got %q", result)
	}
}

func TestPersonaSelfToolInvalidFile(t *testing.T) {
	dir := resolvedTempDir(t)
	persona := newTestPersona(dir)
	tool := NewPersonaSelfTool(persona)

	result, err := tool.Execute(context.Background(), `{"action":"read","file":"identity"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(result, "Error:") {
		t.Fatalf("expected Error result for invalid file, got %q", result)
	}
}

func TestPersonaSelfToolInvalidJSON(t *testing.T) {
	dir := resolvedTempDir(t)
	persona := newTestPersona(dir)
	tool := NewPersonaSelfTool(persona)

	result, err := tool.Execute(context.Background(), `not json`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(result, "Error:") {
		t.Fatalf("expected Error result for invalid JSON, got %q", result)
	}
}

func TestPersonaSelfToolMetadata(t *testing.T) {
	tool := NewPersonaSelfTool(&config.Persona{
		MemoryPath: "/tmp/MEMORY.md",
		SoulPath:   "/tmp/SOUL.md",
		AgentsPath: "/tmp/AGENTS.md",
	})

	if tool.Name() != "persona_self" {
		t.Errorf("Name() = %q", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description() is empty")
	}
	if tool.FullDescription() == "" {
		t.Error("FullDescription() is empty")
	}
	if tool.Parameters() == nil {
		t.Error("Parameters() is nil")
	}
}

func TestPersonaSelfToolReadEmptyFile(t *testing.T) {
	dir := resolvedTempDir(t)
	persona := newTestPersona(dir)
	os.WriteFile(persona.MemoryPath, []byte(""), 0o644)

	tool := NewPersonaSelfTool(persona)
	result, err := tool.Execute(context.Background(), `{"action":"read"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "empty") {
		t.Errorf("result = %q, want 'empty'", result)
	}
}

func TestPersonaSelfToolWriteCreatesDir(t *testing.T) {
	dir := resolvedTempDir(t)
	persona := &config.Persona{
		MemoryPath: filepath.Join(dir, "nested", "deep", "MEMORY.md"),
		SoulPath:   filepath.Join(dir, "nested", "deep", "SOUL.md"),
		AgentsPath: filepath.Join(dir, "nested", "deep", "AGENTS.md"),
	}

	tool := NewPersonaSelfTool(persona)
	result, err := tool.Execute(context.Background(), `{"action":"write","content":"test"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "updated") {
		t.Errorf("result = %q", result)
	}

	data, _ := os.ReadFile(persona.MemoryPath)
	if string(data) != "test" {
		t.Errorf("file content = %q, want %q", data, "test")
	}
}

func TestPersonaSelfToolConcurrentWrite(t *testing.T) {
	dir := resolvedTempDir(t)
	persona := newTestPersona(dir)
	tool := NewPersonaSelfTool(persona)

	// Run with -race to verify no data races. We don't assert final
	// value ordering since concurrent writes are inherently non-deterministic.
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			content := strings.Repeat("x", i+1)
			tool.Execute(context.Background(), `{"action":"write","content":"`+content+`"}`)
		}()
	}
	wg.Wait()

	// Verify something was written and the getter doesn't panic.
	mem := persona.GetMemory()
	if mem == "" {
		t.Error("expected non-empty memory after concurrent writes")
	}
	if _, err := os.ReadFile(persona.MemoryPath); err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
}
