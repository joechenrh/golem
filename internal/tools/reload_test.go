package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSkillMD(t *testing.T, dir, name, desc, body string) {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + desc + "\n---\n" + body
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSkillStore_Reload(t *testing.T) {
	t.Run("adds new skill", func(t *testing.T) {
		dir := t.TempDir()
		writeSkillMD(t, dir, "hello", "Say hello", "# Hello\nSay hello.")

		store := NewSkillStore()
		n := store.Reload([]string{dir})

		if n != 1 {
			t.Errorf("got %d updated, want 1", n)
		}
		if store.Get("hello") == nil {
			t.Error("hello not registered")
		}
	})

	t.Run("skips unchanged skill", func(t *testing.T) {
		dir := t.TempDir()
		writeSkillMD(t, dir, "hello", "Say hello", "# Hello\nSay hello.")

		store := NewSkillStore()
		store.Reload([]string{dir})

		n := store.Reload([]string{dir})
		if n != 0 {
			t.Errorf("got %d updated, want 0 (unchanged)", n)
		}
	})

	t.Run("updates changed skill", func(t *testing.T) {
		dir := t.TempDir()
		writeSkillMD(t, dir, "hello", "Say hello", "# Hello v1")

		store := NewSkillStore()
		store.Reload([]string{dir})

		writeSkillMD(t, dir, "hello", "Say hello", "# Hello v2")

		n := store.Reload([]string{dir})
		if n != 1 {
			t.Errorf("got %d updated, want 1", n)
		}
		if store.Get("hello").Body != "# Hello v2" {
			t.Error("skill body not updated")
		}
	})

	t.Run("handles missing dir", func(t *testing.T) {
		store := NewSkillStore()
		n := store.Reload([]string{"/nonexistent/path"})
		if n != 0 {
			t.Errorf("got %d updated, want 0", n)
		}
	})

	t.Run("multiple dirs", func(t *testing.T) {
		dir1 := t.TempDir()
		dir2 := t.TempDir()
		writeSkillMD(t, dir1, "alpha", "Alpha skill", "Alpha body")
		writeSkillMD(t, dir2, "beta", "Beta skill", "Beta body")

		store := NewSkillStore()
		n := store.Reload([]string{dir1, dir2})
		if n != 2 {
			t.Errorf("got %d updated, want 2", n)
		}
		if store.Get("alpha") == nil {
			t.Error("alpha not registered")
		}
		if store.Get("beta") == nil {
			t.Error("beta not registered")
		}
	})
}
