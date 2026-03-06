# 10 — Memory System

## 1. Overview

Golem has two independent memory subsystems. **Shared memory** is backed by TiDB Cloud Serverless (HTTP Data API), scoped across sessions and agents, and exposed through the `memory_store` and `memory_recall` tools. **Persona memory** is backed by a local `MEMORY.md` file on disk, scoped per-agent, and exposed through the `persona_memory` tool.

Shared memory provides persistent key-value storage with hybrid search (vector + keyword). Persona memory is a simple read/write scratchpad for an agent's curated notes.

All shared-memory code lives in `internal/memory/client.go`. The tool wrappers live in `internal/tools/builtin/memory_tool.go` and `internal/tools/builtin/persona_memory_tool.go`.

## 2. Client

`memory.Client` talks to TiDB Cloud Serverless via its HTTP Data API endpoint (`https://http-{host}/v1beta/sql`). Authentication is HTTP Basic Auth. Every SQL statement is sent as a JSON POST body containing the database name and query string. The response carries column type metadata and row data.

Key constructor parameters are `host`, `user`, `pass`, and `dbName` (defaults to `"mnemos"`). When `autoEmbedModel` is non-empty (e.g. `"tidbcloud_free/amazon/titan-embed-text-v2"`), TiDB's built-in embedding generation is enabled. `autoEmbedDims` controls vector dimensions (default 1024).

### Auto-init (InitSchema)

`InitSchema` runs once via `sync.Once` and performs three idempotent DDL steps. It creates the `memories` table with columns for identity (`id`, `space_id`), content (`content`, `key_name`, `source`), structured metadata (`tags` as JSON, `metadata` as JSON), a vector `embedding` column, versioning (`version`, `updated_by`), and timestamps. When `autoEmbedModel` is set the embedding column is a generated stored column that calls TiDB's `EMBED_TEXT` function; otherwise it is a nullable vector. After creating the table, it adds a vector index using cosine distance and a full-text search index on `content` with a multilingual parser.

After DDL, a probe query tests whether FTS is operational. If it succeeds, `ftsAvailable` is set to `true` and subsequent keyword searches use FTS; otherwise they fall back to `LIKE`.

## 3. Store

Source: `internal/memory/client.go`

`Client.Store` inserts a single memory row with a generated UUID as `id`, a hardcoded `space_id` of `"default"`, the caller-supplied content, key, source, and tags (marshalled as a JSON array or `NULL`). All string values are escaped through three helpers: `sqlEscape` escapes dangerous characters inside a string, `sqlQ` wraps the result in single quotes, and `sqlNullableQ` returns `NULL` for empty strings and delegates to `sqlQ` otherwise. These are used everywhere user-controlled values appear in SQL because the TiDB HTTP Data API does not support parameterised queries.

## 4. Search

`Client.Search` dispatches to one of two paths based on whether `autoEmbedModel` is configured. Both paths request `fetch = limit * 3` rows from each leg to give RRF enough candidates.

**Simple path (no autoEmbedModel).** Calls `keywordSearch`, which builds SQL using FTS when available (`WHERE fts_match_word(query, content)` ordered by FTS score descending) or falls back to `LIKE` matching ordered by `updated_at DESC`. Results are truncated to `limit`.

**Hybrid path (with autoEmbedModel).** Calls `hybridSearch`, which runs two legs sequentially. The vector leg uses `VEC_EMBED_COSINE_DISTANCE` to find the nearest vectors by cosine distance, with TiDB auto-embedding the query string inline. The keyword leg uses the same FTS-or-LIKE logic as the simple path. Results from both legs are merged with RRF.

## 5. RRF Merge

`rrfMerge` combines two ranked result lists using Reciprocal Rank Fusion.

Scoring formula (per result, per leg):

```
score += 1 / (K + rank + 1)
```

where `K = 60` and `rank` is the 0-based position in that leg's result list. A memory appearing in both legs gets the sum of its two scores. After scoring, memories are sorted by descending score, truncated to `limit`, and each memory's `.Score` field is set to its RRF score. The constant K = 60 is the standard value from the original RRF paper; it dampens the influence of high-ranked results so that appearing in both legs matters more than topping one leg.

## 6. Hybrid Search Error Handling

`hybridSearch` tolerates single-leg failures. When both legs succeed, a normal RRF merge runs. When one leg fails, the surviving leg's results are passed to `rrfMerge` with an empty slice for the failed leg; the merge still works, with scores coming from one leg only. When both legs fail, an error is returned combining both error messages. This makes the system resilient to transient vector-index or FTS-index issues.

## 7. Memory Tools

Source: `internal/tools/builtin/memory_tool.go`

Both tools hold a `*memory.Client` and delegate directly to it.

### memory_store

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `content` | string | yes | The information to remember |
| `tags` | string[] | no | 1-3 short categorization tags |
| `key` | string | no | Unique key for upsert semantics |
| `source` | string | no | Source agent identifier (default `"golem"`) |

Returns `"Memory stored (id: <uuid>)"` on success.

### memory_recall

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `query` | string | yes | Search query (2-3 keywords recommended) |
| `limit` | integer | no | Max results (default 10) |

Returns a formatted listing: index, key, source, tags, content (truncated to 500 chars), and RRF score when available.

## 8. Persona Memory Tool

Source: `internal/tools/builtin/persona_memory_tool.go`

This tool operates on a local file (`~/.golem/agent/<name>/MEMORY.md`), not TiDB.

### persona_memory

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `action` | `"read"` or `"write"` | yes | Operation to perform |
| `content` | string | write only | Full replacement content for MEMORY.md |

A **read** returns file contents, or a message indicating the file does not exist or is empty. A **write** atomically replaces the file via `os.WriteFile`, creating parent directories if needed.

### How it differs from the TiDB tools

| Dimension | `persona_memory` | `memory_store` / `memory_recall` |
|-----------|-------------------|----------------------------------|
| Storage | Local filesystem | TiDB Cloud Serverless |
| Scope | Single agent | Shared across all agents and sessions |
| Search | None (whole-file read) | Hybrid vector + keyword |
| Structure | Free-form Markdown | Structured rows (content, key, tags, source) |
| Persistence | Survives restarts, local to host | Cloud-hosted, survives host loss |
| Size guidance | Under 200 lines | Unbounded (row-per-memory) |

`persona_memory` is designed for an agent's curated self-knowledge (project conventions, user preferences). The TiDB-backed tools are for factual memories that benefit from search and cross-agent sharing.

## 9. Current Gaps

1. **No upsert on key** -- `Store` always inserts. If a memory with the same `(space_id, key_name)` already exists, the insert fails due to the UNIQUE index. There is no "on duplicate key update" path.
2. **No delete or update** -- once stored, memories cannot be modified or removed through the tool interface.
3. **No tag filtering** -- `Search` ignores tags entirely; there is no JSON containment filter.
4. **Single space** -- `space_id` is hardcoded to `"default"`. Multi-tenant or per-agent spaces are not supported.
5. **Sequential hybrid legs** -- the two search legs run sequentially because the TiDB HTTP API handles one query at a time. Running them in parallel with separate HTTP requests could halve latency.
6. **No parameterised queries** -- all values are manually escaped via `sqlEscape`. This is a limitation of the TiDB HTTP Data API rather than an oversight, but it increases injection surface area.
7. **Persona memory is whole-file replace** -- the `write` action replaces the entire file. There is no append or patch operation, so the agent must read, merge, and rewrite.
