package agent

import (
	"strings"

	"github.com/joechenrh/golem/internal/ctxmgr"
)

// planCheckPrefixLen is the number of characters at the start of a response
// to check for intent phrases. Plans open with intent; greetings or
// answers that happen to contain intent words deeper in the text should
// not trigger a nudge.
const planCheckPrefixLen = 200

// looksLikePlan returns true if the opening of the content appears to
// describe intended actions rather than providing a final answer.
func looksLikePlan(content string) bool {
	prefix := content
	if len(prefix) > planCheckPrefixLen {
		prefix = prefix[:planCheckPrefixLen]
	}

	lower := strings.ToLower(prefix)
	for _, phrase := range []string{
		"i'll ", "i will ", "let me ", "i'm going to ",
		"i'll\n", "i will\n", "let me\n",
		"first, i'll", "first, let me",
		"i can help", "i can do",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	// Chinese intent phrases must appear at a sentence boundary:
	// start of text, or after a newline / period / comma / exclamation.
	for _, phrase := range []string{
		"我来", "让我", "我会", "我将",
		"首先", "接下来我",
	} {
		if startsWithPhrase(prefix, phrase) {
			return true
		}
	}
	return false
}

// startsWithPhrase checks if phrase appears at the start of text or
// immediately after a sentence boundary (newline or CJK punctuation).
func startsWithPhrase(text, phrase string) bool {
	idx := strings.Index(text, phrase)
	if idx < 0 {
		return false
	}
	if idx == 0 {
		return true
	}
	// Check the rune immediately before the match.
	for i := idx - 1; i >= 0; i-- {
		r := rune(text[i])
		// Skip whitespace.
		if r == ' ' || r == '\t' {
			continue
		}
		// Sentence boundaries.
		switch r {
		case '\n', '.', ',', '!', '?',
			'\u3002', // fullwidth period
			'\uff0c', // fullwidth comma
			'\uff01', // fullwidth exclamation
			'\uff1f': // fullwidth question mark
			return true
		}
		// Part of a larger word/phrase — not a boundary.
		return false
	}
	return true
}

// nudgeMessage returns a nudge prompt in the same language as the content.
func nudgeMessage(content string) string {
	if isMostlyCJK(content) {
		return "不要只描述你打算做什么——现在就使用可用的工具来执行。"
	}
	return "Don't just describe what you'll do — use the available tools now to proceed."
}

// isMostlyCJK returns true if CJK characters make up the majority of
// non-whitespace, non-punctuation runes in the text.
func isMostlyCJK(s string) bool {
	var cjk, other int
	for _, r := range s {
		if r <= ' ' {
			continue
		}
		if ctxmgr.IsCJK(r) {
			cjk++
		} else {
			other++
		}
	}
	return cjk > other
}
