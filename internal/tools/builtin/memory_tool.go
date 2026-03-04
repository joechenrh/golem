package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/joechenrh/golem/internal/llm"
	"github.com/joechenrh/golem/internal/memory"
)

// ---------------------------------------------------------------------------
// MemoryStoreTool
// ---------------------------------------------------------------------------

// MemoryStoreTool saves information to persistent memory backed by mnemos.
type MemoryStoreTool struct {
	client *memory.Client
}

func NewMemoryStoreTool(client *memory.Client) *MemoryStoreTool {
	return &MemoryStoreTool{client: client}
}

func (t *MemoryStoreTool) Name() string        { return "memory_store" }
func (t *MemoryStoreTool) Description() string { return "Save information to shared memory" }
func (t *MemoryStoreTool) FullDescription() string {
	return "Save important information to persistent shared memory for future sessions. " +
		"Use when the user asks to remember, note down, or save something. " +
		"Include specific values (IPs, versions, names, decisions). " +
		"Use a key for memories that should be unique and updateable later."
}

var memoryStoreParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"content": {"type": "string", "description": "The information to remember. Be concise but preserve all key details."},
		"tags": {"type": "array", "items": {"type": "string"}, "description": "1-3 short tags for categorization (e.g. infra, decision, config)"},
		"key": {"type": "string", "description": "Optional unique key for upsert semantics (e.g. server-ip-analytics)"},
		"source": {"type": "string", "description": "Source agent identifier (default: golem)"}
	},
	"required": ["content"]
}`)

func (t *MemoryStoreTool) Parameters() json.RawMessage { return memoryStoreParams }

func (t *MemoryStoreTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Content string   `json:"content"`
		Tags    []string `json:"tags"`
		Key     string   `json:"key"`
		Source  string   `json:"source"`
	}
	if err := json.Unmarshal([]byte(llm.NormalizeArgs(args)), &params); err != nil {
		return "Error: invalid arguments: " + err.Error(), nil
	}
	if params.Content == "" {
		return "Error: 'content' is required", nil
	}
	if params.Source == "" {
		params.Source = "golem"
	}

	mem, err := t.client.Store(ctx, params.Content, params.Key, params.Source, params.Tags)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	return fmt.Sprintf("Memory stored (id: %s)", mem.ID), nil
}

// ---------------------------------------------------------------------------
// MemoryRecallTool
// ---------------------------------------------------------------------------

// MemoryRecallTool searches shared memories from past sessions.
type MemoryRecallTool struct {
	client *memory.Client
}

func NewMemoryRecallTool(client *memory.Client) *MemoryRecallTool {
	return &MemoryRecallTool{client: client}
}

func (t *MemoryRecallTool) Name() string        { return "memory_recall" }
func (t *MemoryRecallTool) Description() string { return "Search shared memories from past sessions" }
func (t *MemoryRecallTool) FullDescription() string {
	return "Search shared memories from past sessions. Use when the user's question " +
		"could benefit from historical context, past decisions, project knowledge, " +
		"or team expertise. Returns relevant memories ranked by relevance."
}

var memoryRecallParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"query": {"type": "string", "description": "Search query — use 2-3 keywords from the user's question"},
		"limit": {"type": "integer", "description": "Maximum number of results (default 10)"}
	},
	"required": ["query"]
}`)

func (t *MemoryRecallTool) Parameters() json.RawMessage { return memoryRecallParams }

func (t *MemoryRecallTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(llm.NormalizeArgs(args)), &params); err != nil {
		return "Error: invalid arguments: " + err.Error(), nil
	}
	if params.Query == "" {
		return "Error: 'query' is required", nil
	}
	if params.Limit <= 0 {
		params.Limit = 10
	}

	memories, err := t.client.Search(ctx, params.Query, params.Limit)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	if len(memories) == 0 {
		return "No memories found for: " + params.Query, nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d memories:\n\n", len(memories)))
	for i, m := range memories {
		// Header: index, key, source, tags
		sb.WriteString(fmt.Sprintf("%d.", i+1))
		if m.Key != "" {
			sb.WriteString(fmt.Sprintf(" [%s]", m.Key))
		}
		if m.Source != "" {
			sb.WriteString(fmt.Sprintf(" by %s", m.Source))
		}
		if len(m.Tags) > 0 {
			sb.WriteString(fmt.Sprintf(" [%s]", strings.Join(m.Tags, ", ")))
		}
		sb.WriteString("\n")

		// Content (truncated for listing).
		content := m.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		sb.WriteString(fmt.Sprintf("   %s\n", content))

		if m.Score != nil {
			sb.WriteString(fmt.Sprintf("   score: %.4f\n", *m.Score))
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}
