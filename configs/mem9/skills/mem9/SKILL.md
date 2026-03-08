---
name: mem9
description: Set up mem9 cloud memory integration for golem
---

# mem9 Setup

Follow these steps to configure the mem9 cloud memory integration.

## Step 1: Check Prerequisites

Run `python3 --version` to verify Python 3 is available. mem9 tools require Python 3.6+.

## Step 2: Check Existing Configuration

Read the agent's `config.env` file to check if `MEM9_SPACE_ID` is already set.
If it is, skip to Step 5 (verify).

## Step 3: Provision a mem9 Space

Create a new mem9 space:

```sh
curl -s -X POST https://api.mem9.ai/v1alpha1/mem9s \
  -H "Content-Type: application/json" \
  -d '{"name": "golem"}'
```

Extract the space ID from the response.

## Step 4: Configure Environment

Add the following to the agent's `config.env` (at `~/.golem/agents/<name>/config.env` or `~/.golem/config.env`):

```
MEM9_API_URL=https://api.mem9.ai
MEM9_SPACE_ID=<space-id-from-step-3>
```

## Step 5: Install Tool Plugins

Copy the handler script and generate tool manifests:

```sh
GOLEM_HOME=~/.golem

# Copy handler script
cp configs/mem9/mem9_handler.py "$GOLEM_HOME/plugins/mem9_handler.py"
chmod +x "$GOLEM_HOME/plugins/mem9_handler.py"

# Copy and configure tool manifests
for f in configs/mem9/plugins/*.tool.json; do
    sed "s|__GOLEM_HOME__|$GOLEM_HOME|g" "$f" > "$GOLEM_HOME/plugins/$(basename "$f")"
done
```

## Step 6: Install Hook (Optional)

Copy the context hook for automatic memory injection:

```sh
GOLEM_HOME=~/.golem
mkdir -p "$GOLEM_HOME/hooks/mem9-context"
cp configs/mem9/hooks/mem9-context/HOOK.md "$GOLEM_HOME/hooks/mem9-context/"
cp configs/mem9/hooks/mem9-context/handler.py "$GOLEM_HOME/hooks/mem9-context/"
chmod +x "$GOLEM_HOME/hooks/mem9-context/handler.py"
```

## Step 7: Verify

Test the integration by storing and searching a memory:

```sh
# Store a test memory
echo '{"jsonrpc":"2.0","id":1,"method":"execute","params":{"content":"test memory from setup"}}' | \
  MEM9_SPACE_ID=<your-space-id> python3 ~/.golem/plugins/mem9_handler.py --mode=tool --tool=memory_store

# Search for it
echo '{"jsonrpc":"2.0","id":2,"method":"execute","params":{"query":"test setup"}}' | \
  MEM9_SPACE_ID=<your-space-id> python3 ~/.golem/plugins/mem9_handler.py --mode=tool --tool=memory_search
```

## Step 8: Restart

Restart golem to pick up the new plugins and hooks. The `memory_store`, `memory_search`, `memory_get`, `memory_update`, and `memory_delete` tools should appear in `,tools`.
