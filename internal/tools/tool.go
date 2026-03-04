package tools

import (
	"context"
	"encoding/json"
)

// Tool is the interface that all agent tools implement.
type Tool interface {
	// Name returns the tool's unique identifier (e.g. "read_file", "shell_exec").
	Name() string

	// Description returns a short description for compact/progressive mode.
	Description() string

	// FullDescription returns the full description for expanded mode.
	// Defaults to Description() if the tool has no expanded form.
	FullDescription() string

	// Parameters returns the JSON Schema for the tool's arguments.
	Parameters() json.RawMessage

	// Execute runs the tool with the given raw JSON arguments.
	Execute(ctx context.Context, args string) (string, error)
}
