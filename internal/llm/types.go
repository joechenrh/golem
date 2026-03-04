package llm

// Provider represents a named LLM provider.
type Provider string

const (
	ProviderOpenAI    Provider = "openai"
	ProviderAnthropic Provider = "anthropic"
)

// Role represents a message role in the conversation.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message represents a conversation message.
type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall // assistant messages may contain tool calls
	ToolCallID string     // for tool result messages
	Name       string     // tool name for tool results
}

// ToolCall represents a tool invocation from the model.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // raw JSON string
}

// ToolDefinition describes a tool the model can call.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]interface{} // JSON Schema object
}

// ChatRequest holds the input to an LLM call.
type ChatRequest struct {
	Model        string
	Messages     []Message
	Tools        []ToolDefinition
	MaxTokens    int
	Temperature  float64
	SystemPrompt string // separate field; Anthropic requires top-level system
}

// ChatResponse holds a complete non-streaming response.
type ChatResponse struct {
	Content      string
	ToolCalls    []ToolCall
	Usage        Usage
	FinishReason string // "stop", "tool_calls", "length"
}

// Usage tracks token consumption.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// StreamEventType discriminates streaming events.
type StreamEventType string

const (
	StreamContentDelta  StreamEventType = "content_delta"
	StreamToolCallDelta StreamEventType = "tool_call_delta"
	StreamDone          StreamEventType = "done"
	StreamError         StreamEventType = "error"
)

// StreamEvent represents a single event in a streaming response.
type StreamEvent struct {
	Type     StreamEventType
	Content  string         // for content deltas
	ToolCall *ToolCallDelta // for tool call deltas
	Error    error          // for error events
}

// ToolCallDelta represents a partial tool call during streaming.
type ToolCallDelta struct {
	ID        string
	Name      string
	Arguments string // partial JSON fragment
}
