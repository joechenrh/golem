package builtin

import (
	"context"
	"encoding/json"
)

// stubTool is a placeholder tool that returns "not implemented" for future features.
type stubTool struct {
	name   string
	desc   string
	params json.RawMessage
}

func (s *stubTool) Name() string               { return s.name }
func (s *stubTool) Description() string         { return s.desc }
func (s *stubTool) FullDescription() string     { return s.desc }
func (s *stubTool) Parameters() json.RawMessage { return s.params }

func (s *stubTool) Execute(_ context.Context, _ string) (string, error) {
	return "This tool is not yet implemented.", nil
}

var stubInputParams = json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`)
var stubQueryParams = json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)
var stubURLParams = json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`)
var stubIDParams = json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`)

// WebStubs returns stub tools for web operations.
func WebStubs() []stubTool {
	return []stubTool{
		{name: "web_fetch", desc: "Fetch content from a URL", params: stubURLParams},
		{name: "web_search", desc: "Search the web", params: stubQueryParams},
	}
}

// MemoryStubs returns stub tools for memory operations.
func MemoryStubs() []stubTool {
	return []stubTool{
		{name: "memory_store", desc: "Store a memory for later recall", params: stubInputParams},
		{name: "memory_search", desc: "Search stored memories", params: stubQueryParams},
		{name: "memory_get", desc: "Get a memory by ID", params: stubIDParams},
		{name: "memory_update", desc: "Update an existing memory", params: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"content":{"type":"string"}},"required":["id","content"]}`)},
		{name: "memory_delete", desc: "Delete a memory by ID", params: stubIDParams},
	}
}

// ScheduleStubs returns stub tools for scheduling operations.
func ScheduleStubs() []stubTool {
	return []stubTool{
		{name: "schedule_add", desc: "Add a scheduled task", params: stubInputParams},
		{name: "schedule_list", desc: "List scheduled tasks", params: json.RawMessage(`{"type":"object","properties":{}}`)},
		{name: "schedule_remove", desc: "Remove a scheduled task", params: stubIDParams},
	}
}
