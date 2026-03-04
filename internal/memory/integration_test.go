package memory

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestIntegration_StoreAndRecall tests the full store + search flow against
// a real TiDB Cloud Serverless instance. Skipped if MNEMO_DB_HOST is not set.
func TestIntegration_StoreAndRecall(t *testing.T) {
	host := os.Getenv("MNEMO_DB_HOST")
	if host == "" {
		t.Skip("MNEMO_DB_HOST not set, skipping integration test")
	}

	user := os.Getenv("MNEMO_DB_USER")
	pass := os.Getenv("MNEMO_DB_PASS")
	dbName := os.Getenv("MNEMO_DB_NAME")
	if dbName == "" {
		dbName = "mnemos"
	}

	client := NewClient(
		&http.Client{Timeout: 30 * time.Second},
		host, user, pass, dbName, "", 0,
	)
	ctx := context.Background()

	// Use a unique key per test run to avoid conflicts.
	testKey := "golem-integration-test-" + uuid.New().String()[:8]

	// 1. Init schema.
	t.Log("Initializing schema...")
	client.InitSchema(ctx)
	t.Logf("Schema initialized (FTS available: %v)", client.ftsAvailable)

	// 2. Store a memory.
	t.Log("Storing memory...")
	mem, err := client.Store(ctx, "integration test: golem memory system is working correctly", testKey, "golem-test", []string{"test", "integration"})
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}
	t.Logf("Stored: id=%s key=%s", mem.ID, mem.Key)

	// Cleanup at end regardless of result.
	defer func() {
		t.Log("Cleaning up...")
		_, err := client.execSQL(ctx, fmt.Sprintf(
			"DELETE FROM %s.memories WHERE id = '%s'", dbName, sqlEscape(mem.ID),
		))
		if err != nil {
			t.Logf("Warning: cleanup failed: %v", err)
		} else {
			t.Log("Cleanup done")
		}
	}()

	// 3. Search for it (LIKE requires exact substring match).
	t.Log("Searching for memory...")
	results, err := client.Search(ctx, "golem memory system", 5)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	t.Logf("Found %d results", len(results))

	found := false
	for _, r := range results {
		t.Logf("  - [%s] %s (tags: %v)", r.ID, truncate(r.Content, 80), r.Tags)
		if r.ID == mem.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("stored memory %s not found in search results", mem.ID)
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
