package tools

import (
	"encoding/json"

	"github.com/joechenrh/golem/internal/llm"
)

// ParseArgs unmarshals raw tool arguments (with NormalizeArgs applied) into
// the pointed-to struct v. Returns a non-empty error string on failure that
// follows the "Error: ..." convention used by all builtin tools, or "" on
// success.
func ParseArgs(args string, v any) string {
	if err := json.Unmarshal([]byte(llm.NormalizeArgs(args)), v); err != nil {
		return "Error: invalid arguments: " + err.Error()
	}
	return ""
}
