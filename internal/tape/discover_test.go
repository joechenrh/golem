package tape

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAgentDir(t *testing.T) {
	base := t.TempDir()

	dir, err := AgentDir(base, "myagent")
	if err != nil {
		t.Fatalf("AgentDir() error: %v", err)
	}

	want := filepath.Join(base, "myagent")
	if dir != want {
		t.Errorf("AgentDir() = %q, want %q", dir, want)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat(%q) error: %v", dir, err)
	}
	if !info.IsDir() {
		t.Errorf("%q is not a directory", dir)
	}
}

func TestAgentDir_Idempotent(t *testing.T) {
	base := t.TempDir()

	dir1, err := AgentDir(base, "agent")
	if err != nil {
		t.Fatalf("first AgentDir() error: %v", err)
	}

	dir2, err := AgentDir(base, "agent")
	if err != nil {
		t.Fatalf("second AgentDir() error: %v", err)
	}

	if dir1 != dir2 {
		t.Errorf("AgentDir() not idempotent: %q != %q", dir1, dir2)
	}
}
