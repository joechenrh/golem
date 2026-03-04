---
name: lark
description: Send messages to Lark/Feishu group chats
---

# Lark Messaging Skill

You have two Lark tools available when the Lark channel is enabled:

## Tools

### `lark_list_chats`
Lists all Lark group chats the bot belongs to. Returns chat_id, name, and description for each group. **Always call this first** if you don't know the chat_id.

### `lark_send`
Sends a message to a Lark group chat. Messages are rendered as interactive cards with markdown.

Parameters:
- `chat_id` (required): The chat_id of the target group (get this from `lark_list_chats`)
- `message` (required): The message content (see formatting rules below)

## Message Formatting

Lark cards use a **limited subset of markdown**. You MUST follow these rules when composing the `message` for `lark_send`:

### Supported syntax
- `**bold**` → **bold**
- `*italic*` → *italic*
- `~~strikethrough~~` → ~~strikethrough~~
- `[link text](url)` → clickable link
- `` `inline code` `` → inline code
- Fenced code blocks (` ``` `)
- `- item` → unordered list
- `1. item` → ordered list
- `---` → horizontal rule

### NOT supported (will show as raw text)
- `# Headers` — use `**bold text**` on its own line instead
- `> Blockquotes` — use `*italic text*` instead
- `| Tables |` — use lists or plain text alignment instead
- Images via `![alt](url)` — not supported in markdown element

### Formatting guidelines
1. Use `**bold**` for titles and section headings, placed on their own line
2. Use blank lines to separate sections for readability
3. Keep messages concise — Lark cards have limited vertical space
4. For structured data, prefer unordered lists over tables

### Good example
```
**Deploy Status**

Service: api-gateway
Branch: main
Status: *successful*

**Changes**
- Fixed auth token refresh
- Updated rate limit config

[View details](https://ci.example.com/build/123)
```

### Bad example (will render incorrectly)
```
## Deploy Status

> Deployment completed successfully

| Service | Status |
|---------|--------|
| api-gw  | ok     |
```

## Workflow

1. If the user asks to send a message to a Lark group but doesn't provide a chat_id:
   - Call `lark_list_chats` to find available groups
   - Match the group name the user mentioned
   - Use the corresponding chat_id
2. Call `lark_send` with the chat_id and message
3. Confirm to the user that the message was sent

## Examples

User: "Send 'Hello team!' to the test_mcp group"
1. Call `lark_list_chats` to find the chat_id for "test_mcp"
2. Call `lark_send` with `{"chat_id": "oc_xxx", "message": "Hello team!"}`

User: "Tell the dev group that the build is fixed"
1. Call `lark_list_chats` to find the "dev" group
2. Call `lark_send` with `{"chat_id": "oc_xxx", "message": "The build is fixed."}`
