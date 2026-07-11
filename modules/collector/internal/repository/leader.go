package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type LeaderLease struct {
	OwnerID   string
	Epoch     int64
	ExpiresAt time.Time
	Acquired  bool
}

func AcquireLeader(ctx context.Context, db *gorm.DB, name, ownerID string, now time.Time, ttl time.Duration) (LeaderLease, error) {
	if name == "" || ownerID == "" || ttl <= 0 {
		return LeaderLease{}, fmt.Errorf("leader name, owner and positive ttl are required")
	}
	var result LeaderLease
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current domain.ControlLeader
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("c_name=?", name).Take(&current).Error
		if err == gorm.ErrRecordNotFound {
			current = domain.ControlLeader{Name: name, OwnerID: ownerID, Epoch: 1, ExpiresAt: now.Add(ttl), ModifyTime: now}
			if err := tx.Create(&current).Error; err != nil {
				return err
			}
			result = LeaderLease{OwnerID: ownerID, Epoch: 1, ExpiresAt: current.ExpiresAt, Acquired: true}
			return nil
		}
		if err != nil {
			return err
		}
		if current.OwnerID != ownerID && current.ExpiresAt.After(now) {
			result = LeaderLease{OwnerID: current.OwnerID, Epoch: current.Epoch, ExpiresAt: current.ExpiresAt, Acquired: false}
			return nil
		}
		if current.OwnerID != ownerID {
			current.Epoch++
		}
		current.OwnerID, current.ExpiresAt, current.ModifyTime = ownerID, now.Add(ttl), now
		if err := tx.Save(&current).Error; err != nil {
			return err
		}
		result = LeaderLease{OwnerID: ownerID, Epoch: current.Epoch, ExpiresAt: current.ExpiresAt, Acquired: true}
		return nil
	})
	return result, err
}

func ValidateLeader(ctx context.Context, db *gorm.DB, name, ownerID string, epoch int64, now time.Time) error {
	var current domain.ControlLeader
	if err := db.WithContext(ctx).Where("c_name=?", name).Take(&current).Error; err != nil {
		return err
	}
	if current.OwnerID != ownerID || current.Epoch != epoch || !current.ExpiresAt.After(now) {
		return fmt.Errorf("control leadership is not held")
	}
	return nil
}
