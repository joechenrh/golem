package router

import (
	"encoding/json"
	"strings"
)

// ParseToolArgs converts a key=value argument string into a JSON object.
// Supports unquoted values (key=value) and double-quoted values (key="value with spaces").
// Returns "{}" for empty input.
func ParseToolArgs(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}"
	}

	args := make(map[string]string)
	for raw != "" {
		// Find key.
		eqIdx := strings.IndexByte(raw, '=')
		if eqIdx < 0 {
			break
		}
		key := strings.TrimSpace(raw[:eqIdx])
		raw = raw[eqIdx+1:]

		// Parse value: quoted or unquoted.
		var value string
		if len(raw) > 0 && raw[0] == '"' {
			// Quoted value: find closing quote.
			closeIdx := strings.IndexByte(raw[1:], '"')
			if closeIdx < 0 {
				// No closing quote — take the rest.
				value = raw[1:]
				raw = ""
			} else {
				value = raw[1 : closeIdx+1]
				raw = strings.TrimSpace(raw[closeIdx+2:])
			}
		} else {
			// Unquoted value: take until next space.
			spaceIdx := strings.IndexByte(raw, ' ')
			if spaceIdx < 0 {
				value = raw
				raw = ""
			} else {
				value = raw[:spaceIdx]
				raw = strings.TrimSpace(raw[spaceIdx+1:])
			}
		}

		args[key] = value
	}

	b, _ := json.Marshal(args)
	return string(b)
}
