// Package projection persists CloudNode management-console query state.
package projection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	JobItemStatusPending       = "pending"
	JobItemStatusRunning       = "running"
	JobItemStatusSuccess       = "success"
	JobItemStatusFailed        = "failed"
	JobItemStatusCanceled      = "canceled"
	JobItemStatusEnqueueFailed = "enqueue_failed"

	JobItemAttemptStatusRunning  = "running"
	JobItemAttemptStatusSuccess  = "success"
	JobItemAttemptStatusFailed   = "failed"
	JobItemAttemptStatusLost     = "lost"
	JobItemAttemptStatusCanceled = "canceled"

	ReportStatusSuccess  = "success"
	ReportStatusFailed   = "failed"
	ReportStatusCanceled = "canceled"

	ErrorKindRetryable = "retryable"
	ErrorKindPermanent = "permanent"

	EnqueueStatusPending   = "pending"
	EnqueueStatusQueued    = "queued"
	EnqueueStatusFailed    = "enqueue_failed"
	EnqueueStatusDelivered = "delivered"
)

var (
	ErrConflict       = errors.New("job item state conflict")
	ErrStaleAttempt   = errors.New("stale job item attempt")
	ErrInactive       = errors.New("job item is not running")
	ErrInvalidJobItem = errors.New("invalid job item")
)

// Clock allows deterministic repository tests.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

// RepositoryOptions configures projection lifecycle semantics.
type RepositoryOptions struct {
	Clock              Clock
	RecoverAfterMillis int64
	DefaultMaxAttempts int
}

// Repository persists JobItem projections into SQLite.
type Repository struct {
	db                 *gorm.DB
	clock              Clock
	recoverAfterMillis int64
	defaultMaxAttempts int
}

// QueueMeta records queue metadata on the projection row.
type QueueMeta struct {
	Subject    string
	Stream     string
	StreamSeq  uint64
	AckSubject string
}

// CreateResult is returned for each submitted JobItem.
type CreateResult struct {
	JobItemID    string
	Status       pb.JobItemAckStatus
	RejectReason string
	Created      bool
	Deduplicated bool
}

// RunningRequest marks a pending projection row as leased to a node.
type RunningRequest struct {
	SpaceID    string
	JobItemID  string
	NodeID     string
	AckSubject string
	StreamSeq  uint64
}

// RunningState describes a newly running attempt.
type RunningState struct {
	AttemptNo  int
	AckSubject string
	RecoverAt  time.Time
}

// ReportEvent is the durable projection input for a finished attempt.
type ReportEvent struct {
	SpaceID       string
	JobItemID     string
	NodeID        string
	AttemptNo     int32
	Status        string
	ErrorKind     string
	ErrorCode     string
	ErrorMessage  string
	ResultSummary map[string]any
	DurationMS    int64
	Time          time.Time
}

type JobItem struct {
	ID               int        `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID          string     `gorm:"column:c_space_id;uniqueIndex:idx_cloud_job_items_unique"`
	JobID            string     `gorm:"column:c_job_id"`
	JobItemID        string     `gorm:"column:c_job_item_id;uniqueIndex:idx_cloud_job_items_unique"`
	JobType          string     `gorm:"column:c_job_type"`
	CodePackageID    string     `gorm:"column:c_code_package_id"`
	Params           string     `gorm:"column:c_params"`
	Priority         int        `gorm:"column:c_priority"`
	Status           string     `gorm:"column:c_status"`
	RunningNode      string     `gorm:"column:c_running_node"`
	AttemptNo        int        `gorm:"column:c_attempt_no"`
	RecoverAt        *time.Time `gorm:"column:c_recover_at"`
	ResultSummary    string     `gorm:"column:c_result_summary"`
	LastErrorKind    string     `gorm:"column:c_last_error_kind"`
	LastErrorCode    string     `gorm:"column:c_last_error_code"`
	LastErrorMessage string     `gorm:"column:c_last_error_message"`
	QueueSubject     string     `gorm:"column:c_queue_subject"`
	QueueMsgID       string     `gorm:"column:c_queue_msg_id"`
	StreamSeq        uint64     `gorm:"column:c_stream_seq"`
	AckSubject       string     `gorm:"column:c_ack_subject"`
	EnqueueStatus    string     `gorm:"column:c_enqueue_status"`
	ControlVersion   int64      `gorm:"column:c_control_version"`
	CancelReason     string     `gorm:"column:c_cancel_reason"`
	StartedAt        *time.Time `gorm:"column:c_start_time"`
	FinishedAt       *time.Time `gorm:"column:c_finish_time"`
	CreateTime       time.Time  `gorm:"column:c_ctime"`
	ModifyTime       time.Time  `gorm:"column:c_mtime"`
}

func (*JobItem) TableName() string { return "t_cloud_job_items" }

type JobItemAttempt struct {
	ID            int        `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID       string     `gorm:"column:c_space_id;uniqueIndex:idx_cloud_job_item_attempts_unique"`
	JobItemID     string     `gorm:"column:c_job_item_id;uniqueIndex:idx_cloud_job_item_attempts_unique"`
	AttemptNo     int        `gorm:"column:c_attempt_no;uniqueIndex:idx_cloud_job_item_attempts_unique"`
	NodeID        string     `gorm:"column:c_node_id"`
	Status        string     `gorm:"column:c_status"`
	ErrorKind     string     `gorm:"column:c_error_kind"`
	ErrorCode     string     `gorm:"column:c_error_code"`
	ErrorMessage  string     `gorm:"column:c_error_message"`
	ResultSummary string     `gorm:"column:c_result_summary"`
	StartedAt     time.Time  `gorm:"column:c_started_at"`
	FinishedAt    *time.Time `gorm:"column:c_finished_at"`
	CreateTime    time.Time  `gorm:"column:c_ctime"`
	ModifyTime    time.Time  `gorm:"column:c_mtime"`
}

func (*JobItemAttempt) TableName() string { return "t_cloud_job_item_attempts" }

// NewRepository creates a SQLite projection repository.
func NewRepository(db *gorm.DB, opts RepositoryOptions) *Repository {
	if opts.Clock == nil {
		opts.Clock = systemClock{}
	}
	if opts.RecoverAfterMillis <= 0 {
		opts.RecoverAfterMillis = int64(16 * time.Minute / time.Millisecond)
	}
	if opts.DefaultMaxAttempts <= 0 {
		opts.DefaultMaxAttempts = 3
	}
	return &Repository{
		db:                 db,
		clock:              opts.Clock,
		recoverAfterMillis: opts.RecoverAfterMillis,
		defaultMaxAttempts: opts.DefaultMaxAttempts,
	}
}

func (r *Repository) CreatePending(ctx context.Context, item *pb.JobItem, meta QueueMeta) (*CreateResult, error) {
	model, reject := buildJobItem(item, meta, r.now())
	if reject != "" {
		return &CreateResult{
			JobItemID:    strings.TrimSpace(item.GetJobItemId()),
			Status:       pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_REJECTED,
			RejectReason: reject,
		}, nil
	}
	result := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&model)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return &CreateResult{
			JobItemID:    model.JobItemID,
			Status:       pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_DEDUPLICATED,
			Deduplicated: true,
		}, nil
	}
	return &CreateResult{
		JobItemID: model.JobItemID,
		Status:    pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED,
		Created:   true,
	}, nil
}

func (r *Repository) MarkPublished(ctx context.Context, spaceID, jobItemID string, meta QueueMeta) error {
	now := r.now()
	return r.db.WithContext(ctx).Model(&JobItem{}).
		Where("c_space_id = ? AND c_job_item_id = ?", spaceID, jobItemID).
		Updates(map[string]any{
			"c_queue_subject":  strings.TrimSpace(meta.Subject),
			"c_queue_msg_id":   strings.TrimSpace(meta.Stream),
			"c_stream_seq":     meta.StreamSeq,
			"c_enqueue_status": EnqueueStatusQueued,
			"c_mtime":          now,
		}).Error
}

func (r *Repository) MarkEnqueueFailed(ctx context.Context, spaceID, jobItemID string, message string) error {
	now := r.now()
	return r.db.WithContext(ctx).Model(&JobItem{}).
		Where("c_space_id = ? AND c_job_item_id = ?", spaceID, jobItemID).
		Updates(map[string]any{
			"c_status":             JobItemStatusEnqueueFailed,
			"c_enqueue_status":     EnqueueStatusFailed,
			"c_last_error_kind":    ErrorKindRetryable,
			"c_last_error_code":    "ENQUEUE_FAILED",
			"c_last_error_message": strings.TrimSpace(message),
			"c_mtime":              now,
		}).Error
}

func (r *Repository) TryMarkRunning(ctx context.Context, req RunningRequest) (bool, RunningState, error) {
	now := r.now()
	var state RunningState
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item JobItem
		if err := tx.Where("c_space_id = ? AND c_job_item_id = ?", req.SpaceID, req.JobItemID).First(&item).Error; err != nil {
			return err
		}
		if item.Status == JobItemStatusRunning {
			if item.RecoverAt == nil || item.RecoverAt.After(now) {
				return ErrConflict
			}
			if err := tx.Model(&JobItemAttempt{}).
				Where("c_space_id = ? AND c_job_item_id = ? AND c_attempt_no = ? AND c_status = ?", item.SpaceID, item.JobItemID, item.AttemptNo, JobItemAttemptStatusRunning).
				Updates(map[string]any{
					"c_status":        JobItemAttemptStatusLost,
					"c_error_kind":    ErrorKindRetryable,
					"c_error_code":    "ATTEMPT_LOST",
					"c_error_message": "running attempt exceeded recover_at",
					"c_finished_at":   now,
					"c_mtime":         now,
				}).Error; err != nil {
				return err
			}
		} else if item.Status != JobItemStatusPending && item.Status != JobItemStatusEnqueueFailed {
			return ErrConflict
		}
		if item.AttemptNo >= r.defaultMaxAttempts {
			if err := markTerminalFailed(tx, item, now, "MAX_ATTEMPTS_EXCEEDED", "max attempts exceeded"); err != nil {
				return err
			}
			return ErrConflict
		}
		attemptNo := item.AttemptNo + 1
		recoverAt := now.Add(time.Duration(r.recoverAfterMillis) * time.Millisecond)
		result := tx.Model(&JobItem{}).
			Where("c_id = ? AND c_attempt_no = ? AND c_status IN ?", item.ID, item.AttemptNo, []string{JobItemStatusPending, JobItemStatusEnqueueFailed, JobItemStatusRunning}).
			Updates(map[string]any{
				"c_status":         JobItemStatusRunning,
				"c_running_node":   strings.TrimSpace(req.NodeID),
				"c_attempt_no":     attemptNo,
				"c_recover_at":     recoverAt,
				"c_ack_subject":    strings.TrimSpace(req.AckSubject),
				"c_stream_seq":     req.StreamSeq,
				"c_enqueue_status": EnqueueStatusDelivered,
				"c_start_time":     now,
				"c_mtime":          now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrConflict
		}
		attempt := JobItemAttempt{
			SpaceID:    item.SpaceID,
			JobItemID:  item.JobItemID,
			AttemptNo:  attemptNo,
			NodeID:     strings.TrimSpace(req.NodeID),
			Status:     JobItemAttemptStatusRunning,
			StartedAt:  now,
			CreateTime: now,
			ModifyTime: now,
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&attempt).Error; err != nil {
			return err
		}
		state = RunningState{AttemptNo: attemptNo, AckSubject: strings.TrimSpace(req.AckSubject), RecoverAt: recoverAt}
		return nil
	})
	if errors.Is(err, ErrConflict) {
		return false, RunningState{}, nil
	}
	if err != nil {
		return false, RunningState{}, err
	}
	return true, state, nil
}

func (r *Repository) MarkReportedBatch(ctx context.Context, reports []ReportEvent) error {
	if len(reports) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, report := range reports {
			if err := r.markReported(tx, report); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Repository) MarkCanceled(ctx context.Context, spaceID, jobItemID, reason string) error {
	now := r.now()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item JobItem
		if err := tx.Where("c_space_id = ? AND c_job_item_id = ?", spaceID, jobItemID).First(&item).Error; err != nil {
			return err
		}
		if item.Status != JobItemStatusPending && item.Status != JobItemStatusRunning && item.Status != JobItemStatusEnqueueFailed {
			return ErrConflict
		}
		result := tx.Model(&JobItem{}).
			Where("c_id = ? AND c_status IN ?", item.ID, []string{JobItemStatusPending, JobItemStatusRunning, JobItemStatusEnqueueFailed}).
			Updates(map[string]any{
				"c_status":          JobItemStatusCanceled,
				"c_cancel_reason":   strings.TrimSpace(reason),
				"c_control_version": gorm.Expr("c_control_version + 1"),
				"c_recover_at":      nil,
				"c_finish_time":     now,
				"c_mtime":           now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrConflict
		}
		if item.Status == JobItemStatusRunning {
			return tx.Model(&JobItemAttempt{}).
				Where("c_space_id = ? AND c_job_item_id = ? AND c_attempt_no = ? AND c_status = ?", item.SpaceID, item.JobItemID, item.AttemptNo, JobItemAttemptStatusRunning).
				Updates(map[string]any{
					"c_status":      JobItemAttemptStatusCanceled,
					"c_error_kind":  ErrorKindPermanent,
					"c_error_code":  "CANCELED",
					"c_finished_at": now,
					"c_mtime":       now,
				}).Error
		}
		return nil
	})
}

func (r *Repository) Get(ctx context.Context, req *pb.GetJobItemReq) (*pb.JobItemDetail, error) {
	var item JobItem
	if err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_job_item_id = ?", req.GetSpaceId(), req.GetJobItemId()).First(&item).Error; err != nil {
		return nil, err
	}
	return jobItemDetail(item), nil
}

func (r *Repository) GetModel(ctx context.Context, spaceID, jobItemID string) (*JobItem, error) {
	var item JobItem
	if err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_job_item_id = ?", spaceID, jobItemID).First(&item).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *Repository) List(ctx context.Context, req *pb.ListJobItemsReq) ([]*pb.JobItemDetail, *pb.PageResult, error) {
	page, size := pageValues(req.GetPage())
	q := r.db.WithContext(ctx).Model(&JobItem{}).Where("c_space_id = ?", req.GetSpaceId())
	if strings.TrimSpace(req.GetJobId()) != "" {
		q = q.Where("c_job_id = ?", strings.TrimSpace(req.GetJobId()))
	}
	if strings.TrimSpace(req.GetJobType()) != "" {
		q = q.Where("c_job_type = ?", strings.TrimSpace(req.GetJobType()))
	}
	if status := jobItemStatusToDB(req.GetStatus()); status != "" {
		q = q.Where("c_status = ?", status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, nil, err
	}
	var items []JobItem
	if err := q.Order("c_ctime DESC").Offset((page - 1) * size).Limit(size).Find(&items).Error; err != nil {
		return nil, nil, err
	}
	out := make([]*pb.JobItemDetail, 0, len(items))
	for _, item := range items {
		out = append(out, jobItemDetail(item))
	}
	return out, &pb.PageResult{
		Page:    uint32(page),
		Size:    uint32(size),
		Total:   uint32(total),
		HasMore: int64(page*size) < total,
	}, nil
}

func (r *Repository) ListAttempts(ctx context.Context, req *pb.ListJobItemAttemptsReq) ([]*pb.JobItemAttempt, error) {
	var attempts []JobItemAttempt
	if err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_job_item_id = ?", req.GetSpaceId(), req.GetJobItemId()).
		Order("c_attempt_no ASC").
		Find(&attempts).Error; err != nil {
		return nil, err
	}
	out := make([]*pb.JobItemAttempt, 0, len(attempts))
	for _, attempt := range attempts {
		out = append(out, &pb.JobItemAttempt{
			AttemptNo:     int32(attempt.AttemptNo),
			NodeId:        attempt.NodeID,
			Status:        attemptStatusToPB(attempt.Status),
			ErrorKind:     errorKindToPB(attempt.ErrorKind),
			ErrorCode:     attempt.ErrorCode,
			ErrorMessage:  attempt.ErrorMessage,
			ResultSummary: jsonToStruct(attempt.ResultSummary),
			StartedAt:     timeToPB(&attempt.StartedAt),
			FinishedAt:    timeToPB(attempt.FinishedAt),
		})
	}
	return out, nil
}

func (r *Repository) ListEnqueueRetryCandidates(ctx context.Context, limit int) ([]*pb.JobItem, error) {
	if limit <= 0 {
		limit = 100
	}
	var items []JobItem
	if err := r.db.WithContext(ctx).
		Where("c_status IN ? AND (c_enqueue_status IN ? OR c_stream_seq = 0)",
			[]string{JobItemStatusPending, JobItemStatusEnqueueFailed},
			[]string{EnqueueStatusPending, EnqueueStatusFailed}).
		Order("c_ctime ASC").
		Limit(limit).
		Find(&items).Error; err != nil {
		return nil, err
	}
	out := make([]*pb.JobItem, 0, len(items))
	for _, item := range items {
		out = append(out, &pb.JobItem{
			SpaceId:       item.SpaceID,
			JobId:         item.JobID,
			JobItemId:     item.JobItemID,
			JobType:       item.JobType,
			CodePackageId: item.CodePackageID,
			Params:        jsonToStruct(item.Params),
			Priority:      int32(item.Priority),
		})
	}
	return out, nil
}

func (r *Repository) ListCancelDirectives(ctx context.Context, spaceID, nodeID string, limit int) ([]*pb.ControlDirective, error) {
	if limit <= 0 {
		limit = 20
	}
	var items []JobItem
	if err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_running_node = ? AND c_status = ? AND c_attempt_no > 0", spaceID, nodeID, JobItemStatusCanceled).
		Order("c_mtime DESC").
		Limit(limit).
		Find(&items).Error; err != nil {
		return nil, err
	}
	out := make([]*pb.ControlDirective, 0, len(items))
	for _, item := range items {
		out = append(out, &pb.ControlDirective{
			Type:      pb.ControlDirectiveType_CONTROL_DIRECTIVE_CANCEL,
			JobItemId: item.JobItemID,
			AttemptNo: int32(item.AttemptNo),
			Reason:    firstNonEmpty(item.CancelReason, "job item canceled"),
		})
	}
	return out, nil
}

func (r *Repository) ClearCancelDirective(ctx context.Context, spaceID, jobItemID string, attemptNo int32) error {
	return r.db.WithContext(ctx).Model(&JobItem{}).
		Where("c_space_id = ? AND c_job_item_id = ? AND c_attempt_no = ? AND c_status = ?", spaceID, jobItemID, attemptNo, JobItemStatusCanceled).
		Updates(map[string]any{
			"c_running_node": "",
			"c_ack_subject":  "",
			"c_mtime":        r.now(),
		}).Error
}

func (r *Repository) now() time.Time {
	return r.clock.Now().UTC()
}

func (r *Repository) markReported(tx *gorm.DB, report ReportEvent) error {
	now := report.Time.UTC()
	if now.IsZero() {
		now = r.now()
	}
	var item JobItem
	if err := tx.Where("c_space_id = ? AND c_job_item_id = ?", report.SpaceID, report.JobItemID).First(&item).Error; err != nil {
		return err
	}
	if item.AttemptNo != int(report.AttemptNo) || item.RunningNode != strings.TrimSpace(report.NodeID) {
		var attempt JobItemAttempt
		err := tx.Where("c_space_id = ? AND c_job_item_id = ? AND c_attempt_no = ?", report.SpaceID, report.JobItemID, report.AttemptNo).First(&attempt).Error
		if err == nil && attempt.Status != JobItemAttemptStatusRunning {
			return nil
		}
		return ErrStaleAttempt
	}
	if item.Status != JobItemStatusRunning {
		return nil
	}
	nextStatus := JobItemStatusSuccess
	attemptStatus := JobItemAttemptStatusSuccess
	errorKind := normalizeErrorKind(report.ErrorKind)
	switch report.Status {
	case ReportStatusSuccess:
		nextStatus = JobItemStatusSuccess
		attemptStatus = JobItemAttemptStatusSuccess
	case ReportStatusCanceled:
		nextStatus = JobItemStatusCanceled
		attemptStatus = JobItemAttemptStatusCanceled
	case ReportStatusFailed:
		attemptStatus = JobItemAttemptStatusFailed
		if errorKind == ErrorKindRetryable && item.AttemptNo < r.defaultMaxAttempts {
			nextStatus = JobItemStatusPending
		} else {
			nextStatus = JobItemStatusFailed
			if errorKind == "" {
				errorKind = ErrorKindPermanent
			}
		}
	default:
		return fmt.Errorf("invalid report status %q", report.Status)
	}
	resultSummary := mapToJSON(report.ResultSummary)
	updates := map[string]any{
		"c_status":             nextStatus,
		"c_result_summary":     resultSummary,
		"c_last_error_kind":    errorKind,
		"c_last_error_code":    strings.TrimSpace(report.ErrorCode),
		"c_last_error_message": strings.TrimSpace(report.ErrorMessage),
		"c_mtime":              now,
	}
	if nextStatus != JobItemStatusRunning {
		updates["c_running_node"] = ""
		updates["c_recover_at"] = nil
		updates["c_ack_subject"] = ""
	}
	if nextStatus == JobItemStatusSuccess || nextStatus == JobItemStatusFailed || nextStatus == JobItemStatusCanceled {
		updates["c_finish_time"] = now
	}
	result := tx.Model(&JobItem{}).
		Where("c_id = ? AND c_attempt_no = ? AND c_status = ?", item.ID, item.AttemptNo, JobItemStatusRunning).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrStaleAttempt
	}
	return tx.Model(&JobItemAttempt{}).
		Where("c_space_id = ? AND c_job_item_id = ? AND c_attempt_no = ?", item.SpaceID, item.JobItemID, item.AttemptNo).
		Updates(map[string]any{
			"c_status":         attemptStatus,
			"c_error_kind":     errorKind,
			"c_error_code":     strings.TrimSpace(report.ErrorCode),
			"c_error_message":  strings.TrimSpace(report.ErrorMessage),
			"c_result_summary": resultSummary,
			"c_finished_at":    now,
			"c_mtime":          now,
		}).Error
}

func buildJobItem(item *pb.JobItem, meta QueueMeta, now time.Time) (JobItem, string) {
	if item == nil {
		return JobItem{}, "item is required"
	}
	spaceID := strings.TrimSpace(item.GetSpaceId())
	jobID := strings.TrimSpace(item.GetJobId())
	jobItemID := strings.TrimSpace(item.GetJobItemId())
	jobType := strings.TrimSpace(item.GetJobType())
	codePackageID := strings.TrimSpace(item.GetCodePackageId())
	switch {
	case spaceID == "":
		return JobItem{}, "space_id is required"
	case jobID == "":
		return JobItem{}, "job_id is required"
	case jobItemID == "":
		return JobItem{}, "job_item_id is required"
	case jobType == "":
		return JobItem{}, "job_type is required"
	case codePackageID == "":
		return JobItem{}, "code_package_id is required"
	}
	return JobItem{
		SpaceID:       spaceID,
		JobID:         jobID,
		JobItemID:     jobItemID,
		JobType:       jobType,
		CodePackageID: codePackageID,
		Params:        structToJSON(item.GetParams()),
		Priority:      int(item.GetPriority()),
		Status:        JobItemStatusPending,
		QueueSubject:  strings.TrimSpace(meta.Subject),
		QueueMsgID:    strings.TrimSpace(meta.Stream),
		StreamSeq:     meta.StreamSeq,
		AckSubject:    strings.TrimSpace(meta.AckSubject),
		EnqueueStatus: EnqueueStatusPending,
		CreateTime:    now,
		ModifyTime:    now,
	}, ""
}

func markTerminalFailed(tx *gorm.DB, item JobItem, now time.Time, code string, message string) error {
	return tx.Model(&JobItem{}).
		Where("c_id = ? AND c_attempt_no = ?", item.ID, item.AttemptNo).
		Updates(map[string]any{
			"c_status":             JobItemStatusFailed,
			"c_running_node":       "",
			"c_recover_at":         nil,
			"c_last_error_kind":    ErrorKindPermanent,
			"c_last_error_code":    code,
			"c_last_error_message": message,
			"c_finish_time":        now,
			"c_mtime":              now,
		}).Error
}

func normalizeErrorKind(kind string) string {
	switch strings.TrimSpace(strings.ToLower(kind)) {
	case ErrorKindRetryable:
		return ErrorKindRetryable
	case ErrorKindPermanent:
		return ErrorKindPermanent
	default:
		return ""
	}
}

func structToJSON(st *structpb.Struct) string {
	if st == nil {
		return "{}"
	}
	raw, err := json.Marshal(st.AsMap())
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func jsonToStruct(raw string) *structpb.Struct {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return &structpb.Struct{}
	}
	values := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return &structpb.Struct{}
	}
	st, err := structpb.NewStruct(values)
	if err != nil {
		return &structpb.Struct{}
	}
	return st
}

func mapToJSON(values map[string]any) string {
	if values == nil {
		return "{}"
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func timeToPB(ts *time.Time) *timestamppb.Timestamp {
	if ts == nil || ts.IsZero() {
		return nil
	}
	return timestamppb.New(ts.UTC())
}

func pageValues(page *pb.Page) (int, int) {
	if page == nil {
		return 1, 50
	}
	p := int(page.GetPage())
	size := int(page.GetSize())
	if p <= 0 {
		p = 1
	}
	if size <= 0 {
		size = 50
	}
	if size > 1000 {
		size = 1000
	}
	return p, size
}

func jobItemDetail(item JobItem) *pb.JobItemDetail {
	return &pb.JobItemDetail{
		SpaceId:          item.SpaceID,
		JobId:            item.JobID,
		JobItemId:        item.JobItemID,
		JobType:          item.JobType,
		CodePackageId:    item.CodePackageID,
		Params:           jsonToStruct(item.Params),
		Priority:         int32(item.Priority),
		Status:           jobItemStatusToPB(item.Status),
		RunningNode:      item.RunningNode,
		AttemptNo:        int32(item.AttemptNo),
		RecoverAt:        timeToPB(item.RecoverAt),
		ResultSummary:    jsonToStruct(item.ResultSummary),
		LastErrorKind:    errorKindToPB(item.LastErrorKind),
		LastErrorCode:    item.LastErrorCode,
		LastErrorMessage: item.LastErrorMessage,
		CreateTime:       timeToPB(&item.CreateTime),
		StartTime:        timeToPB(item.StartedAt),
		FinishTime:       timeToPB(item.FinishedAt),
	}
}

func jobItemStatusToDB(status pb.JobItemStatus) string {
	switch status {
	case pb.JobItemStatus_JOB_ITEM_STATUS_PENDING:
		return JobItemStatusPending
	case pb.JobItemStatus_JOB_ITEM_STATUS_RUNNING:
		return JobItemStatusRunning
	case pb.JobItemStatus_JOB_ITEM_STATUS_SUCCESS:
		return JobItemStatusSuccess
	case pb.JobItemStatus_JOB_ITEM_STATUS_FAILED:
		return JobItemStatusFailed
	case pb.JobItemStatus_JOB_ITEM_STATUS_CANCELED:
		return JobItemStatusCanceled
	case pb.JobItemStatus_JOB_ITEM_STATUS_ENQUEUE_FAILED:
		return JobItemStatusEnqueueFailed
	default:
		return ""
	}
}

func jobItemStatusToPB(status string) pb.JobItemStatus {
	switch status {
	case JobItemStatusPending:
		return pb.JobItemStatus_JOB_ITEM_STATUS_PENDING
	case JobItemStatusRunning:
		return pb.JobItemStatus_JOB_ITEM_STATUS_RUNNING
	case JobItemStatusSuccess:
		return pb.JobItemStatus_JOB_ITEM_STATUS_SUCCESS
	case JobItemStatusFailed:
		return pb.JobItemStatus_JOB_ITEM_STATUS_FAILED
	case JobItemStatusEnqueueFailed:
		return pb.JobItemStatus_JOB_ITEM_STATUS_ENQUEUE_FAILED
	case JobItemStatusCanceled:
		return pb.JobItemStatus_JOB_ITEM_STATUS_CANCELED
	default:
		return pb.JobItemStatus_JOB_ITEM_STATUS_UNSPECIFIED
	}
}

func errorKindToPB(kind string) pb.JobItemErrorKind {
	switch kind {
	case ErrorKindRetryable:
		return pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_RETRYABLE
	case ErrorKindPermanent:
		return pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_PERMANENT
	default:
		return pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_UNSPECIFIED
	}
}

func attemptStatusToPB(status string) pb.JobItemAttemptStatus {
	switch status {
	case JobItemAttemptStatusRunning:
		return pb.JobItemAttemptStatus_JOB_ITEM_ATTEMPT_STATUS_RUNNING
	case JobItemAttemptStatusSuccess:
		return pb.JobItemAttemptStatus_JOB_ITEM_ATTEMPT_STATUS_SUCCESS
	case JobItemAttemptStatusFailed:
		return pb.JobItemAttemptStatus_JOB_ITEM_ATTEMPT_STATUS_FAILED
	case JobItemAttemptStatusLost:
		return pb.JobItemAttemptStatus_JOB_ITEM_ATTEMPT_STATUS_LOST
	case JobItemAttemptStatusCanceled:
		return pb.JobItemAttemptStatus_JOB_ITEM_ATTEMPT_STATUS_CANCELED
	default:
		return pb.JobItemAttemptStatus_JOB_ITEM_ATTEMPT_STATUS_UNSPECIFIED
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
