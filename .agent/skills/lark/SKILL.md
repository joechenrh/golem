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
Sends a text message to a Lark group chat.

Parameters:
- `chat_id` (required): The chat_id of the target group (get this from `lark_list_chats`)
- `message` (required): The text message to send

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
