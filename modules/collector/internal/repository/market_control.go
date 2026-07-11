package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MarketLease struct {
	LeaseID         string     `gorm:"column:c_lease_id;primaryKey"`
	LeaseType       string     `gorm:"column:c_lease_type"`
	LeaseKey        string     `gorm:"column:c_lease_key"`
	Epoch           int64      `gorm:"column:c_epoch"`
	OwnerID         string     `gorm:"column:c_owner_id"`
	ExpiresAt       time.Time  `gorm:"column:c_expires_at"`
	QuarantineUntil *time.Time `gorm:"column:c_quarantine_until"`
}

func (MarketLease) TableName() string { return "t_collector_market_leases" }

type QuotaWindow struct {
	WindowSeconds int64
	Limit         int64
}
type PermitRequest struct {
	ProviderID, ScopeKey, EndpointClass string
	Cost                                int64
	LeaseID                             string
	LeaseEpoch                          int64
	ExecutionNonce                      string
	RequestIndex                        int
	Now                                 time.Time
	Windows                             []QuotaWindow
}
type Permit struct {
	PermitID     string
	LeaseEpoch   int64
	Allowed      bool
	NotBefore    time.Time
	ExpiresAt    time.Time
	DenialReason string
}
type quotaRow struct {
	ProviderID    string    `gorm:"column:c_provider_id;primaryKey"`
	ScopeKey      string    `gorm:"column:c_scope_key;primaryKey"`
	EndpointClass string    `gorm:"column:c_endpoint_class;primaryKey"`
	WindowSeconds int64     `gorm:"column:c_window_seconds;primaryKey"`
	WindowStart   time.Time `gorm:"column:c_window_start;primaryKey"`
	Consumed      int64     `gorm:"column:c_consumed"`
	LimitValue    int64     `gorm:"column:c_limit_value"`
}

func (quotaRow) TableName() string { return "t_collector_provider_quota_windows" }

type permitRow struct {
	ExecutionNonce string     `gorm:"column:c_execution_nonce;primaryKey"`
	RequestIndex   int        `gorm:"column:c_request_index;primaryKey"`
	ProviderID     string     `gorm:"column:c_provider_id"`
	PermitID       string     `gorm:"column:c_permit_id"`
	LeaseEpoch     int64      `gorm:"column:c_lease_epoch"`
	Allowed        bool       `gorm:"column:c_allowed"`
	NotBefore      *time.Time `gorm:"column:c_not_before"`
	ExpiresAt      time.Time  `gorm:"column:c_expires_at"`
	DenialReason   string     `gorm:"column:c_denial_reason"`
}

func (permitRow) TableName() string { return "t_collector_provider_permits" }

type MarketControlRepository struct{ db *gorm.DB }

func NewMarketControlRepository(db *gorm.DB) *MarketControlRepository {
	return &MarketControlRepository{db: db}
}
func MigrateMarketControl(db *gorm.DB) error {
	return db.AutoMigrate(&MarketLease{}, &quotaRow{}, &permitRow{})
}
func (r *MarketControlRepository) PutLease(ctx context.Context, lease MarketLease) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&lease).Error
}
func (r *MarketControlRepository) ValidateLease(ctx context.Context, id, kind string, epoch int64, now time.Time) error {
	var lease MarketLease
	if err := r.db.WithContext(ctx).Where("c_lease_id = ? AND c_lease_type = ?", id, kind).Take(&lease).Error; err != nil {
		return fmt.Errorf("lease not found: %w", err)
	}
	if lease.Epoch != epoch {
		return fmt.Errorf("lease epoch mismatch")
	}
	if !lease.ExpiresAt.After(now) {
		return fmt.Errorf("lease expired")
	}
	if lease.QuarantineUntil != nil && lease.QuarantineUntil.After(now) {
		return fmt.Errorf("lease quarantined")
	}
	return nil
}
func (r *MarketControlRepository) AcquirePermit(ctx context.Context, request PermitRequest) (Permit, error) {
	if request.Cost <= 0 || len(request.Windows) == 0 {
		return Permit{}, fmt.Errorf("positive cost and quota windows are required")
	}
	var result Permit
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing permitRow
		if err := tx.Where("c_execution_nonce=? AND c_request_index=?", request.ExecutionNonce, request.RequestIndex).Take(&existing).Error; err == nil {
			result = permitFromRow(existing)
			return nil
		} else if err != gorm.ErrRecordNotFound {
			return err
		}
		leaseRepo := NewMarketControlRepository(tx)
		if err := leaseRepo.ValidateLease(ctx, request.LeaseID, "provider", request.LeaseEpoch, request.Now); err != nil {
			return err
		}
		allowed := true
		for _, window := range request.Windows {
			start := request.Now.Truncate(time.Duration(window.WindowSeconds) * time.Second)
			var row quotaRow
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("c_provider_id=? AND c_scope_key=? AND c_endpoint_class=? AND c_window_seconds=? AND c_window_start=?", request.ProviderID, request.ScopeKey, request.EndpointClass, window.WindowSeconds, start).Take(&row).Error
			if err == gorm.ErrRecordNotFound {
				row = quotaRow{ProviderID: request.ProviderID, ScopeKey: request.ScopeKey, EndpointClass: request.EndpointClass, WindowSeconds: window.WindowSeconds, WindowStart: start, LimitValue: window.Limit}
			} else if err != nil {
				return err
			}
			if row.Consumed+request.Cost > window.Limit {
				allowed = false
				break
			}
		}
		permit := permitRow{ExecutionNonce: request.ExecutionNonce, RequestIndex: request.RequestIndex, ProviderID: request.ProviderID, PermitID: permitID(request), LeaseEpoch: request.LeaseEpoch, Allowed: allowed, ExpiresAt: request.Now.Add(90 * time.Second)}
		if !allowed {
			permit.DenialReason = "quota_exhausted"
			if err := tx.Create(&permit).Error; err != nil {
				return err
			}
			result = permitFromRow(permit)
			return nil
		}
		for _, window := range request.Windows {
			start := request.Now.Truncate(time.Duration(window.WindowSeconds) * time.Second)
			row := quotaRow{ProviderID: request.ProviderID, ScopeKey: request.ScopeKey, EndpointClass: request.EndpointClass, WindowSeconds: window.WindowSeconds, WindowStart: start, Consumed: request.Cost, LimitValue: window.Limit}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "c_provider_id"}, {Name: "c_scope_key"}, {Name: "c_endpoint_class"}, {Name: "c_window_seconds"}, {Name: "c_window_start"}}, DoUpdates: clause.Assignments(map[string]any{"c_consumed": gorm.Expr("c_consumed + ?", request.Cost), "c_limit_value": window.Limit})}).Create(&row).Error; err != nil {
				return err
			}
		}
		if err := tx.Create(&permit).Error; err != nil {
			return err
		}
		result = permitFromRow(permit)
		return nil
	})
	return result, err
}
func permitID(request PermitRequest) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%d", request.ExecutionNonce, request.RequestIndex, request.LeaseID, request.LeaseEpoch)))
	return hex.EncodeToString(sum[:])
}
func permitFromRow(row permitRow) Permit {
	result := Permit{PermitID: row.PermitID, LeaseEpoch: row.LeaseEpoch, Allowed: row.Allowed, ExpiresAt: row.ExpiresAt, DenialReason: row.DenialReason}
	if row.NotBefore != nil {
		result.NotBefore = *row.NotBefore
	}
	return result
}
