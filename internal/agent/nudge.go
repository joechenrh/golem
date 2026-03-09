package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/joechenrh/golem/internal/ctxmgr"
	"github.com/joechenrh/golem/internal/llm"
	"github.com/joechenrh/golem/internal/tape"
)

const ambiguousMaxLen = 100

const classifierSystemPrompt = `You are a response classifier for an AI agent that has tools available.
Given the agent's response and the user's last message, classify the situation.

Respond with JSON only:
{"decision": "nudge" | "accept" | "stuck", "task_summary": "..."}

Rules:
- "nudge": The agent is describing a plan instead of acting. It knows what to do but didn't call a tool.
- "accept": The agent's response is a valid final answer (explanation, confirmation, clarification question).
- "stuck": The agent is repeating itself, giving empty promises, or clearly lost. When stuck, write a 1-sentence task_summary of what the user actually wants done.

task_summary is only required when decision is "stuck".`

type classifierResult struct {
	Decision    string `json:"decision"`
	TaskSummary string `json:"task_summary"`
}

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

// classifyResponse calls the classifier LLM to decide how to handle an
// ambiguous agent response. Returns ("nudge"|"accept"|"stuck", taskSummary, true)
// on success, or ("", "", false) if the call fails or returns unparseable output.
func classifyResponse(
	ctx context.Context,
	client llm.Client,
	model string,
	userMessage string,
	agentResponse string,
	toolNames []string,
) (decision string, taskSummary string, ok bool) {
	_, modelName := llm.ParseModelProvider(model)
	userContent := fmt.Sprintf(
		"User's last message: %s\nAgent's response: %s\nAvailable tools: %s",
		userMessage, agentResponse, strings.Join(toolNames, ", "),
	)

	resp, err := client.Chat(ctx, llm.ChatRequest{
		Model:        modelName,
		SystemPrompt: classifierSystemPrompt,
		Messages:     []llm.Message{{Role: llm.RoleUser, Content: userContent}},
		MaxTokens:    150,
	})
	if err != nil {
		return "", "", false
	}
	return parseClassifierResponse(resp.Content)
}

// parseClassifierResponse extracts decision and task_summary from classifier JSON.
func parseClassifierResponse(body string) (decision string, taskSummary string, ok bool) {
	var result classifierResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &result); err != nil {
		return "", "", false
	}
	switch result.Decision {
	case "nudge", "accept", "stuck":
		return result.Decision, result.TaskSummary, true
	default:
		return "", "", false
	}
}

// isAmbiguousResponse returns true if the response is short enough and the
// session has tool history, making it worth asking the classifier.
func isAmbiguousResponse(content string, tapeStore tape.Store) bool {
	if len(content) >= ambiguousMaxLen {
		return false
	}
	return hasToolHistory(tapeStore)
}

// hasToolHistory checks if any tool-role message exists in the tape.
func hasToolHistory(tapeStore tape.Store) bool {
	entries, err := tapeStore.Entries()
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Kind != tape.KindMessage {
			continue
		}
		var msg struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(e.Payload, &msg) == nil && msg.Role == "tool" {
			return true
		}
	}
	return false
}

// taskReminderMessage returns a language-aware stuck-recovery message
// that re-injects the task summary.
func taskReminderMessage(taskSummary string, content string) string {
	if isMostlyCJK(content) {
		return fmt.Sprintf(
			"你似乎卡住了。用户需要：%s。请立即调用工具完成任务，不要解释或确认，直接执行。",
			taskSummary,
		)
	}
	return fmt.Sprintf(
		"You appear stuck. The user wants: %s. Call the appropriate tool now to complete this task. Do not explain or acknowledge — act.",
		taskSummary,
	)
}
