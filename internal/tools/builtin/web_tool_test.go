package builtin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// WebSearchTool tests
// ---------------------------------------------------------------------------

// cannedDDGLiteHTML mimics the DuckDuckGo Lite result table format.
const cannedDDGLiteHTML = `<html><body>
<table>
<tr><td><a class="result-link" href="https://example.com/one">First Result</a></td></tr>
<tr><td class="result-snippet">This is the first snippet.</td></tr>
<tr><td><a class="result-link" href="https://example.com/two">Second Result</a></td></tr>
<tr><td class="result-snippet">This is the second snippet.</td></tr>
<tr><td><a class="result-link" href="https://example.com/three">Third Result</a></td></tr>
<tr><td class="result-snippet">This is the third snippet.</td></tr>
</table>
</body></html>`

func newTestSearchTool(serverURL string) *WebSearchTool {
	return &WebSearchTool{
		client:    &http.Client{},
		searchURL: serverURL,
	}
}

func TestWebSearch_Basic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, cannedDDGLiteHTML)
	}))
	defer srv.Close()

	tool := newTestSearchTool(srv.URL)
	result, err := tool.Execute(context.Background(), `{"query":"test"}`)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "First Result") {
		t.Error("expected First Result in output")
	}
	if !strings.Contains(result, "https://example.com/one") {
		t.Error("expected URL in output")
	}
	if !strings.Contains(result, "first snippet") {
		t.Error("expected snippet in output")
	}
	if !strings.Contains(result, "Second Result") {
		t.Error("expected Second Result in output")
	}
}

func TestWebSearch_CountLimitsResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, cannedDDGLiteHTML)
	}))
	defer srv.Close()

	tool := newTestSearchTool(srv.URL)
	result, err := tool.Execute(context.Background(), `{"query":"test","count":1}`)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "First Result") {
		t.Error("expected First Result")
	}
	if strings.Contains(result, "Second Result") {
		t.Error("did not expect Second Result with count=1")
	}
}

func TestWebSearch_MissingQuery(t *testing.T) {
	tool := newTestSearchTool("http://unused")
	result, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Error") || !strings.Contains(result, "query") {
		t.Errorf("expected error about missing query, got: %s", result)
	}
}

func TestWebSearch_InvalidJSON(t *testing.T) {
	tool := newTestSearchTool("http://unused")
	result, err := tool.Execute(context.Background(), `not json`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Error") {
		t.Errorf("expected error for invalid JSON, got: %s", result)
	}
}

func TestWebSearch_NoResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body><table></table></body></html>`)
	}))
	defer srv.Close()

	tool := newTestSearchTool(srv.URL)
	result, err := tool.Execute(context.Background(), `{"query":"xyzzy"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "No results found") {
		t.Errorf("expected no results message, got: %s", result)
	}
}

// ---------------------------------------------------------------------------
// WebFetchTool tests
// ---------------------------------------------------------------------------

func TestWebFetch_HTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body>
			<nav>Navigation</nav>
			<script>var x = 1;</script>
			<style>body { color: red; }</style>
			<main><p>Hello World</p><p>Second paragraph</p></main>
			<footer>Footer stuff</footer>
		</body></html>`)
	}))
	defer srv.Close()

	tool := NewWebFetchTool(&http.Client{})
	result, err := tool.Execute(context.Background(), fmt.Sprintf(`{"url":"%s"}`, srv.URL))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "Hello World") {
		t.Error("expected 'Hello World' in output")
	}
	if !strings.Contains(result, "Second paragraph") {
		t.Error("expected 'Second paragraph' in output")
	}
	if strings.Contains(result, "var x = 1") {
		t.Error("script content should be stripped")
	}
	if strings.Contains(result, "Navigation") {
		t.Error("nav content should be stripped")
	}
	if strings.Contains(result, "Footer stuff") {
		t.Error("footer content should be stripped")
	}
	if strings.Contains(result, "color: red") {
		t.Error("style content should be stripped")
	}
}

func TestWebFetch_PlainText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "Plain text content here")
	}))
	defer srv.Close()

	tool := NewWebFetchTool(&http.Client{})
	result, err := tool.Execute(context.Background(), fmt.Sprintf(`{"url":"%s"}`, srv.URL))
	if err != nil {
		t.Fatal(err)
	}

	if result != "Plain text content here" {
		t.Errorf("expected plain text passthrough, got: %s", result)
	}
}

func TestWebFetch_MaxLength(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, strings.Repeat("x", 1000))
	}))
	defer srv.Close()

	tool := NewWebFetchTool(&http.Client{})
	result, err := tool.Execute(context.Background(), fmt.Sprintf(`{"url":"%s","max_length":100}`, srv.URL))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "[Content truncated]") {
		t.Error("expected truncation marker")
	}
	// 100 chars of content + "\n\n[Content truncated]"
	if !strings.HasPrefix(result, strings.Repeat("x", 100)) {
		t.Error("expected 100 x's before truncation")
	}
}

func TestWebFetch_MissingURL(t *testing.T) {
	tool := NewWebFetchTool(&http.Client{})
	result, err := tool.Execute(context.Background(), `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Error") || !strings.Contains(result, "url") {
		t.Errorf("expected error about missing url, got: %s", result)
	}
}

func TestWebFetch_BadScheme(t *testing.T) {
	tool := NewWebFetchTool(&http.Client{})
	result, err := tool.Execute(context.Background(), `{"url":"ftp://example.com/file"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "only http and https") {
		t.Errorf("expected scheme error, got: %s", result)
	}
}

func TestWebFetch_InvalidJSON(t *testing.T) {
	tool := NewWebFetchTool(&http.Client{})
	result, err := tool.Execute(context.Background(), `not json`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Error") {
		t.Errorf("expected error for invalid JSON, got: %s", result)
	}
}

// ---------------------------------------------------------------------------
// extractText / cleanText unit tests
// ---------------------------------------------------------------------------

func TestExtractText_SkipsTags(t *testing.T) {
	input := `<html><body>
		<script>alert('xss')</script>
		<style>.x{color:red}</style>
		<p>Visible text</p>
		<noscript>No JS</noscript>
	</body></html>`

	text := extractText(strings.NewReader(input))
	if !strings.Contains(text, "Visible text") {
		t.Error("expected visible text")
	}
	if strings.Contains(text, "alert") {
		t.Error("script content should be skipped")
	}
	if strings.Contains(text, "color:red") {
		t.Error("style content should be skipped")
	}
	if strings.Contains(text, "No JS") {
		t.Error("noscript content should be skipped")
	}
}

func TestExtractText_BlockElements(t *testing.T) {
	input := `<div>Block1</div><div>Block2</div><p>Para</p>`
	text := extractText(strings.NewReader(input))
	cleaned := cleanText(text)

	// Block elements should produce separate lines.
	if !strings.Contains(cleaned, "Block1\nBlock2") && !strings.Contains(cleaned, "Block1\n\nBlock2") {
		t.Errorf("expected blocks on separate lines, got: %q", cleaned)
	}
}

func TestCleanText_CollapsesBlankLines(t *testing.T) {
	input := "Line1\n\n\n\n\nLine2\n\n\nLine3"
	result := cleanText(input)
	if strings.Contains(result, "\n\n\n") {
		t.Errorf("expected collapsed blank lines, got: %q", result)
	}
	if !strings.Contains(result, "Line1") || !strings.Contains(result, "Line2") || !strings.Contains(result, "Line3") {
		t.Errorf("expected all lines preserved, got: %q", result)
	}
}

// ---------------------------------------------------------------------------
// parseDDGLite unit tests
// ---------------------------------------------------------------------------

func TestParseDDGLite(t *testing.T) {
	results := parseDDGLite(cannedDDGLiteHTML, 10)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	r := results[0]
	if r.Title != "First Result" {
		t.Errorf("title = %q", r.Title)
	}
	if r.URL != "https://example.com/one" {
		t.Errorf("url = %q", r.URL)
	}
	if r.Snippet != "This is the first snippet." {
		t.Errorf("snippet = %q", r.Snippet)
	}
}

func TestParseDDGLite_CountLimit(t *testing.T) {
	results := parseDDGLite(cannedDDGLiteHTML, 2)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}
