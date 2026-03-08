#!/usr/bin/env python3
"""mem9 save hook — saves session summaries to mem9 on reset.

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


def _request(method, path, body=None):
    url = f"{API_BASE}{path}"
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

    data = event.get("data", {})
    summary = data.get("summary", "")
    if not summary:
        print(json.dumps({"content": ""}))
        return

    try:
        body = {"content": summary, "tags": ["session-summary"], "source": "golem"}
        _request("POST", "/memories", body=body)
        print(json.dumps({"content": "Session summary saved to mem9."}))
    except Exception as exc:
        print(json.dumps({"content": f"Failed to save summary: {exc}"}))


if __name__ == "__main__":
    main()
