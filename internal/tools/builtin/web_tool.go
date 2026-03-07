package builtin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"

	"github.com/joechenrh/golem/internal/tools"
)

const (
	// defaultUserAgent is the User-Agent header used for web requests to avoid bot blocking.
	defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	// defaultMaxFetchLen is the default max content length for web_fetch.
	defaultMaxFetchLen = 5000
)

// searchResult holds a single web search result.
type searchResult struct {
	Title   string
	URL     string
	Snippet string
}

// ---------------------------------------------------------------------------
// WebSearchTool
// ---------------------------------------------------------------------------

// WebSearchTool searches the web using a configurable backend.
type WebSearchTool struct {
	client    *http.Client
	backend   string // "bing" or "stub"
	searchURL string // overridable for testing
}

// NewWebSearchTool creates a web search tool.
// backend is "bing" (default) or "stub".
func NewWebSearchTool(
	client *http.Client, backend string,
) *WebSearchTool {
	if backend == "" {
		backend = "bing"
	}
	return &WebSearchTool{
		client:    client,
		backend:   backend,
		searchURL: "https://www.bing.com/search",
	}
}

func (t *WebSearchTool) Name() string        { return "web_search" }
func (t *WebSearchTool) Description() string { return "Search the web" }
func (t *WebSearchTool) FullDescription() string {
	return "Search the web and return titles, URLs, and snippets. " +
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

func (t *WebSearchTool) Execute(
	ctx context.Context, args string,
) (string, error) {
	var params struct {
		Query string `json:"query"`
		Count int    `json:"count"`
	}
	if errMsg := tools.ParseArgs(args, &params); errMsg != "" {
		return errMsg, nil
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

func (t *WebSearchTool) search(
	ctx context.Context, query string, count int,
) ([]searchResult, error) {
	switch t.backend {
	case "stub":
		return t.searchStub(query)
	default:
		return t.searchBing(ctx, query, count)
	}
}

func (t *WebSearchTool) searchStub(
	query string,
) ([]searchResult, error) {
	return []searchResult{{
		Title:   "Search URL",
		URL:     fmt.Sprintf("https://duckduckgo.com/?q=%s", url.QueryEscape(query)),
		Snippet: "Tip: Use web_fetch to retrieve specific pages.",
	}}, nil
}

func (t *WebSearchTool) searchBing(
	ctx context.Context, query string, count int,
) ([]searchResult, error) {
	u := fmt.Sprintf("%s?q=%s&count=%d", t.searchURL, url.QueryEscape(query), count)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", defaultUserAgent)

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	return parseBingResults(string(body), count), nil
}

// parseBingResults extracts search results from Bing HTML.
// Results live in <li class="b_algo"> blocks with <h2><a href="...">title</a></h2>
// and a <p> snippet.
func parseBingResults(
	htmlStr string, count int,
) []searchResult {
	tokenizer := html.NewTokenizer(strings.NewReader(htmlStr))
	var results []searchResult
	var inAlgo, inH2, inLink, inSnippetP bool
	var current searchResult

	for {
		tt := tokenizer.Next()
		if tt == html.ErrorToken {
			break
		}

		switch tt {
		case html.StartTagToken:
			tn, hasAttr := tokenizer.TagName()
			tag := string(tn)

			if tag == "li" && hasAttr {
				cls := getAttr(tokenizer, "class")
				if strings.Contains(cls, "b_algo") {
					inAlgo = true
					current = searchResult{}
				}
			}
			if !inAlgo {
				continue
			}

			if tag == "h2" {
				inH2 = true
			}
			if tag == "a" && inH2 && hasAttr {
				href := getAttr(tokenizer, "href")
				if href != "" {
					current.URL = decodeBingURL(href)
					inLink = true
				}
			}
			if tag == "p" && !inH2 && current.URL != "" && !inSnippetP {
				inSnippetP = true
			}

		case html.EndTagToken:
			tn, _ := tokenizer.TagName()
			tag := string(tn)

			if !inAlgo {
				continue
			}

			if tag == "a" && inLink {
				inLink = false
			}
			if tag == "h2" {
				inH2 = false
			}
			if tag == "p" && inSnippetP {
				inSnippetP = false
			}
			if tag == "li" {
				if current.URL != "" {
					results = append(results, current)
					if len(results) >= count {
						return results
					}
				}
				inAlgo = false
			}

		case html.TextToken:
			if !inAlgo {
				continue
			}
			text := strings.TrimSpace(string(tokenizer.Text()))
			if text == "" {
				continue
			}
			if inLink {
				current.Title += text
			}
			if inSnippetP {
				if current.Snippet != "" {
					current.Snippet += " "
				}
				current.Snippet += text
			}
		}
	}
	return results
}

// decodeBingURL extracts the real URL from a Bing redirect link.
// Bing wraps URLs as /ck/a?!&&p=...&u=a1<base64>&... where the base64 payload
// (after stripping the "a1" prefix) is the actual URL encoded in URL-safe base64.
// If the URL is not a Bing redirect, it is returned as-is.
func decodeBingURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	uParam := parsed.Query().Get("u")
	if uParam == "" {
		return rawURL
	}
	// Strip the "a1" prefix that Bing prepends.
	encoded := strings.TrimPrefix(uParam, "a1")
	if encoded == "" {
		return rawURL
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return rawURL
	}
	return string(decoded)
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

func (t *WebFetchTool) Execute(
	ctx context.Context, args string,
) (string, error) {
	var params struct {
		URL       string `json:"url"`
		MaxLength int    `json:"max_length"`
	}
	if errMsg := tools.ParseArgs(args, &params); errMsg != "" {
		return errMsg, nil
	}
	if params.URL == "" {
		return "Error: 'url' is required", nil
	}
	if params.MaxLength <= 0 {
		params.MaxLength = defaultMaxFetchLen
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
	req.Header.Set("User-Agent", defaultUserAgent)

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
