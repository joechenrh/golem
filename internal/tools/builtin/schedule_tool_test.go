package builtin

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joechenrh/golem/internal/scheduler"
)

func newTestStore(t *testing.T) *scheduler.Store {
	t.Helper()
	return scheduler.NewStore(filepath.Join(t.TempDir(), "schedules.json"))
}

func TestScheduleAddTool(t *testing.T) {
	store := newTestStore(t)
	tool := NewScheduleAddTool(store, nil)

	args, _ := json.Marshal(map[string]string{
		"cron_expr":    "@daily",
		"prompt":       "send standup reminder",
		"channel_name": "lark",
		"channel_id":   "oc_123",
		"description":  "daily standup",
	})

	result, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "Schedule created") {
		t.Errorf("expected success message, got %q", result)
	}

	list := store.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(list))
	}
	if list[0].Prompt != "send standup reminder" {
		t.Errorf("expected prompt %q, got %q", "send standup reminder", list[0].Prompt)
	}
}

func TestScheduleAddTool_InvalidCron(t *testing.T) {
	store := newTestStore(t)
	tool := NewScheduleAddTool(store, nil)

	args, _ := json.Marshal(map[string]string{
		"cron_expr":    "not valid",
		"prompt":       "test",
		"channel_name": "lark",
		"channel_id":   "oc_1",
	})

	result, _ := tool.Execute(context.Background(), string(args))
	if !strings.Contains(result, "Error:") {
		t.Errorf("expected error for invalid cron, got %q", result)
	}
}

func TestScheduleAddTool_MissingFields(t *testing.T) {
	store := newTestStore(t)
	tool := NewScheduleAddTool(store, nil)

	tests := []struct {
		name string
		args map[string]string
	}{
		{"missing cron", map[string]string{"prompt": "x", "channel_name": "y", "channel_id": "z"}},
		{"missing prompt", map[string]string{"cron_expr": "@daily", "channel_name": "y", "channel_id": "z"}},
		{"missing channel_name", map[string]string{"cron_expr": "@daily", "prompt": "x", "channel_id": "z"}},
		{"missing channel_id", map[string]string{"cron_expr": "@daily", "prompt": "x", "channel_name": "y"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, _ := json.Marshal(tt.args)
			result, _ := tool.Execute(context.Background(), string(args))
			if !strings.Contains(result, "Error:") {
				t.Errorf("expected error, got %q", result)
			}
		})
	}
}

func TestScheduleListTool_Empty(t *testing.T) {
	store := newTestStore(t)
	tool := NewScheduleListTool(store)

	result, err := tool.Execute(context.Background(), "{}")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result != "No scheduled tasks." {
		t.Errorf("expected empty message, got %q", result)
	}
}

func TestScheduleListTool_WithSchedules(t *testing.T) {
	store := newTestStore(t)
	store.Add("@daily", "prompt1", "lark", "oc_1", "daily task")
	store.Add("@hourly", "prompt2", "lark", "oc_2", "hourly task")

	tool := NewScheduleListTool(store)
	result, err := tool.Execute(context.Background(), "{}")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "daily task") || !strings.Contains(result, "hourly task") {
		t.Errorf("expected both tasks in output, got %q", result)
	}
	if !strings.Contains(result, "2") {
		t.Errorf("expected count in output, got %q", result)
	}
}

func TestScheduleRemoveTool(t *testing.T) {
	store := newTestStore(t)
	id, _ := store.Add("@daily", "prompt", "lark", "oc_1", "to remove")

	tool := NewScheduleRemoveTool(store, nil)
	args, _ := json.Marshal(map[string]string{"id": id})
	result, err := tool.Execute(context.Background(), string(args))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "removed") {
		t.Errorf("expected removal message, got %q", result)
	}
	if len(store.List()) != 0 {
		t.Error("expected empty list after removal")
	}
}

func TestScheduleRemoveTool_NotFound(t *testing.T) {
	store := newTestStore(t)
	tool := NewScheduleRemoveTool(store, nil)

	args, _ := json.Marshal(map[string]string{"id": "nonexistent"})
	result, _ := tool.Execute(context.Background(), string(args))
	if !strings.Contains(result, "Error:") {
		t.Errorf("expected error for nonexistent ID, got %q", result)
	}
}
