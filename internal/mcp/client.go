// Package mcp implements a Model Context Protocol (MCP) client using
// stdio transport (JSON-RPC 2.0 over stdin/stdout).
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/joechenrh/golem/internal/llm"
)

// ServerManifest describes an MCP server loaded from a *.mcp.json file.
type ServerManifest struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// Client communicates with a single MCP server subprocess.
type Client struct {
	command string
	args    []string
	env     map[string]string
	logger  *zap.Logger

	mu     sync.Mutex
	proc   *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Scanner
	nextID atomic.Int64
}

// jsonRPCRequest is a JSON-RPC 2.0 request.
type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"` // int64 for requests, omitted for notifications
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// jsonRPCResponse is a JSON-RPC 2.0 response.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MCP protocol types.

type mcpToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

type mcpToolsListResult struct {
	Tools []mcpToolSchema `json:"tools"`
}

type mcpCallToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type mcpCallToolResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// Dial launches an MCP server subprocess and performs the initialize handshake.
func Dial(command string, args []string, env map[string]string, logger *zap.Logger) (*Client, error) {
	c := &Client{
		command: command,
		args:    args,
		env:     env,
		logger:  logger,
	}

	if err := c.start(); err != nil {
		return nil, fmt.Errorf("mcp: start %q: %w", command, err)
	}

	if err := c.initialize(); err != nil {
		c.Close()
		return nil, fmt.Errorf("mcp: initialize %q: %w", command, err)
	}

	return c, nil
}

func (c *Client) start() error {
	cmd := exec.Command(c.command, c.args...)
	if len(c.env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range c.env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return err
	}

	if err := cmd.Start(); err != nil {
		stdin.Close()
		return err
	}

	// Drain stderr in background.
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			c.logger.Debug("mcp server stderr",
				zap.String("command", c.command),
				zap.String("line", scanner.Text()))
		}
	}()

	c.proc = cmd
	c.stdin = stdin
	c.reader = bufio.NewScanner(stdout)
	c.reader.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	return nil
}

func (c *Client) initialize() error {
	// Send initialize request.
	initResult, err := c.call("initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]string{
			"name":    "golem",
			"version": "1.0.0",
		},
	})
	if err != nil {
		return fmt.Errorf("initialize request: %w", err)
	}

	c.logger.Debug("mcp server initialized",
		zap.String("command", c.command),
		zap.String("result", string(initResult)))

	// Send initialized notification (no id, no response expected).
	return c.notify("notifications/initialized", nil)
}

// ListTools calls tools/list and returns tool definitions.
func (c *Client) ListTools() ([]llm.ToolDefinition, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	result, err := c.call("tools/list", nil)
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}

	var listResult mcpToolsListResult
	if err := json.Unmarshal(result, &listResult); err != nil {
		return nil, fmt.Errorf("tools/list: unmarshal: %w", err)
	}

	defs := make([]llm.ToolDefinition, len(listResult.Tools))
	for i, t := range listResult.Tools {
		params := t.InputSchema
		if params == nil {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		defs[i] = llm.ToolDefinition{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  params,
		}
	}
	return defs, nil
}

// CallTool invokes a tool on the MCP server.
func (c *Client) CallTool(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	result, err := c.call("tools/call", mcpCallToolParams{
		Name:      name,
		Arguments: arguments,
	})
	if err != nil {
		return "", fmt.Errorf("tools/call %q: %w", name, err)
	}

	var callResult mcpCallToolResult
	if err := json.Unmarshal(result, &callResult); err != nil {
		return "", fmt.Errorf("tools/call %q: unmarshal: %w", name, err)
	}

	// Extract text content.
	var text string
	for _, c := range callResult.Content {
		if c.Type == "text" && c.Text != "" {
			if text != "" {
				text += "\n"
			}
			text += c.Text
		}
	}

	if callResult.IsError {
		return "", fmt.Errorf("%s", text)
	}
	return text, nil
}

// Close shuts down the MCP server subprocess.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.stdin != nil {
		c.stdin.Close()
	}
	if c.proc != nil {
		// Give the process a moment to exit cleanly.
		done := make(chan struct{})
		go func() {
			c.proc.Wait()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(3 * time.Second):
			c.proc.Process.Kill()
			<-done
		}

		c.logger.Debug("mcp server stopped", zap.String("command", c.command))
	}
	c.proc = nil
	c.stdin = nil
	c.reader = nil
}

// call sends a JSON-RPC request and waits for a response.
// Must be called with c.mu held (except during initialize which holds no external lock).
func (c *Client) call(method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	data = append(data, '\n')

	if _, err := c.stdin.Write(data); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}

	if !c.reader.Scan() {
		if err := c.reader.Err(); err != nil {
			return nil, fmt.Errorf("read: %w", err)
		}
		return nil, fmt.Errorf("server closed connection")
	}

	var resp jsonRPCResponse
	if err := json.Unmarshal(c.reader.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("server error %d: %s", resp.Error.Code, resp.Error.Message)
	}

	return resp.Result, nil
}

// notify sends a JSON-RPC notification (no ID, no response expected).
func (c *Client) notify(method string, params any) error {
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}
	data = append(data, '\n')

	if _, err := c.stdin.Write(data); err != nil {
		return fmt.Errorf("write notification: %w", err)
	}
	return nil
}
