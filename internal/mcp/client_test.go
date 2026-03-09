package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"testing"

	"go.uber.org/zap"
)

// mockMCPServer simulates an MCP server over a net.Conn pair.
func mockMCPServer(t *testing.T, conn io.ReadWriteCloser) {
	t.Helper()
	decoder := json.NewDecoder(conn)
	encoder := json.NewEncoder(conn)

	for {
		var req jsonRPCRequest
		if err := decoder.Decode(&req); err != nil {
			return
		}

		switch req.Method {
		case "initialize":
			encoder.Encode(jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: json.RawMessage(`{
					"protocolVersion": "2024-11-05",
					"capabilities": {"tools": {}},
					"serverInfo": {"name": "test-server", "version": "1.0.0"}
				}`),
			})

		case "notifications/initialized":
			// Notification — no response.

		case "tools/list":
			encoder.Encode(jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: json.RawMessage(`{
					"tools": [
						{
							"name": "echo",
							"description": "Echo back the input",
							"inputSchema": {
								"type": "object",
								"properties": {
									"message": {"type": "string"}
								},
								"required": ["message"]
							}
						},
						{
							"name": "add",
							"description": "Add two numbers",
							"inputSchema": {
								"type": "object",
								"properties": {
									"a": {"type": "number"},
									"b": {"type": "number"}
								}
							}
						}
					]
				}`),
			})

		case "tools/call":
			paramsBytes, _ := json.Marshal(req.Params)
			var params mcpCallToolParams
			json.Unmarshal(paramsBytes, &params)

			switch params.Name {
			case "echo":
				var args struct {
					Message string `json:"message"`
				}
				json.Unmarshal(params.Arguments, &args)
				encoder.Encode(jsonRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  json.RawMessage(`{"content": [{"type": "text", "text": "echo: ` + args.Message + `"}]}`),
				})
			case "fail":
				encoder.Encode(jsonRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Result:  json.RawMessage(`{"content": [{"type": "text", "text": "something went wrong"}], "isError": true}`),
				})
			default:
				encoder.Encode(jsonRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error:   &jsonRPCError{Code: -32601, Message: "unknown tool"},
				})
			}
		}
	}
}

// newTestClient creates a Client connected to a mock MCP server via in-process pipes.
func newTestClient(t *testing.T) *Client {
	t.Helper()

	serverConn, clientConn := net.Pipe()

	go mockMCPServer(t, serverConn)

	c := &Client{
		command: "test",
		logger:  zap.NewNop(),
		stdin:   clientConn,
	}

	scanner := bufio.NewScanner(clientConn)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	c.reader = scanner

	if err := c.initialize(); err != nil {
		serverConn.Close()
		clientConn.Close()
		t.Fatalf("initialize: %v", err)
	}

	t.Cleanup(func() {
		serverConn.Close()
		clientConn.Close()
	})

	return c
}

func TestListTools(t *testing.T) {
	c := newTestClient(t)

	tools, err := c.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}

	if tools[0].Name != "echo" {
		t.Errorf("tool[0].Name = %q, want echo", tools[0].Name)
	}
	if tools[1].Name != "add" {
		t.Errorf("tool[1].Name = %q, want add", tools[1].Name)
	}
	if tools[0].Description != "Echo back the input" {
		t.Errorf("tool[0].Description = %q", tools[0].Description)
	}
}

func TestCallTool(t *testing.T) {
	c := newTestClient(t)

	result, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{"message": "hello"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	if result != "echo: hello" {
		t.Errorf("result = %q, want %q", result, "echo: hello")
	}
}

func TestCallToolError(t *testing.T) {
	c := newTestClient(t)

	_, err := c.CallTool(context.Background(), "fail", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error from isError=true response")
	}
	if err.Error() != "something went wrong" {
		t.Errorf("error = %q", err.Error())
	}
}
