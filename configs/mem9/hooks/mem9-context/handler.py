#!/usr/bin/env python3
"""mem9 context hook — standalone handler for before_llm_call and after_reset events.

Reads event JSON from stdin, calls mem9 API, returns JSON on stdout.

Environment:
  MEM9_API_URL   Base URL (default: https://api.mem9.ai)
  MEM9_SPACE_ID  Space / tenant ID (required)
"""

import json
import os
import sys
import urllib.error
import urllib.request

API_URL = os.environ.get("MEM9_API_URL", "https://api.mem9.ai").rstrip("/")
SPACE_ID = os.environ.get("MEM9_SPACE_ID", "")
API_BASE = f"{API_URL}/v1alpha1/mem9s/{SPACE_ID}"


def _request(method, path, body=None, params=None):
    url = f"{API_BASE}{path}"
    if params:
        qs = "&".join(f"{k}={urllib.request.quote(str(v))}" for k, v in params.items() if v is not None)
        if qs:
            url = f"{url}?{qs}"

    data = json.dumps(body).encode() if body else None
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Content-Type", "application/json")

    with urllib.request.urlopen(req, timeout=15) as resp:
        return json.loads(resp.read().decode())


def main():
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


if __name__ == "__main__":
    main()
