package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/joechenrh/golem/internal/tools"
)

const defaultMaxHTTPResponseLen = 50_000

// blockedHeaders are request headers that the LLM is not allowed to set
// to prevent credential exfiltration and request smuggling.
var blockedHeaders = map[string]bool{
	"authorization":       true,
	"proxy-authorization": true,
	"cookie":              true,
	"set-cookie":          true,
	"x-forwarded-for":     true,
	"x-real-ip":           true,
}

func isBlockedHeader(name string) bool {
	return blockedHeaders[strings.ToLower(name)]
}

// HTTPRequestTool makes arbitrary HTTP requests (GET, POST, PUT, etc.).
// Unlike web_fetch which extracts readable text from HTML, this tool returns
// raw response bodies and exposes headers, status codes, and custom methods.
type HTTPRequestTool struct {
	client *http.Client
}

// NewHTTPRequestTool creates an HTTP request tool using the given client.
func NewHTTPRequestTool(client *http.Client) *HTTPRequestTool {
	return &HTTPRequestTool{client: client}
}

func (t *HTTPRequestTool) Name() string        { return "http_request" }
func (t *HTTPRequestTool) Description() string { return "Make HTTP requests to APIs and web services" }
func (t *HTTPRequestTool) FullDescription() string {
	return "Make HTTP requests with full control over method, headers, and body. " +
		"Use this for REST API calls, webhooks, and services that return JSON or structured data. " +
		"For fetching and reading web pages, prefer web_fetch instead."
}

var httpRequestParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"url": {"type": "string", "description": "Request URL (http or https)"},
		"method": {"type": "string", "description": "HTTP method: GET, POST, PUT, PATCH, DELETE, HEAD (default GET)"},
		"headers": {"type": "object", "description": "Request headers as key-value pairs", "additionalProperties": {"type": "string"}},
		"body": {"type": "string", "description": "Request body (for POST, PUT, PATCH)"},
		"max_length": {"type": "integer", "description": "Maximum response body length in bytes (default 50000)"}
	},
	"required": ["url"]
}`)

func (t *HTTPRequestTool) Parameters() json.RawMessage { return httpRequestParams }

func (t *HTTPRequestTool) Execute(
	ctx context.Context, args string,
) (string, error) {
	var params struct {
		URL       string            `json:"url"`
		Method    string            `json:"method"`
		Headers   map[string]string `json:"headers"`
		Body      string            `json:"body"`
		MaxLength int               `json:"max_length"`
	}
	if errMsg := tools.ParseArgs(args, &params); errMsg != "" {
		return errMsg, nil
	}
	if params.URL == "" {
		return "Error: 'url' is required", nil
	}
	if params.Method == "" {
		params.Method = "GET"
	}
	params.Method = strings.ToUpper(params.Method)
	if params.MaxLength <= 0 {
		params.MaxLength = defaultMaxHTTPResponseLen
	}

	// Validate method.
	switch params.Method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD":
		// ok
	default:
		return fmt.Sprintf("Error: unsupported HTTP method: %s", params.Method), nil
	}

	// Validate URL scheme.
	parsed, err := url.Parse(params.URL)
	if err != nil {
		return "Error: invalid URL: " + err.Error(), nil
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "Error: only http and https URLs are supported", nil
	}

	// Build request.
	var bodyReader io.Reader
	if params.Body != "" {
		bodyReader = strings.NewReader(params.Body)
	}
	req, err := http.NewRequestWithContext(ctx, params.Method, params.URL, bodyReader)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	for k, v := range params.Headers {
		if isBlockedHeader(k) {
			return fmt.Sprintf("Error: setting header %q is not allowed for security reasons", k), nil
		}
		req.Header.Set(k, v)
	}

	// Execute.
	resp, err := t.client.Do(req)
	if err != nil {
		return "Error: request failed: " + err.Error(), nil
	}
	defer resp.Body.Close()

	// Read response body (limit to 1MB raw read).
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "Error: reading response: " + err.Error(), nil
	}

	// Truncate if needed.
	bodyStr := string(body)
	truncated := false
	if len(bodyStr) > params.MaxLength {
		bodyStr = bodyStr[:params.MaxLength]
		truncated = true
	}

	// Format response.
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("HTTP %d %s\n", resp.StatusCode, resp.Status))

	// Include response headers.
	for k, vals := range resp.Header {
		for _, v := range vals {
			sb.WriteString(fmt.Sprintf("%s: %s\n", k, v))
		}
	}
	sb.WriteString("\n")
	sb.WriteString(bodyStr)
	if truncated {
		sb.WriteString("\n\n[Response truncated]")
	}

	return sb.String(), nil
}
