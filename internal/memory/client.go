package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
)

const defaultSpaceID = "default"

// Memory represents a single memory object stored in mnemos.
type Memory struct {
	ID        string   `json:"id"`
	Content   string   `json:"content"`
	Key       string   `json:"key,omitempty"`
	Source    string   `json:"source,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Version   int      `json:"version"`
	UpdatedBy string   `json:"updated_by,omitempty"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
	Score     *float64 `json:"score,omitempty"`
}

// Client talks directly to TiDB Cloud Serverless via its HTTP Data API.
// This implements mnemos "direct mode" — no mnemo-server required.
type Client struct {
	httpClient     *http.Client
	apiURL         string // https://http-{host}/v1beta/sql
	user           string
	pass           string
	dbName         string
	autoEmbedModel string // e.g. "tidbcloud_free/amazon/titan-embed-text-v2"
	autoEmbedDims  int    // default 1024 for auto-embed

	initOnce     sync.Once
	ftsAvailable bool
}

// NewClient creates a new mnemos direct-mode client.
func NewClient(
	httpClient *http.Client,
	host, user, pass, dbName string,
	autoEmbedModel string, autoEmbedDims int,
) *Client {
	if dbName == "" {
		dbName = "mnemos"
	}
	if autoEmbedDims <= 0 {
		autoEmbedDims = 1024
	}
	return &Client{
		httpClient:     httpClient,
		apiURL:         "https://http-" + host + "/v1beta/sql",
		user:           user,
		pass:           pass,
		dbName:         dbName,
		autoEmbedModel: autoEmbedModel,
		autoEmbedDims:  autoEmbedDims,
	}
}

// NewClientForTest creates a Client with a custom apiURL for testing.
func NewClientForTest(
	httpClient *http.Client,
	apiURL, user, pass, dbName string,
) *Client {
	return &Client{
		httpClient: httpClient,
		apiURL:     apiURL,
		user:       user,
		pass:       pass,
		dbName:     dbName,
	}
}

// ---------------------------------------------------------------------------
// Schema initialization (mirrors mnemo_direct_init from common.sh)
// ---------------------------------------------------------------------------

// InitSchema creates the memories table and indexes if they don't exist.
// Safe to call multiple times — uses CREATE IF NOT EXISTS.
func (c *Client) InitSchema(ctx context.Context) {
	c.initOnce.Do(func() {
		c.doInitSchema(ctx)
	})
}

func (c *Client) doInitSchema(ctx context.Context) {
	var embeddingCol string
	if c.autoEmbedModel != "" {
		embeddingCol = fmt.Sprintf(
			"embedding VECTOR(%d) GENERATED ALWAYS AS (EMBED_TEXT('%s', content)) STORED,",
			c.autoEmbedDims, sqlEscape(c.autoEmbedModel),
		)
	} else {
		embeddingCol = "embedding VECTOR(1536) NULL,"
	}

	createSQL := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.memories (
  id          VARCHAR(36)       PRIMARY KEY,
  space_id    VARCHAR(36)       NOT NULL,
  content     TEXT              NOT NULL,
  key_name    VARCHAR(255),
  source      VARCHAR(100),
  tags        JSON,
  metadata    JSON,
  %s
  version     INT               DEFAULT 1,
  updated_by  VARCHAR(100),
  created_at  TIMESTAMP         DEFAULT CURRENT_TIMESTAMP,
  updated_at  TIMESTAMP         DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  UNIQUE INDEX idx_key    (space_id, key_name),
  INDEX idx_space         (space_id),
  INDEX idx_source        (space_id, source),
  INDEX idx_updated       (space_id, updated_at)
)`, c.dbName, embeddingCol)

	c.execSQL(ctx, createSQL) //nolint: ignore errors (IF NOT EXISTS)

	// Add vector index.
	c.execSQL(ctx, fmt.Sprintf(
		"ALTER TABLE %s.memories ADD VECTOR INDEX idx_cosine ((VEC_COSINE_DISTANCE(embedding)))",
		c.dbName,
	))

	// Add FTS index.
	c.execSQL(ctx, fmt.Sprintf(
		"ALTER TABLE %s.memories ADD FULLTEXT INDEX idx_fts_content (content) WITH PARSER MULTILINGUAL ADD_COLUMNAR_REPLICA_ON_DEMAND",
		c.dbName,
	))

	// Probe FTS availability (LIMIT 1 to actually exercise the index).
	probeSQL := fmt.Sprintf(
		"SELECT fts_match_word('probe', content) FROM %s.memories WHERE space_id = %s AND fts_match_word('probe', content) LIMIT 1",
		c.dbName, sqlQ(defaultSpaceID),
	)
	_, err := c.execSQL(ctx, probeSQL)
	c.ftsAvailable = err == nil
}

// ---------------------------------------------------------------------------
// SQL execution
// ---------------------------------------------------------------------------

// sqlResponse is the TiDB HTTP Data API response format.
type sqlResponse struct {
	Types []sqlColumn     `json:"types"`
	Rows  [][]any `json:"rows"`
}

type sqlColumn struct {
	Name string `json:"name"`
}

func (c *Client) execSQL(
	ctx context.Context, query string,
) (*sqlResponse, error) {
	body, err := json.Marshal(map[string]string{
		"database": c.dbName,
		"query":    query,
	})
	if err != nil {
		return nil, fmt.Errorf("marshalling SQL request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.user, c.pass)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("TiDB API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result sqlResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return &result, nil
}

// ---------------------------------------------------------------------------
// Row parsing helpers
// ---------------------------------------------------------------------------

func rowToMemory(row []any, colIdx map[string]int) Memory {
	m := Memory{}
	if i, ok := colIdx["id"]; ok {
		m.ID, _ = row[i].(string)
	}
	if i, ok := colIdx["content"]; ok {
		m.Content, _ = row[i].(string)
	}
	if i, ok := colIdx["key_name"]; ok {
		m.Key, _ = row[i].(string)
	}
	if i, ok := colIdx["source"]; ok {
		m.Source, _ = row[i].(string)
	}
	if i, ok := colIdx["tags"]; ok {
		if tagsStr, ok := row[i].(string); ok && tagsStr != "" {
			json.Unmarshal([]byte(tagsStr), &m.Tags)
		}
	}
	if i, ok := colIdx["version"]; ok {
		switch v := row[i].(type) {
		case float64:
			m.Version = int(v)
		case string:
			fmt.Sscanf(v, "%d", &m.Version)
		}
	}
	if i, ok := colIdx["updated_by"]; ok {
		m.UpdatedBy, _ = row[i].(string)
	}
	if i, ok := colIdx["created_at"]; ok {
		m.CreatedAt, _ = row[i].(string)
	}
	if i, ok := colIdx["updated_at"]; ok {
		m.UpdatedAt, _ = row[i].(string)
	}
	return m
}

func buildColIdx(types []sqlColumn) map[string]int {
	idx := make(map[string]int, len(types))
	for i, t := range types {
		idx[t.Name] = i
	}
	return idx
}

// sqlEscape escapes a string for safe inclusion in a SQL single-quoted literal.
// It handles the most common injection vectors: single quotes, backslashes,
// null bytes, and control characters.
func sqlEscape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\'':
			b.WriteString("''")
		case '\\':
			b.WriteString("\\\\")
		case '\x00':
			// Drop null bytes — they can truncate strings in some SQL engines.
		case '\n':
			b.WriteString("\\n")
		case '\r':
			b.WriteString("\\r")
		case '\x1a': // Ctrl+Z (EOF on Windows)
			b.WriteString("\\Z")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// sqlQ returns a single-quoted, escaped SQL string literal: 'escaped_value'.
// Use this for all user-controlled values in SQL queries to ensure escaping
// is never accidentally omitted.
func sqlQ(s string) string {
	return "'" + sqlEscape(s) + "'"
}

// sqlNullableQ returns NULL if s is empty, otherwise a quoted escaped literal.
func sqlNullableQ(s string) string {
	if s == "" {
		return "NULL"
	}
	return sqlQ(s)
}

func parseRows(result *sqlResponse) []Memory {
	if result == nil || len(result.Rows) == 0 {
		return nil
	}
	colIdx := buildColIdx(result.Types)
	memories := make([]Memory, 0, len(result.Rows))
	for _, row := range result.Rows {
		memories = append(memories, rowToMemory(row, colIdx))
	}
	return memories
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

// Store creates a new memory (mirrors mnemo_post_memory from common.sh).
func (c *Client) Store(
	ctx context.Context,
	content, key, source string, tags []string,
) (*Memory, error) {
	c.InitSchema(ctx)

	id := uuid.New().String()

	tagsVal := "NULL"
	if tags != nil {
		tagsJSON, _ := json.Marshal(tags)
		tagsVal = sqlQ(string(tagsJSON))
	}

	query := fmt.Sprintf(
		"INSERT INTO %s.memories (id, space_id, content, key_name, source, tags, version, updated_by) VALUES (%s, %s, %s, %s, %s, %s, 1, %s)",
		c.dbName, sqlQ(id), sqlQ(defaultSpaceID), sqlQ(content), sqlNullableQ(key), sqlNullableQ(source), tagsVal, sqlNullableQ(source),
	)

	if _, err := c.execSQL(ctx, query); err != nil {
		return nil, err
	}

	return &Memory{
		ID:      id,
		Content: content,
		Key:     key,
		Source:  source,
		Tags:    tags,
		Version: 1,
	}, nil
}

// ---------------------------------------------------------------------------
// Search (hybrid: vector + keyword with RRF, mirrors mnemo_search)
// ---------------------------------------------------------------------------

const rrfK = 60.0

// Search queries memories using hybrid search (vector + keyword with RRF merge).
// Falls back to keyword-only or LIKE depending on available features.
func (c *Client) Search(
	ctx context.Context, queryText string, limit int,
) ([]Memory, error) {
	c.InitSchema(ctx)

	if limit <= 0 {
		limit = 10
	}
	fetch := limit * 3

	// If auto-embed is configured, run hybrid search (vector + keyword).
	if c.autoEmbedModel != "" {
		return c.hybridSearch(ctx, queryText, limit, fetch)
	}

	// Otherwise, keyword-only search.
	return c.keywordSearch(ctx, queryText, limit, fetch)
}

func (c *Client) hybridSearch(
	ctx context.Context, query string,
	limit, fetch int,
) ([]Memory, error) {
	// Vector leg: cosine distance via auto-embed.
	qv := sqlQ(query)
	vecSQL := fmt.Sprintf(
		"SELECT id, content, key_name, source, tags, version, updated_by, created_at, updated_at, VEC_EMBED_COSINE_DISTANCE(embedding, %s) AS distance FROM %s.memories WHERE space_id = %s AND embedding IS NOT NULL ORDER BY VEC_EMBED_COSINE_DISTANCE(embedding, %s) LIMIT %d",
		qv, c.dbName, sqlQ(defaultSpaceID), qv, fetch,
	)

	// Keyword leg.
	kwSQL := c.keywordSQL(query, fetch)

	// Execute both legs (sequentially — TiDB HTTP API is one query at a time).
	// Individual leg errors are tolerated as long as at least one succeeds.
	vecResult, vecErr := c.execSQL(ctx, vecSQL)
	kwResult, kwErr := c.execSQL(ctx, kwSQL)

	if vecErr != nil && kwErr != nil {
		return nil, fmt.Errorf("both search legs failed: vec=%w, kw=%v", vecErr, kwErr)
	}

	vecRows := parseRows(vecResult)
	kwRows := parseRows(kwResult)

	return rrfMerge(vecRows, kwRows, limit), nil
}

func (c *Client) keywordSearch(
	ctx context.Context, query string,
	limit, fetch int,
) ([]Memory, error) {
	kwSQL := c.keywordSQL(query, fetch)

	result, err := c.execSQL(ctx, kwSQL)
	if err != nil {
		return nil, err
	}

	memories := parseRows(result)
	if len(memories) > limit {
		memories = memories[:limit]
	}
	return memories, nil
}

func (c *Client) keywordSQL(query string, fetch int) string {
	qv := sqlQ(query)
	qs := sqlQ(defaultSpaceID)
	if c.ftsAvailable {
		return fmt.Sprintf(
			"SELECT id, content, key_name, source, tags, version, updated_by, created_at, updated_at, fts_match_word(%s, content) AS fts_score FROM %s.memories WHERE space_id = %s AND fts_match_word(%s, content) ORDER BY fts_match_word(%s, content) DESC LIMIT %d",
			qv, c.dbName, qs, qv, qv, fetch,
		)
	}
	return fmt.Sprintf(
		"SELECT id, content, key_name, source, tags, version, updated_by, created_at, updated_at FROM %s.memories WHERE space_id = %s AND content LIKE CONCAT('%%', %s, '%%') ORDER BY updated_at DESC LIMIT %d",
		c.dbName, qs, qv, fetch,
	)
}

// rrfMerge merges two ranked lists using Reciprocal Rank Fusion (K=60).
func rrfMerge(vecRows, kwRows []Memory, limit int) []Memory {
	scores := make(map[string]float64)
	mems := make(map[string]Memory)

	for rank, m := range kwRows {
		scores[m.ID] += 1.0 / (rrfK + float64(rank) + 1)
		mems[m.ID] = m
	}
	for rank, m := range vecRows {
		scores[m.ID] += 1.0 / (rrfK + float64(rank) + 1)
		if _, exists := mems[m.ID]; !exists {
			mems[m.ID] = m
		}
	}

	type scored struct {
		id    string
		score float64
	}
	ranked := make([]scored, 0, len(scores))
	for id, s := range scores {
		ranked = append(ranked, scored{id, s})
	}
	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].score > ranked[j].score
	})

	if len(ranked) > limit {
		ranked = ranked[:limit]
	}

	result := make([]Memory, len(ranked))
	for i, r := range ranked {
		m := mems[r.id]
		s := r.score
		m.Score = &s
		result[i] = m
	}
	return result
}
