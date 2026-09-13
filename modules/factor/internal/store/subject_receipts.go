package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SubjectReceiptInput struct {
	SpaceID         string
	EventID         string
	CatalogRevision int64
	Source          *storagepb.ViewSourceSubjectReady
	Tasks           []engine.FactorTask
}

type subjectReceipt struct {
	SpaceID         string    `gorm:"column:c_space_id;primaryKey"`
	EventID         string    `gorm:"column:c_event_id;primaryKey"`
	CatalogRevision int64     `gorm:"column:c_catalog_revision;primaryKey"`
	PeriodTime      int64     `gorm:"column:c_period_time"`
	SourceViewID    string    `gorm:"column:c_source_view_id"`
	EventJSON       string    `gorm:"column:c_event_json"`
	OutcomesJSON    string    `gorm:"column:c_outcomes_json"`
	Status          string    `gorm:"column:c_status"`
	UpdatedAt       time.Time `gorm:"column:c_updated_at"`
}

func (subjectReceipt) TableName() string { return "t_factor_subject_receipts" }

// CommitSubjectReceipt resolves all planned tasks from durable state, including
// tasks omitted from this attempt because a previous attempt completed them.
func (s *Store) CommitSubjectReceipt(ctx context.Context, input SubjectReceiptInput) error {
	p := input.Source
	if input.SpaceID == "" || input.EventID == "" || input.CatalogRevision <= 0 || p.GetPeriodTime() <= 0 || p.GetSourceViewId() == "" || p.GetInputContractVersion() == "" || p.GetSubjectId() == "" || p.GetSourceNodeId() == "" || p.GetSourceStoreId() == "" || p.GetSourceSequence() == 0 || p.GetSourceEventId() == "" {
		return fmt.Errorf("subject receipt identity is incomplete")
	}
	payload, err := protojson.Marshal(p)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var cutoff int64
		if err := tx.Raw("SELECT c_completed_before FROM t_factor_subject_gc WHERE c_id = 1").Scan(&cutoff).Error; err != nil {
			return err
		}
		if p.PeriodTime < cutoff {
			return nil
		}
		var identity subjectReceipt
		identityErr := tx.Where("c_space_id = ? AND c_event_id = ?", input.SpaceID, input.EventID).Take(&identity).Error
		if identityErr != nil && !errors.Is(identityErr, gorm.ErrRecordNotFound) {
			return identityErr
		}
		if identityErr == nil {
			var original storagepb.ViewSourceSubjectReady
			if err := protojson.Unmarshal([]byte(identity.EventJSON), &original); err != nil {
				return err
			}
			if !proto.Equal(&original, p) {
				return fmt.Errorf("subject receipt event identity was reused")
			}
		}
		outcomes := map[string]string{}
		status := "complete"
		if len(input.Tasks) == 0 {
			status = "no_op"
		}
		for _, task := range input.Tasks {
			if task.SpaceID != input.SpaceID || task.TriggerEventID != input.EventID || task.CatalogRevision != input.CatalogRevision || task.SourceViewID != p.SourceViewId || task.SubjectID != p.SubjectId || task.Freq != p.Frequency || task.PeriodTime != p.PeriodTime || task.SourceSeriesTag != p.SeriesTag || task.InputContractVersion != p.InputContractVersion || task.SourceNodeID != p.SourceNodeId || task.SourceStoreID != p.SourceStoreId || task.SourceSequence != p.SourceSequence || task.SourceEventID != p.SourceEventId {
				return fmt.Errorf("receipt task does not match source event")
			}
			scope, err := subjectTaskScope(task)
			if err != nil {
				return err
			}
			var head subjectHead
			if err := tx.Where("c_scope_key = ?", scope).Take(&head).Error; err != nil {
				return err
			}
			sequence, err := strconv.ParseUint(head.SourceSequence, 10, 64)
			if err != nil {
				return err
			}
			if head.SourceNode != task.SourceNodeID || head.SourceStore != task.SourceStoreID {
				return fmt.Errorf("receipt source incarnation is incomparable")
			}
			outcome := "superseded"
			if sequence <= task.SourceSequence && head.CatalogRevision <= task.CatalogRevision {
				if sequence != task.SourceSequence || head.TaskID != task.TaskID {
					return fmt.Errorf("receipt task was not admitted")
				}
				var run subjectRun
				if err := tx.Where("c_task_id = ? AND c_scope_key = ?", task.TaskID, scope).Take(&run).Error; err != nil {
					return err
				}
				if run.Status != "complete" && run.Status != "failed" {
					return fmt.Errorf("receipt task has no persisted outcome")
				}
				outcome = run.Status
			}
			outcomes[task.TaskID] = outcome
			if outcome == "failed" {
				status = "failed"
			} else if outcome == "superseded" && status != "failed" {
				status = "superseded"
			}
		}
		var previous subjectReceipt
		lookup := tx.Where("c_space_id = ? AND c_event_id = ? AND c_catalog_revision = ?", input.SpaceID, input.EventID, input.CatalogRevision).Take(&previous).Error
		if lookup != nil && !errors.Is(lookup, gorm.ErrRecordNotFound) {
			return lookup
		}
		if lookup == nil {
			var priorOutcomes map[string]string
			if err := json.Unmarshal([]byte(previous.OutcomesJSON), &priorOutcomes); err != nil {
				return err
			}
			if len(priorOutcomes) != len(outcomes) {
				return fmt.Errorf("subject receipt planned task set changed")
			}
			for taskID := range priorOutcomes {
				if _, found := outcomes[taskID]; !found {
					return fmt.Errorf("subject receipt planned task set changed")
				}
			}
		}
		raw, err := json.Marshal(outcomes)
		if err != nil {
			return err
		}
		row := subjectReceipt{SpaceID: input.SpaceID, EventID: input.EventID, CatalogRevision: input.CatalogRevision, PeriodTime: p.PeriodTime, SourceViewID: p.SourceViewId, EventJSON: string(payload), OutcomesJSON: string(raw), Status: status, UpdatedAt: time.Now().UTC()}
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error
	})
}
