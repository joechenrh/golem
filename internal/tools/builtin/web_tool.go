package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"

	"github.com/joechenrh/golem/internal/llm"
)

// searchResult holds a single DuckDuckGo search result.
type searchResult struct {
	Title   string
	URL     string
	Snippet string
}

// ---------------------------------------------------------------------------
// WebSearchTool
// ---------------------------------------------------------------------------

// WebSearchTool searches the web via DuckDuckGo Lite.
type WebSearchTool struct {
	client    *http.Client
	searchURL string // overridable for testing
}

// NewWebSearchTool creates a web search tool using the given HTTP client.
func NewWebSearchTool(client *http.Client) *WebSearchTool {
	return &WebSearchTool{
		client:    client,
		searchURL: "https://lite.duckduckgo.com/lite/",
	}
}

func (t *WebSearchTool) Name() string        { return "web_search" }
func (t *WebSearchTool) Description() string { return "Search the web using DuckDuckGo" }
func (t *WebSearchTool) FullDescription() string {
	return "Search the web using DuckDuckGo and return titles, URLs, and snippets. " +
		"Use this to find current information, documentation, news, etc."
}

var webSearchParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"query": {"type": "string", "description": "Search query"},
		"count": {"type": "integer", "description": "Number of results to return (default 5, max 20)"}
	},
	"required": ["query"]
}`)

func (t *WebSearchTool) Parameters() json.RawMessage { return webSearchParams }

func (t *WebSearchTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Query string `json:"query"`
		Count int    `json:"count"`
	}
	if err := json.Unmarshal([]byte(llm.NormalizeArgs(args)), &params); err != nil {
		return "Error: invalid arguments: " + err.Error(), nil
	}
	if params.Query == "" {
		return "Error: 'query' is required", nil
	}
	if params.Count <= 0 {
		params.Count = 5
	}
	if params.Count > 20 {
		params.Count = 20
	}

	results, err := t.search(ctx, params.Query, params.Count)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	if len(results) == 0 {
		return "No results found for: " + params.Query, nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Search results for %q:\n\n", params.Query))
	for i, r := range results {
		sb.WriteString(fmt.Sprintf("%d. %s\n   %s\n", i+1, r.Title, r.URL))
		if r.Snippet != "" {
			sb.WriteString(fmt.Sprintf("   %s\n", r.Snippet))
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

func (t *WebSearchTool) search(ctx context.Context, query string, count int) ([]searchResult, error) {
	form := url.Values{"q": {query}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.searchURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Golem/0.1)")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	return parseDDGLite(string(body), count), nil
}

// parseDDGLite extracts search results from DuckDuckGo Lite HTML.
// The Lite page uses a table layout where result links are in <a> tags with
// class "result-link" and snippets follow in <td class="result-snippet">.
func parseDDGLite(htmlStr string, count int) []searchResult {
	tokenizer := html.NewTokenizer(strings.NewReader(htmlStr))
	var results []searchResult
	var current searchResult
	var inResultLink, inSnippet bool

	for {
		tt := tokenizer.Next()
		if tt == html.ErrorToken {
			break
		}

		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			tn, hasAttr := tokenizer.TagName()
			tagName := string(tn)

			if tagName == "a" && hasAttr {
				cls, href := getAAttrs(tokenizer)
				if cls == "result-link" && href != "" {
					// Start of a new result.
					if current.URL != "" {
						results = append(results, current)
						if len(results) >= count {
							return results
						}
					}
					current = searchResult{URL: href}
					inResultLink = true
				}
			}
			if tagName == "td" && hasAttr {
				cls := getAttr(tokenizer, "class")
				if cls == "result-snippet" {
					inSnippet = true
				}
			}

		case html.EndTagToken:
			tn, _ := tokenizer.TagName()
			tagName := string(tn)
			if tagName == "a" {
				inResultLink = false
			}
			if tagName == "td" && inSnippet {
				inSnippet = false
			}

		case html.TextToken:
			text := strings.TrimSpace(string(tokenizer.Text()))
			if text == "" {
				continue
			}
			if inResultLink {
				current.Title = text
			}
			if inSnippet && current.URL != "" {
				if current.Snippet != "" {
					current.Snippet += " "
				}
				current.Snippet += text
			}
		}
	}
	// Don't forget the last result.
	if current.URL != "" {
		results = append(results, current)
	}
	return results
}

// getAAttrs returns class and href attribute values from the current <a> token.
func getAAttrs(z *html.Tokenizer) (class, href string) {
	for {
		key, val, more := z.TagAttr()
		k := string(key)
		if k == "class" {
			class = string(val)
		}
		if k == "href" {
			href = string(val)
		}
		if !more {
			break
		}
	}
	return
}

// getAttr returns the value of a named attribute from the current token.
func getAttr(z *html.Tokenizer, name string) string {
	for {
		key, val, more := z.TagAttr()
		if string(key) == name {
			return string(val)
		}
		if !more {
			break
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// WebFetchTool
// ---------------------------------------------------------------------------

// WebFetchTool fetches content from a URL and extracts readable text.
type WebFetchTool struct {
	client *http.Client
}

// NewWebFetchTool creates a web fetch tool using the given HTTP client.
func NewWebFetchTool(client *http.Client) *WebFetchTool {
	return &WebFetchTool{client: client}
}

func (t *WebFetchTool) Name() string        { return "web_fetch" }
func (t *WebFetchTool) Description() string { return "Fetch and extract text content from a URL" }
func (t *WebFetchTool) FullDescription() string {
	return "Fetch content from a URL and extract readable text. " +
		"Strips HTML tags, scripts, styles, and navigation. " +
		"Returns plain text suitable for reading and summarization."
}

var webFetchParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"url": {"type": "string", "description": "URL to fetch (http or https)"},
		"max_length": {"type": "integer", "description": "Maximum length of returned text (default 5000)"}
	},
	"required": ["url"]
}`)

func (t *WebFetchTool) Parameters() json.RawMessage { return webFetchParams }

func (t *WebFetchTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		URL       string `json:"url"`
		MaxLength int    `json:"max_length"`
	}
	if err := json.Unmarshal([]byte(llm.NormalizeArgs(args)), &params); err != nil {
		return "Error: invalid arguments: " + err.Error(), nil
	}
	if params.URL == "" {
		return "Error: 'url' is required", nil
	}
	if params.MaxLength <= 0 {
		params.MaxLength = 5000
	}

	// Validate URL scheme.
	parsed, err := url.Parse(params.URL)
	if err != nil {
		return "Error: invalid URL: " + err.Error(), nil
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "Error: only http and https URLs are supported", nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, params.URL, nil)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Golem/0.1)")

	resp, err := t.client.Do(req)
	if err != nil {
		return "Error: fetch failed: " + err.Error(), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("Error: URL returned status %d", resp.StatusCode), nil
	}

	// Limit read to 1MB.
	body := io.LimitReader(resp.Body, 1<<20)

	ct := resp.Header.Get("Content-Type")
	var text string
	if strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml") {
		text = extractText(body)
	} else {
		raw, err := io.ReadAll(body)
		if err != nil {
			return "Error: reading response: " + err.Error(), nil
		}
		text = string(raw)
	}

	text = cleanText(text)

	// Truncate if needed.
	if len(text) > params.MaxLength {
		text = text[:params.MaxLength] + "\n\n[Content truncated]"
	}

	return text, nil
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// skipTags is the set of HTML elements whose content is discarded.
var skipTags = map[string]bool{
	"script":   true,
	"style":    true,
	"nav":      true,
	"footer":   true,
	"header":   true,
	"noscript": true,
	"svg":      true,
}

// blockTags insert a newline before their content.
var blockTags = map[string]bool{
	"p": true, "div": true, "br": true, "h1": true, "h2": true,
	"h3": true, "h4": true, "h5": true, "h6": true, "li": true,
	"tr": true, "blockquote": true, "pre": true, "article": true,
	"section": true, "main": true, "dt": true, "dd": true,
}

// extractText parses HTML and returns readable plain text.
func extractText(r io.Reader) string {
	tokenizer := html.NewTokenizer(r)
	var sb strings.Builder
	var skipDepth int

	for {
		tt := tokenizer.Next()
		if tt == html.ErrorToken {
			break
		}

		switch tt {
		case html.StartTagToken:
			tn, _ := tokenizer.TagName()
			tag := string(tn)
			if skipTags[tag] {
				skipDepth++
			}
			if skipDepth == 0 && blockTags[tag] {
				sb.WriteString("\n")
			}

		case html.EndTagToken:
			tn, _ := tokenizer.TagName()
			tag := string(tn)
			if skipTags[tag] && skipDepth > 0 {
				skipDepth--
			}
			if skipDepth == 0 && blockTags[tag] {
				sb.WriteString("\n")
			}

		case html.TextToken:
			if skipDepth > 0 {
				continue
			}
			text := string(tokenizer.Text())
			sb.WriteString(text)
		}
	}
	return sb.String()
}

// cleanText collapses multiple blank lines and trims whitespace.
func cleanText(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	blankCount := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			blankCount++
			if blankCount <= 1 {
				out = append(out, "")
			}
		} else {
			blankCount = 0
			out = append(out, trimmed)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
