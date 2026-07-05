// Package repository contains CloudNode persistence adapters.
package repository

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

	JobItemAttemptStatusRunning = "running"
	JobItemAttemptStatusSuccess = "success"
	JobItemAttemptStatusFailed  = "failed"
	JobItemAttemptStatusLost    = "lost"

	JobItemErrorKindRetryable = "retryable"
	JobItemErrorKindPermanent = "permanent"

	defaultJobItemLimit       = 10
	maxJobItemLimit           = 100
	defaultRecoverAfterMillis = int64(16 * 60 * 1000)
	defaultMaxAttempts        = 3
)

var (
	ErrPollingNodeNotFound = errors.New("polling node not found")
	ErrStaleJobItemAttempt = errors.New("stale job item attempt")
	ErrJobItemInactive     = errors.New("job item is not running")
	ErrJobItemConflict     = errors.New("job item state conflict")
)

type JobItemClock interface {
	Now() time.Time
}

type JobItemRepositoryOptions struct {
	Clock              JobItemClock
	DispatchPolicy     DispatchPolicy
	DefaultLimit       int
	MaxLimit           int
	RecoverAfterMillis int64
	DefaultMaxAttempts int
}

type systemJobItemClock struct{}

func (systemJobItemClock) Now() time.Time {
	return time.Now().UTC()
}

// QueueStore is the narrow async JobItem queue contract used by CloudNode RPC.
type QueueStore interface {
	Submit(ctx context.Context, items []*pb.JobItem) ([]*pb.JobItemAck, error)
	Poll(ctx context.Context, req *pb.PollJobItemsReq) ([]*pb.PolledJobItem, error)
	Report(ctx context.Context, req *pb.ReportJobItemStatusReq) error
	Cancel(ctx context.Context, req *pb.CancelJobItemReq) error
	Get(ctx context.Context, req *pb.GetJobItemReq) (*pb.JobItemDetail, error)
	List(ctx context.Context, req *pb.ListJobItemsReq) ([]*pb.JobItemDetail, *pb.PageResult, error)
	ListAttempts(ctx context.Context, req *pb.ListJobItemAttemptsReq) ([]*pb.JobItemAttempt, error)
}

// DispatchPolicy controls the deterministic order used when leasing pending JobItems.
type DispatchPolicy interface {
	Apply(*gorm.DB) *gorm.DB
}

// PriorityFIFODispatchPolicy leases higher priority first, then older JobItems.
type PriorityFIFODispatchPolicy struct{}

func (PriorityFIFODispatchPolicy) Apply(q *gorm.DB) *gorm.DB {
	return q.Order("c_priority DESC, c_ctime ASC")
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

type JobItemRepository struct {
	db                 *gorm.DB
	clock              JobItemClock
	dispatchPolicy     DispatchPolicy
	defaultLimit       int
	maxLimit           int
	recoverAfterMillis int64
	defaultMaxAttempts int
}

var _ QueueStore = (*JobItemRepository)(nil)

func NewJobItemRepository(db *gorm.DB) *JobItemRepository {
	return NewJobItemRepositoryWithOptions(db, JobItemRepositoryOptions{})
}

func NewJobItemRepositoryWithOptions(db *gorm.DB, opts JobItemRepositoryOptions) *JobItemRepository {
	if opts.Clock == nil {
		opts.Clock = systemJobItemClock{}
	}
	if opts.DispatchPolicy == nil {
		opts.DispatchPolicy = PriorityFIFODispatchPolicy{}
	}
	if opts.DefaultLimit <= 0 {
		opts.DefaultLimit = defaultJobItemLimit
	}
	if opts.MaxLimit <= 0 {
		opts.MaxLimit = maxJobItemLimit
	}
	if opts.DefaultLimit > opts.MaxLimit {
		opts.DefaultLimit = opts.MaxLimit
	}
	if opts.RecoverAfterMillis <= 0 {
		opts.RecoverAfterMillis = defaultRecoverAfterMillis
	}
	if opts.DefaultMaxAttempts <= 0 {
		opts.DefaultMaxAttempts = defaultMaxAttempts
	}
	return &JobItemRepository{
		db:                 db,
		clock:              opts.Clock,
		dispatchPolicy:     opts.DispatchPolicy,
		defaultLimit:       opts.DefaultLimit,
		maxLimit:           opts.MaxLimit,
		recoverAfterMillis: opts.RecoverAfterMillis,
		defaultMaxAttempts: opts.DefaultMaxAttempts,
	}
}

func (r *JobItemRepository) now() time.Time {
	return r.clock.Now().UTC()
}

func (r *JobItemRepository) Submit(ctx context.Context, items []*pb.JobItem) ([]*pb.JobItemAck, error) {
	now := r.now()
	acks := make([]*pb.JobItemAck, 0, len(items))
	if len(items) == 0 {
		return acks, nil
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, item := range items {
			model, reject := buildJobItem(item, now)
			if reject != "" {
				acks = append(acks, &pb.JobItemAck{
					JobItemId:    jobItemID(item),
					Status:       pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_REJECTED,
					RejectReason: reject,
				})
				continue
			}
			result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				acks = append(acks, &pb.JobItemAck{
					JobItemId: model.JobItemID,
					Status:    pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_DEDUPLICATED,
				})
				continue
			}
			acks = append(acks, &pb.JobItemAck{
				JobItemId: model.JobItemID,
				Status:    pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED,
			})
		}
		return nil
	})
	return acks, err
}

func (r *JobItemRepository) Poll(ctx context.Context, req *pb.PollJobItemsReq) ([]*pb.PolledJobItem, error) {
	now := r.now()
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = r.defaultLimit
	}
	if limit > r.maxLimit {
		limit = r.maxLimit
	}
	var out []*pb.PolledJobItem
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		node, err := loadPollingJobNode(tx, req)
		if err != nil {
			return err
		}
		if err := r.recoverExpiredRunning(tx, req.GetSpaceId(), now); err != nil {
			return err
		}
		q := tx.Model(&JobItem{}).
			Where("c_space_id = ? AND c_status = ?", req.GetSpaceId(), JobItemStatusPending).
			Where("c_code_package_id = ?", node.PackageID)
		if len(req.GetSupportedJobTypes()) > 0 {
			q = q.Where("c_job_type IN ?", compactJobItemStrings(req.GetSupportedJobTypes()))
		}
		var items []JobItem
		if err := r.dispatchPolicy.Apply(q).Limit(limit).Find(&items).Error; err != nil {
			return err
		}
		for _, item := range items {
			if item.AttemptNo >= r.defaultMaxAttempts {
				if err := failMaxedJobItem(tx, item, now); err != nil {
					return err
				}
				continue
			}
			attemptNo := item.AttemptNo + 1
			recoverAt := now.Add(time.Duration(r.recoverAfterMillis) * time.Millisecond)
			result := tx.Model(&JobItem{}).
				Where("c_space_id = ? AND c_job_item_id = ? AND c_attempt_no = ? AND c_status = ?", item.SpaceID, item.JobItemID, item.AttemptNo, JobItemStatusPending).
				Updates(map[string]any{
					"c_status":       JobItemStatusRunning,
					"c_running_node": req.GetNodeId(),
					"c_attempt_no":   attemptNo,
					"c_recover_at":   recoverAt,
					"c_start_time":   now,
					"c_mtime":        now,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				continue
			}
			attempt := JobItemAttempt{
				SpaceID:    item.SpaceID,
				JobItemID:  item.JobItemID,
				AttemptNo:  attemptNo,
				NodeID:     req.GetNodeId(),
				Status:     JobItemAttemptStatusRunning,
				StartedAt:  now,
				CreateTime: now,
				ModifyTime: now,
			}
			if err := tx.Create(&attempt).Error; err != nil {
				return err
			}
			out = append(out, &pb.PolledJobItem{
				SpaceId:       item.SpaceID,
				JobId:         item.JobID,
				JobItemId:     item.JobItemID,
				JobType:       item.JobType,
				CodePackageId: item.CodePackageID,
				Params:        jsonToStruct(item.Params),
				AttemptNo:     int32(attemptNo),
			})
		}
		return nil
	})
	return out, err
}

func (r *JobItemRepository) Report(ctx context.Context, req *pb.ReportJobItemStatusReq) error {
	now := r.now()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if strings.TrimSpace(req.GetNodeId()) == "" {
			return fmt.Errorf("node_id is required")
		}
		if req.GetAttemptNo() <= 0 {
			return fmt.Errorf("attempt_no is required")
		}
		var item JobItem
		if err := tx.Where("c_space_id = ? AND c_job_item_id = ?", req.GetSpaceId(), req.GetJobItemId()).First(&item).Error; err != nil {
			return err
		}
		if item.RunningNode != req.GetNodeId() || item.AttemptNo != int(req.GetAttemptNo()) {
			return fmt.Errorf("%w: running_node=%s attempt_no=%d", ErrStaleJobItemAttempt, item.RunningNode, item.AttemptNo)
		}
		if item.Status != JobItemStatusRunning {
			return fmt.Errorf("%w: status=%s", ErrJobItemInactive, item.Status)
		}
		resultSummary := structToJSON(req.GetResultSummary())
		errorKind := jobItemErrorKindToDB(req.GetErrorKind())
		nextStatus := JobItemStatusSuccess
		attemptStatus := JobItemAttemptStatusSuccess
		if req.GetStatus() == pb.JobItemReportStatus_JOB_ITEM_REPORT_STATUS_FAILED {
			attemptStatus = JobItemAttemptStatusFailed
			if errorKind == JobItemErrorKindRetryable && item.AttemptNo < r.defaultMaxAttempts {
				nextStatus = JobItemStatusPending
			} else {
				nextStatus = JobItemStatusFailed
				if errorKind == "" {
					errorKind = JobItemErrorKindPermanent
				}
			}
		} else if req.GetStatus() != pb.JobItemReportStatus_JOB_ITEM_REPORT_STATUS_SUCCESS {
			return fmt.Errorf("invalid job item report status: %v", req.GetStatus())
		}
		updates := map[string]any{
			"c_status":             nextStatus,
			"c_result_summary":     resultSummary,
			"c_last_error_kind":    errorKind,
			"c_last_error_code":    strings.TrimSpace(req.GetErrorCode()),
			"c_last_error_message": strings.TrimSpace(req.GetErrorMessage()),
			"c_mtime":              now,
		}
		if nextStatus != JobItemStatusRunning {
			updates["c_running_node"] = ""
			updates["c_recover_at"] = nil
		}
		if nextStatus == JobItemStatusSuccess || nextStatus == JobItemStatusFailed {
			updates["c_finish_time"] = now
		}
		result := tx.Model(&JobItem{}).
			Where("c_id = ? AND c_running_node = ? AND c_attempt_no = ? AND c_status = ?", item.ID, req.GetNodeId(), req.GetAttemptNo(), JobItemStatusRunning).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrStaleJobItemAttempt
		}
		return tx.Model(&JobItemAttempt{}).
			Where("c_space_id = ? AND c_job_item_id = ? AND c_attempt_no = ?", item.SpaceID, item.JobItemID, item.AttemptNo).
			Updates(map[string]any{
				"c_status":         attemptStatus,
				"c_error_kind":     errorKind,
				"c_error_code":     strings.TrimSpace(req.GetErrorCode()),
				"c_error_message":  strings.TrimSpace(req.GetErrorMessage()),
				"c_result_summary": resultSummary,
				"c_finished_at":    now,
				"c_mtime":          now,
			}).Error
	})
}

func (r *JobItemRepository) Cancel(ctx context.Context, req *pb.CancelJobItemReq) error {
	now := r.now()
	result := r.db.WithContext(ctx).Model(&JobItem{}).
		Where("c_space_id = ? AND c_job_item_id = ? AND c_status = ?", req.GetSpaceId(), req.GetJobItemId(), JobItemStatusPending).
		Updates(map[string]any{
			"c_status":      JobItemStatusCanceled,
			"c_finish_time": now,
			"c_mtime":       now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrJobItemConflict
	}
	return nil
}

func (r *JobItemRepository) Get(ctx context.Context, req *pb.GetJobItemReq) (*pb.JobItemDetail, error) {
	var item JobItem
	if err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_job_item_id = ?", req.GetSpaceId(), req.GetJobItemId()).First(&item).Error; err != nil {
		return nil, err
	}
	return jobItemDetail(item), nil
}

func (r *JobItemRepository) List(ctx context.Context, req *pb.ListJobItemsReq) ([]*pb.JobItemDetail, *pb.PageResult, error) {
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

func (r *JobItemRepository) ListAttempts(ctx context.Context, req *pb.ListJobItemAttemptsReq) ([]*pb.JobItemAttempt, error) {
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

func (r *JobItemRepository) recoverExpiredRunning(tx *gorm.DB, spaceID string, now time.Time) error {
	var expired []JobItem
	if err := tx.Where("c_space_id = ? AND c_status = ? AND c_recover_at IS NOT NULL AND c_recover_at < ?", spaceID, JobItemStatusRunning, now).Find(&expired).Error; err != nil {
		return err
	}
	for _, item := range expired {
		if err := tx.Model(&JobItemAttempt{}).
			Where("c_space_id = ? AND c_job_item_id = ? AND c_attempt_no = ?", item.SpaceID, item.JobItemID, item.AttemptNo).
			Updates(map[string]any{
				"c_status":      JobItemAttemptStatusLost,
				"c_finished_at": now,
				"c_mtime":       now,
			}).Error; err != nil {
			return err
		}
		if item.AttemptNo >= r.defaultMaxAttempts {
			if err := failMaxedJobItem(tx, item, now); err != nil {
				return err
			}
			continue
		}
		if err := tx.Model(&JobItem{}).
			Where("c_id = ? AND c_attempt_no = ? AND c_status = ?", item.ID, item.AttemptNo, JobItemStatusRunning).
			Updates(map[string]any{
				"c_status":             JobItemStatusPending,
				"c_running_node":       "",
				"c_recover_at":         nil,
				"c_last_error_kind":    JobItemErrorKindRetryable,
				"c_last_error_code":    "ATTEMPT_LOST",
				"c_last_error_message": "running attempt exceeded recover_at",
				"c_mtime":              now,
			}).Error; err != nil {
			return err
		}
	}
	return nil
}

func loadPollingJobNode(tx *gorm.DB, req *pb.PollJobItemsReq) (CloudNode, error) {
	if strings.TrimSpace(req.GetNodeId()) == "" {
		return CloudNode{}, fmt.Errorf("node_id is required")
	}
	var node CloudNode
	err := tx.Where("c_space_id = ? AND c_node_id = ? AND c_is_deleted = ?", req.GetSpaceId(), req.GetNodeId(), false).First(&node).Error
	if err == nil {
		return node, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return CloudNode{}, fmt.Errorf("%w: node=%s space=%s", ErrPollingNodeNotFound, req.GetNodeId(), req.GetSpaceId())
	}
	return CloudNode{}, err
}

func buildJobItem(item *pb.JobItem, now time.Time) (JobItem, string) {
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
		CreateTime:    now,
		ModifyTime:    now,
	}, ""
}

func jobItemID(item *pb.JobItem) string {
	if item == nil {
		return ""
	}
	return strings.TrimSpace(item.GetJobItemId())
}

func failMaxedJobItem(tx *gorm.DB, item JobItem, now time.Time) error {
	return tx.Model(&JobItem{}).
		Where("c_id = ? AND c_attempt_no = ?", item.ID, item.AttemptNo).
		Updates(map[string]any{
			"c_status":             JobItemStatusFailed,
			"c_running_node":       "",
			"c_recover_at":         nil,
			"c_last_error_kind":    JobItemErrorKindPermanent,
			"c_last_error_code":    "MAX_ATTEMPTS_EXCEEDED",
			"c_last_error_message": "max attempts exceeded",
			"c_finish_time":        now,
			"c_mtime":              now,
		}).Error
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
	case JobItemStatusCanceled:
		return pb.JobItemStatus_JOB_ITEM_STATUS_CANCELED
	case JobItemStatusEnqueueFailed:
		return pb.JobItemStatus_JOB_ITEM_STATUS_ENQUEUE_FAILED
	default:
		return pb.JobItemStatus_JOB_ITEM_STATUS_UNSPECIFIED
	}
}

func jobItemErrorKindToDB(kind pb.JobItemErrorKind) string {
	switch kind {
	case pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_RETRYABLE:
		return JobItemErrorKindRetryable
	case pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_PERMANENT:
		return JobItemErrorKindPermanent
	default:
		return ""
	}
}

func errorKindToPB(kind string) pb.JobItemErrorKind {
	switch kind {
	case JobItemErrorKindRetryable:
		return pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_RETRYABLE
	case JobItemErrorKindPermanent:
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
	default:
		return pb.JobItemAttemptStatus_JOB_ITEM_ATTEMPT_STATUS_UNSPECIFIED
	}
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

func compactJobItemStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
