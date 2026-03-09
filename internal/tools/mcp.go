package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"go.uber.org/zap"

	"github.com/joechenrh/golem/internal/mcp"
)

// MCPTool wraps a single tool from an MCP server as a tools.Tool.
// All tools from one MCP server share the same mcp.Client (one subprocess).
type MCPTool struct {
	name        string
	description string
	parameters  json.RawMessage
	client      *mcp.Client
}

func (t *MCPTool) Name() string              { return t.name }
func (t *MCPTool) Description() string        { return t.description }
func (t *MCPTool) FullDescription() string    { return t.description }
func (t *MCPTool) Parameters() json.RawMessage { return t.parameters }

func (t *MCPTool) Execute(ctx context.Context, args string) (string, error) {
	result, err := t.client.CallTool(ctx, t.name, json.RawMessage(args))
	if err != nil {
		return fmt.Sprintf("Error: %s", err), nil
	}
	return result, nil
}

// LoadMCPServers reads all *.mcp.json files from the given directory,
// connects to each MCP server, discovers tools, and returns MCPTool instances.
// Each manifest file represents one MCP server that may expose many tools.
func LoadMCPServers(dir string, logger *zap.Logger) ([]Tool, []*mcp.Client, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("reading MCP plugin dir: %w", err)
	}

	var allTools []Tool
	var clients []*mcp.Client

	for _, entry := range entries {
		if entry.IsDir() || !isMCPManifest(entry.Name()) {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			logger.Warn("failed to read MCP manifest", zap.String("path", path), zap.Error(err))
			continue
		}

		var manifest mcp.ServerManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			logger.Warn("failed to parse MCP manifest", zap.String("path", path), zap.Error(err))
			continue
		}

		if manifest.Command == "" {
			logger.Warn("MCP manifest missing command", zap.String("path", path))
			continue
		}

		// Expand environment variables in command and args.
		manifest.Command = os.ExpandEnv(manifest.Command)
		for i, arg := range manifest.Args {
			manifest.Args[i] = os.ExpandEnv(arg)
		}
		for k, v := range manifest.Env {
			manifest.Env[k] = os.ExpandEnv(v)
		}

		client, err := mcp.Dial(manifest.Command, manifest.Args, manifest.Env, logger)
		if err != nil {
			logger.Warn("failed to connect to MCP server",
				zap.String("path", path),
				zap.String("command", manifest.Command),
				zap.Error(err))
			continue
		}
		clients = append(clients, client)

		toolDefs, err := client.ListTools()
		if err != nil {
			logger.Warn("failed to list MCP tools",
				zap.String("path", path),
				zap.Error(err))
			client.Close()
			continue
		}

		for _, td := range toolDefs {
			allTools = append(allTools, &MCPTool{
				name:        td.Name,
				description: td.Description,
				parameters:  td.Parameters,
				client:      client,
			})
		}

		logger.Info("loaded MCP server",
			zap.String("command", manifest.Command),
			zap.Int("tools", len(toolDefs)))
	}

	return allTools, clients, nil
}

func isMCPManifest(name string) bool {
	return len(name) > len(".mcp.json") &&
		name[len(name)-len(".mcp.json"):] == ".mcp.json"
}
