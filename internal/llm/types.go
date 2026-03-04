package llm

import "encoding/json"

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
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// ToolCall represents a tool invocation from the model.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // raw JSON string
}

// ToolDefinition describes a tool the model can call.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // raw JSON Schema
}

// ChatRequest holds the input to an LLM call.
type ChatRequest struct {
	Model        string           `json:"model"`
	Messages     []Message        `json:"messages"`
	Tools        []ToolDefinition `json:"tools,omitempty"`
	MaxTokens    int              `json:"max_tokens,omitempty"`
	Temperature  float64          `json:"temperature,omitempty"`
	SystemPrompt string           `json:"system_prompt,omitempty"` // separate field; Anthropic requires top-level system
}

// ChatResponse holds a complete non-streaming response.
type ChatResponse struct {
	Content      string     `json:"content"`
	ToolCalls    []ToolCall `json:"tool_calls,omitempty"`
	Usage        Usage      `json:"usage"`
	FinishReason string     `json:"finish_reason"`
}

// Usage tracks token consumption.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
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
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"` // partial JSON fragment
}
