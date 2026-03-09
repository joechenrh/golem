package agent

import (
	"strings"

	"github.com/joechenrh/golem/internal/ctxmgr"
)

// planCheckPrefixLen is the number of characters at the start of a response
// to check for intent phrases. Plans open with intent; greetings or
// answers that happen to contain intent words deeper in the text should
// not trigger a nudge.
const planCheckPrefixLen = 400

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
		"马上", "正在", "稍等",
		"给我", "我现在就",
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
// The message reminds the LLM to call tools with correct parameters rather
// than just describing its intentions.
func nudgeMessage(content string) string {
	if isMostlyCJK(content) {
		return "请直接调用工具完成任务，确保传入所有必需参数。不要只描述你打算做什么。"
	}
	return "Call the appropriate tool now with all required parameters. Don't just describe what you plan to do."
}

// emptyResponseHint returns a recovery message injected after consecutive
// empty LLM responses to break the retry loop and provide guidance.
func emptyResponseHint(afterToolError bool) string {
	if afterToolError {
		return "The previous tool call returned an error. Review the error message, check the required parameters, and retry with correct arguments."
	}
	return "Your previous response was empty. If you need to use a tool, call it with the correct parameters. Otherwise, reply with a text answer."
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
