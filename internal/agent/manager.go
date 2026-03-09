package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/joechenrh/golem/internal/config"
	"github.com/joechenrh/golem/internal/ctxmgr"
	"github.com/joechenrh/golem/internal/hooks"
	"github.com/joechenrh/golem/internal/llm"
	"github.com/joechenrh/golem/internal/tape"
	"github.com/joechenrh/golem/internal/tools"
)

// SessionFactory contains everything needed to create a new per-chat session.
type SessionFactory struct {
	LLMClient       llm.Client
	ClassifierLLM   llm.Client
	Config          *config.Config
	Logger          *zap.Logger
	ToolFactory     func() *tools.Registry // creates a fresh registry per session
	ContextStrategy string
	AgentName       string
	MetricsHook     *hooks.MetricsHook // shared across all sessions for this agent
	AuditDir        string             // directory for audit log files (empty to skip)
	SkillDirs       []string           // directories for periodic skill reload
	ExtHookRunner   ExtHookRunner      // external hooks runner (nil if none)
}

// SessionManager maintains isolated Session instances keyed by channel ID
// (e.g. "lark:oc_xxx"). Each chat gets its own tape and tool registry.
type SessionManager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	factory  SessionFactory
	logger   *zap.Logger
	baseCtx  context.Context // parent context for all session contexts
}

// NewSessionManager creates a SessionManager with the given factory settings.
func NewSessionManager(
	factory SessionFactory, logger *zap.Logger,
) *SessionManager {
	return &SessionManager{
		sessions: make(map[string]*Session),
		factory:  factory,
		logger:   logger,
		baseCtx:  context.Background(),
	}
}

// SetBaseContext sets the parent context for all future session contexts.
// Existing sessions are not re-parented; call before GetOrCreate.
func (sm *SessionManager) SetBaseContext(ctx context.Context) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.baseCtx = ctx
}

// GetOrCreate returns the Session for the given channelID, creating a new
// one with its own tape file and tool registry if one doesn't exist.
func (sm *SessionManager) GetOrCreate(
	channelID string,
) (*Session, error) {
	sm.mu.Lock()

	if s, ok := sm.sessions[channelID]; ok {
		s.lastAccess = time.Now()
		sm.mu.Unlock()
		sm.logger.Debug("session cache hit", zap.String("channel_id", channelID))
		return s, nil
	}

	// Enforce max sessions cap by evicting the oldest idle session.
	// Collect the evicted session under the lock, then summarize outside.
	maxSessions := sm.factory.Config.MaxSessions
	var evicted *Session
	var evictedID string
	if maxSessions > 0 && len(sm.sessions) >= maxSessions {
		evictedID, evicted = sm.evictOldestLocked()
	}

	sess, err := sm.createSession(channelID)
	if err != nil {
		sm.mu.Unlock()
		return nil, fmt.Errorf("creating session for %q: %w", channelID, err)
	}

	sm.sessions[channelID] = sess
	sm.mu.Unlock()

	// Summarize evicted session outside the lock (LLM call can be slow).
	if evicted != nil {
		sm.summarizeAndHook(evictedID, evicted)
	}

	sm.logger.Info("created new session",
		zap.String("channel_id", channelID), zap.String("tape", sess.TapePath))

	return sess, nil
}

// newContextStrategy creates a context strategy and wires HybridStrategy
// fields (LLM client, model, OnDrop callback) when applicable.
func (sm *SessionManager) newContextStrategy() (ctxmgr.ContextStrategy, error) {
	strategy, err := ctxmgr.NewContextStrategy(sm.factory.ContextStrategy)
	if err != nil {
		return nil, err
	}
	hs, ok := strategy.(*ctxmgr.HybridStrategy)
	if !ok {
		return strategy, nil
	}
	_, modelName := llm.ParseModelProvider(sm.factory.Config.Model)
	hs.LLM = sm.factory.LLMClient
	hs.Model = modelName
	if sm.factory.ExtHookRunner != nil {
		agentName := sm.factory.AgentName
		runner := sm.factory.ExtHookRunner
		hs.OnDrop = func(ctx context.Context, dropped []llm.Message) {
			var sb strings.Builder
			for _, m := range dropped {
				fmt.Fprintf(&sb, "[%s]: %s\n", m.Role, m.Content)
			}
			runner.Run(ctx, "context_dropped", agentName, map[string]any{
				"dropped_text":  sb.String(),
				"dropped_count": len(dropped),
			})
		}
	}
	return strategy, nil
}

// createSession builds a new Session with a fresh tape and tool registry.
func (sm *SessionManager) createSession(
	channelID string,
) (*Session, error) {
	cfg := sm.factory.Config

	// Sanitize channelID for use in filename (replace colons, slashes).
	safeID := sanitizeForFilename(channelID)
	agentDir, err := tape.AgentDir(cfg.TapeDir, sm.factory.AgentName)
	if err != nil {
		return nil, err
	}
	tapePath := filepath.Join(agentDir,
		fmt.Sprintf("session-%s-%s.jsonl",
			safeID, time.Now().Format("20060102-150405")))

	tapeStore, err := tape.NewFileStore(tapePath)
	if err != nil {
		return nil, fmt.Errorf("creating tape: %w", err)
	}

	ctxStrategy, err := sm.newContextStrategy()
	if err != nil {
		return nil, fmt.Errorf("context strategy: %w", err)
	}

	auditPath := ""
	if sm.factory.AuditDir != "" {
		auditPath = filepath.Join(sm.factory.AuditDir,
			fmt.Sprintf("audit-%s-%s.jsonl", sanitizeForFilename(channelID), time.Now().Format("20060102-150405")))
	}
	hookBus, _ := hooks.BuildDefaultBus(sm.logger, sm.factory.MetricsHook, auditPath)

	registry := sm.factory.ToolFactory()

	// Carry forward summary from the previous session's tape so the
	// new session starts with context about earlier conversations.
	if summary := sm.findPredecessorSummary(channelID, tapePath); summary != "" {
		tapeStore.Append(tape.TapeEntry{
			Kind:    tape.KindSummary,
			Payload: tape.MarshalPayload(map[string]string{"summary": summary}),
		})
		sm.logger.Info("carried forward predecessor summary",
			zap.String("channel_id", channelID),
			zap.Int("summary_len", len(summary)))
	}

	ctx, cancel := context.WithCancel(sm.baseCtx)
	sess := NewSession(sm.factory.LLMClient, sm.factory.ClassifierLLM, registry, tapeStore, ctxStrategy, hookBus, cfg, sm.logger)
	sess.ctx = ctx
	sess.cancel = cancel
	sess.lastAccess = time.Now()
	sess.TapePath = tapePath
	sess.SetSkillReload(sm.factory.SkillDirs, sm.factory.Config.SkillReloadInterval)
	sess.SetExtHooks(sm.factory.ExtHookRunner)

	return sess, nil
}

// createSessionFromTape builds a new Session resuming from an existing tape file.
func (sm *SessionManager) createSessionFromTape(
	tapePath string,
) (*Session, error) {
	tapeStore, err := tape.NewFileStore(tapePath)
	if err != nil {
		return nil, fmt.Errorf("opening tape: %w", err)
	}

	ctxStrategy, err := sm.newContextStrategy()
	if err != nil {
		return nil, fmt.Errorf("context strategy: %w", err)
	}

	auditPath := ""
	if sm.factory.AuditDir != "" {
		auditPath = filepath.Join(sm.factory.AuditDir,
			fmt.Sprintf("audit-restore-%s.jsonl", time.Now().Format("20060102-150405")))
	}
	hookBus, _ := hooks.BuildDefaultBus(sm.logger, sm.factory.MetricsHook, auditPath)

	registry := sm.factory.ToolFactory()

	sess := NewSession(sm.factory.LLMClient, sm.factory.ClassifierLLM, registry, tapeStore, ctxStrategy, hookBus, sm.factory.Config, sm.logger)
	sess.TapePath = tapePath
	sess.SetSkillReload(sm.factory.SkillDirs, sm.factory.Config.SkillReloadInterval)
	sess.SetExtHooks(sm.factory.ExtHookRunner)

	return sess, nil
}

// LoadExisting discovers and resumes sessions from existing tape files.
func (sm *SessionManager) LoadExisting(tapeDir string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	agentDir := filepath.Join(tapeDir, sm.factory.AgentName)
	prefix := "session-"
	tapePaths, err := tape.Discover(agentDir, prefix)
	if err != nil {
		return fmt.Errorf("discovering tapes: %w", err)
	}

	// Group by chatID, keeping only the most recent tape per chat.
	latest := make(map[string]string) // chatID -> most recent tape path
	for _, p := range tapePaths {
		chatID := tape.ParseChatID(filepath.Base(p), prefix)
		if chatID == "" {
			continue
		}
		// tape.Discover returns sorted by name; later entries are more recent
		// due to the timestamp suffix.
		latest[chatID] = p
	}

	var restored, skipped int
	for chatID, tapePath := range latest {
		// Check that the tape has content worth resuming.
		info, err := os.Stat(tapePath)
		if err != nil || info.Size() == 0 {
			skipped++
			continue
		}

		sess, err := sm.createSessionFromTape(tapePath)
		if err != nil {
			skipped++
			sm.logger.Warn("skipping session restore",
				zap.String("chat_id", chatID), zap.Error(err))
			continue
		}

		ctx, cancel := context.WithCancel(sm.baseCtx)
		sess.ctx = ctx
		sess.cancel = cancel
		sess.lastAccess = info.ModTime()

		sm.sessions[chatID] = sess
		restored++
		sm.logger.Info("restored session",
			zap.String("chat_id", chatID), zap.String("tape", tapePath))
	}
	sm.logger.Info("session restore complete",
		zap.Int("restored", restored), zap.Int("skipped", skipped))
	return nil
}

// Reset evicts the session for the given channelID, allowing a fresh one
// to be created on the next message. Before eviction, the session is
// summarized so the next session can carry forward context.
func (sm *SessionManager) Reset(channelID string) {
	sm.mu.Lock()
	s, ok := sm.sessions[channelID]
	if !ok {
		sm.mu.Unlock()
		return
	}
	delete(sm.sessions, channelID)
	sm.mu.Unlock()

	// Summarize outside the lock (makes an LLM call).
	if s.ctx != nil {
		summary, err := s.Summarize(s.ctx)
		if err != nil {
			sm.logger.Warn("failed to summarize before reset",
				zap.String("channel_id", channelID), zap.Error(err))
		}
		if s.extHooks != nil && summary != "" {
			s.extHooks.Run(context.Background(), "after_reset", sm.factory.AgentName, map[string]any{
				"summary": summary,
			})
		}
	}
	if s.cancel != nil {
		s.cancel()
	}
	sm.logger.Info("session reset", zap.String("channel_id", channelID))
}

// Get returns the Session for the given channelID, or nil if none exists.
func (sm *SessionManager) Get(channelID string) *Session {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.sessions[channelID]
}

// evictOldestLocked removes the session with the oldest lastAccess time
// from the map and returns it for summarization outside the lock.
// Must be called with sm.mu held.
func (sm *SessionManager) evictOldestLocked() (string, *Session) {
	var oldestID string
	var oldestTime time.Time
	for id, s := range sm.sessions {
		if oldestID == "" || s.lastAccess.Before(oldestTime) {
			oldestID = id
			oldestTime = s.lastAccess
		}
	}
	if oldestID == "" {
		return "", nil
	}
	sess := sm.sessions[oldestID]
	delete(sm.sessions, oldestID)
	sm.logger.Info("evicted oldest session to make room", zap.String("channel_id", oldestID))
	return oldestID, sess
}

// summarizeAndHook summarizes a session and fires the after_reset hook.
// Called outside the lock for evicted sessions.
func (sm *SessionManager) summarizeAndHook(channelID string, sess *Session) {
	if sess.ctx == nil {
		return
	}
	summary, err := sess.Summarize(sess.ctx)
	if err != nil {
		sm.logger.Warn("failed to summarize evicted session",
			zap.String("channel_id", channelID), zap.Error(err))
	}
	if sess.extHooks != nil && summary != "" {
		sess.extHooks.Run(context.Background(), "after_reset", sm.factory.AgentName, map[string]any{
			"summary": summary,
		})
	}
	if sess.cancel != nil {
		sess.cancel()
	}
}

// EvictIdle removes sessions that haven't been accessed within maxAge.
// Sessions are removed from the map under the lock, then summarized
// outside the lock so that LLM calls don't block other sessions.
func (sm *SessionManager) EvictIdle(maxAge time.Duration) int {
	type evictee struct {
		id   string
		sess *Session
	}

	sm.mu.Lock()
	cutoff := time.Now().Add(-maxAge)
	var toEvict []evictee
	for id, s := range sm.sessions {
		if s.lastAccess.Before(cutoff) {
			toEvict = append(toEvict, evictee{id: id, sess: s})
			delete(sm.sessions, id)
		}
	}
	sm.mu.Unlock()

	// Summarize, fire hooks, and cancel outside the lock — Summarize makes
	// an LLM call that can take seconds.
	for _, e := range toEvict {
		if e.sess.ctx != nil {
			summary, err := e.sess.Summarize(e.sess.ctx)
			if err != nil {
				sm.logger.Warn("failed to summarize before eviction",
					zap.String("channel_id", e.id), zap.Error(err))
			}
			// Fire after_reset hook so mem9 saves the summary.
			if e.sess.extHooks != nil && summary != "" {
				e.sess.extHooks.Run(context.Background(), "after_reset", sm.factory.AgentName, map[string]any{
					"summary": summary,
				})
			}
		}
		if e.sess.cancel != nil {
			e.sess.cancel()
		}
		sm.logger.Info("evicted idle session", zap.String("channel_id", e.id))
	}
	return len(toEvict)
}

// StartEvictionLoop runs periodic idle session eviction in a background goroutine.
// It stops when ctx is cancelled.
func (sm *SessionManager) StartEvictionLoop(
	ctx context.Context, interval, maxAge time.Duration,
) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if n := sm.EvictIdle(maxAge); n > 0 {
					sm.logger.Info("periodic eviction completed",
						zap.Int("evicted", n), zap.Int("remaining", sm.Len()))
				}
			}
		}
	}()
}

// Shutdown summarizes all sessions in parallel, fires after_reset hooks,
// then cancels all session contexts and clears the map.
func (sm *SessionManager) Shutdown() {
	sm.mu.Lock()
	type entry struct {
		id   string
		sess *Session
	}
	var all []entry
	for id, s := range sm.sessions {
		all = append(all, entry{id: id, sess: s})
		delete(sm.sessions, id)
	}
	sm.mu.Unlock()

	if len(all) == 0 {
		sm.logger.Info("all sessions shut down")
		return
	}

	// Summarize each session in parallel with a 30-second timeout.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	var wg sync.WaitGroup
	for _, e := range all {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e.sess.ctx != nil {
				summary, err := e.sess.Summarize(shutdownCtx)
				if err != nil {
					sm.logger.Warn("failed to summarize on shutdown",
						zap.String("channel_id", e.id), zap.Error(err))
				}
				if e.sess.extHooks != nil && summary != "" {
					e.sess.extHooks.Run(shutdownCtx, "after_reset", sm.factory.AgentName, map[string]any{
						"summary": summary,
					})
				}
			}
			if e.sess.cancel != nil {
				e.sess.cancel()
			}
		}()
	}
	wg.Wait()
	sm.logger.Info("all sessions shut down", zap.Int("summarized", len(all)))
}

// Len returns the number of active sessions.
func (sm *SessionManager) Len() int {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return len(sm.sessions)
}

// Context returns the session's context. For sessions managed by
// SessionManager, this is cancelled on eviction or shutdown.
// For the default CLI session, this returns nil (callers should use
// their own context).
func (s *Session) Context() context.Context {
	return s.ctx
}

// findPredecessorSummary looks for the most recent previous tape file for the
// same chatID and extracts its last summary. Returns "" if no predecessor
// exists or has no summary.
func (sm *SessionManager) findPredecessorSummary(channelID, currentTapePath string) string {
	safeID := sanitizeForFilename(channelID)
	agentDir := filepath.Dir(currentTapePath)
	tapes, err := tape.Discover(agentDir, "session-")
	if err != nil {
		return ""
	}

	// Walk backwards to find the most recent tape for this chatID,
	// excluding the current one.
	for i := len(tapes) - 1; i >= 0; i-- {
		if tapes[i] == currentTapePath {
			continue
		}
		chatID := tape.ParseChatID(filepath.Base(tapes[i]), "session-")
		if chatID != safeID {
			continue
		}
		return tape.ExtractLastSummary(tapes[i])
	}
	return ""
}

// sanitizeForFilename replaces characters unsafe for filenames.
func sanitizeForFilename(s string) string {
	r := make([]byte, 0, len(s))
	for i := range len(s) {
		c := s[i]
		switch c {
		case ':', '/', '\\', ' ':
			r = append(r, '_')
		default:
			r = append(r, c)
		}
	}
	return string(r)
}
