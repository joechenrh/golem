package builtin

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	larkchan "github.com/joechenrh/golem/internal/channel/lark"
	"github.com/joechenrh/golem/internal/llm"
)

// LarkReadDocTool lets the agent read Feishu document content.
type LarkReadDocTool struct {
	ch *larkchan.LarkChannel
}

func NewLarkReadDocTool(ch *larkchan.LarkChannel) *LarkReadDocTool {
	return &LarkReadDocTool{ch: ch}
}

func (t *LarkReadDocTool) Name() string        { return "lark_read_doc" }
func (t *LarkReadDocTool) Description() string { return "Read plain text content of a Feishu document" }
func (t *LarkReadDocTool) FullDescription() string {
	return `Read the plain text content of a Feishu/Lark document by its document_id.

How to get the document_id from a Feishu URL:
- Document URL: https://xxx.feishu.cn/docx/ABC123 → document_id is "ABC123"
- You can also pass the full URL; the tool will extract the token automatically.

IMPORTANT — Wiki URLs:
- Wiki URL: https://xxx.feishu.cn/wiki/XYZ789 → the token "XYZ789" is a wiki node token, NOT a document_id.
- For wiki URLs, you must first resolve the wiki node token to a document_id using the Lark API (see the lark-docs skill).

Common errors:
- Code 99991672: The app does not have permission to access this document. Ask the user to grant access.
- Code 99991668: Document not found. Check the document_id is correct.
- Code 99991664: Rate limited. Wait and retry.`
}

var larkReadDocParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"document_id": {"type": "string", "description": "The document_id of the Feishu document, or a full Feishu document URL"}
	},
	"required": ["document_id"]
}`)

func (t *LarkReadDocTool) Parameters() json.RawMessage { return larkReadDocParams }

func (t *LarkReadDocTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		DocumentID string `json:"document_id"`
	}
	if err := json.Unmarshal([]byte(llm.NormalizeArgs(args)), &params); err != nil {
		return "Error: invalid arguments: " + err.Error(), nil
	}
	if params.DocumentID == "" {
		return "Error: 'document_id' is required", nil
	}

	token := extractToken(params.DocumentID)

	content, err := t.ch.ReadDocContent(ctx, token)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	if content == "" {
		return "(document is empty)", nil
	}
	return content, nil
}

// extractToken extracts a bare token from a Feishu URL or returns the input as-is.
// Supports URLs like:
//
//	https://xxx.feishu.cn/docx/ABC123
//	https://xxx.feishu.cn/wiki/XYZ789
//	https://xxx.feishu.cn/sheets/DEF456
//	https://xxx.larksuite.com/docx/ABC123
func extractToken(input string) string {
	input = strings.TrimSpace(input)

	// If it doesn't look like a URL, return as-is.
	if !strings.Contains(input, "://") {
		return input
	}

	u, err := url.Parse(input)
	if err != nil {
		return input
	}

	// Path segments: e.g. /docx/ABC123 → ["", "docx", "ABC123"]
	segments := strings.Split(strings.TrimRight(u.Path, "/"), "/")
	if len(segments) >= 3 {
		return segments[len(segments)-1]
	}

	return input
}
