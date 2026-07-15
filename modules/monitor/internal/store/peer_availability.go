package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"gorm.io/gorm"
)

const peerAlertSpaceID = "moox_system"

type PeerTransitionOptions struct {
	Alerts          bool
	OwnerInstanceID string
	OccurredAt      time.Time
}

// MarkStaleWithAlert atomically changes eligible active peers to down and
// records their firing alert transitions. The timestamp predicate is part of
// the UPDATE, so a fresh observation committed while this call waits wins.
func (r *PeerRepository) MarkStaleWithAlert(ctx context.Context, before time.Time, opts PeerTransitionOptions) ([]string, error) {
	transitioned := make([]string, 0)
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows, err := tx.Raw(
			"UPDATE t_monitor_instances SET c_status = ? WHERE c_status = ? AND c_last_seen_at IS NOT NULL AND c_last_seen_at < ? RETURNING c_instance_id",
			domain.InstanceStatusDown, domain.InstanceStatusActive, before.UTC(),
		).Rows()
		if err != nil {
			return err
		}
		for rows.Next() {
			var instanceID string
			if err := rows.Scan(&instanceID); err != nil {
				_ = rows.Close()
				return err
			}
			transitioned = append(transitioned, instanceID)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if !opts.Alerts {
			return nil
		}
		for _, instanceID := range transitioned {
			if err := recordPeerAlertTransition(ctx, tx, instanceID, false, opts); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return transitioned, nil
}

// UpsertActiveWithAlert stores a fresh peer observation and, when the peer was
// down, records the resolved alert in the same transaction.
func (r *PeerRepository) UpsertActiveWithAlert(ctx context.Context, instance *domain.MonitorInstance, opts PeerTransitionOptions) (bool, error) {
	if instance == nil || instance.InstanceID == "" {
		return false, fmt.Errorf("peer instance_id is required")
	}
	transitioned := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		peers := NewPeerRepository(tx)
		rows, err := tx.Raw(
			"UPDATE t_monitor_instances SET c_base_url = ?, c_status = ?, c_last_seen_at = ?, c_snapshot = ?, c_is_local = ? WHERE c_instance_id = ? AND c_status = ? RETURNING c_instance_id",
			instance.BaseURL, domain.InstanceStatusActive, instance.LastSeenAt, instance.Snapshot, instance.IsLocal, instance.InstanceID, domain.InstanceStatusDown,
		).Rows()
		if err != nil {
			return err
		}
		transitioned = rows.Next()
		if err := rows.Close(); err != nil {
			return err
		}
		if !transitioned {
			instance.Status = domain.InstanceStatusActive
			if err := peers.UpsertInstance(ctx, instance); err != nil {
				return err
			}
		}
		if transitioned && opts.Alerts {
			return recordPeerAlertTransition(ctx, tx, instance.InstanceID, true, opts)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return transitioned, nil
}

func recordPeerAlertTransition(ctx context.Context, tx *gorm.DB, instanceID string, available bool, opts PeerTransitionOptions) error {
	now := opts.OccurredAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	checkID := peerAlertCheckID(instanceID)
	alerts := NewAlertRepository(tx)
	existing, err := alerts.GetState(ctx, peerAlertSpaceID, checkID, checkID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if existing != nil && ((!available && existing.Status == domain.AlertStatusFiring) || (available && existing.Status == domain.AlertStatusResolved)) {
		return nil
	}
	state := &domain.AlertState{
		SpaceID: peerAlertSpaceID, RuleID: checkID, CheckID: checkID,
		Status: domain.AlertStatusFiring, FailureCount: 1,
		OwnerInstanceID: opts.OwnerInstanceID, TriggeredAt: &now, DedupeKey: checkID,
	}
	eventType := domain.AlertEventTriggered
	message := "monitor peer " + instanceID + " is unavailable"
	if available {
		eventType = domain.AlertEventResolved
		message = "monitor peer " + instanceID + " recovered"
		state.Status = domain.AlertStatusResolved
		state.FailureCount = 0
		state.SuccessCount = 1
		state.TriggeredAt = nil
		if existing != nil {
			state.TriggeredAt = existing.TriggeredAt
		}
		state.ResolvedAt = &now
	}
	digest := sha256.Sum256([]byte(instanceID + "\x00" + eventType + "\x00" + now.Format(time.RFC3339Nano)))
	event := &domain.AlertEvent{
		EventID: "peer-alert-" + hex.EncodeToString(digest[:16]), SpaceID: peerAlertSpaceID,
		RuleID: checkID, CheckID: checkID, EventType: eventType, Status: state.Status,
		OwnerInstanceID: opts.OwnerInstanceID, Message: message, Payload: "{}", CreatedAt: now,
	}
	if err := alerts.CreateEventIdempotent(ctx, event); err != nil {
		return err
	}
	return alerts.UpsertState(ctx, state)
}

func peerAlertCheckID(instanceID string) string {
	return "monitor-peer/" + instanceID
}
