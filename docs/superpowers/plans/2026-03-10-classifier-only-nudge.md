# Classifier-Only Nudge System Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace brittle heuristic nudges (looksLikePlan, looksLikeAck) with the LLM classifier as the sole nudge mechanism, and improve the tool-use system prompt to allow clarifying questions.

**Architecture:** Remove Phase 1 heuristic nudge from the ReAct loop. Promote the classifier (old Phase 3) to be the primary nudge mechanism for all non-tool responses when enabled. Keep Phase 2 (stuck escalation) as a fallback after classifier nudges. Improve `toolUseInstruction` to explicitly allow clarifying questions.

**Tech Stack:** Go 1.22+, existing test infrastructure (mockLLMClient, newTestAgent)

---

## Chunk 1: Core Changes

### Task 1: Update `toolUseInstruction` in session.go

**Files:**
- Modify: `internal/agent/session.go:103-107`

- [ ] **Step 1: Update the toolUseInstruction constant**

Replace the current aggressive instruction:

```go
// toolUseInstruction is the shared tool-use guidance included in all system prompts.
const toolUseInstruction = "When you have enough information to act, call tools directly — " +
	"don't describe what you plan to do. If the user's request is ambiguous or " +
	"missing key details, ask a brief clarifying question first. " +
	"Keep narration brief; default to action over explanation.\n"
```

- [ ] **Step 2: Run existing tests to verify nothing breaks**

Run: `go test ./internal/agent/ -run TestBuildSystemPrompt -v`
Expected: PASS (tests check for "golem" and "Working directory", not the exact tool instruction text)

- [ ] **Step 3: Commit**

```bash
git add internal/agent/session.go
git commit -m "refactor: soften toolUseInstruction to allow clarifying questions

Inspired by OpenClaw's 'Tool Call Style' approach: let the LLM decide
when to ask for clarification vs act immediately, instead of forcing
immediate tool use on every turn."
```

### Task 2: Remove heuristic nudge functions from nudge.go

**Files:**
- Modify: `internal/agent/nudge.go`

- [ ] **Step 1: Remove heuristic functions and constants**

Remove these items from `nudge.go`:
- `planCheckPrefixLen` constant (line 48)
- `shouldNudge()` function (lines 55-57)
- `looksLikePlan()` function (lines 61-91)
- `startsWithPhrase()` function (lines 95-125)
- `ackMaxLen` constant (line 249)
- `ackPhrases` variable (lines 254-261)
- `looksLikeAck()` function (lines 266-280)

Keep everything else: `classifyResponse`, `parseClassifierResponse`, `isAmbiguousResponse`, `hasToolHistory`, `nudgeMessage`, `taskReminderMessage`, `emptyResponseHint`, `isMostlyCJK`, `sanitizeTaskSummary`, `classifierSystemPrompt`, `ambiguousMaxLen`.

- [ ] **Step 2: Relax `isAmbiguousResponse` — remove the length cap**

The 100-char limit prevented the classifier from evaluating longer responses (like clarifying questions). Replace with a check that just verifies the classifier is worth calling:

```go
// isAmbiguousResponse returns true when the classifier should evaluate the
// response. Previously gated on a short length limit; now any non-tool
// response is worth classifying when the session has tool history.
func isAmbiguousResponse(content string, tapeStore tape.Store) bool {
	if strings.TrimSpace(content) == "" {
		return false
	}
	return hasToolHistory(tapeStore)
}
```

Note: this requires adding `"strings"` to the import if not already present (it is already imported).

Also remove the now-unused `ambiguousMaxLen` constant.

- [ ] **Step 3: Verify nudge.go compiles**

Run: `go build ./internal/agent/`
Expected: success (no compilation errors)

- [ ] **Step 4: Commit**

```bash
git add internal/agent/nudge.go
git commit -m "refactor: remove heuristic nudge functions (looksLikePlan, looksLikeAck)

The heuristic approach incorrectly nudged valid clarifying questions
(e.g. '可以' starting a question was caught by ackPhrases, Chinese
plan phrases like '我来' caught legitimate responses). The LLM
classifier handles these cases correctly by understanding intent."
```

### Task 3: Restructure ReAct loop nudge phases in session.go

**Files:**
- Modify: `internal/agent/session.go:279-434`

- [ ] **Step 1: Replace Phase 1 + Phase 3 with classifier-only logic**

In `runReActLoop`, replace the three-phase nudge block (lines 342-434) with:

```go
		// ── Nudge: classifier-only (when enabled) ──────────────
		// When a classifier LLM is configured, ask it to decide
		// whether this response is a valid final answer, a plan
		// that should be nudged, or a stuck state.
		if s.classifierLLM != nil && isAmbiguousResponse(resp.Content, s.tape) {
			if nudges < maxNudges {
				lastUserMsg := s.lastUserMessage()
				toolNames := s.tools.Names()
				s.logger.Debug("invoking classifier",
					zap.Int("resp_len", len(resp.Content)),
					zap.Int("iter", iter))
				decision, taskSummary, rawBody, ok := classifyResponse(
					ctx, s.classifierLLM, s.config.ClassifierModel,
					lastUserMsg, resp.Content, toolNames,
				)
				if ok {
					s.logger.Debug("classifier decision",
						zap.String("decision", decision),
						zap.String("task_summary", taskSummary),
						zap.Int("iter", iter))
					switch decision {
					case "nudge":
						s.ephemeralMessages = append(s.ephemeralMessages,
							llm.Message{Role: llm.RoleAssistant, Content: resp.Content},
							llm.Message{Role: llm.RoleUser, Content: nudgeMessage(resp.Content)},
						)
						nudges++
						s.logger.Debug("classifier nudge",
							zap.Int("iter", iter),
							zap.String("discarded", stringutil.Truncate(resp.Content, 200)))
						continue
					case "stuck":
						s.lastTaskSummary = taskSummary
						s.ephemeralMessages = append(s.ephemeralMessages,
							llm.Message{Role: llm.RoleAssistant, Content: resp.Content},
							llm.Message{Role: llm.RoleUser, Content: taskReminderMessage(taskSummary, resp.Content)},
						)
						nudges++
						s.logger.Debug("classifier stuck",
							zap.Int("iter", iter),
							zap.String("task_summary", taskSummary),
							zap.String("discarded", stringutil.Truncate(resp.Content, 200)))
						continue
					case "accept":
						s.logger.Debug("classifier accepted response",
							zap.Int("iter", iter))
						// Fall through to accept.
					}
				} else {
					s.logger.Warn("classifier returned unparseable response, accepting",
						zap.Int("iter", iter),
						zap.String("raw_body", stringutil.Truncate(rawBody, 200)))
				}
			}
			// Nudge budget exhausted — fall through to accept.
		}

		// Stuck escalation: if classifier nudged at least once and the
		// LLM still returned text-only, inject a task-specific reminder.
		if nudges >= 1 && !stuckEscalated {
			stuckEscalated = true
			summary := s.lastTaskSummary
			if summary == "" {
				summary = sanitizeTaskSummary(s.lastUserMessage())
			}
			if summary != "" {
				s.ephemeralMessages = append(s.ephemeralMessages,
					llm.Message{Role: llm.RoleAssistant, Content: resp.Content},
					llm.Message{Role: llm.RoleUser, Content: taskReminderMessage(summary, resp.Content)},
				)
				nudges++
				s.logger.Debug("injecting task reminder (stuck escalation)",
					zap.Int("nudge", nudges), zap.Int("iter", iter),
					zap.String("task_summary", summary),
					zap.String("discarded", stringutil.Truncate(resp.Content, 200)))
				continue
			}
		}
```

Also remove the `lastToolFailed` variable declaration (line 283) and its usage at lines 321, 343-344, 358 — tool failure no longer triggers heuristic nudges. The existing per-tool failure counting + "reconsider" hint in `processToolCalls` (lines 742-746) is sufficient.

- [ ] **Step 2: Verify compilation**

Run: `go build ./internal/agent/`
Expected: success

- [ ] **Step 3: Commit**

```bash
git add internal/agent/session.go
git commit -m "refactor: replace heuristic nudge phases with classifier-only logic

Phase 1 (looksLikePlan/looksLikeAck) removed entirely. The LLM
classifier is now the sole nudge mechanism, gated by
GOLEM_CLASSIFIER_MODEL config. When no classifier is configured,
responses are accepted as-is (no nudging). Stuck escalation remains
as a fallback after classifier nudges."
```

### Task 4: Update tests

**Files:**
- Modify: `internal/agent/agent_test.go`
- Modify: `internal/agent/agent_integration_test.go`

- [ ] **Step 1: Remove `TestShouldNudge` test**

Delete the `TestShouldNudge` function (lines 509-568) — it tested removed heuristic functions.

- [ ] **Step 2: Remove `TestNudgeMessage` test**

Delete the `TestNudgeMessage` function (lines 571-581) — `nudgeMessage` still exists but was only tested via heuristic paths. Keep the function, remove the dedicated test (it's trivially correct).

Actually, `nudgeMessage` is still used by the classifier path. Keep the test — it's still valid. **Do NOT delete `TestNudgeMessage`.**

- [ ] **Step 3: Rewrite `TestHandleInput_AckNudge` to use classifier**

Replace the test (lines 962-1001) with a classifier-based version:

```go
func TestHandleInput_ClassifierNudge(t *testing.T) {
	// First response is "好的" → classifier says "nudge" → nudge injected.
	// Second response calls a tool.
	// Third response is the final answer.
	client := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{Content: "好的", FinishReason: "stop"},
			{
				FinishReason: "tool_calls",
				ToolCalls:    []llm.ToolCall{{ID: "tc1", Name: "test_tool", Arguments: `{}`}},
			},
			{Content: "Done! Here is the result.", FinishReason: "stop"},
		},
	}
	classifierClient := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{Content: `{"decision":"nudge"}`, FinishReason: "stop"},
		},
	}
	agent := newTestAgentWithClassifier(t, client, classifierClient, &mockTool{name: "test_tool", result: "ok"})

	// Seed tool history so isAmbiguousResponse returns true.
	agent.tape.Append(tape.TapeEntry{
		Kind:    tape.KindMessage,
		Payload: json.RawMessage(`{"role":"tool","content":"prev","tool_call_id":"old","name":"test_tool"}`),
	})

	result, err := agent.HandleInput(context.Background(), cliMsg("帮我查一下"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if strings.Contains(result, "好的") {
		t.Errorf("result should not contain nudged response, got %q", result)
	}
	if !strings.Contains(result, "Done!") {
		t.Errorf("result = %q, want final answer", result)
	}
	// LLM called 3 times: ack → (classifier nudge) → tool call → final.
	if client.callCount != 3 {
		t.Errorf("callCount = %d, want 3", client.callCount)
	}
}
```

- [ ] **Step 4: Add `newTestAgentWithClassifier` helper**

Add this after `newTestAgent`:

```go
func newTestAgentWithClassifier(t *testing.T, client, classifierClient *mockLLMClient, extraTools ...tools.Tool) *Session {
	t.Helper()

	dir := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(dir)
	tapePath := filepath.Join(resolved, "tape.jsonl")
	store, err := tape.NewFileStore(tapePath)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	strategy, _ := ctxmgr.NewContextStrategy("anchor")
	registry := tools.NewRegistry()
	for _, tool := range extraTools {
		registry.Register(tool)
	}

	logger := zap.NewNop()
	bus := hooks.NewBus(logger)

	cfg := &config.Config{
		Model:           "openai:gpt-4o",
		ClassifierModel: "openai:gpt-4o-mini",
		MaxToolIter:     15,
	}

	return NewSession(client, classifierClient, registry, store, strategy, bus, cfg, logger)
}
```

- [ ] **Step 5: Rewrite `TestHandleInputStream_NudgeDoesNotLeak` to use classifier**

Replace lines 1003-1040:

```go
func TestHandleInputStream_NudgeDoesNotLeak(t *testing.T) {
	// LLM returns a plan-like response first, classifier says "nudge",
	// then the real answer.
	client := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{Content: "I'll read the file now.", FinishReason: "stop"},
			{Content: "The file contains hello world.", FinishReason: "stop"},
		},
	}
	classifierClient := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{Content: `{"decision":"nudge"}`, FinishReason: "stop"},
			{Content: `{"decision":"accept"}`, FinishReason: "stop"},
		},
	}
	agent := newTestAgentWithClassifier(t, client, classifierClient)

	// Seed tool history so classifier is invoked.
	agent.tape.Append(tape.TapeEntry{
		Kind:    tape.KindMessage,
		Payload: json.RawMessage(`{"role":"tool","content":"prev","tool_call_id":"old","name":"test_tool"}`),
	})

	tokenCh := make(chan string, 100)
	var tokens []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for tok := range tokenCh {
			tokens = append(tokens, tok)
		}
	}()

	err := agent.HandleInputStream(context.Background(), cliMsg("Read the file"), tokenCh)
	close(tokenCh)
	<-done

	if err != nil {
		t.Fatalf("HandleInputStream: %v", err)
	}
	joined := strings.Join(tokens, "")
	if strings.Contains(joined, "I'll read the file") {
		t.Errorf("nudged response leaked to stream: %q", joined)
	}
	if !strings.Contains(joined, "hello world") {
		t.Errorf("final response missing from stream: %q", joined)
	}
}
```

- [ ] **Step 6: Add test for classifier-disabled (no nudging)**

```go
func TestHandleInput_NoClassifier_AcceptsDirectly(t *testing.T) {
	// Without classifier, plan-like responses are accepted as-is.
	client := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{Content: "I'll read the file now.", FinishReason: "stop"},
		},
	}
	agent := newTestAgent(t, client) // no classifier

	result, err := agent.HandleInput(context.Background(), cliMsg("Read the file"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if result != "I'll read the file now." {
		t.Errorf("result = %q, want plan-like response accepted as-is", result)
	}
	if client.callCount != 1 {
		t.Errorf("callCount = %d, want 1 (no nudging without classifier)", client.callCount)
	}
}
```

- [ ] **Step 7: Add test for classifier accepting clarifying questions**

```go
func TestHandleInput_ClassifierAcceptsClarification(t *testing.T) {
	// Classifier correctly accepts a clarifying question as a final answer.
	client := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{Content: "可以。你想查哪方面的新闻？", FinishReason: "stop"},
		},
	}
	classifierClient := &mockLLMClient{
		responses: []*llm.ChatResponse{
			{Content: `{"decision":"accept"}`, FinishReason: "stop"},
		},
	}
	agent := newTestAgentWithClassifier(t, client, classifierClient)

	// Seed tool history so classifier is invoked.
	agent.tape.Append(tape.TapeEntry{
		Kind:    tape.KindMessage,
		Payload: json.RawMessage(`{"role":"tool","content":"prev","tool_call_id":"old","name":"test_tool"}`),
	})

	result, err := agent.HandleInput(context.Background(), cliMsg("你能查新闻吗"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if result != "可以。你想查哪方面的新闻？" {
		t.Errorf("result = %q, want clarifying question accepted", result)
	}
	if client.callCount != 1 {
		t.Errorf("callCount = %d, want 1", client.callCount)
	}
}
```

- [ ] **Step 8: Update integration test `TestIntegration_NudgeBehavior`**

In `agent_integration_test.go`, the integration test at line 481 tests heuristic nudge behavior. Update it to test classifier nudge instead. The integration test harness uses a different mock structure — check `newTestHarness`:

```go
func TestIntegration_NudgeBehavior(t *testing.T) {
	h := newTestHarness(t, []mockResponse{
		// First response: plan-like text.
		{content: "I'll read the directory and then check the files."},
		// After classifier nudge, LLM uses a tool.
		{toolCalls: []mockToolCall{
			{id: "tc1", name: "list_directory", args: `{"path":"."}`},
		}},
		// Final answer after tool result.
		{content: "The workspace is empty."},
	})

	// Wire a classifier that nudges the first response.
	h.agent.classifierLLM = &staticClassifierClient{
		responses: []string{`{"decision":"nudge"}`},
	}
	h.agent.config.ClassifierModel = "openai:gpt-4o-mini"

	// Seed tool history so classifier fires.
	h.tape.Append(tape.TapeEntry{
		Kind:    tape.KindMessage,
		Payload: json.RawMessage(`{"role":"tool","content":"prev","tool_call_id":"old","name":"test_tool"}`),
	})

	result, err := h.agent.HandleInput(context.Background(), msg("Check the workspace"))
	if err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if result != "The workspace is empty." {
		t.Errorf("result = %q", result)
	}
	// 3 LLM calls: plan → (classifier nudge) → tool call → final answer.
	if h.mock.callCount() != 3 {
		t.Errorf("LLM called %d times, want 3", h.mock.callCount())
	}

	// Nudge messages should be ephemeral — NOT persisted to the tape.
	entries, _ := h.tape.Entries()
	for _, e := range entries {
		pm := e.PayloadMap()
		content, _ := pm["content"].(string)
		if strings.Contains(content, "Call the appropriate tool") || strings.Contains(content, "调用工具") {
			t.Error("nudge message should not be persisted to tape")
			break
		}
	}
}
```

Note: you may need to add a `staticClassifierClient` type that implements `llm.Client` for the integration tests, or reuse the existing mock. Check how `newTestHarness` sets up mocks and adapt accordingly.

- [ ] **Step 9: Run all agent tests**

Run: `go test ./internal/agent/ -v -count=1`
Expected: all tests PASS

- [ ] **Step 10: Run full test suite**

Run: `go test ./... -count=1`
Expected: all tests PASS

- [ ] **Step 11: Run go vet**

Run: `go vet ./...`
Expected: no issues

- [ ] **Step 12: Commit**

```bash
git add internal/agent/agent_test.go internal/agent/agent_integration_test.go
git commit -m "test: update nudge tests for classifier-only approach

Remove heuristic-based test cases (TestShouldNudge). Add classifier-
based tests: nudge via classifier, no-classifier passthrough,
classifier accepting clarifying questions. Update integration test
to wire a mock classifier."
```

### Task 5: Update design documentation

**Files:**
- Modify: `design/02-agent-session.md` (nudge system description)

- [ ] **Step 1: Update the nudge system section**

Find the section describing the nudge/classifier phases and update it to reflect the new two-phase approach:
1. Classifier (primary, when `GOLEM_CLASSIFIER_MODEL` is set)
2. Stuck escalation (fallback after classifier nudge)

Remove references to `looksLikePlan`, `looksLikeAck`, `shouldNudge`, and heuristic plan/ack detection.

- [ ] **Step 2: Commit**

```bash
git add design/02-agent-session.md
git commit -m "docs: update design doc for classifier-only nudge system"
```
