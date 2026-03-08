package exthook

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestHookMD(t *testing.T, dir, name, events string) {
	t.Helper()
	hookDir := filepath.Join(dir, name)
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: Test hook\nevents: " + events + "\ncommand: ./handler.sh\n---\nBody"
	if err := os.WriteFile(filepath.Join(hookDir, "HOOK.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// Write a dummy handler so the command path resolves.
	if err := os.WriteFile(filepath.Join(hookDir, "handler.sh"), []byte("#!/bin/sh\necho ok"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestDiscover(t *testing.T) {
	t.Run("finds hooks", func(t *testing.T) {
		dir := t.TempDir()
		writeTestHookMD(t, dir, "hook-a", "[before_llm_call]")
		writeTestHookMD(t, dir, "hook-b", "[after_reset]")

		hooks, err := Discover(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(hooks) != 2 {
			t.Errorf("found %d hooks, want 2", len(hooks))
		}
	})

	t.Run("missing dir", func(t *testing.T) {
		hooks, err := Discover("/nonexistent/path")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(hooks) != 0 {
			t.Errorf("found %d hooks, want 0", len(hooks))
		}
	})

	t.Run("skips invalid", func(t *testing.T) {
		dir := t.TempDir()
		writeTestHookMD(t, dir, "valid", "[before_llm_call]")

		// Create an invalid HOOK.md (no events).
		invalidDir := filepath.Join(dir, "invalid")
		os.MkdirAll(invalidDir, 0o755)
		os.WriteFile(filepath.Join(invalidDir, "HOOK.md"), []byte("---\nname: bad\ndescription: bad\ncommand: ./x\n---\n"), 0o644)

		hooks, err := Discover(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(hooks) != 1 {
			t.Errorf("found %d hooks, want 1 (should skip invalid)", len(hooks))
		}
	})

	t.Run("skips non-directories", func(t *testing.T) {
		dir := t.TempDir()
		writeTestHookMD(t, dir, "valid", "[before_llm_call]")
		// Create a regular file at the top level (should be skipped).
		os.WriteFile(filepath.Join(dir, "not-a-dir.txt"), []byte("hello"), 0o644)

		hooks, err := Discover(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(hooks) != 1 {
			t.Errorf("found %d hooks, want 1", len(hooks))
		}
	})
}
