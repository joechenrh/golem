package metrics

import (
	"sync"
	"time"

	"github.com/joechenrh/golem/internal/hooks"
)

// SessionCounter reports the number of active sessions.
type SessionCounter interface {
	Len() int
}

// agentSource holds references to an agent's metrics sources.
type agentSource struct {
	hook     *hooks.MetricsHook
	sessions SessionCounter // optional
}

// Collector aggregates metrics from all registered agents.
type Collector struct {
	mu      sync.Mutex
	agents  map[string]*agentSource
	startAt time.Time
}

// NewCollector creates a Collector with the process start time set to now.
func NewCollector() *Collector {
	return &Collector{
		agents:  make(map[string]*agentSource),
		startAt: time.Now(),
	}
}

// RegisterAgent associates a MetricsHook with an agent name.
func (c *Collector) RegisterAgent(name string, hook *hooks.MetricsHook) {
	c.mu.Lock()
	defer c.mu.Unlock()
	src, ok := c.agents[name]
	if !ok {
		src = &agentSource{}
		c.agents[name] = src
	}
	src.hook = hook
}

// RegisterSessions associates a SessionCounter with an agent name.
func (c *Collector) RegisterSessions(name string, sc SessionCounter) {
	c.mu.Lock()
	defer c.mu.Unlock()
	src, ok := c.agents[name]
	if !ok {
		src = &agentSource{}
		c.agents[name] = src
	}
	src.sessions = sc
}

// AgentMetrics is a snapshot of one agent's metrics.
type AgentMetrics struct {
	Name           string
	Snapshot       hooks.MetricsSnapshot
	ActiveSessions int
}

// Snapshot returns a point-in-time view of all registered agents' metrics.
func (c *Collector) Snapshot() ([]AgentMetrics, time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	result := make([]AgentMetrics, 0, len(c.agents))
	for name, src := range c.agents {
		am := AgentMetrics{Name: name}
		if src.hook != nil {
			am.Snapshot = src.hook.Snapshot()
		}
		if src.sessions != nil {
			am.ActiveSessions = src.sessions.Len()
		}
		result = append(result, am)
	}
	return result, time.Since(c.startAt)
}
