---
name: seedream
description: Generate images using Volcengine Seedream API
---

# Seedream Image Generation Skill

Generate images by calling the Volcengine (火山方舟) Seedream API via `shell_exec`.

## API Details

- **Endpoint**: `https://ark.cn-beijing.volces.com/api/v3/images/generations`
- **Method**: POST
- **Auth**: `Authorization: Bearer $ARK_API_KEY`

## Request Format

```json
{
  "model": "<model_id>",
  "prompt": "<description of the image>",
  "size": "2K",
  "response_format": "url"
}
```

### Parameters

| Parameter | Required | Default | Description |
|-----------|----------|---------|-------------|
| `model` | yes | — | Model ID (see below) |
| `prompt` | yes | — | Image description, supports Chinese and English. Max ~300 Chinese chars or ~600 English words |
| `size` | no | `2K` | Output resolution: `1K`, `2K`, or `4K`. Can also use `<width>x<height>` (range 720–4096 per side) |
| `response_format` | no | `url` | `url` returns a download URL; `b64_json` returns base64-encoded image |
| `seed` | no | random | Integer seed for reproducibility. Same seed + same prompt = same image |
| `watermark` | no | false | Add invisible watermark |
| `image` | no | — | Array of reference image URLs for image-to-image or multi-image fusion |
| `sequential_image_generation` | no | `disabled` | Set to `enabled` to generate a batch of related images |

### Available Models

| Model ID | Version |
|----------|---------|
| `doubao-seedream-5-0-260128` | Seedream 5.0 (default) |
| `doubao-seedream-5-0-260128` | Seedream 5.0 Lite |
| `doubao-seedream-4-5-251128` | Seedream 4.5 |
| `doubao-seedream-4-0-250828` | Seedream 4.0 |

Use `doubao-seedream-5-0-260128` unless the user specifies otherwise.

## Response Format

```json
{
  "created": 1726051200,
  "data": [
    {
      "url": "https://...",
      "revised_prompt": "..."
    }
  ]
}
```

The `url` field contains a temporary download link for the generated image. Extract and return it to the user.

## How to Call

Use `shell_exec` with `curl`:

```bash
curl -s -X POST https://ark.cn-beijing.volces.com/api/v3/images/generations \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ARK_API_KEY" \
  -d '{
    "model": "doubao-seedream-5-0-260128",
    "prompt": "a cat sitting on a windowsill at sunset, watercolor style",
    "sequential_image_generation": "disabled",
    "response_format": "url",
    "size": "2K",
    "stream": false,
    "watermark": false
  }'
```

## Workflow

1. Compose a detailed prompt based on the user's request (add style, lighting, composition details if not specified)
2. Choose the appropriate model (default to 5.0 Lite)
3. Call the API via `shell_exec` using `curl`
4. Parse the JSON response and extract the image URL from `data[0].url`
5. If the current channel is Lark (chat_id is available from the conversation context), use `lark_send` with `chat_id` and `image` set to the URL to send the image natively
6. Otherwise, return the URL as a markdown image: `![Generated Image](<url>)`

## Tips for Better Prompts

- Be specific about style: "realistic photo", "watercolor", "oil painting", "anime style", "flat illustration"
- Include composition details: "close-up", "wide angle", "bird's eye view"
- Describe lighting: "golden hour", "studio lighting", "neon glow"
- Specify mood: "serene", "dramatic", "playful"
- For Chinese prompts, be equally descriptive: "水彩风格的猫咪坐在窗台上，夕阳余晖，温暖色调"
