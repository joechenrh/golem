#!/usr/bin/env python3
"""mem9 handler — JSON-RPC 2.0 tool server and lifecycle hook handler.

Two modes:
  --mode=tool  --tool=<name>   Long-running JSON-RPC server (stdin/stdout).
  --mode=hook                  One-shot hook handler (stdin → stdout).

Environment:
  MEM9_API_URL   Base URL of the mem9 API (default: https://api.mem9.ai)
  MEM9_SPACE_ID  Space / tenant ID (required)
"""

import argparse
import json
import os
import sys
import urllib.error
import urllib.request

API_URL = os.environ.get("MEM9_API_URL", "https://api.mem9.ai").rstrip("/")
SPACE_ID = os.environ.get("MEM9_SPACE_ID", "")
API_BASE = f"{API_URL}/v1alpha1/mem9s/{SPACE_ID}"


# ---------------------------------------------------------------------------
# HTTP helpers
# ---------------------------------------------------------------------------

def _request(method, path, body=None, params=None):
    """Send an HTTP request to the mem9 API and return parsed JSON."""
    url = f"{API_BASE}{path}"
    if params:
        qs = "&".join(f"{k}={urllib.request.quote(str(v))}" for k, v in params.items() if v is not None)
        if qs:
            url = f"{url}?{qs}"

    data = json.dumps(body).encode() if body else None
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Content-Type", "application/json")

    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            return json.loads(resp.read().decode())
    except urllib.error.HTTPError as exc:
        error_body = exc.read().decode() if exc.fp else str(exc)
        raise RuntimeError(f"mem9 API {method} {path}: {exc.code} — {error_body}") from exc
    except urllib.error.URLError as exc:
        raise RuntimeError(f"mem9 API {method} {path}: {exc.reason}") from exc


# ---------------------------------------------------------------------------
# Tool implementations
# ---------------------------------------------------------------------------

def tool_memory_store(params):
    content = params.get("content", "")
    if not content:
        return "Error: 'content' is required"
    body = {"content": content}
    if params.get("tags"):
        body["tags"] = params["tags"]
    if params.get("source"):
        body["source"] = params["source"]
    result = _request("POST", "/memories", body=body)
    mem_id = result.get("id", "unknown")
    return f"Memory stored (id: {mem_id})"


def tool_memory_search(params):
    query = params.get("query", "")
    if not query:
        return "Error: 'query' is required"
    limit = params.get("limit", 10)
    result = _request("GET", "/memories", params={"q": query, "limit": limit})
    memories = result if isinstance(result, list) else result.get("memories", [])
    if not memories:
        return f"No memories found for: {query}"
    lines = [f"Found {len(memories)} memories:\n"]
    for i, m in enumerate(memories, 1):
        header = f"{i}."
        if m.get("source"):
            header += f" by {m['source']}"
        if m.get("tags"):
            header += f" [{', '.join(m['tags'])}]"
        lines.append(header)
        content = m.get("content", "")
        if len(content) > 500:
            content = content[:500] + "..."
        lines.append(f"   {content}")
        if m.get("score") is not None:
            lines.append(f"   score: {m['score']:.4f}")
        lines.append("")
    return "\n".join(lines)


def tool_memory_get(params):
    mem_id = params.get("id", "")
    if not mem_id:
        return "Error: 'id' is required"
    result = _request("GET", f"/memories/{mem_id}")
    return json.dumps(result, indent=2)


def tool_memory_update(params):
    mem_id = params.get("id", "")
    content = params.get("content", "")
    if not mem_id:
        return "Error: 'id' is required"
    if not content:
        return "Error: 'content' is required"
    _request("PUT", f"/memories/{mem_id}", body={"content": content})
    return f"Memory updated (id: {mem_id})"


def tool_memory_delete(params):
    mem_id = params.get("id", "")
    if not mem_id:
        return "Error: 'id' is required"
    _request("DELETE", f"/memories/{mem_id}")
    return f"Memory deleted (id: {mem_id})"


TOOLS = {
    "memory_store": tool_memory_store,
    "memory_search": tool_memory_search,
    "memory_get": tool_memory_get,
    "memory_update": tool_memory_update,
    "memory_delete": tool_memory_delete,
}


# ---------------------------------------------------------------------------
# JSON-RPC 2.0 server (tool mode)
# ---------------------------------------------------------------------------

def run_tool_server(tool_name):
    if not SPACE_ID:
        print(json.dumps({
            "jsonrpc": "2.0", "id": 0,
            "error": {"code": -32600, "message": "MEM9_SPACE_ID not set"},
        }), flush=True)
        return

    handler = TOOLS.get(tool_name)
    if not handler:
        print(json.dumps({
            "jsonrpc": "2.0", "id": 0,
            "error": {"code": -32601, "message": f"unknown tool: {tool_name}"},
        }), flush=True)
        return

    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
        except json.JSONDecodeError:
            print(json.dumps({
                "jsonrpc": "2.0", "id": None,
                "error": {"code": -32700, "message": "parse error"},
            }), flush=True)
            continue

        req_id = req.get("id", 0)
        params = req.get("params", {})
        if isinstance(params, str):
            try:
                params = json.loads(params)
            except json.JSONDecodeError:
                params = {}

        try:
            result = handler(params)
            print(json.dumps({
                "jsonrpc": "2.0", "id": req_id,
                "result": result,
            }), flush=True)
        except Exception as exc:
            print(json.dumps({
                "jsonrpc": "2.0", "id": req_id,
                "error": {"code": -32000, "message": str(exc)},
            }), flush=True)


# ---------------------------------------------------------------------------
# Hook mode
# ---------------------------------------------------------------------------

def run_hook():
    if not SPACE_ID:
        print(json.dumps({"content": ""}))
        return

    raw = sys.stdin.read().strip()
    if not raw:
        print(json.dumps({"content": ""}))
        return

    try:
        event = json.loads(raw)
    except json.JSONDecodeError:
        print(json.dumps({"content": ""}))
        return

    event_type = event.get("event", "")

    if event_type == "before_llm_call":
        user_msg = event.get("user_message", "")
        if not user_msg:
            print(json.dumps({"content": ""}))
            return
        try:
            result = _request("GET", "/memories", params={"q": user_msg, "limit": 5})
            memories = result if isinstance(result, list) else result.get("memories", [])
            if not memories:
                print(json.dumps({"content": ""}))
                return
            lines = ["[Relevant memories from previous sessions]"]
            for m in memories:
                content = m.get("content", "")
                if len(content) > 300:
                    content = content[:300] + "..."
                lines.append(f"- {content}")
            print(json.dumps({"content": "\n".join(lines)}))
        except Exception:
            print(json.dumps({"content": ""}))

    elif event_type == "after_reset":
        summary = event.get("summary", "")
        if not summary:
            print(json.dumps({"content": ""}))
            return
        try:
            body = {"content": summary, "tags": ["session-summary"], "source": "golem"}
            _request("POST", "/memories", body=body)
            print(json.dumps({"content": "Session summary saved to mem9."}))
        except Exception as exc:
            print(json.dumps({"content": f"Failed to save summary: {exc}"}))

    else:
        print(json.dumps({"content": ""}))


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(description="mem9 handler")
    parser.add_argument("--mode", choices=["tool", "hook"], required=True)
    parser.add_argument("--tool", default="")
    args = parser.parse_args()

    if args.mode == "tool":
        if not args.tool:
            print("Error: --tool is required in tool mode", file=sys.stderr)
            sys.exit(1)
        run_tool_server(args.tool)
    else:
        run_hook()


if __name__ == "__main__":
    main()
