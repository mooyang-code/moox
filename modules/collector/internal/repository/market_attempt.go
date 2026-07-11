package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type FinalizeAttemptRequest struct {
	Attempt  domain.MarketAttempt
	Subjects []domain.AttemptSubject
	Outbox   []domain.AttemptOutbox
	Now      time.Time
}
type AttemptReceipt struct {
	Attempt          domain.MarketAttempt
	Subjects         []domain.AttemptSubject
	Outbox           []domain.AttemptOutbox
	AlreadyFinalized bool
}
type MarketAttemptRepository struct{ db *gorm.DB }

func NewMarketAttemptRepository(db *gorm.DB) *MarketAttemptRepository {
	return &MarketAttemptRepository{db: db}
}

func (r *MarketAttemptRepository) GetReceipt(ctx context.Context, jobItemID string, attemptNo int32) (AttemptReceipt, error) {
	return getAttemptReceipt(r.db.WithContext(ctx), jobItemID, attemptNo)
}

func (r *MarketAttemptRepository) Finalize(ctx context.Context, request FinalizeAttemptRequest) (AttemptReceipt, error) {
	if request.Attempt.JobItemID == "" || request.Attempt.AttemptNo <= 0 {
		return AttemptReceipt{}, fmt.Errorf("job_item_id and positive attempt_no are required")
	}
	now := request.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var receipt AttemptReceipt
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		existing, err := getAttemptReceipt(tx, request.Attempt.JobItemID, request.Attempt.AttemptNo)
		if err == nil && existing.Attempt.Finalized {
			existing.AlreadyFinalized = true
			receipt = existing
			return nil
		}
		if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}
		attempt := request.Attempt
		attempt.Finalized = true
		attempt.FinalizedAt = &now
		attempt.ModifyTime = now
		if attempt.CreateTime.IsZero() {
			attempt.CreateTime = now
		}
		if attempt.Summary == "" {
			attempt.Summary = "{}"
		}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "c_job_item_id"}, {Name: "c_attempt_no"}}, DoUpdates: clause.AssignmentColumns([]string{"c_plan_id", "c_market_id", "c_space_id", "c_provider_id", "c_feed", "c_phase", "c_window_start", "c_window_end", "c_cursor", "c_status", "c_summary", "c_error_class", "c_finalized", "c_finalized_at", "c_mtime"})}).Create(&attempt).Error; err != nil {
			return err
		}
		for index := range request.Subjects {
			subject := request.Subjects[index]
			subject.JobItemID, subject.AttemptNo = attempt.JobItemID, attempt.AttemptNo
			if subject.TaskID == "" {
				return fmt.Errorf("attempt subject task_id is required")
			}
			if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&subject).Error; err != nil {
				return err
			}
			taskStatus := domain.InstanceStatusFailed
			if subject.Status == "success" || subject.Status == "empty" {
				taskStatus = domain.InstanceStatusSuccess
			}
			if err := tx.Model(&domain.TaskInstance{}).Where("c_space_id=? AND c_task_id=?", attempt.SpaceID, subject.TaskID).Updates(map[string]any{"c_last_exec_status": taskStatus, "c_result": attempt.Summary, "c_last_exec_time": now, "c_mtime": now}).Error; err != nil {
				return err
			}
		}
		for index := range request.Outbox {
			item := request.Outbox[index]
			item.ParentJobItemID, item.ParentAttemptNo = attempt.JobItemID, attempt.AttemptNo
			if item.OutboxID == "" {
				item.OutboxID = stableOutboxID(attempt.JobItemID, attempt.AttemptNo, item.Kind, item.Payload)
			}
			if item.Status == "" {
				item.Status = "pending"
			}
			if item.NextAttemptAt.IsZero() {
				item.NextAttemptAt = now
			}
			if item.CreateTime.IsZero() {
				item.CreateTime = now
			}
			item.ModifyTime = now
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&item).Error; err != nil {
				return err
			}
		}
		var receiptErr error
		receipt, receiptErr = getAttemptReceipt(tx, attempt.JobItemID, attempt.AttemptNo)
		return receiptErr
	})
	return receipt, err
}

func getAttemptReceipt(tx *gorm.DB, jobItemID string, attemptNo int32) (AttemptReceipt, error) {
	var attempt domain.MarketAttempt
	if err := tx.Where("c_job_item_id=? AND c_attempt_no=?", jobItemID, attemptNo).Take(&attempt).Error; err != nil {
		return AttemptReceipt{}, err
	}
	var subjects []domain.AttemptSubject
	if err := tx.Where("c_job_item_id=? AND c_attempt_no=?", jobItemID, attemptNo).Order("c_task_id").Find(&subjects).Error; err != nil {
		return AttemptReceipt{}, err
	}
	var outbox []domain.AttemptOutbox
	if err := tx.Where("c_parent_job_item_id=? AND c_parent_attempt_no=?", jobItemID, attemptNo).Order("c_outbox_id").Find(&outbox).Error; err != nil {
		return AttemptReceipt{}, err
	}
	return AttemptReceipt{Attempt: attempt, Subjects: subjects, Outbox: outbox}, nil
}
func stableOutboxID(jobItemID string, attemptNo int32, kind, payload string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%s", jobItemID, attemptNo, kind, payload)))
	return hex.EncodeToString(sum[:])
}
