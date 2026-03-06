package scheduler

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStore_AddAndList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedules.json")
	s := NewStore(path)

	id, err := s.Add("@every 1m", "hello", "lark", "oc_123", "test schedule")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty ID")
	}

	list := s.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(list))
	}
	if list[0].ID != id {
		t.Errorf("expected ID %q, got %q", id, list[0].ID)
	}
	if !list[0].Enabled {
		t.Error("expected Enabled=true")
	}

	// Verify file was written.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("schedules file not created: %v", err)
	}
}

func TestStore_Remove(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "schedules.json"))

	id, _ := s.Add("@daily", "prompt", "lark", "oc_1", "daily")
	if err := s.Remove(id); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(s.List()) != 0 {
		t.Error("expected empty list after remove")
	}
}

func TestStore_RemoveNotFound(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "schedules.json"))

	if err := s.Remove("nonexistent"); err == nil {
		t.Error("expected error for nonexistent ID")
	}
}

func TestStore_Get(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "schedules.json"))

	id, _ := s.Add("@hourly", "prompt", "lark", "oc_1", "hourly")

	sched, ok := s.Get(id)
	if !ok {
		t.Fatal("expected to find schedule")
	}
	if sched.Description != "hourly" {
		t.Errorf("expected description %q, got %q", "hourly", sched.Description)
	}

	_, ok = s.Get("nonexistent")
	if ok {
		t.Error("expected not found for nonexistent ID")
	}
}

func TestStore_UpdateLastFired(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "schedules.json"))

	id, _ := s.Add("@daily", "prompt", "lark", "oc_1", "daily")
	now := time.Now()
	s.UpdateLastFired(id, now)

	sched, ok := s.Get(id)
	if !ok {
		t.Fatal("expected to find schedule")
	}
	if sched.LastFiredAt.IsZero() {
		t.Error("expected LastFiredAt to be set")
	}
}

func TestStore_LoadPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedules.json")

	// Write with one store.
	s1 := NewStore(path)
	id, _ := s1.Add("@daily", "prompt", "lark", "oc_1", "daily")

	// Load with a fresh store.
	s2 := NewStore(path)
	if err := s2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	list := s2.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 schedule after load, got %d", len(list))
	}
	if list[0].ID != id {
		t.Errorf("expected ID %q, got %q", id, list[0].ID)
	}
}

func TestStore_LoadMissingFile(t *testing.T) {
	s := NewStore("/tmp/nonexistent-test-schedules.json")
	if err := s.Load(); err != nil {
		t.Fatalf("Load should not error on missing file: %v", err)
	}
	if len(s.List()) != 0 {
		t.Error("expected empty list")
	}
}

func TestStore_InvalidCron(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(filepath.Join(dir, "schedules.json"))

	_, err := s.Add("not a cron", "prompt", "lark", "oc_1", "bad")
	if err == nil {
		t.Error("expected error for invalid cron expression")
	}
}
