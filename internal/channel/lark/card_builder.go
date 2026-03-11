package lark

import (
	"encoding/json"
	"slices"
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
//
// buildStructuredCardWithFooter is like buildStructuredCard but inserts a
// note element before the action buttons to display footer text.
func buildStructuredCardWithFooter(text, footer string) []byte {
	return buildStructuredCardInner(text, footer)
}

func buildStructuredCard(text string) []byte {
	return buildStructuredCardInner(text, "")
}

func buildStructuredCardInner(text, footer string) []byte {
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

	if footer != "" {
		elements = append(elements, map[string]any{
			"tag": "note",
			"elements": []any{
				map[string]any{"tag": "plain_text", "content": footer},
			},
		})
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
	return slices.Contains(hrPatterns, trimmed)
}
