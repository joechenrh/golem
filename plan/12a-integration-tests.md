# Step 12a: Integration Tests

## Scope

End-to-end tests that wire real components together with a mock LLM server. Verify the full request flow: user input → agent loop → LLM call → tool execution → tape persistence → response.

## File

`internal/agent/agent_integration_test.go`

## Key Points

### Test Infrastructure

```go
// testHarness wires all components with a mock LLM server.
type testHarness struct {
    agent    *AgentLoop
    tape     *tape.FileStore
    registry *tools.Registry
    llmSrv   *httptest.Server
    // controls what the mock LLM responds with
    responses []ChatResponse
}

func newTestHarness(t *testing.T) *testHarness
```

The mock LLM server (`httptest.Server`) returns pre-configured responses in order. This lets tests script multi-turn conversations including tool calls.

### Test Cases

#### 1. Simple text Q&A

```
User: "What is 2+2?"
Mock LLM: responds "4"
Verify: tape has user + assistant entries, response = "4"
```

#### 2. Single tool call

```
User: "Read the go.mod file"
Mock LLM: responds with tool_call{name: "read_file", args: {"path": "go.mod"}}
Tool: read_file executes, returns file content
Mock LLM: responds with final text including file content
Verify: tape has user + assistant(tool_call) + tool_result + assistant entries
```

#### 3. Multi-step tool calls

```
User: "List files then read main.go"
Mock LLM: tool_call{list_directory}
Tool: returns file listing
Mock LLM: tool_call{read_file, "main.go"}
Tool: returns file content
Mock LLM: final text answer
Verify: tape has correct sequence, all tool results recorded
```

#### 4. Tool call iteration limit

```
User: "Do something"
Mock LLM: returns tool_call on every response (infinite loop)
Verify: agent stops after MaxToolIter, returns limit message
```

#### 5. Comma command bypass

```
User: ",help"
Verify: no LLM call made, response contains command list
```

```
User: ",tape.info"
Verify: response contains tape statistics
```

#### 6. Context strategy integration

```
Add entries + anchor to tape
User: "Continue"
Verify: only messages after anchor are sent to LLM (inspect mock server's received request)
```

#### 7. Streaming mode

```
User: "Hello"
Mock LLM: streams tokens via SSE
Verify: tokenCh receives individual tokens, final content matches
```

#### 8. LLM error handling

```
Mock LLM: returns 500 (retried), then 401 (not retried)
Verify: appropriate error returned, tape records the attempt
```

### Mock LLM Server Design

```go
type mockLLMServer struct {
    t         *testing.T
    responses []mockResponse
    calls     []ChatRequest  // records all received requests for assertions
    callIdx   int
}

type mockResponse struct {
    response *ChatResponse  // for non-streaming
    stream   []StreamEvent  // for streaming
    status   int            // HTTP status (default 200)
}
```

The mock server:
- Records all incoming requests for later assertion (verify correct messages/tools sent)
- Returns responses in order (callIdx increments per call)
- Supports both JSON and SSE streaming responses
- Can simulate errors by setting non-200 status

### Build Tag

Integration tests use a build tag so they can be run separately:

```go
//go:build integration
```

Run with: `go test -tags=integration ./internal/agent/`

Or add a Makefile target:

```makefile
test-integration:
	go test -tags=integration ./internal/agent/
```

## Design Decisions

- Tests use real components (tape, tools, registry, context strategy) with only the LLM mocked — this catches integration issues that unit tests miss
- Mock server records requests so tests can assert on what was sent to the LLM (e.g., verify context strategy filtered old messages)
- Build tag keeps integration tests separate from fast unit tests in `go test ./...`
- No external dependencies needed — `httptest.Server` provides the mock

## Done When

- `go test -tags=integration ./internal/agent/` passes
- All 8 test scenarios above are covered
- Mock server correctly handles both streaming and non-streaming
- Tests verify tape contents after each interaction
- Tests verify correct messages are sent to the LLM
