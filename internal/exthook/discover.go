package exthook

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// Discover scans a directory for subdirectories containing HOOK.md files.
// Pattern: <dir>/*/HOOK.md (same as skill discovery).
func Discover(dir string) ([]*HookDef, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read hooks dir %s: %w", dir, err)
	}

	var hooks []*HookDef
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		hookPath := filepath.Join(dir, entry.Name(), "HOOK.md")
		if _, err := os.Stat(hookPath); os.IsNotExist(err) {
			continue
		}
		h, err := ParseHook(hookPath)
		if err != nil {
			// Skip invalid hooks, but log so operators can diagnose.
			log.Printf("skipping invalid hook %s: %v", hookPath, err)
			continue
		}
		hooks = append(hooks, h)
	}
	return hooks, nil
}
