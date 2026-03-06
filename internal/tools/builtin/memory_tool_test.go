package builtin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/joechenrh/golem/internal/memory"
)

type tidbResponse struct {
	Types []tidbColumn `json:"types"`
	Rows  [][]any      `json:"rows"`
}

type tidbColumn struct {
	Name string `json:"name"`
}

var memoryColumns = []tidbColumn{
	{Name: "id"}, {Name: "content"}, {Name: "key_name"}, {Name: "source"},
	{Name: "tags"}, {Name: "version"}, {Name: "updated_by"}, {Name: "created_at"}, {Name: "updated_at"},
}

func newTestMemoryClient(handler http.Handler) *memory.Client {
	srv := httptest.NewTLSServer(handler)
	return memory.NewClientForTest(srv.Client(), srv.URL, "testuser", "testpass", "mnemos")
}

func TestMemoryStoreTool_Execute(t *testing.T) {
	client := newTestMemoryClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(tidbResponse{})
	}))

	tool := NewMemoryStoreTool(client)
	result, err := tool.Execute(context.Background(), `{"content":"deploy key on server X","key":"deploy-key","tags":["infra"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Memory stored") {
		t.Errorf("expected 'Memory stored', got: %s", result)
	}
}

func TestMemoryStoreTool_DefaultSource(t *testing.T) {
	var capturedQuery string
	client := newTestMemoryClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query string `json:"query"`
		}
		json.Unmarshal(body, &req)
		capturedQuery = req.Query
		json.NewEncoder(w).Encode(tidbResponse{})
	}))

	tool := NewMemoryStoreTool(client)
	tool.Execute(context.Background(), `{"content":"test"}`)

	if !strings.Contains(capturedQuery, "golem") {
		t.Errorf("expected default source 'golem' in query, got: %s", capturedQuery)
	}
}

func TestMemoryStoreTool_MissingContent(t *testing.T) {
	client := newTestMemoryClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	tool := NewMemoryStoreTool(client)

	result, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "'content' is required") {
		t.Errorf("expected content required error, got: %s", result)
	}
}

func TestMemoryStoreTool_ServerError(t *testing.T) {
	client := newTestMemoryClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "server error")
	}))

	tool := NewMemoryStoreTool(client)
	result, err := tool.Execute(context.Background(), `{"content":"test"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result, "Error:") {
		t.Errorf("expected error result, got: %s", result)
	}
}

func TestMemoryRecallTool_Execute(t *testing.T) {
	client := newTestMemoryClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(tidbResponse{
			Types: memoryColumns,
			Rows: [][]any{
				{"mem-1", "deploy key is on server X", "deploy-key", "golem", `["infra"]`, float64(1), nil, "2024-01-01", "2024-01-01"},
				{"mem-2", "auth flow uses JWT tokens", nil, "claude", nil, float64(1), nil, "2024-01-01", "2024-01-01"},
			},
		})
	}))

	tool := NewMemoryRecallTool(client)
	result, err := tool.Execute(context.Background(), `{"query":"deploy key"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Found 2 memories") {
		t.Errorf("expected 'Found 2 memories', got: %s", result)
	}
	if !strings.Contains(result, "deploy-key") {
		t.Errorf("expected key 'deploy-key' in result, got: %s", result)
	}
	if !strings.Contains(result, "by golem") {
		t.Errorf("expected 'by golem' in result, got: %s", result)
	}
	if !strings.Contains(result, "infra") {
		t.Errorf("expected tag 'infra' in result, got: %s", result)
	}
}

func TestMemoryRecallTool_EmptyResults(t *testing.T) {
	client := newTestMemoryClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(tidbResponse{Types: memoryColumns, Rows: [][]any{}})
	}))

	tool := NewMemoryRecallTool(client)
	result, err := tool.Execute(context.Background(), `{"query":"nothing"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "No memories found") {
		t.Errorf("expected 'No memories found', got: %s", result)
	}
}

func TestMemoryRecallTool_MissingQuery(t *testing.T) {
	client := newTestMemoryClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	tool := NewMemoryRecallTool(client)

	result, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "'query' is required") {
		t.Errorf("expected query required error, got: %s", result)
	}
}
