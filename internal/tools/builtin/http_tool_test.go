package builtin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPRequest_GetJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok","count":42}`)
	}))
	defer srv.Close()

	tool := NewHTTPRequestTool(&http.Client{})
	result, err := tool.Execute(context.Background(), fmt.Sprintf(`{"url":"%s"}`, srv.URL))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "200") {
		t.Error("expected status 200 in output")
	}
	if !strings.Contains(result, `"status":"ok"`) {
		t.Error("expected JSON body in output")
	}
	if !strings.Contains(result, "Content-Type: application/json") {
		t.Error("expected Content-Type header in output")
	}
}

func TestHTTPRequest_PostWithBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type header, got %s", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("X-Custom") != "test-value" {
			t.Errorf("expected X-Custom header, got %s", r.Header.Get("X-Custom"))
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":1}`)
	}))
	defer srv.Close()

	tool := NewHTTPRequestTool(&http.Client{})
	args := fmt.Sprintf(`{
		"url": "%s",
		"method": "POST",
		"headers": {"Content-Type": "application/json", "X-Custom": "test-value"},
		"body": "{\"name\":\"test\"}"
	}`, srv.URL)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "201") {
		t.Error("expected status 201 in output")
	}
	if !strings.Contains(result, `{"id":1}`) {
		t.Error("expected response body in output")
	}
}

func TestHTTPRequest_Delete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	tool := NewHTTPRequestTool(&http.Client{})
	result, err := tool.Execute(context.Background(), fmt.Sprintf(`{"url":"%s","method":"delete"}`, srv.URL))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "204") {
		t.Error("expected status 204 in output")
	}
}

func TestHTTPRequest_MaxLength(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, strings.Repeat("x", 1000))
	}))
	defer srv.Close()

	tool := NewHTTPRequestTool(&http.Client{})
	result, err := tool.Execute(context.Background(), fmt.Sprintf(`{"url":"%s","max_length":100}`, srv.URL))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "[Response truncated]") {
		t.Error("expected truncation marker")
	}
}

func TestHTTPRequest_MissingURL(t *testing.T) {
	tool := NewHTTPRequestTool(&http.Client{})
	result, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Error") || !strings.Contains(result, "url") {
		t.Errorf("expected error about missing url, got: %s", result)
	}
}

func TestHTTPRequest_BadScheme(t *testing.T) {
	tool := NewHTTPRequestTool(&http.Client{})
	result, err := tool.Execute(context.Background(), `{"url":"ftp://example.com"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "only http and https") {
		t.Errorf("expected scheme error, got: %s", result)
	}
}

func TestHTTPRequest_UnsupportedMethod(t *testing.T) {
	tool := NewHTTPRequestTool(&http.Client{})
	result, err := tool.Execute(context.Background(), `{"url":"https://example.com","method":"TRACE"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "unsupported HTTP method") {
		t.Errorf("expected method error, got: %s", result)
	}
}

func TestHTTPRequest_InvalidJSON(t *testing.T) {
	tool := NewHTTPRequestTool(&http.Client{})
	result, err := tool.Execute(context.Background(), `not json`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Error") {
		t.Errorf("expected error for invalid JSON, got: %s", result)
	}
}

func TestHTTPRequest_Non200Status(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":"internal"}`)
	}))
	defer srv.Close()

	tool := NewHTTPRequestTool(&http.Client{})
	result, err := tool.Execute(context.Background(), fmt.Sprintf(`{"url":"%s"}`, srv.URL))
	if err != nil {
		t.Fatal(err)
	}

	// Non-200 should still return the body, not an error string.
	if !strings.Contains(result, "500") {
		t.Error("expected status 500 in output")
	}
	if !strings.Contains(result, `"error":"internal"`) {
		t.Error("expected error body in output")
	}
}

func TestHTTPRequest_Head(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "HEAD" {
			t.Errorf("expected HEAD, got %s", r.Method)
		}
		w.Header().Set("X-Test", "hello")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tool := NewHTTPRequestTool(&http.Client{})
	result, err := tool.Execute(context.Background(), fmt.Sprintf(`{"url":"%s","method":"HEAD"}`, srv.URL))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "200") {
		t.Error("expected status 200")
	}
	if !strings.Contains(result, "X-Test: hello") {
		t.Error("expected X-Test header in output")
	}
}
