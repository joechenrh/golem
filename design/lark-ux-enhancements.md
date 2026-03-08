# Lark Bot UX Enhancements Plan

## Current State

The bot uses Lark interactive cards with streaming (800ms card patches), images in/out,
per-chat session isolation, thinking indicator, error feedback, slash commands (/help,
/new, /status), session reset & feedback buttons, rich card formatting, and proactive
scheduled messages. Remaining gaps: file/document handling, thread support.

---

## Tier 1 — Quick Wins

### 1. Thinking Indicator [DONE]

**Problem:** Users see nothing for 800ms+ after sending a message. No way to tell if the
bot is alive or processing.

**Solution:** Immediately send a lightweight card ("Thinking...") when a message is
received. Replace it with the first streamed content once tokens arrive.

**Files:**
- `internal/channel/lark/lark.go` — `SendStream()`: create the placeholder card before
  the first token arrives instead of waiting for the ticker.

**Scope:** ~30 lines changed.

---

### 2. Error Feedback to Users [DONE]

**Problem:** When the LLM call fails, the user sees silence — no message, no error. The
bot looks broken.

**Solution:** Catch fatal errors in the message processing path and send a user-facing
error card: *"Something went wrong. Please try again later."* Log the real error
server-side as before.

**Files:**
- `internal/app/app.go` — `processMessage()`: on error, call `ch.Send()` with a
  friendly error message instead of only logging.
- `internal/channel/lark/lark.go` — add a `SendError(chatID, msg)` helper that sends a
  red-tinted or clearly-marked error card.

**Scope:** ~40 lines added.

---

### 3. Slash Commands in Lark (`/help`, `/new`, `/status`) [DONE]

**Problem:** CLI users have `:help`, `:quit`, `:tools` etc. Lark users have none. They
must phrase everything as natural language.

**Solution:** Detect messages starting with `/` in the Lark channel and route them:

| Command   | Action                                           |
|-----------|--------------------------------------------------|
| `/help`   | Send a card listing available commands and tools  |
| `/new`    | Reset session (evict + create fresh)              |
| `/status` | Show session info: age, token usage, tool count   |

**Files:**
- `internal/channel/lark/lark.go` — `onMessageReceive()`: detect `/` prefix, handle
  locally or pass to session as a `:` command.
- `internal/agent/commands.go` — ensure `handleCommand` supports the relevant commands.
- `internal/agent/manager.go` — add `Reset(chatID)` method to evict and recreate.

**Scope:** ~80 lines added.

---

## Tier 2 — Meaningful UX Upgrades

### 4. Session Reset Button on Cards [DONE]

**Problem:** Users have no visible way to start over. Session lifecycle is invisible.

**Solution:** Append an action button row to response cards with a "New conversation"
button. When clicked, Lark sends a card action callback; the bot resets the session.

**Files:**
- `internal/channel/lark/lark.go` — `buildCard()`: add an `"actions"` element with a
  button whose `value` is `{"action":"reset_session"}`.
- `internal/channel/lark/lark.go` — register a card action handler via the Lark SDK
  (`OnP2CardActionTrigger`) that resets the session.
- `internal/agent/manager.go` — `Reset(chatID)` (shared with Tier 1 item 3).

**Scope:** ~100 lines added.

---

### 5. Feedback Buttons [DONE]

**Problem:** No mechanism to collect user satisfaction signals.

**Solution:** Add thumbs-up / thumbs-down buttons to every response card. On click, store
the feedback in the tape as a `KindFeedback` entry. Optionally also store in shared memory for
cross-session analysis.

**Files:**
- `internal/channel/lark/lark.go` — card action handler: detect `{"action":"feedback",
  "value":"up|down"}`.
- `internal/tape/tape.go` — add `KindFeedback` entry kind.
- `internal/agent/session.go` — `RecordFeedback(messageID, value)` method.

**Scope:** ~80 lines added.

---

### 6. Rich Card Formatting [DONE]

**Problem:** `sanitizeLarkMarkdown` strips headers, blockquotes, and inline code. All
responses are a single markdown element — no visual structure.

**Solution:** Build structured cards with:
- `header` element for detected title lines
- `hr` dividers between logical sections
- Multiple `markdown` elements for better spacing
- `column_set` for side-by-side content (e.g., before/after diffs)

**Files:**
- `internal/channel/lark/lark.go` — replace `buildCard()` with `buildStructuredCard()`
  that parses the markdown into Lark card elements.
- `internal/channel/lark/card_builder.go` — new file with card element construction
  helpers.

**Scope:** ~200 lines. Significant but self-contained in the Lark channel package.

---

## Tier 3 — Differentiating Features

### 7. File / Document Handling

**Problem:** Users can only send text and images. Sending a PDF, spreadsheet, or code
file is ignored.

**Solution:** Handle Lark file message types. Download the file, extract text (plain text
for code, pdf-to-text for PDFs), and include in the LLM context as a user attachment.

**Files:**
- `internal/channel/lark/lark.go` — `extractContent()`: handle `"file"` message type,
  download via Lark message resource API.
- `internal/channel/channel.go` — add `Attachments []Attachment` to `IncomingMessage`.
- `internal/agent/session.go` — include attachment text in the user message context.

**Scope:** ~150 lines + dependency on a text extraction library for PDFs.

---

### 8. Proactive Scheduled Messages [DONE]

**Problem:** The scheduler tool exists but has no way to push messages to Lark when a
scheduled task fires.

**Solution:** When a scheduled task triggers, resolve its target chat and send the
message via the Lark channel. Users can say "remind me at 3pm to deploy" and get a card
at 3pm.

**Files:**
- `internal/scheduler/scheduler.go` — on task fire, call a `NotifyFunc` callback.
- `internal/app/app.go` — wire `NotifyFunc` to the Lark channel's `Send()`.
- `internal/tools/builtin/schedule_tool.go` — store `chat_id` in the task metadata.

**Scope:** ~100 lines. Mostly wiring; scheduler infra already exists.

---

### 9. Thread Support

**Problem:** In busy group chats, bot replies clutter the main conversation. Multi-turn
exchanges are hard to follow.

**Solution:** Reply in Lark message threads using `root_id`. First bot reply creates a
thread; subsequent replies in the same session use the same thread root.

**Files:**
- `internal/channel/lark/lark.go` — `Send()` and `SendStream()`: accept optional
  `root_id`. Track `chatID → rootMessageID` mapping.
- `internal/channel/channel.go` — add `ThreadID` field to `IncomingMessage`.
- `internal/agent/manager.go` — associate session with thread root ID.

**Scope:** ~80 lines. Lark API supports `root_id` natively; main work is plumbing.

---

## Tier 4 — Future UX Ideas

### 10. Typing Indicator Between Tool Calls

**Problem:** During multi-step ReAct loops, the user sees the thinking card but has no
sense of progress. They don't know if the bot is stuck or working on step 5 of 10.

**Solution:** Patch the thinking card with a brief progress note when tool calls complete:
e.g. "Working... (reading files)" or "Working... (searching web)". This gives users a
sense of forward motion without exposing raw tool names.

### 11. Welcome/Onboarding Card

**Problem:** New users don't know what the bot can do. The first interaction has no
guidance.

**Solution:** Send a welcome card on first message in a new session. Include the bot's
name, capabilities, example prompts, and a link to `/help`. Only trigger when session has
zero tape entries.

### 12. Multi-Language Slash Command Responses

**Problem:** Slash command responses (`/help`, `/status`) are always in English. In
Chinese-speaking teams this creates friction.

**Solution:** Detect the user's language from their most recent message (using the
existing `isMostlyCJK` heuristic) and respond in the matching language.

### 13. Collapsible Long Outputs

**Problem:** Long tool outputs (file reads, search results) create very tall cards that
push prior messages off screen.

**Solution:** Use Lark's `collapsible_panel` element for tool output sections over a
threshold (e.g. 20 lines). The user sees a summary and can expand for full content.

### 14. Pinned System Status Card

**Problem:** In active group chats, status information (model, token usage, session age)
is buried in the message stream.

**Solution:** Maintain a pinned card at the top of the chat that auto-updates with
session stats. Update it on each response. Requires Lark's pin message API.

