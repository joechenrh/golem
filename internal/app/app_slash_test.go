package app

import "testing"

func TestSlashCommandDetection(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		isCommand bool
	}{
		{"status command", "/status", true},
		{"help command", "/help", true},
		{"new command not bypassed", "/new", false},
		{"unknown slash", "/unknown", false},
		{"regular message", "fix issue #123", false},
		{"slash in middle", "please /help me", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isSlash := isRemoteSlashCommand(tt.text)
			if isSlash != tt.isCommand {
				t.Errorf("isRemoteSlashCommand(%q) = %v, want %v", tt.text, isSlash, tt.isCommand)
			}
		})
	}
}
