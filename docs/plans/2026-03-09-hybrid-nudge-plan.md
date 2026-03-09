# Hybrid Nudge System Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace the brittle hardcoded nudge system with a hybrid approach that uses heuristics for clear cases and a lightweight LLM classifier for ambiguous cases, with stuck-recovery via task re-injection.

**Architecture:** The ReAct loop's nudge decision point gains a three-tier flow: (1) heuristic match → nudge, (2) ambiguous short response → classifier call → nudge/accept/stuck, (3) stuck escalation → task reminder injection. A second LLM client (cheap model) handles classification. Config adds one global env var.

**Tech Stack:** Go, existing `llm.Client` interface, `encoding/json` for classifier response parsing.

---

### Task 1: Heuristic Improvements (prefix len + remove "收到")

Independent of the classifier — reduces false positives/negatives immediately.

**Files:**
- Modify: `internal/agent/nudge.go:13` (planCheckPrefixLen)
- Modify: `internal/agent/nudge.go:36-44` (Chinese phrase list)
- Modify: `internal/agent/agent_test.go:508-531` (TestLooksLikePlan)

**Step 1: Update the test expectations first**

In `internal/agent/agent_test.go`, update `TestLooksLikePlan` table:

```go
func TestLooksLikePlan(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"english plan", "I'll read the file and check for errors.", true},
		{"english let me", "Let me look into this.", true},
		{"english answer", "The answer is 42.", false},
		{"chinese plan", "我来看一下这个文件。", true},
		{"chinese plan rang", "让我检查一下。", true},
		{"chinese greeting", "你好！我是 Golem，你的助手。你现在想让我帮你做什么？", false},
		{"chinese answer", "这个问题的答案是42。", false},
		{"chinese shoudao not plan", "收到，我明白了。", false},
		{"intent at char 300", strings.Repeat("x", 300) + "I'll do it now", true},
		{"intent at char 450", strings.Repeat("x", 450) + "I'll do it now", false},
		{"intent buried deep", strings.Repeat("正常内容。", 50) + "让我看看", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := looksLikePlan(tt.content); got != tt.want {
				t.Errorf("looksLikePlan() = %v, want %v", got, tt.want)
			}
		})
	}
}
```

**Step 2: Run the test — it should fail**

Run: `go test ./internal/agent/ -run TestLooksLikePlan -v -count=1`
Expected: FAIL on "chinese shoudao not plan" (currently matches "收到") and "intent at char 300" (currently misses at 200).

**Step 3: Update nudge.go**

In `internal/agent/nudge.go`:

Change line 13:
```go
const planCheckPrefixLen = 400
```

Remove `"收到",` from the Chinese phrase list (line 39).

**Step 4: Run the test — it should pass**

Run: `go test ./internal/agent/ -run TestLooksLikePlan -v -count=1`
Expected: PASS

**Step 5: Run full test suite + vet**

Run: `go vet ./... && go test ./internal/agent/ -v -count=1`
Expected: All pass.

**Step 6: Format and commit**

```bash
gofmt -w internal/agent/nudge.go internal/agent/agent_test.go
git add internal/agent/nudge.go internal/agent/agent_test.go
git -c commit.gpgsign=false commit -m "Improve nudge heuristics: widen prefix to 400, remove false-positive 收到"
```

---

### Task 2: Add ClassifierModel to config

**Files:**
- Modify: `internal/config/config.go:90-153` (Config struct)
- Modify: `internal/config/config.go:186-220` (Load function)
- Modify: `.env.example`

**Step 1: Write a config test**

Create `internal/config/config_test.go` test (or add to existing):

```go
func TestLoadClassifierModel(t *testing.T) {
	// Set env var, load config, check field.
	t.Setenv("GOLEM_MODEL", "openai:gpt-4o")
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("GOLEM_CLASSIFIER_MODEL", "openai:gpt-4o-mini")
	cfg, err := Load("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClassifierModel != "openai:gpt-4o-mini" {
		t.Errorf("ClassifierModel = %q, want %q", cfg.ClassifierModel, "openai:gpt-4o-mini")
	}
}

func TestLoadClassifierModelEmpty(t *testing.T) {
	t.Setenv("GOLEM_MODEL", "openai:gpt-4o")
	t.Setenv("OPENAI_API_KEY", "test-key")
	cfg, err := Load("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClassifierModel != "" {
		t.Errorf("ClassifierModel = %q, want empty", cfg.ClassifierModel)
	}
}
```

**Step 2: Run test — should fail**

Run: `go test ./internal/config/ -run TestLoadClassifierModel -v -count=1`
Expected: FAIL (field doesn't exist yet).

**Step 3: Add the field and parsing**

In `internal/config/config.go`, add to Config struct after line 107 (UseNativeWebSearch):

```go
	ClassifierModel string // lightweight model for nudge classification (e.g. "openai:gpt-4o-mini", empty=disabled)
```

In `Load()`, add to the global tier section (after line 193, WebSearchBackend):

```go
		ClassifierModel:  g.str("GOLEM_CLASSIFIER_MODEL", ""),
```

**Step 4: Update .env.example**

Add after the GOLEM_MODEL lines:

```bash
# Nudge classifier (optional — lightweight model for ambiguous response classification)
# GOLEM_CLASSIFIER_MODEL=openai:gpt-4o-mini
```

**Step 5: Run test — should pass**

Run: `go test ./internal/config/ -run TestLoadClassifierModel -v -count=1`
Expected: PASS

**Step 6: Format and commit**

```bash
gofmt -w internal/config/config.go
git add internal/config/config.go .env.example
# If config_test.go is new, add it too
git add internal/config/config_test.go
git -c commit.gpgsign=false commit -m "Add GOLEM_CLASSIFIER_MODEL to global config"
```

---

### Task 3: Build classifier LLM client and wire to Session

**Files:**
- Modify: `internal/app/app.go:609-644` (BuildLLMClient area — add BuildClassifierClient)
- Modify: `internal/agent/session.go:28-75` (Session struct)
- Modify: `internal/agent/session.go:135-153` (NewSession)
- Modify: `internal/agent/manager.go:22-33` (SessionFactory)
- Modify: `internal/agent/manager.go:188` and `manager.go:222` (NewSession calls)
- Modify: `internal/app/app.go:400` and `app.go:866` (NewSession calls)
- Modify: `internal/agent/agent_test.go:110` (test helper NewSession call)
- Modify: `internal/agent/agent_integration_test.go:244` (integration test NewSession call)

**Step 1: Add classifierLLM field to Session**

In `internal/agent/session.go`, add to Session struct after `llm` field (line 29):

```go
	classifierLLM llm.Client // lightweight model for nudge classification (nil = disabled)
```

**Step 2: Update NewSession to accept classifierLLM**

```go
func NewSession(
	llmClient llm.Client,
	classifierLLM llm.Client, // nil = classifier disabled
	toolRegistry *tools.Registry,
	tapeStore tape.Store,
	ctxStrategy ctxmgr.ContextStrategy,
	hookBus *hooks.Bus,
	cfg *config.Config,
	logger *zap.Logger,
) *Session {
	return &Session{
		llm:             llmClient,
		classifierLLM:   classifierLLM,
		tools:           toolRegistry,
		tape:            tapeStore,
		contextStrategy: ctxStrategy,
		hooks:           hookBus,
		config:          cfg,
		logger:          logger,
	}
}
```

**Step 3: Add BuildClassifierClient to app.go**

In `internal/app/app.go`, after `BuildLLMClient` (line 644):

```go
// BuildClassifierClient creates a lightweight LLM client for nudge
// classification. Returns nil if ClassifierModel is not configured.
func BuildClassifierClient(cfg *config.Config, logger *zap.Logger) (llm.Client, error) {
	if cfg.ClassifierModel == "" {
		return nil, nil
	}
	provider, _ := llm.ParseModelProvider(cfg.ClassifierModel)
	apiKey := cfg.APIKeys[string(provider)]
	if apiKey == "" {
		return nil, fmt.Errorf("no API key for classifier provider %q — set %s_API_KEY",
			provider, strings.ToUpper(string(provider)))
	}

	// Auto-register unknown providers as OpenAI-compatible.
	if provider != llm.ProviderOpenAI && provider != llm.ProviderAnthropic {
		baseURL := cfg.BaseURLs[string(provider)]
		if baseURL == "" {
			return nil, fmt.Errorf("classifier provider %q requires %s_BASE_URL",
				provider, strings.ToUpper(string(provider)))
		}
		llm.RegisterProvider(provider, baseURL, llm.NewOpenAICompatibleClient)
	}

	var opts []llm.ClientOption
	if baseURL := cfg.BaseURLs[string(provider)]; baseURL != "" {
		opts = append(opts, llm.WithBaseURL(baseURL))
	}
	if logger != nil {
		opts = append(opts, llm.WithLogger(logger.Named("classifier")))
	}
	client, err := llm.NewClient(provider, apiKey, opts...)
	if err != nil {
		return nil, fmt.Errorf("classifier client: %w", err)
	}
	return llm.NewRateLimitedClient(client, cfg.LLMRateLimit, logger), nil
}
```

**Step 4: Update all NewSession call sites**

Add `classifierLLM` (or `nil`) as the second argument to every `NewSession` call:

- `internal/app/app.go:400`: `agent.NewSession(llmClient, classifierLLM, registry, ...)`
  (build `classifierLLM` via `BuildClassifierClient` earlier in the function)
- `internal/app/app.go:866`: `agent.NewSession(llmClient, classifierLLM, registry, ...)`
  (same — use the classifier client built for this agent)
- `internal/agent/manager.go:188`: `NewSession(sm.factory.LLMClient, sm.factory.ClassifierLLM, registry, ...)`
- `internal/agent/manager.go:222`: `NewSession(sm.factory.LLMClient, sm.factory.ClassifierLLM, registry, ...)`
- `internal/agent/agent_test.go:110`: `NewSession(client, nil, registry, ...)`  (nil in tests)
- `internal/agent/agent_integration_test.go:244`: `NewSession(client, nil, registry, ...)`

Add `ClassifierLLM llm.Client` to SessionFactory struct (after LLMClient field, line 24).

**Step 5: Run tests to verify wiring compiles and passes**

Run: `go vet ./... && go test ./internal/agent/ -v -count=1 -timeout 120s`
Expected: All pass.

**Step 6: Format and commit**

```bash
gofmt -w internal/app/app.go internal/agent/session.go internal/agent/manager.go internal/agent/agent_test.go internal/agent/agent_integration_test.go
git add internal/app/app.go internal/agent/session.go internal/agent/manager.go internal/agent/agent_test.go internal/agent/agent_integration_test.go
git -c commit.gpgsign=false commit -m "Wire classifier LLM client through Session and SessionFactory"
```

---

### Task 4: Implement classifyResponse and hasToolHistory in nudge.go

**Files:**
- Modify: `internal/agent/nudge.go` (add classifyResponse, hasToolHistory, taskReminderMessage)

**Step 1: Write tests for classifier**

Add to `internal/agent/agent_test.go`:

```go
func TestClassifyResponseParsing(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantDec  string
		wantTask string
		wantOK   bool
	}{
		{
			name:    "nudge decision",
			body:    `{"decision":"nudge"}`,
			wantDec: "nudge", wantTask: "", wantOK: true,
		},
		{
			name:    "accept decision",
			body:    `{"decision":"accept"}`,
			wantDec: "accept", wantTask: "", wantOK: true,
		},
		{
			name:     "stuck with summary",
			body:     `{"decision":"stuck","task_summary":"Rewrite the doc in English"}`,
			wantDec:  "stuck",
			wantTask: "Rewrite the doc in English",
			wantOK:   true,
		},
		{
			name:   "invalid json",
			body:   `not json`,
			wantOK: false,
		},
		{
			name:   "unknown decision",
			body:   `{"decision":"unknown"}`,
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec, task, ok := parseClassifierResponse(tt.body)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if dec != tt.wantDec {
				t.Errorf("decision = %q, want %q", dec, tt.wantDec)
			}
			if task != tt.wantTask {
				t.Errorf("task_summary = %q, want %q", task, tt.wantTask)
			}
		})
	}
}

func TestTaskReminderMessage(t *testing.T) {
	en := taskReminderMessage("Rewrite the doc in English", "Hello, I will rewrite the doc.")
	if !strings.Contains(en, "Rewrite the doc in English") {
		t.Errorf("English reminder missing task: %q", en)
	}
	if !strings.Contains(en, "stuck") {
		t.Errorf("English reminder missing stuck indicator: %q", en)
	}

	cn := taskReminderMessage("用英语重写文档", "好的，我来重写一下这个文档。这个文档需要用中文来完成。")
	if !strings.Contains(cn, "用英语重写文档") {
		t.Errorf("Chinese reminder missing task: %q", cn)
	}
	if !strings.Contains(cn, "卡住") {
		t.Errorf("Chinese reminder missing stuck indicator: %q", cn)
	}
}

func TestHasToolHistory(t *testing.T) {
	// This test uses the tape mock from existing test infrastructure.
	// A tape with a tool-role entry should return true; empty tape returns false.
}
```

**Step 2: Run tests — should fail**

Run: `go test ./internal/agent/ -run "TestClassifyResponse|TestTaskReminder|TestHasToolHistory" -v -count=1`
Expected: FAIL (functions don't exist yet).

**Step 3: Implement in nudge.go**

Add to `internal/agent/nudge.go`:

```go
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

// classifierResult holds the parsed response from the classifier LLM.
type classifierResult struct {
	Decision    string `json:"decision"`
	TaskSummary string `json:"task_summary"`
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

// hasToolHistory checks if any tool-role message exists in the tape,
// indicating this session has used tools before.
func hasToolHistory(tapeStore tape.Store) bool {
	entries, err := tapeStore.Entries()
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Kind != tape.KindMessage {
			continue
		}
		var msg struct{ Role string `json:"role"` }
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
```

**Step 4: Run tests — should pass**

Run: `go test ./internal/agent/ -run "TestClassifyResponse|TestTaskReminder" -v -count=1`
Expected: PASS

**Step 5: Format and commit**

```bash
gofmt -w internal/agent/nudge.go internal/agent/agent_test.go
git add internal/agent/nudge.go internal/agent/agent_test.go
git -c commit.gpgsign=false commit -m "Add classifier response parsing, task reminder, and hasToolHistory"
```

---

### Task 5: Integrate three-tier decision flow into ReAct loop

**Files:**
- Modify: `internal/agent/session.go:262-293` (nudge decision block in runReActLoop)

**Step 1: Write integration test for stuck escalation**

Add to `internal/agent/agent_integration_test.go` a test that simulates:
1. LLM returns short text (ambiguous, no tool call)
2. Classifier is nil → falls back to heuristic → accepts response
This confirms backward compatibility when classifier is disabled.

```go
func TestIntegration_NudgeWithoutClassifier(t *testing.T) {
	// Set up session with classifierLLM=nil.
	// Provide a stub LLM that returns a short response like "OK" (no tool calls).
	// Verify: response is accepted (no crash, no infinite loop).
}
```

**Step 2: Update the ReAct loop nudge block**

Replace the nudge decision block in `internal/agent/session.go` (lines 279-293):

```go
		// No tool calls — decide how to handle the response.
		// Phase 1: heuristic detection of obvious plans.
		if looksLikePlan(resp.Content) {
			if nudges < maxNudges {
				s.ephemeralMessages = append(s.ephemeralMessages,
					llm.Message{Role: llm.RoleAssistant, Content: resp.Content},
					llm.Message{Role: llm.RoleUser, Content: nudgeMessage(resp.Content)},
				)
				nudges++
				s.logger.Debug("nudging LLM to use tools (heuristic)",
					zap.Int("nudge", nudges), zap.Int("iter", iter))
				continue
			}
			// Nudge budget exhausted — fall through to accept.
		}

		// Phase 2: if this is a second+ nudge attempt with no tool call, assume stuck.
		if nudges >= 1 && s.lastTaskSummary != "" {
			s.ephemeralMessages = append(s.ephemeralMessages,
				llm.Message{Role: llm.RoleAssistant, Content: resp.Content},
				llm.Message{Role: llm.RoleUser, Content: taskReminderMessage(s.lastTaskSummary, resp.Content)},
			)
			nudges++
			s.logger.Debug("injecting task reminder (stuck escalation)",
				zap.Int("nudge", nudges), zap.Int("iter", iter),
				zap.String("task_summary", s.lastTaskSummary))
			if nudges < maxNudges+1 { // one extra attempt for task reminder
				continue
			}
			// Give up — fall through to accept.
		}

		// Phase 3: classifier for ambiguous short responses.
		if nudges == 0 && s.classifierLLM != nil && isAmbiguousResponse(resp.Content, s.tape) {
			lastUserMsg := s.lastUserMessage()
			toolNames := s.tools.Names()
			decision, taskSummary, ok := classifyResponse(
				ctx, s.classifierLLM, s.config.ClassifierModel,
				lastUserMsg, resp.Content, toolNames,
			)
			if ok {
				s.logger.Debug("classifier decision",
					zap.String("decision", decision),
					zap.String("task_summary", taskSummary))
				switch decision {
				case "nudge":
					s.ephemeralMessages = append(s.ephemeralMessages,
						llm.Message{Role: llm.RoleAssistant, Content: resp.Content},
						llm.Message{Role: llm.RoleUser, Content: nudgeMessage(resp.Content)},
					)
					nudges++
					continue
				case "stuck":
					s.lastTaskSummary = taskSummary
					s.ephemeralMessages = append(s.ephemeralMessages,
						llm.Message{Role: llm.RoleAssistant, Content: resp.Content},
						llm.Message{Role: llm.RoleUser, Content: taskReminderMessage(taskSummary, resp.Content)},
					)
					nudges++
					continue
				case "accept":
					// Fall through to accept.
				}
			}
			// Classifier failed to parse → fall through to accept.
		}
		lastToolFailed = false

		// Final answer — no tool calls.
		content := s.processAssistantResponse(ctx, resp)
		return content, nil
```

**Step 3: Add lastTaskSummary field and lastUserMessage helper**

In Session struct, add:
```go
	lastTaskSummary string // task summary from classifier, used for stuck escalation
```

Add helper method:
```go
// lastUserMessage returns the most recent user message text from the tape.
func (s *Session) lastUserMessage() string {
	entries, err := s.tape.Entries()
	if err != nil {
		return ""
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind != tape.KindMessage {
			continue
		}
		var msg struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if json.Unmarshal(entries[i].Payload, &msg) == nil && msg.Role == "user" {
			return msg.Content
		}
	}
	return ""
}
```

Reset `lastTaskSummary` at the top of `runReActLoop` (alongside the existing `nudges = 0` reset):
```go
	s.lastTaskSummary = ""
```

Also update `maxNudges` comment to note that stuck escalation gets one extra attempt:
```go
const (
	// max auto-nudges per user turn before accepting the response.
	// Stuck escalation (task reminder) gets one additional attempt beyond this.
	maxNudges = 2
)
```

**Step 4: Run all tests**

Run: `go vet ./... && go test ./internal/agent/ -v -count=1 -timeout 120s`
Expected: All pass.

**Step 5: Format and commit**

```bash
gofmt -w internal/agent/session.go
git add internal/agent/session.go
git -c commit.gpgsign=false commit -m "Integrate three-tier nudge decision flow with classifier and stuck escalation"
```

---

### Task 6: Update design documentation

**Files:**
- Modify: `design/02-agent-session.md` (nudge section)

**Step 1: Update the nudge section**

Find the section describing the nudge system and update it to document:
- Three-tier decision flow (heuristic → classifier → stuck escalation)
- `GOLEM_CLASSIFIER_MODEL` config
- The `ambiguousMaxLen = 100` gate
- Two-phase escalation: nudge → task reminder → give up

**Step 2: Commit**

```bash
git add design/02-agent-session.md
git -c commit.gpgsign=false commit -m "Update design doc to reflect hybrid nudge system"
```

---

### Task 7: End-to-end manual verification

**Step 1: Set up test config**

```bash
echo 'GOLEM_CLASSIFIER_MODEL=openai:gpt-4o-mini' >> ~/.golem/config.env
```

**Step 2: Run the agent and test these scenarios**

1. **Clear plan (heuristic)**: Send a message that triggers a tool call. Verify the LLM plans → gets nudged → calls tool.
2. **Ambiguous short response**: Send "继续" after a tool-using conversation. Verify the classifier is called (check debug logs for "classifier decision").
3. **Stuck loop**: Simulate the stuck pattern — send repeated "继续". Verify task reminder is injected instead of generic nudge on the second attempt.
4. **No classifier configured**: Remove `GOLEM_CLASSIFIER_MODEL`. Verify backward-compatible heuristic-only behavior.

**Step 3: Check logs**

Look for these log lines:
- `"nudging LLM to use tools (heuristic)"` — Phase 1
- `"classifier decision"` with decision field — Phase 3
- `"injecting task reminder (stuck escalation)"` — Phase 2
