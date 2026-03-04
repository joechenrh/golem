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

func (s *stubTool) Name() string                { return s.name }
func (s *stubTool) Description() string         { return s.desc }
func (s *stubTool) FullDescription() string     { return s.desc }
func (s *stubTool) Parameters() json.RawMessage { return s.params }

func (s *stubTool) Execute(_ context.Context, _ string) (string, error) {
	return "This tool is not yet implemented.", nil
}

var stubInputParams = json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`)
var stubIDParams = json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`)

// ScheduleStubs returns stub tools for scheduling operations.
func ScheduleStubs() []stubTool {
	return []stubTool{
		{name: "schedule_add", desc: "Add a scheduled task", params: stubInputParams},
		{name: "schedule_list", desc: "List scheduled tasks", params: json.RawMessage(`{"type":"object","properties":{}}`)},
		{name: "schedule_remove", desc: "Remove a scheduled task", params: stubIDParams},
	}
}
