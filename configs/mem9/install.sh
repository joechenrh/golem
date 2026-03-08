#!/usr/bin/env bash
# Install mem9 memory integration for a golem agent.
#
# Usage:
#   ./install.sh                # install globally (all agents)
#   ./install.sh --agent=larkbot # install hook for a specific agent
#
# Prerequisites: python3, curl

set -euo pipefail

GOLEM_HOME="${GOLEM_HOME:-$HOME/.golem}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
AGENT=""

for arg in "$@"; do
    case "$arg" in
        --agent=*) AGENT="${arg#*=}" ;;
        *) echo "Unknown argument: $arg"; exit 1 ;;
    esac
done

# --- Preflight checks ---

if ! command -v python3 &>/dev/null; then
    echo "Error: python3 is required but not found."
    exit 1
fi

if ! command -v curl &>/dev/null; then
    echo "Error: curl is required but not found."
    exit 1
fi

# --- Determine config file for env vars ---

if [ -n "$AGENT" ]; then
    CONFIG_FILE="$GOLEM_HOME/agents/$AGENT/config.env"
    mkdir -p "$(dirname "$CONFIG_FILE")"
else
    CONFIG_FILE="$GOLEM_HOME/config.env"
    mkdir -p "$GOLEM_HOME"
fi

# --- Step 1: Check or provision MEM9_SPACE_ID ---

EXISTING_SPACE_ID=""
if [ -f "$CONFIG_FILE" ]; then
    EXISTING_SPACE_ID=$(grep -oP '(?<=^MEM9_SPACE_ID=).*' "$CONFIG_FILE" 2>/dev/null || true)
fi

if [ -n "$EXISTING_SPACE_ID" ]; then
    echo "Found existing MEM9_SPACE_ID=$EXISTING_SPACE_ID in $CONFIG_FILE"
    SPACE_ID="$EXISTING_SPACE_ID"
else
    echo "Provisioning new mem9 space..."
    RESPONSE=$(curl -s -X POST https://api.mem9.ai/v1alpha1/mem9s \
        -H "Content-Type: application/json" \
        -d '{"name":"golem"}')
    SPACE_ID=$(echo "$RESPONSE" | python3 -c "import sys,json; print(json.load(sys.stdin)['id'])" 2>/dev/null || true)

    if [ -z "$SPACE_ID" ]; then
        echo "Error: failed to provision mem9 space. Response:"
        echo "$RESPONSE"
        exit 1
    fi

    echo "Provisioned mem9 space: $SPACE_ID"

    # Write env vars to config file.
    {
        echo ""
        echo "# mem9 Cloud Memory"
        echo "MEM9_API_URL=https://api.mem9.ai"
        echo "MEM9_SPACE_ID=$SPACE_ID"
    } >> "$CONFIG_FILE"
    echo "Wrote MEM9_API_URL and MEM9_SPACE_ID to $CONFIG_FILE"
fi

# --- Step 2: Install handler script + tool manifests ---

mkdir -p "$GOLEM_HOME/plugins"

cp "$SCRIPT_DIR/mem9_handler.py" "$GOLEM_HOME/plugins/mem9_handler.py"
chmod +x "$GOLEM_HOME/plugins/mem9_handler.py"
echo "Installed handler: $GOLEM_HOME/plugins/mem9_handler.py"

for f in "$SCRIPT_DIR"/plugins/*.tool.json; do
    BASENAME="$(basename "$f")"
    sed "s|__GOLEM_HOME__|$GOLEM_HOME|g" "$f" > "$GOLEM_HOME/plugins/$BASENAME"
    echo "Installed tool manifest: $BASENAME"
done

# --- Step 3: Install context hook ---

if [ -n "$AGENT" ]; then
    HOOK_DIR="$GOLEM_HOME/agents/$AGENT/hooks/mem9-context"
else
    HOOK_DIR="$GOLEM_HOME/hooks/mem9-context"
fi

mkdir -p "$HOOK_DIR"
cp "$SCRIPT_DIR/hooks/mem9-context/HOOK.md" "$HOOK_DIR/"
cp "$SCRIPT_DIR/hooks/mem9-context/handler.py" "$HOOK_DIR/"
chmod +x "$HOOK_DIR/handler.py"
echo "Installed hook: $HOOK_DIR"

# --- Step 4: Verify ---

echo ""
echo "Verifying mem9 connection..."

STORE_RESULT=$(echo '{"jsonrpc":"2.0","id":1,"method":"execute","params":{"content":"mem9 install verification"}}' | \
    MEM9_SPACE_ID="$SPACE_ID" python3 "$GOLEM_HOME/plugins/mem9_handler.py" --mode=tool --tool=memory_store 2>/dev/null || true)

if echo "$STORE_RESULT" | python3 -c "import sys,json; r=json.load(sys.stdin); assert 'result' in r" 2>/dev/null; then
    echo "OK — test memory stored successfully."
else
    echo "Warning: verification failed. Response: $STORE_RESULT"
    echo "Check MEM9_API_URL and MEM9_SPACE_ID, then restart golem."
    exit 1
fi

echo ""
echo "Done! Restart golem to load the new plugins and hooks."
echo "Run ,tools in chat to confirm memory_store/memory_search are available."
