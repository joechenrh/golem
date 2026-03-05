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

// LarkWriteDocTool lets the agent write/replace content in a Feishu document.
type LarkWriteDocTool struct {
	ch *larkchan.LarkChannel
}

func NewLarkWriteDocTool(ch *larkchan.LarkChannel) *LarkWriteDocTool {
	return &LarkWriteDocTool{ch: ch}
}

func (t *LarkWriteDocTool) Name() string { return "lark_write_doc" }
func (t *LarkWriteDocTool) Description() string {
	return "Replace all content of a Feishu document with new text"
}
func (t *LarkWriteDocTool) FullDescription() string {
	return `Replace the entire content of a Feishu/Lark document with new plain text.

WARNING: This tool REPLACES ALL existing content in the document. The previous content will be lost.

Typical read-modify-write workflow:
1. Use lark_read_doc to read the current content
2. Modify the text as needed
3. Use lark_write_doc to write the modified content back

CRITICAL — Content restrictions (Feishu block API):
- Each text block supports up to 100,000 characters. Each line becomes one block.
- A document can hold at most 40,000 blocks.
- The API creates blocks in batches of 50 per request; very large documents are handled automatically.
- SPECIAL CHARACTERS: The Feishu API rejects certain Unicode characters in text content.
  Before writing, you MUST sanitize the text:
  - Replace arrow symbols: "→" with "->", "←" with "<-", "↑" with "^", "↓" with "v"
  - Replace curly/smart quotes: use straight quotes " and ' instead of " " ' '
  - Replace em-dash "—" with "--", en-dash "–" with "-"
  - Replace ellipsis "…" with "..."
  - Replace bullet "•" with "-"
  - Remove or replace any other non-ASCII punctuation (e.g. ©, ™, ®)
  - Keep CJK characters (Chinese, Japanese, Korean) — they work fine
  - Keep standard ASCII punctuation — it all works
  If the write fails with an API error, the most likely cause is a special character.
  Clean the content and retry.

How to get the document_id from a Feishu URL:
- Document URL: https://xxx.feishu.cn/docx/ABC123 -- document_id is "ABC123"
- You can also pass the full URL; the tool will extract the token automatically.

IMPORTANT -- Wiki URLs:
- Wiki URL: https://xxx.feishu.cn/wiki/XYZ789 -- the token is a wiki node token, NOT a document_id.
- For wiki URLs, you must first resolve the wiki node token to a document_id.

Common errors:
- Code 99991672: The app does not have permission. Ask the user to grant edit access.
- Code 99991668: Document not found. Check the document_id is correct.
- Code 99991664: Rate limited. Wait and retry.`
}

var larkWriteDocParams = json.RawMessage(`{
	"type": "object",
	"properties": {
		"document_id": {"type": "string", "description": "The document_id of the Feishu document, or a full Feishu document URL"},
		"content": {"type": "string", "description": "The new plain text content to write to the document (replaces all existing content)"}
	},
	"required": ["document_id", "content"]
}`)

func (t *LarkWriteDocTool) Parameters() json.RawMessage { return larkWriteDocParams }

func (t *LarkWriteDocTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		DocumentID string `json:"document_id"`
		Content    string `json:"content"`
	}
	if err := json.Unmarshal([]byte(llm.NormalizeArgs(args)), &params); err != nil {
		return "Error: invalid arguments: " + err.Error(), nil
	}
	if params.DocumentID == "" {
		return "Error: 'document_id' is required", nil
	}
	if params.Content == "" {
		return "Error: 'content' is required", nil
	}

	token := extractToken(params.DocumentID)

	if err := t.ch.WriteDocContent(ctx, token, params.Content); err != nil {
		return "Error: " + err.Error(), nil
	}
	return "Document content updated successfully.", nil
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
