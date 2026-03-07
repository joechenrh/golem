package lark

import (
	"encoding/json"
	"strings"
)

// hrRe matches markdown horizontal rules: ---, ***, ___ (with optional spaces).
var hrPatterns = []string{"---", "***", "___"}

// buildStructuredCard parses markdown into Lark card elements for richer
// visual formatting:
//   - A leading "# Title" becomes the card header
//   - Horizontal rules (---, ***, ___) become hr dividers
//   - Remaining text is split into separate markdown elements per section
//   - Action buttons are appended at the end
func buildStructuredCard(text string) []byte {
	lines := strings.Split(text, "\n")

	var header map[string]any
	startLine := 0

	// Check if first non-empty line is a top-level header.
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "# ") && !strings.HasPrefix(trimmed, "## ") {
			title := strings.TrimPrefix(trimmed, "# ")
			header = map[string]any{
				"template": "blue",
				"title": map[string]string{
					"tag":     "plain_text",
					"content": title,
				},
			}
			startLine = i + 1
		}
		break
	}

	// Group remaining lines into sections, splitting on horizontal rules.
	var elements []any
	var section strings.Builder

	flushSection := func() {
		content := strings.TrimSpace(section.String())
		if content != "" {
			elements = append(elements, map[string]any{
				"tag":     "markdown",
				"content": sanitizeLarkMarkdown(content),
			})
		}
		section.Reset()
	}

	for _, line := range lines[startLine:] {
		trimmed := strings.TrimSpace(line)

		if isHorizontalRule(trimmed) {
			flushSection()
			elements = append(elements, map[string]any{"tag": "hr"})
			continue
		}

		section.WriteString(line)
		section.WriteByte('\n')
	}
	flushSection()

	// Fallback: if no elements were generated, use a single markdown block.
	if len(elements) == 0 {
		elements = []any{
			map[string]any{
				"tag":     "markdown",
				"content": sanitizeLarkMarkdown(text),
			},
		}
	}

	elements = append(elements, actionButtons)
	card := map[string]any{"elements": elements}
	if header != nil {
		card["header"] = header
	}

	content, _ := json.Marshal(card)
	return content
}

// isHorizontalRule checks if a trimmed line is a markdown horizontal rule.
func isHorizontalRule(trimmed string) bool {
	for _, pat := range hrPatterns {
		if trimmed == pat {
			return true
		}
	}
	return false
}
