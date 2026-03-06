package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// ExternalToolManifest describes an external tool plugin loaded from a JSON file.
// The plugin communicates via JSON-RPC 2.0 over stdin/stdout.
type ExternalToolManifest struct {
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	FullDesc       string          `json:"full_description"`
	Parameters     json.RawMessage `json:"parameters"`
	Command        string          `json:"command"`        // path to executable
	Args           []string        `json:"args,omitempty"` // extra args passed to the executable
	WorkDir        string          `json:"work_dir,omitempty"`
	TimeoutSeconds int             `json:"timeout_seconds,omitempty"` // default: 30
}

// ExternalTool wraps an external process as a Tool.
// It lazily starts the process on first Execute and communicates
// via JSON-RPC 2.0 over stdin/stdout.
type ExternalTool struct {
	manifest ExternalToolManifest

	mu     sync.Mutex
	proc   *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner
	nextID atomic.Int64
}

// jsonRPCRequest is a JSON-RPC 2.0 request.
type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

// jsonRPCResponse is a JSON-RPC 2.0 response.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// NewExternalTool creates an ExternalTool from a manifest.
func NewExternalTool(m ExternalToolManifest) *ExternalTool {
	return &ExternalTool{manifest: m}
}

func (t *ExternalTool) Name() string        { return t.manifest.Name }
func (t *ExternalTool) Description() string { return t.manifest.Description }
func (t *ExternalTool) FullDescription() string {
	if t.manifest.FullDesc != "" {
		return t.manifest.FullDesc
	}
	return t.manifest.Description
}
func (t *ExternalTool) Parameters() json.RawMessage { return t.manifest.Parameters }

func (t *ExternalTool) Execute(ctx context.Context, args string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.ensureRunning(); err != nil {
		return "", fmt.Errorf("external tool %q: start process: %w", t.manifest.Name, err)
	}

	id := t.nextID.Add(1)
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "execute",
		Params:  json.RawMessage(args),
	}

	data, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("external tool %q: marshal request: %w", t.manifest.Name, err)
	}
	data = append(data, '\n')

	if _, err := t.stdin.Write(data); err != nil {
		// Process may have died; restart on next call.
		t.cleanup()
		return "", fmt.Errorf("external tool %q: write request: %w", t.manifest.Name, err)
	}

	if !t.stdout.Scan() {
		err := t.stdout.Err()
		t.cleanup()
		if err != nil {
			return "", fmt.Errorf("external tool %q: read response: %w", t.manifest.Name, err)
		}
		return "", fmt.Errorf("external tool %q: process exited unexpectedly", t.manifest.Name)
	}

	var resp jsonRPCResponse
	if err := json.Unmarshal(t.stdout.Bytes(), &resp); err != nil {
		return "", fmt.Errorf("external tool %q: unmarshal response: %w", t.manifest.Name, err)
	}

	if resp.Error != nil {
		return fmt.Sprintf("Error: %s (code %d)", resp.Error.Message, resp.Error.Code), nil
	}

	// Result is a JSON string — unwrap if quoted, otherwise return raw.
	var result string
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return string(resp.Result), nil
	}
	return result, nil
}

// ensureRunning starts the external process if not already running.
func (t *ExternalTool) ensureRunning() error {
	if t.proc != nil {
		return nil
	}

	cmd := exec.Command(t.manifest.Command, t.manifest.Args...)
	if t.manifest.WorkDir != "" {
		cmd.Dir = t.manifest.WorkDir
	}
	cmd.Stderr = os.Stderr // let plugin errors surface

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

	t.proc = cmd
	t.stdin = stdin
	t.stdout = bufio.NewScanner(stdout)
	t.stdout.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	return nil
}

// cleanup stops the external process.
func (t *ExternalTool) cleanup() {
	if t.stdin != nil {
		t.stdin.Close()
	}
	if t.proc != nil {
		t.proc.Process.Kill()
		t.proc.Wait()
	}
	t.proc = nil
	t.stdin = nil
	t.stdout = nil
}

// Close stops the external process. Safe to call multiple times.
func (t *ExternalTool) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cleanup()
}

// LoadExternalTools reads all *.tool.json files from the given directory
// and returns the corresponding ExternalTool instances.
func LoadExternalTools(dir string) ([]*ExternalTool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading plugin dir: %w", err)
	}

	var tools []*ExternalTool
	for _, entry := range entries {
		if entry.IsDir() || !isToolManifest(entry.Name()) {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}

		var manifest ExternalToolManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}

		if manifest.Name == "" || manifest.Command == "" {
			return nil, fmt.Errorf("%s: name and command are required", path)
		}
		if manifest.Parameters == nil {
			manifest.Parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}

		tools = append(tools, NewExternalTool(manifest))
	}

	return tools, nil
}

func isToolManifest(name string) bool {
	return len(name) > len(".tool.json") &&
		name[len(name)-len(".tool.json"):] == ".tool.json"
}
