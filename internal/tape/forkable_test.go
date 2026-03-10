package tape

import (
	"testing"
)

func TestForkableStore_AppendAndEntries(t *testing.T) {
	parent := newTestStore(t)
	parent.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "parent1"})})

	forked := Fork(parent)
	forked.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "pending1"})})

	entries, err := forked.Entries()
	if err != nil {
		t.Fatalf("Entries() error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if p := entries[0].PayloadMap(); p["content"] != "parent1" {
		t.Errorf("entries[0] content = %v, want parent1", p["content"])
	}
	if p := entries[1].PayloadMap(); p["content"] != "pending1" {
		t.Errorf("entries[1] content = %v, want pending1", p["content"])
	}
}

func TestForkableStore_AutoGeneratesIDAndTimestamp(t *testing.T) {
	parent := newTestStore(t)
	forked := Fork(parent)

	forked.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "test"})})

	entries, _ := forked.Entries()
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	if entries[0].ID == "" {
		t.Error("expected auto-generated ID")
	}
	if entries[0].Timestamp.IsZero() {
		t.Error("expected auto-set timestamp")
	}
}

func TestForkableStore_Commit(t *testing.T) {
	parent := newTestStore(t)
	forked := Fork(parent)

	forked.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "committed"})})

	// Parent should have no entries before commit.
	parentEntries, _ := parent.Entries()
	if len(parentEntries) != 0 {
		t.Fatalf("parent entries before commit = %d, want 0", len(parentEntries))
	}

	if err := forked.Commit(); err != nil {
		t.Fatalf("Commit() error: %v", err)
	}

	// Parent should now have the entry.
	parentEntries, _ = parent.Entries()
	if len(parentEntries) != 1 {
		t.Fatalf("parent entries after commit = %d, want 1", len(parentEntries))
	}
	if p := parentEntries[0].PayloadMap(); p["content"] != "committed" {
		t.Errorf("committed content = %v, want committed", p["content"])
	}

	// Pending should be cleared.
	if forked.Pending() != 0 {
		t.Errorf("Pending() after commit = %d, want 0", forked.Pending())
	}
}

func TestForkableStore_Rollback(t *testing.T) {
	parent := newTestStore(t)
	parent.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "original"})})

	forked := Fork(parent)
	forked.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "discarded"})})

	forked.Rollback()

	// Forked should only show parent entries.
	entries, _ := forked.Entries()
	if len(entries) != 1 {
		t.Fatalf("len(entries) after rollback = %d, want 1", len(entries))
	}
	if p := entries[0].PayloadMap(); p["content"] != "original" {
		t.Errorf("content = %v, want original", p["content"])
	}

	// Parent should be unchanged.
	parentEntries, _ := parent.Entries()
	if len(parentEntries) != 1 {
		t.Fatalf("parent entries = %d, want 1", len(parentEntries))
	}
}

func TestForkableStore_SearchFindsPending(t *testing.T) {
	parent := newTestStore(t)
	parent.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "alpha"})})

	forked := Fork(parent)
	forked.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "alpha beta"})})

	results, err := forked.Search("alpha")
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
}

func TestForkableStore_LastAnchorFindsPending(t *testing.T) {
	parent := newTestStore(t)
	parent.AddAnchor("parent-anchor")

	forked := Fork(parent)
	forked.AddAnchor("pending-anchor")

	anchor, err := forked.LastAnchor()
	if err != nil {
		t.Fatalf("LastAnchor() error: %v", err)
	}
	if anchor == nil {
		t.Fatal("expected non-nil anchor")
	}
	p := anchor.PayloadMap()
	if p["label"] != "pending-anchor" {
		t.Errorf("anchor label = %v, want pending-anchor", p["label"])
	}
}

func TestForkableStore_LastAnchorFallsBackToParent(t *testing.T) {
	parent := newTestStore(t)
	parent.AddAnchor("parent-anchor")

	forked := Fork(parent)
	// No anchor in pending, should fall back to parent.
	forked.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "msg"})})

	anchor, err := forked.LastAnchor()
	if err != nil {
		t.Fatalf("LastAnchor() error: %v", err)
	}
	if anchor == nil {
		t.Fatal("expected non-nil anchor from parent")
	}
	p := anchor.PayloadMap()
	if p["label"] != "parent-anchor" {
		t.Errorf("anchor label = %v, want parent-anchor", p["label"])
	}
}

func TestForkableStore_Info(t *testing.T) {
	parent := newTestStore(t)
	parent.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "msg1"})})
	parent.AddAnchor("anchor1")
	parent.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "msg2"})})

	forked := Fork(parent)
	forked.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "pending1"})})
	forked.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "pending2"})})

	info := forked.Info()
	if info.TotalEntries != 5 {
		t.Errorf("TotalEntries = %d, want 5", info.TotalEntries)
	}
	if info.AnchorCount != 1 {
		t.Errorf("AnchorCount = %d, want 1", info.AnchorCount)
	}
	// Parent has 1 entry after anchor + 2 pending = 3 entries since anchor.
	if info.EntriesSinceAnchor != 3 {
		t.Errorf("EntriesSinceAnchor = %d, want 3", info.EntriesSinceAnchor)
	}
}

func TestForkableStore_InfoWithPendingAnchor(t *testing.T) {
	parent := newTestStore(t)
	parent.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "msg1"})})

	forked := Fork(parent)
	forked.AddAnchor("pending-anchor")
	forked.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "after"})})

	info := forked.Info()
	if info.TotalEntries != 3 {
		t.Errorf("TotalEntries = %d, want 3", info.TotalEntries)
	}
	if info.AnchorCount != 1 {
		t.Errorf("AnchorCount = %d, want 1", info.AnchorCount)
	}
	if info.EntriesSinceAnchor != 1 {
		t.Errorf("EntriesSinceAnchor = %d, want 1", info.EntriesSinceAnchor)
	}
}

func TestForkableStore_EntriesSince(t *testing.T) {
	parent := newTestStore(t)
	parent.AddAnchor("a1")
	parent.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "after-parent"})})

	forked := Fork(parent)
	forked.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "pending1"})})

	// Find the anchor ID.
	anchor, _ := forked.LastAnchor()
	if anchor == nil {
		t.Fatal("expected anchor")
	}

	since, err := forked.EntriesSince(anchor.ID)
	if err != nil {
		t.Fatalf("EntriesSince() error: %v", err)
	}
	if len(since) != 2 {
		t.Fatalf("len(since) = %d, want 2", len(since))
	}
	if p := since[0].PayloadMap(); p["content"] != "after-parent" {
		t.Errorf("since[0] content = %v, want after-parent", p["content"])
	}
	if p := since[1].PayloadMap(); p["content"] != "pending1" {
		t.Errorf("since[1] content = %v, want pending1", p["content"])
	}
}

func TestForkableStore_Pending(t *testing.T) {
	parent := newTestStore(t)
	forked := Fork(parent)

	if forked.Pending() != 0 {
		t.Errorf("Pending() = %d, want 0", forked.Pending())
	}

	forked.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "a"})})
	forked.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "b"})})

	if forked.Pending() != 2 {
		t.Errorf("Pending() = %d, want 2", forked.Pending())
	}
}

func TestForkableStore_EmptyForkEntriesReturnsParent(t *testing.T) {
	parent := newTestStore(t)
	parent.Append(TapeEntry{Kind: KindMessage, Payload: MarshalPayload(map[string]any{"content": "only-parent"})})

	forked := Fork(parent)

	entries, err := forked.Entries()
	if err != nil {
		t.Fatalf("Entries() error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
}
