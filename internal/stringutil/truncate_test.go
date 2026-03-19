package stringutil

import (
	"strings"
	"testing"
)

func TestTruncateHeadTail_Short(t *testing.T) {
	got := TruncateHeadTail("hello", 100)
	if got != "hello" {
		t.Errorf("short input changed: %q", got)
	}
}

func TestTruncateHeadTail_Long(t *testing.T) {
	input := strings.Repeat("H", 500) + strings.Repeat("T", 500)
	got := TruncateHeadTail(input, 200)
	if !strings.HasPrefix(got, "HHH") {
		t.Errorf("head not preserved: %q", got[:20])
	}
	if !strings.HasSuffix(got, "TTT") {
		t.Errorf("tail not preserved: %q", got[len(got)-20:])
	}
	if !strings.Contains(got, "[truncated") {
		t.Error("missing truncation note")
	}
	// Total must not massively exceed maxBytes (note adds a few bytes)
	if len(got) > 250 {
		t.Errorf("output too long: %d", len(got))
	}
}

func TestTruncateHeadTail_ExactLimit(t *testing.T) {
	input := strings.Repeat("x", 100)
	got := TruncateHeadTail(input, 100)
	if got != input {
		t.Error("exact limit should be unchanged")
	}
}

func TestTruncateHeadTail_VerySmallMax(t *testing.T) {
	input := strings.Repeat("x", 1000)
	got := TruncateHeadTail(input, 10)
	// Should fallback to TruncateWithNote
	if len(got) > 30 {
		t.Errorf("very small max should truncate aggressively: len=%d", len(got))
	}
}
