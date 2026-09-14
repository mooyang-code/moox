package trigger

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RecalcAccepted  = "accepted"
	RecalcRunning   = "running"
	RecalcSucceeded = "succeeded"
	RecalcFailed    = "failed"
	RecalcCancelled = "cancelled"

	RecalcFailureMissingInput = "missing_input"
	RecalcFailureAlgorithm    = "algorithm_failure"
	RecalcFailureViewWaiting  = "view_waiting"
)

var (
	ErrRecalcMissingInput  = fmt.Errorf("recalc input is missing")
	ErrRecalcViewWaiting   = fmt.Errorf("recalc is waiting for View application")
	ErrRecalcEngineOffline = fmt.Errorf("recalc engine is offline")
	ErrRecalcJobNotFound   = fmt.Errorf("recalc job is not found")
)

type RecalcSpec struct {
	RequestID         string
	SpaceID           string
	DatasetID         string
	SourceViewID      string
	SubjectID         string
	Frequency         string
	FactorID          string
	BindingID         string
	BindingGeneration string
	StartTime         time.Time
	EndTime           time.Time
}

type RecalcJob struct {
	JobID             string `gorm:"column:c_job_id;primaryKey"`
	RequestID         string `gorm:"column:c_request_id"`
	SpaceID           string `gorm:"column:c_space_id"`
	DatasetID         string `gorm:"column:c_dataset_id"`
	SourceViewID      string `gorm:"column:c_source_view_id"`
	SubjectID         string `gorm:"column:c_subject_id"`
	Frequency         string `gorm:"column:c_freq"`
	FactorID          string `gorm:"column:c_factor_id"`
	BindingID         string `gorm:"column:c_binding_id"`
	BindingGeneration string `gorm:"column:c_binding_generation"`
	StartTime         int64  `gorm:"column:c_start_time"`
	EndTime           int64  `gorm:"column:c_end_time"`
	Status            string `gorm:"column:c_status"`
	FailureClass      string `gorm:"column:c_failure_class"`
	Error             string `gorm:"column:c_error"`
}

func (RecalcJob) TableName() string { return "t_factor_recalc_jobs" }

type RecalcExecutor interface {
	Run(context.Context, RecalcJob) error
}

type RecalcService struct {
	db       *store.Store
	executor RecalcExecutor
}

func NewRecalcService(db *store.Store, executor RecalcExecutor) (*RecalcService, error) {
	if db == nil {
		return nil, fmt.Errorf("recalc store is required")
	}
	return &RecalcService{db: db, executor: executor}, nil
}

func (s *RecalcService) Accept(ctx context.Context, spec RecalcSpec) (RecalcJob, error) {
	if s == nil {
		return RecalcJob{}, fmt.Errorf("recalc service is not initialized")
	}
	spec.RequestID = strings.TrimSpace(spec.RequestID)
	spec.SpaceID = strings.TrimSpace(spec.SpaceID)
	spec.SourceViewID = strings.TrimSpace(spec.SourceViewID)
	spec.SubjectID = strings.TrimSpace(spec.SubjectID)
	spec.Frequency = strings.TrimSpace(spec.Frequency)
	if spec.RequestID == "" || spec.SpaceID == "" || spec.SourceViewID == "" || spec.SubjectID == "" || spec.Frequency == "" || spec.StartTime.IsZero() || !spec.StartTime.Before(spec.EndTime) {
		return RecalcJob{}, fmt.Errorf("recalc job identity is incomplete")
	}
	job := RecalcJob{
		JobID: spec.RequestID, RequestID: spec.RequestID, SpaceID: spec.SpaceID, DatasetID: strings.TrimSpace(spec.DatasetID),
		SourceViewID: spec.SourceViewID, SubjectID: spec.SubjectID, Frequency: spec.Frequency,
		FactorID: strings.TrimSpace(spec.FactorID), BindingID: strings.TrimSpace(spec.BindingID),
		BindingGeneration: strings.TrimSpace(spec.BindingGeneration),
		StartTime:         spec.StartTime.UTC().Unix(), EndTime: spec.EndTime.UTC().Unix(),
		Status: RecalcAccepted,
	}
	var stored RecalcJob
	err := s.db.WithTx(ctx, func(tx *gorm.DB) error {
		var existing RecalcJob
		lookup := tx.Where("c_request_id = ?", spec.RequestID).Take(&existing).Error
		if lookup == nil {
			stored = existing
			return nil
		}
		if !errors.Is(lookup, gorm.ErrRecordNotFound) {
			return lookup
		}
		if err := tx.Create(&job).Error; err != nil {
			return err
		}
		stored = job
		return nil
	})
	return stored, err
}

func (s *RecalcService) Cancel(ctx context.Context, jobID string) error {
	if s == nil {
		return fmt.Errorf("recalc service is not initialized")
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return fmt.Errorf("recalc job id is required")
	}
	return s.db.WithTx(ctx, func(tx *gorm.DB) error {
		result := tx.Model(&RecalcJob{}).Where("c_job_id = ? AND c_status IN ?", jobID, []string{RecalcAccepted, RecalcRunning}).Updates(map[string]any{
			"c_status": RecalcCancelled, "c_mtime": time.Now().UTC(),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			var existing RecalcJob
			if err := tx.Where("c_job_id = ?", jobID).Take(&existing).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrRecalcJobNotFound
				}
				return err
			}
			if existing.Status == RecalcCancelled {
				return nil
			}
			return fmt.Errorf("recalc job %s cannot be cancelled", jobID)
		}
		return nil
	})
}

func (s *RecalcService) Execute(ctx context.Context, jobID string) error {
	if s == nil {
		return fmt.Errorf("recalc service is not initialized")
	}
	if s.executor == nil {
		return ErrRecalcEngineOffline
	}
	job, err := s.Get(ctx, jobID)
	if err != nil {
		return err
	}
	if job.Status == RecalcCancelled || job.Status == RecalcSucceeded {
		return nil
	}
	if job.Status == RecalcFailed {
		return nil
	}
	claimed, err := s.claim(ctx, job.JobID)
	if err != nil || !claimed {
		return err
	}
	runErr := s.executor.Run(ctx, job)
	if errors.Is(runErr, ErrRecalcEngineOffline) {
		return s.revertToAccepted(ctx, job.JobID)
	}
	status := RecalcSucceeded
	failure := ""
	message := ""
	if runErr != nil {
		status = RecalcFailed
		message = runErr.Error()
		switch {
		case errors.Is(runErr, ErrRecalcMissingInput):
			failure = RecalcFailureMissingInput
		case errors.Is(runErr, ErrRecalcViewWaiting):
			failure = RecalcFailureViewWaiting
		default:
			failure = RecalcFailureAlgorithm
		}
	}
	return s.db.WithTx(ctx, func(tx *gorm.DB) error {
		var current RecalcJob
		if err := tx.Where("c_job_id = ?", job.JobID).Take(&current).Error; err != nil {
			return err
		}
		if current.Status == RecalcCancelled {
			return nil
		}
		return tx.Model(&RecalcJob{}).Where("c_job_id = ?", job.JobID).Updates(map[string]any{
			"c_status": status, "c_failure_class": failure, "c_error": message, "c_mtime": time.Now().UTC(),
		}).Error
	})
}

func (s *RecalcService) Get(ctx context.Context, jobID string) (RecalcJob, error) {
	if s == nil {
		return RecalcJob{}, fmt.Errorf("recalc service is not initialized")
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return RecalcJob{}, fmt.Errorf("recalc job id is required")
	}
	var job RecalcJob
	err := s.db.WithTx(ctx, func(tx *gorm.DB) error {
		lookup := tx.Where("c_job_id = ?", jobID).Take(&job).Error
		if errors.Is(lookup, gorm.ErrRecordNotFound) {
			return ErrRecalcJobNotFound
		}
		return lookup
	})
	return job, err
}

type EngineHeartbeat struct {
	EngineID        string     `gorm:"column:c_engine_id;primaryKey"`
	DesiredRevision int64      `gorm:"column:c_desired_revision"`
	AppliedRevision int64      `gorm:"column:c_applied_revision"`
	LastSeen        *time.Time `gorm:"column:c_last_seen"`
	ModifyTime      time.Time  `gorm:"column:c_mtime"`
}

func (EngineHeartbeat) TableName() string { return "t_factor_engine_status" }

func (s *RecalcService) Heartbeat(ctx context.Context, engineID string, desired, applied int64) error {
	if s == nil {
		return fmt.Errorf("recalc service is not initialized")
	}
	engineID = strings.TrimSpace(engineID)
	if engineID == "" {
		return fmt.Errorf("engine id is required")
	}
	now := time.Now().UTC()
	row := EngineHeartbeat{EngineID: engineID, DesiredRevision: desired, AppliedRevision: applied, LastSeen: &now, ModifyTime: now}
	return s.db.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error
	})
}

func (s *RecalcService) LatestHeartbeat(ctx context.Context) (EngineHeartbeat, error) {
	if s == nil {
		return EngineHeartbeat{}, fmt.Errorf("recalc service is not initialized")
	}
	var row EngineHeartbeat
	err := s.db.WithTx(ctx, func(tx *gorm.DB) error {
		lookup := tx.Order("c_last_seen DESC").Take(&row).Error
		if errors.Is(lookup, gorm.ErrRecordNotFound) {
			return nil
		}
		return lookup
	})
	return row, err
}

func (s *RecalcService) CountByStatus(ctx context.Context, status string) (int64, error) {
	if s == nil {
		return 0, fmt.Errorf("recalc service is not initialized")
	}
	var n int64
	err := s.db.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Model(&RecalcJob{}).Where("c_status = ?", status).Count(&n).Error
	})
	return n, err
}

func (s *RecalcService) ClaimNext(ctx context.Context) (RecalcJob, bool, error) {
	if s == nil {
		return RecalcJob{}, false, fmt.Errorf("recalc service is not initialized")
	}
	var job RecalcJob
	found := false
	err := s.db.WithTx(ctx, func(tx *gorm.DB) error {
		var candidate RecalcJob
		lookup := tx.Where("c_status = ?", RecalcAccepted).Order("c_ctime ASC").Take(&candidate).Error
		if errors.Is(lookup, gorm.ErrRecordNotFound) {
			return nil
		}
		if lookup != nil {
			return lookup
		}
		now := time.Now().UTC()
		result := tx.Model(&RecalcJob{}).Where("c_job_id = ? AND c_status = ?", candidate.JobID, RecalcAccepted).Updates(map[string]any{
			"c_status": RecalcRunning, "c_mtime": now,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return nil
		}
		candidate.Status = RecalcRunning
		job = candidate
		found = true
		return nil
	})
	return job, found, err
}

func (s *RecalcService) ApplyResult(ctx context.Context, jobID, status, failure, message string) error {
	if s == nil {
		return fmt.Errorf("recalc service is not initialized")
	}
	jobID = strings.TrimSpace(jobID)
	status = strings.TrimSpace(status)
	if jobID == "" || status == "" {
		return fmt.Errorf("recalc result identity is incomplete")
	}
	if status != RecalcAccepted && status != RecalcSucceeded && status != RecalcFailed && status != RecalcCancelled {
		return fmt.Errorf("unsupported recalc status %s", status)
	}
	return s.db.WithTx(ctx, func(tx *gorm.DB) error {
		var current RecalcJob
		if err := tx.Where("c_job_id = ?", jobID).Take(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRecalcJobNotFound
			}
			return err
		}
		if current.Status == RecalcCancelled {
			return nil
		}
		if current.Status != RecalcRunning && status != RecalcCancelled {
			return fmt.Errorf("recalc job %s is not running", jobID)
		}
		return tx.Model(&RecalcJob{}).Where("c_job_id = ?", jobID).Updates(map[string]any{
			"c_status": status, "c_failure_class": strings.TrimSpace(failure), "c_error": strings.TrimSpace(message), "c_mtime": time.Now().UTC(),
		}).Error
	})
}

func (s *RecalcService) claim(ctx context.Context, jobID string) (bool, error) {
	claimed := false
	err := s.db.WithTx(ctx, func(tx *gorm.DB) error {
		result := tx.Model(&RecalcJob{}).Where("c_job_id = ? AND c_status = ?", jobID, RecalcAccepted).Updates(map[string]any{
			"c_status": RecalcRunning, "c_mtime": time.Now().UTC(),
		})
		if result.Error != nil {
			return result.Error
		}
		claimed = result.RowsAffected == 1
		return nil
	})
	return claimed, err
}

func (s *RecalcService) revertToAccepted(ctx context.Context, jobID string) error {
	return s.db.WithTx(ctx, func(tx *gorm.DB) error {
		return tx.Model(&RecalcJob{}).Where("c_job_id = ? AND c_status = ?", jobID, RecalcRunning).Updates(map[string]any{
			"c_status": RecalcAccepted, "c_mtime": time.Now().UTC(),
		}).Error
	})
}
