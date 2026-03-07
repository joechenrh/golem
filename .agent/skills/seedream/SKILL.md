---
name: seedream
description: Generate images using Volcengine Seedream API
---

# Seedream Image Generation

Generate images via the Volcengine Seedream API. **Act immediately** — call `shell_exec` with the curl command below. Do NOT ask for confirmation.

## Quick Start

```bash
curl -s -X POST https://ark.cn-beijing.volces.com/api/v3/images/generations \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ARK_API_KEY" \
  -d '{
    "model": "doubao-seedream-5-0-260128",
    "prompt": "<YOUR PROMPT HERE>",
    "response_format": "url",
    "size": "2K",
    "stream": false,
    "watermark": false
  }'
```

Set `shell_exec` timeout to 120 seconds — image generation takes 10-30s.

## Parameters

| Parameter | Default | Values |
|-----------|---------|--------|
| `model` | `doubao-seedream-5-0-260128` | Also: `doubao-seedream-4-5-251128` |
| `size` | `2K` | `1K`, `2K`, `4K` |
| `response_format` | `url` | `url`, `b64_json` |
| `seed` | random | Integer for reproducibility |

## Response

The API returns JSON with `data[0].url` containing the image URL.

## After Generation

Parse the JSON response. Extract the URL from `data[0].url`, then:

- **On Lark**: Call `lark_send` with `chat_id` (from conversation context) and `image` set to the URL. This uploads and displays the image natively.
- **Other channels**: Return the URL as `![image](<url>)`.

## Prompt Tips

If the user's request is vague, enrich the prompt with style, lighting, composition, and mood details. Support both Chinese and English prompts.
