---
name: lark-docs
description: Read, search, and manage Feishu/Lark documents via API
---

# Lark Document Operations Skill

This skill covers Feishu/Lark document operations beyond basic reading. For reading document content, use the built-in `lark_read_doc` tool directly.

## Prerequisites

The following environment variables must be set:
- `LARK_APP_ID` — Lark app ID
- `LARK_APP_SECRET` — Lark app secret

If these are not configured, Lark document operations are unavailable. Tell the user to configure them.

## Authentication

All API calls require a `tenant_access_token`. Obtain one before making requests:

```bash
curl -s -X POST 'https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal' \
  -H 'Content-Type: application/json' \
  -d "{\"app_id\": \"$LARK_APP_ID\", \"app_secret\": \"$LARK_APP_SECRET\"}" \
  | jq -r '.tenant_access_token'
```

Store the token in a variable for subsequent calls:

```bash
TOKEN=$(curl -s -X POST 'https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal' \
  -H 'Content-Type: application/json' \
  -d "{\"app_id\": \"$LARK_APP_ID\", \"app_secret\": \"$LARK_APP_SECRET\"}" \
  | jq -r '.tenant_access_token')
```

## URL Parsing

Feishu URLs follow these patterns — the last path segment is the token:

| URL Pattern | Type | Token Usage |
|---|---|---|
| `https://xxx.feishu.cn/docx/TOKEN` | Document | Use TOKEN directly as document_id with `lark_read_doc` |
| `https://xxx.feishu.cn/wiki/TOKEN` | Wiki node | Must resolve to document_id first (see below) |
| `https://xxx.feishu.cn/sheets/TOKEN` | Spreadsheet | Use with spreadsheet APIs |
| `https://xxx.feishu.cn/base/TOKEN` | Bitable/Base | Use with bitable APIs |

## Wiki Node Resolution

Wiki URLs contain a **node token**, not a document_id. Resolve it first:

```bash
curl -s -X GET "https://open.feishu.cn/open-apis/wiki/v2/spaces/get_node?token=$WIKI_TOKEN" \
  -H "Authorization: Bearer $TOKEN" \
  | jq '.data.node'
```

The response contains:
- `obj_token` — the actual document_id (use this with `lark_read_doc`)
- `obj_type` — the document type (`docx`, `sheet`, `bitable`, etc.)
- `space_id` — the wiki space ID
- `title` — the document title

## Search Documents

Search across all documents the app can access:

```bash
curl -s -X POST 'https://open.feishu.cn/open-apis/suite/docs-api/search/object' \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "search_key": "your search query",
    "count": 10,
    "offset": 0,
    "owner_ids": [],
    "docs_types": [1, 2, 3, 8, 22]
  }'
```

Document type codes: 1=doc, 2=sheet, 3=bitable, 8=docx, 22=slides.

**Note**: This API requires `user_access_token` which is harder to obtain programmatically. If it fails, suggest the user search in Feishu manually and provide the URL.

## Import/Create Documents from Markdown

Create a new document from markdown content:

```bash
# Step 1: Create an empty document
DOC_ID=$(curl -s -X POST 'https://open.feishu.cn/open-apis/docx/v1/documents' \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"title\": \"Document Title\", \"folder_token\": \"\"}" \
  | jq -r '.data.document.document_id')

echo "Created document: $DOC_ID"
```

For importing rich content, use the import API:

```bash
# Upload file and create import task
curl -s -X POST 'https://open.feishu.cn/open-apis/drive/v1/import_tasks' \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "file_extension": "md",
    "file_token": "<file_token_from_upload>",
    "type": "docx"
  }'
```

## Error Codes

| Code | Meaning | Action |
|---|---|---|
| 99991672 | No permission | The app lacks access to this document. Tell the user to add the app to the document's collaborators. |
| 99991668 | Document not found | The document_id is wrong or the document was deleted. |
| 99991664 | Rate limited | Wait a moment and retry. |
| 99991663 | Invalid token | The tenant_access_token has expired. Re-authenticate. |

## Writing with Markdown Formatting

`lark_write_doc` accepts **markdown** content and automatically converts it to native Feishu blocks with proper formatting. This means headings, lists, code blocks, bold/italic, and links are preserved as rich document elements -- not plain text.

### Supported Markdown Syntax

| Markdown | Feishu Result |
|---|---|
| `# Heading 1` | Native H1 heading block |
| `## Heading 2` | Native H2 heading block |
| `### Heading 3` | Native H3 heading block |
| `**bold**` | Bold inline style |
| `*italic*` | Italic inline style |
| `~~strikethrough~~` | Strikethrough inline style |
| `` `inline code` `` | Inline code style |
| Fenced code blocks | Code block |
| `- bullet item` | Bullet list block |
| `1. numbered item` | Ordered list block |
| `[text](url)` | Hyperlink |
| `> blockquote` | Quote block |

### Content Best Practices

- **Always write markdown**, not plain text. Without markdown syntax, the document will have no headings, no lists, no formatting.
- Use standard ASCII punctuation. Avoid curly/smart quotes, em-dashes, and other non-ASCII symbols.
- CJK characters (Chinese, Japanese, Korean) work fine alongside markdown syntax.
- The tool handles large documents automatically (batches of 50 blocks per API call).
- A document can hold at most 40,000 blocks.

### Character Cautions

If a write fails, it may be due to special Unicode characters. Use:
- Straight quotes `"` `'` instead of curly quotes
- `--` instead of em-dash
- `...` instead of ellipsis character
- `->` instead of arrow symbols

## Read-Modify-Write Workflow

Use `lark_read_doc` and `lark_write_doc` together to update document content:

1. **Read** the current content with `lark_read_doc` (returns plain text)
2. **Restructure** the content as proper markdown (add `#` headings, `- ` bullets, `**bold**`, etc.)
3. **Write** the markdown back with `lark_write_doc` (automatically converted to rich Feishu blocks)

**Warning**: `lark_write_doc` replaces ALL content in the document. Always read first to avoid data loss.

Example flow:
```
lark_read_doc(document_id: "ABC123")
-> "Bug Hunting by AI Agents\nPurpose\nDefine a repeatable process..."

(restructure as markdown)

lark_write_doc(document_id: "ABC123", content: "# Bug Hunting by AI Agents\n\n## Purpose\n\nDefine a **repeatable process** for AI-assisted bug hunting.\n\n## Workflow\n\n1. Read target system docs\n2. Generate bug hypotheses\n3. Reproduce and validate\n")
-> "Document content updated successfully."
```

The resulting Feishu document will have proper H1/H2 headings, bold text, and a numbered list.

## Workflow Examples

### User asks to read a wiki page

1. Extract the wiki token from the URL
2. Resolve it via the wiki node API to get the `obj_token`
3. Call `lark_read_doc` with the resolved `obj_token` as `document_id`

### User asks to find a document

1. Ask the user for search keywords
2. Try the search API; if it fails (needs user token), ask the user to find the URL in Feishu
3. Once you have the URL, extract the token and read it

### User asks about a permission error

1. Explain what the error code means (see table above)
2. Guide them to add the Lark app as a collaborator on the document
3. In Feishu: open doc → Share → Add the app name → Grant read access
