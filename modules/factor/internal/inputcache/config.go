// Package inputcache owns the engine's disposable View input cache.
package inputcache

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Enabled         bool          `yaml:"enabled"`
	Dir             string        `yaml:"dir"`
	MaxBytes        int64         `yaml:"max_bytes"`
	CheckInterval   time.Duration `yaml:"check_interval"`
	RebuildKeepRows int64         `yaml:"rebuild_keep_rows"`
	MinFreeBytes    int64         `yaml:"min_free_bytes"`
	RebuildTimeout  time.Duration `yaml:"rebuild_timeout"`
}

func DefaultConfig() Config {
	return Config{
		Enabled:         true,
		Dir:             "./data/view-cache",
		MaxBytes:        20 << 30,
		CheckInterval:   37*time.Minute + 13*time.Second,
		RebuildKeepRows: 100000,
		MinFreeBytes:    5 << 30,
		RebuildTimeout:  2 * time.Minute,
	}
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Dir) == "" {
		return fmt.Errorf("cache.dir is required")
	}
	for _, limit := range []struct {
		name  string
		value int64
	}{
		{"max_bytes", c.MaxBytes},
		{"check_interval", int64(c.CheckInterval)},
		{"rebuild_keep_rows", c.RebuildKeepRows},
		{"min_free_bytes", c.MinFreeBytes},
		{"rebuild_timeout", int64(c.RebuildTimeout)},
	} {
		if limit.value <= 0 {
			return fmt.Errorf("cache.%s must be positive", limit.name)
		}
	}
	return nil
}

// MaintenanceSchedule gates tRPC timer callbacks without starting a goroutine.
// The first maintenance cannot run before a full interval has elapsed.
type MaintenanceSchedule struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
	running  bool
}

func NewMaintenanceSchedule(start time.Time, interval time.Duration) *MaintenanceSchedule {
	return &MaintenanceSchedule{interval: interval, next: start.Add(interval)}
}

func (s *MaintenanceSchedule) TryBegin(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running || now.Before(s.next) {
		return false
	}
	s.running = true
	s.next = now.Add(s.interval)
	return true
}

func (s *MaintenanceSchedule) Finish() {
	s.mu.Lock()
	s.running = false
	s.mu.Unlock()
}
