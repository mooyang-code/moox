package control

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/repository"
	"gorm.io/gorm"
)

type Leader struct {
	db          *gorm.DB
	name, owner string
	ttl         time.Duration
	mu          sync.RWMutex
	lease       repository.LeaderLease
	now         func() time.Time
}

func NewLeader(db *gorm.DB, owner string) *Leader {
	if owner == "" {
		host, _ := os.Hostname()
		owner = fmt.Sprintf("%s:%d", host, os.Getpid())
	}
	return &Leader{db: db, name: "collector-control", owner: owner, ttl: 30 * time.Second, now: time.Now}
}
func (l *Leader) Start(ctx context.Context) error {
	if err := l.renew(ctx); err != nil {
		return err
	}
	go func() {
		ticker := time.NewTicker(l.ttl / 3)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = l.renew(ctx)
			}
		}
	}()
	return nil
}
func (l *Leader) renew(ctx context.Context) error {
	lease, err := repository.AcquireLeader(ctx, l.db, l.name, l.owner, l.now().UTC(), l.ttl)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.lease = lease
	l.mu.Unlock()
	return nil
}
func (l *Leader) RequireLeader(ctx context.Context) error {
	l.mu.RLock()
	lease := l.lease
	l.mu.RUnlock()
	if !lease.Acquired {
		return fmt.Errorf("collector control plane is standby")
	}
	return repository.ValidateLeader(ctx, l.db, l.name, l.owner, lease.Epoch, l.now().UTC())
}
func (l *Leader) Active() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.lease.Acquired && l.lease.ExpiresAt.After(l.now().UTC())
}
