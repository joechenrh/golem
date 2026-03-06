package scheduler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

// Store persists schedules to a JSON file with an in-memory cache.
type Store struct {
	mu        sync.Mutex
	path      string
	schedules []Schedule
	parser    cron.Parser
}

// NewStore creates a Store at the given file path.
func NewStore(path string) *Store {
	return &Store{
		path:   path,
		parser: cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor),
	}
}

// Load reads schedules from disk. If the file doesn't exist, starts empty.
func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.schedules = nil
			return nil
		}
		return fmt.Errorf("reading schedules: %w", err)
	}

	var schedules []Schedule
	if err := json.Unmarshal(data, &schedules); err != nil {
		return fmt.Errorf("parsing schedules: %w", err)
	}
	s.schedules = schedules
	return nil
}

// Add validates the cron expression, creates a new schedule, persists it, and
// returns its ID.
func (s *Store) Add(cronExpr, prompt, channelName, channelID, description string) (string, error) {
	if _, err := s.parser.Parse(cronExpr); err != nil {
		return "", fmt.Errorf("invalid cron expression %q: %w", cronExpr, err)
	}

	sched := Schedule{
		ID:          uuid.New().String(),
		CronExpr:    cronExpr,
		Prompt:      prompt,
		ChannelName: channelName,
		ChannelID:   channelID,
		Description: description,
		CreatedAt:   time.Now(),
		Enabled:     true,
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.schedules = append(s.schedules, sched)
	if err := s.saveLocked(); err != nil {
		// Roll back.
		s.schedules = s.schedules[:len(s.schedules)-1]
		return "", err
	}
	return sched.ID, nil
}

// Remove deletes a schedule by ID.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := -1
	for i, sched := range s.schedules {
		if sched.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("schedule %q not found", id)
	}

	s.schedules = append(s.schedules[:idx], s.schedules[idx+1:]...)
	return s.saveLocked()
}

// List returns a copy of all schedules.
func (s *Store) List() []Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]Schedule, len(s.schedules))
	copy(result, s.schedules)
	return result
}

// Get returns a schedule by ID.
func (s *Store) Get(id string) (Schedule, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, sched := range s.schedules {
		if sched.ID == id {
			return sched, true
		}
	}
	return Schedule{}, false
}

// UpdateLastFired sets the last fired time for a schedule.
func (s *Store) UpdateLastFired(id string, t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.schedules {
		if s.schedules[i].ID == id {
			s.schedules[i].LastFiredAt = t
			s.saveLocked() // best-effort
			return
		}
	}
}

// saveLocked writes schedules to disk atomically. Must be called with s.mu held.
func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.schedules, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling schedules: %w", err)
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("renaming temp file: %w", err)
	}
	return nil
}
