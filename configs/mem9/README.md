# mem9 Integration for Golem

Cloud-based shared memory via [mem9](https://mem9.ai), installed as external tools and hooks.

## Quick Setup

```sh
GOLEM_HOME=~/.golem

# 1. Provision a mem9 space
SPACE_ID=$(curl -s -X POST https://api.mem9.ai/v1alpha1/mem9s \
  -H "Content-Type: application/json" \
  -d '{"name":"golem"}' | python3 -c "import sys,json; print(json.load(sys.stdin)['id'])")

# 2. Configure env vars
echo "MEM9_API_URL=https://api.mem9.ai" >> "$GOLEM_HOME/config.env"
echo "MEM9_SPACE_ID=$SPACE_ID" >> "$GOLEM_HOME/config.env"

# 3. Install handler + tool manifests
cp configs/mem9/mem9_handler.py "$GOLEM_HOME/plugins/"
chmod +x "$GOLEM_HOME/plugins/mem9_handler.py"
for f in configs/mem9/plugins/*.tool.json; do
    sed "s|__GOLEM_HOME__|$GOLEM_HOME|g" "$f" > "$GOLEM_HOME/plugins/$(basename "$f")"
done

# 4. Install hooks (optional)
for hook in mem9-recall mem9-save; do
    mkdir -p "$GOLEM_HOME/hooks/$hook"
    cp configs/mem9/hooks/$hook/* "$GOLEM_HOME/hooks/$hook/"
    chmod +x "$GOLEM_HOME/hooks/$hook/handler.py"
done

# 5. Restart golem
```

## What's Included

| Component | Files | Purpose |
|-----------|-------|---------|
| **Handler** | `mem9_handler.py` | Python3 script with JSON-RPC tool server + hook handler |
| **Tools** | `plugins/*.tool.json` | 5 tool manifests (store, search, get, update, delete) |
| **Hook** | `hooks/mem9-recall/` | Injects relevant memories before LLM calls |
| **Hook** | `hooks/mem9-save/` | Saves session summaries to mem9 on reset |
| **Skill** | `skills/mem9/SKILL.md` | Interactive setup guide (invoke with `,skills` → `skill_mem9`) |

## Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `MEM9_API_URL` | No | `https://api.mem9.ai` | mem9 API base URL |
| `MEM9_SPACE_ID` | Yes | — | Space / tenant identifier |

### Per-Agent Env via HOOK.md

Instead of setting `MEM9_SPACE_ID` globally in `config.env`, you can declare it per-hook in `HOOK.md`:

```yaml
env:
  MEM9_SPACE_ID: space-abc123
```

This is useful when different agents need different spaces. Hook-level env vars override the parent environment.
