package memory

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var testColumns = []sqlColumn{
	{Name: "id"}, {Name: "content"}, {Name: "key_name"}, {Name: "source"},
	{Name: "tags"}, {Name: "version"}, {Name: "updated_by"}, {Name: "created_at"}, {Name: "updated_at"},
}

// mockTiDB creates a test server that mimics the TiDB HTTP Data API.
// handler receives the SQL query and returns the response. Init DDL queries
// (CREATE TABLE, ALTER TABLE, fts probe) are auto-handled with empty success.
func mockTiDB(t *testing.T, handler func(query string) (any, int)) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		user, pass, ok := r.BasicAuth()
		if !ok || user != "testuser" || pass != "testpass" {
			t.Errorf("expected basic auth testuser:testpass, got %s:%s (ok=%v)", user, pass, ok)
		}

		body, _ := io.ReadAll(r.Body)
		var req struct {
			Database string `json:"database"`
			Query    string `json:"query"`
		}
		json.Unmarshal(body, &req)

		// Auto-handle init DDL queries.
		if isInitQuery(req.Query) {
			json.NewEncoder(w).Encode(sqlResponse{})
			return
		}

		resp, status := handler(req.Query)
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(resp)
	}))
}

func isInitQuery(q string) bool {
	return strings.HasPrefix(q, "CREATE TABLE") ||
		strings.HasPrefix(q, "ALTER TABLE") ||
		strings.HasPrefix(q, "SELECT fts_match_word('probe'")
}

func newTestClient(srv *httptest.Server) *Client {
	return NewClientForTest(srv.Client(), srv.URL, "testuser", "testpass", "mnemos")
}

func TestStore(t *testing.T) {
	srv := mockTiDB(t, func(query string) (any, int) {
		if !strings.Contains(query, "INSERT INTO") {
			t.Errorf("expected INSERT, got: %s", query)
		}
		if !strings.Contains(query, "hello world") {
			t.Errorf("expected content in query")
		}
		if !strings.Contains(query, "greeting") {
			t.Errorf("expected key in query")
		}
		if !strings.Contains(query, "golem") {
			t.Errorf("expected source in query")
		}
		return sqlResponse{}, http.StatusOK
	})
	defer srv.Close()

	c := newTestClient(srv)
	mem, err := c.Store(context.Background(), "hello world", "greeting", "golem", []string{"test"})
	if err != nil {
		t.Fatal(err)
	}
	if mem.ID == "" {
		t.Error("expected non-empty ID")
	}
	if mem.Content != "hello world" {
		t.Errorf("expected content 'hello world', got %s", mem.Content)
	}
	if mem.Key != "greeting" {
		t.Errorf("expected key 'greeting', got %s", mem.Key)
	}
	if mem.Source != "golem" {
		t.Errorf("expected source 'golem', got %s", mem.Source)
	}
}

func TestStore_NoOptionals(t *testing.T) {
	srv := mockTiDB(t, func(query string) (any, int) {
		if !strings.Contains(query, "INSERT INTO") {
			t.Errorf("expected INSERT, got: %s", query)
		}
		return sqlResponse{}, http.StatusOK
	})
	defer srv.Close()

	c := newTestClient(srv)
	mem, err := c.Store(context.Background(), "just content", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if mem.ID == "" {
		t.Error("expected non-empty ID")
	}
}

func TestSearch_LIKE(t *testing.T) {
	srv := mockTiDB(t, func(query string) (any, int) {
		// Client has no autoEmbedModel and FTS probe succeeds (empty response = ok),
		// so ftsAvailable=true and it will use FTS. But since our mock returns success
		// for init, it thinks FTS is available. Let's just accept any search query.
		return sqlResponse{
			Types: testColumns,
			Rows: [][]any{
				{"mem-1", "first result", nil, "golem", `["tag1"]`, float64(1), nil, "2024-01-01", "2024-01-01"},
				{"mem-2", "second result", "k2", nil, nil, float64(1), nil, "2024-01-01", "2024-01-01"},
			},
		}, http.StatusOK
	})
	defer srv.Close()

	c := newTestClient(srv)
	results, err := c.Search(context.Background(), "test query", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ID != "mem-1" {
		t.Errorf("expected first result id mem-1, got %s", results[0].ID)
	}
	if len(results[0].Tags) != 1 || results[0].Tags[0] != "tag1" {
		t.Errorf("expected tags [tag1], got %v", results[0].Tags)
	}
}

func TestSearch_EmptyResults(t *testing.T) {
	srv := mockTiDB(t, func(query string) (any, int) {
		return sqlResponse{Types: testColumns, Rows: [][]any{}}, http.StatusOK
	})
	defer srv.Close()

	c := newTestClient(srv)
	results, err := c.Search(context.Background(), "nonexistent", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestStore_ServerError(t *testing.T) {
	srv := mockTiDB(t, func(query string) (any, int) {
		return map[string]string{"error": "internal error"}, http.StatusInternalServerError
	})
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.Store(context.Background(), "content", "", "", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected status 500 in error, got: %s", err.Error())
	}
}

func TestSQLEscape(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"hello", "hello"},
		{"it's", "it''s"},
		{"a''b", "a''''b"},
		{"back\\slash", "back\\\\slash"},
		{"null\x00byte", "nullbyte"},
		{"new\nline", "new\\nline"},
	}
	for _, tt := range tests {
		got := sqlEscape(tt.input)
		if got != tt.want {
			t.Errorf("sqlEscape(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSQLQ(t *testing.T) {
	if got := sqlQ("hello"); got != "'hello'" {
		t.Errorf("sqlQ(hello) = %s, want 'hello'", got)
	}
	if got := sqlQ("it's"); got != "'it''s'" {
		t.Errorf("sqlQ(it's) = %s, want 'it''s'", got)
	}
}

func TestSQLNullableQ(t *testing.T) {
	if got := sqlNullableQ(""); got != "NULL" {
		t.Errorf("sqlNullableQ('') = %s, want NULL", got)
	}
	if got := sqlNullableQ("val"); got != "'val'" {
		t.Errorf("sqlNullableQ(val) = %s, want 'val'", got)
	}
}

func TestStore_SQLInjection(t *testing.T) {
	var capturedQuery string
	srv := mockTiDB(t, func(query string) (any, int) {
		capturedQuery = query
		return sqlResponse{}, http.StatusOK
	})
	defer srv.Close()

	c := newTestClient(srv)

	// Attempt SQL injection via content.
	_, err := c.Store(context.Background(), "'; DROP TABLE memories; --", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	// The injection attempt must be escaped: single quotes doubled.
	if strings.Contains(capturedQuery, "DROP TABLE") && !strings.Contains(capturedQuery, "''") {
		t.Errorf("SQL injection not escaped in query: %s", capturedQuery)
	}
	// The dangerous single-quote must be doubled.
	if !strings.Contains(capturedQuery, "''") {
		t.Errorf("expected escaped quotes in query: %s", capturedQuery)
	}
}

func TestSearch_SQLInjection(t *testing.T) {
	var capturedQuery string
	srv := mockTiDB(t, func(query string) (any, int) {
		capturedQuery = query
		return sqlResponse{Types: testColumns, Rows: [][]any{}}, http.StatusOK
	})
	defer srv.Close()

	c := newTestClient(srv)

	_, err := c.Search(context.Background(), "'; DROP TABLE memories; --", 5)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(capturedQuery, "DROP TABLE") && !strings.Contains(capturedQuery, "''") {
		t.Errorf("SQL injection not escaped in search query: %s", capturedQuery)
	}
}

func TestHybridSearch_BothLegsFail(t *testing.T) {
	// When both vector and keyword SQL queries fail, Search must return an error.
	srv := mockTiDB(t, func(query string) (any, int) {
		// Both legs return HTTP 500.
		return map[string]string{"error": "internal error"}, http.StatusInternalServerError
	})
	defer srv.Close()

	c := newTestClient(srv)
	c.autoEmbedModel = "test-model" // triggers hybridSearch path

	_, err := c.Search(context.Background(), "test query", 5)
	if err == nil {
		t.Fatal("expected error when both search legs fail, got nil")
	}
	if !strings.Contains(err.Error(), "both search legs failed") {
		t.Errorf("expected 'both search legs failed' in error, got: %s", err.Error())
	}
}

func TestHybridSearch_VectorFailsKeywordSucceeds(t *testing.T) {
	// When only the vector leg fails but keyword succeeds, results are
	// still returned without error.
	srv := mockTiDB(t, func(query string) (any, int) {
		if strings.Contains(query, "VEC_EMBED_COSINE_DISTANCE") {
			// Vector leg fails.
			return map[string]string{"error": "vector error"}, http.StatusInternalServerError
		}
		// Keyword leg succeeds.
		return sqlResponse{
			Types: testColumns,
			Rows: [][]any{
				{"kw-1", "keyword result", nil, "golem", nil, float64(1), nil, "2024-01-01", "2024-01-01"},
			},
		}, http.StatusOK
	})
	defer srv.Close()

	c := newTestClient(srv)
	c.autoEmbedModel = "test-model"

	results, err := c.Search(context.Background(), "test query", 5)
	if err != nil {
		t.Fatalf("expected no error when one leg succeeds, got: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "kw-1" {
		t.Errorf("expected result id kw-1, got %s", results[0].ID)
	}
}

func TestHybridSearch_KeywordFailsVectorSucceeds(t *testing.T) {
	// When only the keyword leg fails but vector succeeds, results are
	// still returned without error.
	callCount := 0
	srv := mockTiDB(t, func(query string) (any, int) {
		callCount++
		if strings.Contains(query, "VEC_EMBED_COSINE_DISTANCE") {
			// Vector leg succeeds.
			return sqlResponse{
				Types: append(testColumns, sqlColumn{Name: "distance"}),
				Rows: [][]any{
					{"vec-1", "vector result", nil, "golem", nil, float64(1), nil, "2024-01-01", "2024-01-01", 0.1},
				},
			}, http.StatusOK
		}
		// Keyword leg fails.
		return map[string]string{"error": "keyword error"}, http.StatusInternalServerError
	})
	defer srv.Close()

	c := newTestClient(srv)
	c.autoEmbedModel = "test-model"

	results, err := c.Search(context.Background(), "test query", 5)
	if err != nil {
		t.Fatalf("expected no error when one leg succeeds, got: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "vec-1" {
		t.Errorf("expected result id vec-1, got %s", results[0].ID)
	}
}

func TestRRFMerge(t *testing.T) {
	vecRows := []Memory{
		{ID: "a", Content: "alpha"},
		{ID: "b", Content: "beta"},
	}
	kwRows := []Memory{
		{ID: "b", Content: "beta"},
		{ID: "c", Content: "gamma"},
	}

	result := rrfMerge(vecRows, kwRows, 10)
	if len(result) != 3 {
		t.Fatalf("expected 3 results, got %d", len(result))
	}
	// "b" appears in both lists so should rank highest.
	if result[0].ID != "b" {
		t.Errorf("expected first result to be 'b' (in both lists), got %s", result[0].ID)
	}
	for _, m := range result {
		if m.Score == nil {
			t.Errorf("expected score for %s", m.ID)
		}
	}
}

func TestRRFMerge_Limit(t *testing.T) {
	rows := []Memory{
		{ID: "a"}, {ID: "b"}, {ID: "c"},
	}
	result := rrfMerge(rows, nil, 2)
	if len(result) != 2 {
		t.Errorf("expected 2 results, got %d", len(result))
	}
}
