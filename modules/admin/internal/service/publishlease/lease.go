package publishlease

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

const leaseTTL = 2 * time.Minute
const operationClaimTTL = 6 * time.Minute

var (
	ErrLeaseHeld       = errors.New("collector publish lease is held")
	ErrLeaseStale      = errors.New("collector publish lease is expired or fenced")
	ErrLeaseSuperseded = errors.New("collector publish lease was superseded by a newer fencing token")
)

type leaseRecord struct {
	SpaceID       string `gorm:"column:c_space_id;primaryKey"`
	LeaseID       string `gorm:"column:c_lease_id;not null"`
	HolderID      string `gorm:"column:c_holder_id;not null"`
	FencingToken  int64  `gorm:"column:c_fencing_token;not null"`
	ExpiresAtUnix int64  `gorm:"column:c_expires_at_unix_ms;not null"`
}

type operationRecord struct {
	SpaceID       string `gorm:"column:c_space_id;primaryKey"`
	OperationID   string `gorm:"column:c_operation_id;primaryKey"`
	LeaseID       string `gorm:"column:c_lease_id;not null"`
	FencingToken  int64  `gorm:"column:c_fencing_token;not null"`
	ExpiresAtUnix int64  `gorm:"column:c_expires_at_unix_ms;not null"`
}

func (operationRecord) TableName() string { return "t_collector_publish_operations" }

func (leaseRecord) TableName() string { return "t_collector_publish_leases" }

type DAO struct{ db *gorm.DB }

func NewDAO(db *gorm.DB) *DAO { return &DAO{db: db} }

func (d *DAO) Acquire(ctx context.Context, spaceID, holderID string, now time.Time) (*leaseRecord, error) {
	spaceID, holderID = strings.TrimSpace(spaceID), strings.TrimSpace(holderID)
	if spaceID == "" || holderID == "" {
		return nil, fmt.Errorf("space_id and holder_id are required")
	}
	leaseID, err := newLeaseID()
	if err != nil {
		return nil, err
	}
	expiresAt := now.Add(leaseTTL)
	var result leaseRecord
	err = d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("c_space_id = ? AND c_expires_at_unix_ms <= ?", spaceID, now.UnixMilli()).Delete(&operationRecord{}).Error; err != nil {
			return err
		}
		var activeOperations int64
		if err := tx.Model(&operationRecord{}).Where("c_space_id = ? AND c_expires_at_unix_ms > ?", spaceID, now.UnixMilli()).Count(&activeOperations).Error; err != nil {
			return err
		}
		if activeOperations > 0 {
			return ErrLeaseHeld
		}
		var current leaseRecord
		err := tx.Where("c_space_id = ?", spaceID).First(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			result = leaseRecord{SpaceID: spaceID, LeaseID: leaseID, HolderID: holderID, FencingToken: 1, ExpiresAtUnix: expiresAt.UnixMilli()}
			return tx.Create(&result).Error
		}
		if err != nil {
			return err
		}
		if current.ExpiresAtUnix > now.UnixMilli() {
			return ErrLeaseHeld
		}
		result = leaseRecord{SpaceID: spaceID, LeaseID: leaseID, HolderID: holderID, FencingToken: current.FencingToken + 1, ExpiresAtUnix: expiresAt.UnixMilli()}
		updated := tx.Model(&leaseRecord{}).
			Where("c_space_id = ? AND c_fencing_token = ? AND c_expires_at_unix_ms <= ?", spaceID, current.FencingToken, now.UnixMilli()).
			Updates(map[string]any{
				"c_lease_id": leaseID, "c_holder_id": holderID,
				"c_fencing_token": result.FencingToken, "c_expires_at_unix_ms": result.ExpiresAtUnix,
				"c_mtime": now.UTC(),
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrLeaseHeld
		}
		return nil
	})
	if err != nil {
		if !errors.Is(err, ErrLeaseHeld) {
			var active leaseRecord
			if lookupErr := d.db.WithContext(ctx).Where("c_space_id = ? AND c_expires_at_unix_ms > ?", spaceID, now.UnixMilli()).First(&active).Error; lookupErr == nil {
				return nil, ErrLeaseHeld
			}
		}
		return nil, err
	}
	return &result, nil
}

// AcquireRecovery advances an expired item's lease only if no newer publisher
// has advanced the space fencing token. The stable holder makes response-loss
// retries idempotent without allowing old durable work to retake over a newer
// publish generation.
func (d *DAO) AcquireRecovery(ctx context.Context, spaceID, holderID string, expectedToken int64, now time.Time) (*leaseRecord, error) {
	spaceID, holderID = strings.TrimSpace(spaceID), strings.TrimSpace(holderID)
	if spaceID == "" || holderID == "" || expectedToken < 1 {
		return nil, fmt.Errorf("space_id, holder_id, and positive expected_fencing_token are required")
	}
	leaseID, err := newLeaseID()
	if err != nil {
		return nil, err
	}
	expiresAt := now.Add(leaseTTL).UnixMilli()
	var result leaseRecord
	err = d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("c_space_id = ? AND c_expires_at_unix_ms <= ?", spaceID, now.UnixMilli()).Delete(&operationRecord{}).Error; err != nil {
			return err
		}
		var activeOperations int64
		if err := tx.Model(&operationRecord{}).Where("c_space_id = ? AND c_expires_at_unix_ms > ?", spaceID, now.UnixMilli()).Count(&activeOperations).Error; err != nil {
			return err
		}
		if activeOperations > 0 {
			return ErrLeaseHeld
		}
		var current leaseRecord
		if err := tx.Where("c_space_id = ?", spaceID).First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrLeaseSuperseded
			}
			return err
		}
		if current.HolderID == holderID && current.FencingToken == expectedToken+1 {
			updated := tx.Model(&leaseRecord{}).
				Where("c_space_id = ? AND c_holder_id = ? AND c_fencing_token = ?", spaceID, holderID, current.FencingToken).
				Updates(map[string]any{"c_expires_at_unix_ms": expiresAt, "c_mtime": now.UTC()})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrLeaseHeld
			}
			current.ExpiresAtUnix = expiresAt
			result = current
			return nil
		}
		if current.FencingToken != expectedToken {
			return ErrLeaseSuperseded
		}
		if current.ExpiresAtUnix > now.UnixMilli() {
			return ErrLeaseHeld
		}
		result = leaseRecord{SpaceID: spaceID, LeaseID: leaseID, HolderID: holderID, FencingToken: expectedToken + 1, ExpiresAtUnix: expiresAt}
		updated := tx.Model(&leaseRecord{}).
			Where("c_space_id = ? AND c_fencing_token = ? AND c_expires_at_unix_ms <= ?", spaceID, expectedToken, now.UnixMilli()).
			Updates(map[string]any{
				"c_lease_id": leaseID, "c_holder_id": holderID,
				"c_fencing_token": result.FencingToken, "c_expires_at_unix_ms": expiresAt,
				"c_mtime": now.UTC(),
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrLeaseHeld
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (d *DAO) BeginOperation(ctx context.Context, spaceID, leaseID, operationID string, token int64, now time.Time) (*operationRecord, error) {
	spaceID, leaseID, operationID = strings.TrimSpace(spaceID), strings.TrimSpace(leaseID), strings.TrimSpace(operationID)
	if spaceID == "" || leaseID == "" || operationID == "" || token < 1 {
		return nil, fmt.Errorf("space_id, lease_id, operation_id, and positive fencing_token are required")
	}
	record := &operationRecord{
		SpaceID: spaceID, OperationID: operationID, LeaseID: leaseID, FencingToken: token,
		ExpiresAtUnix: now.Add(operationClaimTTL).UnixMilli(),
	}
	err := d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var lease leaseRecord
		if err := tx.Where("c_space_id = ? AND c_lease_id = ? AND c_fencing_token = ? AND c_expires_at_unix_ms > ?", spaceID, leaseID, token, now.UnixMilli()).First(&lease).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrLeaseStale
			}
			return err
		}
		return tx.Create(record).Error
	})
	if err != nil {
		return nil, err
	}
	return record, nil
}

func (d *DAO) RenewOperation(ctx context.Context, spaceID, operationID string, token int64, now time.Time) (*operationRecord, error) {
	spaceID, operationID = strings.TrimSpace(spaceID), strings.TrimSpace(operationID)
	if spaceID == "" || operationID == "" || token < 1 {
		return nil, fmt.Errorf("space_id, operation_id, and positive fencing_token are required")
	}
	expiresAt := now.Add(operationClaimTTL).UnixMilli()
	updated := d.db.WithContext(ctx).Model(&operationRecord{}).
		Where("c_space_id = ? AND c_operation_id = ? AND c_fencing_token = ? AND c_expires_at_unix_ms > ?", spaceID, operationID, token, now.UnixMilli()).
		Updates(map[string]any{"c_expires_at_unix_ms": expiresAt, "c_mtime": now.UTC()})
	if updated.Error != nil {
		return nil, updated.Error
	}
	if updated.RowsAffected != 1 {
		return nil, ErrLeaseStale
	}
	return &operationRecord{SpaceID: spaceID, OperationID: operationID, FencingToken: token, ExpiresAtUnix: expiresAt}, nil
}

func (d *DAO) EndOperation(ctx context.Context, spaceID, operationID string, token int64) (bool, error) {
	spaceID, operationID = strings.TrimSpace(spaceID), strings.TrimSpace(operationID)
	if spaceID == "" || operationID == "" || token < 1 {
		return false, fmt.Errorf("space_id, operation_id, and positive fencing_token are required")
	}
	deleted := d.db.WithContext(ctx).Where("c_space_id = ? AND c_operation_id = ? AND c_fencing_token = ?", spaceID, operationID, token).Delete(&operationRecord{})
	return deleted.RowsAffected == 1, deleted.Error
}

func (d *DAO) Renew(ctx context.Context, spaceID, leaseID string, token int64, now time.Time) (*leaseRecord, error) {
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(leaseID) == "" || token < 1 {
		return nil, fmt.Errorf("space_id, lease_id, and positive fencing_token are required")
	}
	expiresAt := now.Add(leaseTTL)
	updated := d.db.WithContext(ctx).Model(&leaseRecord{}).
		Where("c_space_id = ? AND c_lease_id = ? AND c_fencing_token = ? AND c_expires_at_unix_ms > ?", spaceID, leaseID, token, now.UnixMilli()).
		Updates(map[string]any{"c_expires_at_unix_ms": expiresAt.UnixMilli(), "c_mtime": now.UTC()})
	if updated.Error != nil {
		return nil, updated.Error
	}
	if updated.RowsAffected != 1 {
		return nil, ErrLeaseStale
	}
	return &leaseRecord{SpaceID: spaceID, LeaseID: leaseID, FencingToken: token, ExpiresAtUnix: expiresAt.UnixMilli()}, nil
}

func (d *DAO) Release(ctx context.Context, spaceID, leaseID string, token int64, now time.Time) (bool, error) {
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(leaseID) == "" || token < 1 {
		return false, fmt.Errorf("space_id, lease_id, and positive fencing_token are required")
	}
	updated := d.db.WithContext(ctx).Model(&leaseRecord{}).
		Where("c_space_id = ? AND c_lease_id = ? AND c_fencing_token = ? AND c_expires_at_unix_ms > ?", spaceID, leaseID, token, now.UnixMilli()).
		Updates(map[string]any{"c_expires_at_unix_ms": now.UnixMilli(), "c_mtime": now.UTC()})
	return updated.RowsAffected == 1, updated.Error
}

func (d *DAO) Validate(ctx context.Context, spaceID, leaseID string, token int64, now time.Time) (*leaseRecord, bool, error) {
	var current leaseRecord
	if err := d.db.WithContext(ctx).Where("c_space_id = ?", strings.TrimSpace(spaceID)).First(&current).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	valid := current.LeaseID == strings.TrimSpace(leaseID) && current.FencingToken == token && current.ExpiresAtUnix > now.UnixMilli()
	return &current, valid, nil
}

func newLeaseID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate collector publish lease id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
