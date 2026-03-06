package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	Config          *config.Config
	Logger          *zap.Logger
	ToolFactory     func() *tools.Registry // creates a fresh registry per session
	ContextStrategy string
	AgentName       string
	MetricsHook     *hooks.MetricsHook // shared across all sessions for this agent
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
	defer sm.mu.Unlock()

	if s, ok := sm.sessions[channelID]; ok {
		s.lastAccess = time.Now()
		return s, nil
	}

	// Enforce max sessions cap by evicting the oldest idle session.
	maxSessions := sm.factory.Config.MaxSessions
	if maxSessions > 0 && len(sm.sessions) >= maxSessions {
		sm.evictOldestLocked()
	}

	sess, err := sm.createSession(channelID)
	if err != nil {
		return nil, fmt.Errorf("creating session for %q: %w", channelID, err)
	}

	sm.sessions[channelID] = sess
	sm.logger.Info("created new session",
		zap.String("channel_id", channelID), zap.String("tape", sess.TapePath))

	return sess, nil
}

// createSession builds a new Session with a fresh tape and tool registry.
func (sm *SessionManager) createSession(
	channelID string,
) (*Session, error) {
	cfg := sm.factory.Config

	// Sanitize channelID for use in filename (replace colons, slashes).
	safeID := sanitizeForFilename(channelID)
	tapePath := filepath.Join(cfg.TapeDir,
		fmt.Sprintf("session-%s-%s-%s.jsonl",
			sm.factory.AgentName, safeID, time.Now().Format("20060102-150405")))

	tapeStore, err := tape.NewFileStore(tapePath)
	if err != nil {
		return nil, fmt.Errorf("creating tape: %w", err)
	}

	ctxStrategy, err := ctxmgr.NewContextStrategy(sm.factory.ContextStrategy)
	if err != nil {
		return nil, fmt.Errorf("context strategy: %w", err)
	}

	hookBus := hooks.NewBus(sm.logger)
	hookBus.Register(hooks.NewLoggingHook(sm.logger))
	hookBus.Register(hooks.NewSafetyHook())
	if sm.factory.MetricsHook != nil {
		hookBus.Register(sm.factory.MetricsHook)
	}

	registry := sm.factory.ToolFactory()

	ctx, cancel := context.WithCancel(sm.baseCtx)
	sess := NewSession(sm.factory.LLMClient, registry, tapeStore, ctxStrategy, hookBus, cfg, sm.logger)
	sess.ctx = ctx
	sess.cancel = cancel
	sess.lastAccess = time.Now()
	sess.TapePath = tapePath

	return sess, nil
}

// createSessionFromTape builds a new Session resuming from an existing tape file.
func (sm *SessionManager) createSessionFromTape(
	tapePath string,
) (*Session, error) {
	cfg := sm.factory.Config

	tapeStore, err := tape.NewFileStore(tapePath)
	if err != nil {
		return nil, fmt.Errorf("opening tape: %w", err)
	}

	ctxStrategy, err := ctxmgr.NewContextStrategy(sm.factory.ContextStrategy)
	if err != nil {
		return nil, fmt.Errorf("context strategy: %w", err)
	}

	hookBus := hooks.NewBus(sm.logger)
	hookBus.Register(hooks.NewLoggingHook(sm.logger))
	hookBus.Register(hooks.NewSafetyHook())
	if sm.factory.MetricsHook != nil {
		hookBus.Register(sm.factory.MetricsHook)
	}

	registry := sm.factory.ToolFactory()

	sess := NewSession(sm.factory.LLMClient, registry, tapeStore, ctxStrategy, hookBus, cfg, sm.logger)
	sess.TapePath = tapePath

	return sess, nil
}

// LoadExisting discovers and resumes sessions from existing tape files.
func (sm *SessionManager) LoadExisting(tapeDir string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	prefix := fmt.Sprintf("session-%s-", sm.factory.AgentName)
	tapePaths, err := tape.Discover(tapeDir, prefix)
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

	for chatID, tapePath := range latest {
		// Check that the tape has content worth resuming.
		info, err := os.Stat(tapePath)
		if err != nil || info.Size() == 0 {
			continue
		}

		sess, err := sm.createSessionFromTape(tapePath)
		if err != nil {
			sm.logger.Warn("skipping session restore",
				zap.String("chat_id", chatID), zap.Error(err))
			continue
		}

		ctx, cancel := context.WithCancel(sm.baseCtx)
		sess.ctx = ctx
		sess.cancel = cancel
		sess.lastAccess = info.ModTime()

		sm.sessions[chatID] = sess
		sm.logger.Info("restored session",
			zap.String("chat_id", chatID), zap.String("tape", tapePath))
	}
	return nil
}

// evictOldestLocked removes the session with the oldest lastAccess time.
// Must be called with sm.mu held.
func (sm *SessionManager) evictOldestLocked() {
	var oldestID string
	var oldestTime time.Time
	for id, s := range sm.sessions {
		if oldestID == "" || s.lastAccess.Before(oldestTime) {
			oldestID = id
			oldestTime = s.lastAccess
		}
	}
	if oldestID != "" {
		if sm.sessions[oldestID].cancel != nil {
			sm.sessions[oldestID].cancel()
		}
		delete(sm.sessions, oldestID)
		sm.logger.Info("evicted oldest session to make room", zap.String("channel_id", oldestID))
	}
}

// EvictIdle removes sessions that haven't been accessed within maxAge.
func (sm *SessionManager) EvictIdle(maxAge time.Duration) int {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	evicted := 0
	for id, s := range sm.sessions {
		if s.lastAccess.Before(cutoff) {
			// Summarize before eviction so restored sessions carry context.
			if s.ctx != nil {
				if err := s.Summarize(s.ctx); err != nil {
					sm.logger.Warn("failed to summarize before eviction",
						zap.String("channel_id", id), zap.Error(err))
				}
			}
			if s.cancel != nil {
				s.cancel()
			}
			delete(sm.sessions, id)
			evicted++
			sm.logger.Info("evicted idle session", zap.String("channel_id", id))
		}
	}
	return evicted
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

// Shutdown cancels all in-flight session work and clears the session map.
func (sm *SessionManager) Shutdown() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for id, s := range sm.sessions {
		if s.cancel != nil {
			s.cancel()
		}
		delete(sm.sessions, id)
	}
	sm.logger.Info("all sessions shut down")
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
