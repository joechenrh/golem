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

func NewLarkReadDocTool(
	ch *larkchan.LarkChannel,
) *LarkReadDocTool {
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

func (t *LarkReadDocTool) Execute(
	ctx context.Context, args string,
) (string, error) {
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

func NewLarkWriteDocTool(
	ch *larkchan.LarkChannel,
) *LarkWriteDocTool {
	return &LarkWriteDocTool{ch: ch}
}

func (t *LarkWriteDocTool) Name() string { return "lark_write_doc" }
func (t *LarkWriteDocTool) Description() string {
	return "Replace all content of a Feishu document with markdown-formatted text"
}
func (t *LarkWriteDocTool) FullDescription() string {
	return `Replace the entire content of a Feishu/Lark document with new content.

The content parameter accepts MARKDOWN format. The tool automatically converts markdown
into native Feishu blocks, preserving rich formatting:
- # Heading 1, ## Heading 2, ### Heading 3, etc. become native Feishu headings
- **bold**, *italic*, ~~strikethrough~~ become inline styles
- - bullet items become bullet list blocks
- 1. numbered items become ordered list blocks
- ` + "`" + `code` + "`" + ` and fenced code blocks become code blocks
- [text](url) become hyperlinks
- > blockquotes become quote blocks

IMPORTANT: Always write content as standard markdown to get proper Feishu formatting.
Do NOT write plain text without markdown syntax -- you will lose all document structure.

WARNING: This tool REPLACES ALL existing content in the document. The previous content will be lost.

Typical read-modify-write workflow:
1. Use lark_read_doc to read the current content (returns plain text)
2. Restructure and enhance the content as markdown
3. Use lark_write_doc to write the markdown content back (formatting is preserved)

Content restrictions:
- A document can hold at most 40,000 blocks.
- The API creates blocks in batches of 50 per request; large documents are handled automatically.
- Avoid non-ASCII punctuation that may cause issues. Use straight quotes instead of curly quotes,
  -- instead of em-dash, ... instead of ellipsis. CJK characters and ASCII punctuation are safe.

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
		"content": {"type": "string", "description": "The new content in markdown format to write to the document (replaces all existing content). Use markdown syntax for headings, lists, bold, code blocks, etc."}
	},
	"required": ["document_id", "content"]
}`)

func (t *LarkWriteDocTool) Parameters() json.RawMessage { return larkWriteDocParams }

func (t *LarkWriteDocTool) Execute(
	ctx context.Context, args string,
) (string, error) {
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
