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

func TestReloadSkills(t *testing.T) {
	t.Run("adds new skill", func(t *testing.T) {
		dir := t.TempDir()
		writeSkillMD(t, dir, "hello", "Say hello", "# Hello\nSay hello.")

		r := NewRegistry()
		n := r.ReloadSkills([]string{dir})

		if n != 1 {
			t.Errorf("got %d updated, want 1", n)
		}
		if r.Get("skill_hello") == nil {
			t.Error("skill_hello not registered")
		}
	})

	t.Run("skips unchanged skill", func(t *testing.T) {
		dir := t.TempDir()
		writeSkillMD(t, dir, "hello", "Say hello", "# Hello\nSay hello.")

		r := NewRegistry()
		r.ReloadSkills([]string{dir})

		// Second reload — no changes.
		n := r.ReloadSkills([]string{dir})
		if n != 0 {
			t.Errorf("got %d updated, want 0 (unchanged)", n)
		}
	})

	t.Run("updates changed skill", func(t *testing.T) {
		dir := t.TempDir()
		writeSkillMD(t, dir, "hello", "Say hello", "# Hello v1")

		r := NewRegistry()
		r.ReloadSkills([]string{dir})

		// Modify the skill.
		writeSkillMD(t, dir, "hello", "Say hello", "# Hello v2")

		n := r.ReloadSkills([]string{dir})
		if n != 1 {
			t.Errorf("got %d updated, want 1", n)
		}
		if r.Get("skill_hello").FullDescription() != "# Hello v2" {
			t.Error("skill body not updated")
		}
	})

	t.Run("handles missing dir", func(t *testing.T) {
		r := NewRegistry()
		n := r.ReloadSkills([]string{"/nonexistent/path"})
		if n != 0 {
			t.Errorf("got %d updated, want 0", n)
		}
	})

	t.Run("multiple dirs", func(t *testing.T) {
		dir1 := t.TempDir()
		dir2 := t.TempDir()
		writeSkillMD(t, dir1, "alpha", "Alpha skill", "Alpha body")
		writeSkillMD(t, dir2, "beta", "Beta skill", "Beta body")

		r := NewRegistry()
		n := r.ReloadSkills([]string{dir1, dir2})
		if n != 2 {
			t.Errorf("got %d updated, want 2", n)
		}
		if r.Get("skill_alpha") == nil {
			t.Error("skill_alpha not registered")
		}
		if r.Get("skill_beta") == nil {
			t.Error("skill_beta not registered")
		}
	})
}
