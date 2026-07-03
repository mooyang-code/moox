// Package repository contains CloudNode persistence adapters.
package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	WorkItemStatusPending = "pending"
	WorkItemStatusLeased  = "leased"
	WorkItemStatusRunning = "running"
	WorkItemStatusSuccess = "success"
	WorkItemStatusFailed  = "failed"

	defaultWorkItemLimit   = 10
	maxWorkItemLimit       = 100
	defaultLeaseMillis     = int64(10 * 60 * 1000)
	defaultMaxAttempts     = 3
)

type WorkItem struct {
	ID              int        `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID         string     `gorm:"column:c_space_id"`
	WorkItemID      string     `gorm:"column:c_work_item_id"`
	IdempotencyKey  string     `gorm:"column:c_idempotency_key"`
	OwnerService    string     `gorm:"column:c_owner_service"`
	OwnerRef        string     `gorm:"column:c_owner_ref"`
	WorkloadType    string     `gorm:"column:c_workload_type"`
	DeploymentID    string     `gorm:"column:c_deployment_id"`
	Payload         string     `gorm:"column:c_payload"`
	PayloadHash     string     `gorm:"column:c_payload_hash"`
	Priority        int        `gorm:"column:c_priority"`
	Status          string     `gorm:"column:c_status"`
	LeasedBy        string     `gorm:"column:c_leased_by"`
	LeaseDeadline   *time.Time `gorm:"column:c_lease_deadline"`
	LeaseTimeoutMs  int64      `gorm:"column:c_lease_timeout_ms"`
	AttemptNo       int        `gorm:"column:c_attempt_no"`
	MaxAttempts     int        `gorm:"column:c_max_attempts"`
	LastError       string     `gorm:"column:c_last_error"`
	Result          string     `gorm:"column:c_result"`
	FinishedAt      *time.Time `gorm:"column:c_finished_at"`
	CreateTime      time.Time  `gorm:"column:c_ctime"`
	ModifyTime      time.Time  `gorm:"column:c_mtime"`
}

func (*WorkItem) TableName() string { return "t_cloud_work_items" }

type WorkItemAttempt struct {
	ID           int        `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID      string     `gorm:"column:c_space_id"`
	WorkItemID   string     `gorm:"column:c_work_item_id"`
	AttemptNo    int        `gorm:"column:c_attempt_no"`
	NodeID       string     `gorm:"column:c_node_id"`
	Status       string     `gorm:"column:c_status"`
	ErrorMessage string     `gorm:"column:c_error_message"`
	StartedAt    time.Time  `gorm:"column:c_started_at"`
	FinishedAt   *time.Time `gorm:"column:c_finished_at"`
}

func (*WorkItemAttempt) TableName() string { return "t_cloud_work_item_attempts" }

type WorkItemRepository struct {
	db *gorm.DB
}

func NewWorkItemRepository(db *gorm.DB) *WorkItemRepository {
	return &WorkItemRepository{db: db}
}

func (r *WorkItemRepository) Submit(ctx context.Context, workItems []*pb.CloudWorkItem) ([]*pb.CloudWorkItemAck, error) {
	now := time.Now().UTC()
	models := make([]WorkItem, 0, len(workItems))
	acks := make([]*pb.CloudWorkItemAck, 0, len(workItems))
	for _, item := range workItems {
		if item == nil {
			continue
		}
		model, err := buildWorkItem(item, now)
		if err != nil {
			return nil, err
		}
		models = append(models, model)
		acks = append(acks, &pb.CloudWorkItemAck{
			WorkItemId: model.WorkItemID,
			OwnerRef:   model.OwnerRef,
			Status:     pb.WorkItemStatus_WORK_ITEM_STATUS_PENDING,
		})
	}
	if len(models) == 0 {
		return acks, nil
	}
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&models).Error
	return acks, err
}

func (r *WorkItemRepository) Poll(ctx context.Context, req *pb.PollWorkItemsReq) ([]WorkItem, error) {
	now := time.Now().UTC()
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = defaultWorkItemLimit
	}
	if limit > maxWorkItemLimit {
		limit = maxWorkItemLimit
	}
	var leased []WorkItem
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		node, nodeFound, err := loadPollingNode(tx, req)
		if err != nil {
			return err
		}
		q := tx.Model(&WorkItem{}).
			Where("(c_status = ? OR (c_status IN ? AND c_lease_deadline < ?))", WorkItemStatusPending, []string{WorkItemStatusLeased, WorkItemStatusRunning}, now)
		if req.GetSpaceId() != "" {
			q = q.Where("c_space_id = ?", req.GetSpaceId())
		}
		if len(req.GetSupportedWorkloads()) > 0 {
			q = q.Where("c_workload_type IN ?", req.GetSupportedWorkloads())
		}
		if nodeFound && node.DeploymentID != "" {
			q = q.Where("c_deployment_id = ?", node.DeploymentID)
		}
		var workItems []WorkItem
		if err := q.Order("c_priority DESC, c_ctime ASC").Limit(limit).Find(&workItems).Error; err != nil {
			return err
		}
		for _, item := range workItems {
			leaseMs := item.LeaseTimeoutMs
			if leaseMs <= 0 {
				leaseMs = defaultLeaseMillis
			}
			deadline := now.Add(time.Duration(leaseMs) * time.Millisecond)
			attemptNo := item.AttemptNo + 1
			updates := map[string]any{
				"c_status":         WorkItemStatusLeased,
				"c_leased_by":      req.GetNodeId(),
				"c_lease_deadline": deadline,
				"c_attempt_no":     attemptNo,
				"c_mtime":          now,
			}
			result := tx.Model(&WorkItem{}).
				Where("c_space_id = ? AND c_work_item_id = ?", item.SpaceID, item.WorkItemID).
				Where("(c_status = ? OR (c_status IN ? AND c_lease_deadline < ?))", WorkItemStatusPending, []string{WorkItemStatusLeased, WorkItemStatusRunning}, now).
				Updates(updates)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				continue
			}
			attempt := WorkItemAttempt{
				SpaceID:    item.SpaceID,
				WorkItemID: item.WorkItemID,
				AttemptNo:  attemptNo,
				NodeID:     req.GetNodeId(),
				Status:     WorkItemStatusRunning,
				StartedAt:  now,
			}
			if err := tx.Create(&attempt).Error; err != nil {
				return err
			}
			item.Status = WorkItemStatusLeased
			item.LeasedBy = req.GetNodeId()
			item.LeaseDeadline = &deadline
			item.AttemptNo = attemptNo
			leased = append(leased, item)
		}
		return nil
	})
	return leased, err
}

func loadPollingNode(tx *gorm.DB, req *pb.PollWorkItemsReq) (CloudNode, bool, error) {
	if req.GetNodeId() == "" {
		return CloudNode{}, false, nil
	}
	q := tx.Where("c_node_id = ? AND c_is_deleted = ?", req.GetNodeId(), false)
	if req.GetSpaceId() != "" {
		q = q.Where("c_space_id = ?", req.GetSpaceId())
	}
	var node CloudNode
	if err := q.First(&node).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return CloudNode{}, false, nil
		}
		return CloudNode{}, false, err
	}
	return node, true, nil
}

func (r *WorkItemRepository) Report(ctx context.Context, req *pb.ReportWorkItemStatusReq) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item WorkItem
		if err := tx.Where("c_space_id = ? AND c_work_item_id = ?", req.GetSpaceId(), req.GetWorkItemId()).First(&item).Error; err != nil {
			if req.GetSpaceId() == "" {
				if err := tx.Where("c_work_item_id = ?", req.GetWorkItemId()).First(&item).Error; err != nil {
					return err
				}
			} else {
				return err
			}
		}
		nextStatus, ok := workItemStatusToDB(req.GetStatus())
		if !ok {
			return fmt.Errorf("invalid work item status: %v", req.GetStatus())
		}
		finished := (*time.Time)(nil)
		if nextStatus == WorkItemStatusSuccess || nextStatus == WorkItemStatusFailed {
			finished = &now
		}
		if nextStatus == WorkItemStatusFailed && item.AttemptNo < item.MaxAttempts {
			nextStatus = WorkItemStatusPending
			finished = nil
		}
		updates := map[string]any{
			"c_status":     nextStatus,
			"c_result":     structToJSON(req.GetResult()),
			"c_last_error": req.GetErrorMessage(),
			"c_mtime":      now,
		}
		if finished != nil {
			updates["c_finished_at"] = *finished
		}
		if nextStatus == WorkItemStatusPending {
			updates["c_leased_by"] = ""
			updates["c_lease_deadline"] = nil
		}
		if err := tx.Model(&WorkItem{}).Where("c_id = ?", item.ID).Updates(updates).Error; err != nil {
			return err
		}
		attemptUpdates := map[string]any{
			"c_status":        nextStatus,
			"c_error_message": req.GetErrorMessage(),
			"c_finished_at":   now,
		}
		return tx.Model(&WorkItemAttempt{}).
			Where("c_space_id = ? AND c_work_item_id = ? AND c_attempt_no = ?", item.SpaceID, item.WorkItemID, item.AttemptNo).
			Updates(attemptUpdates).Error
	})
}

func buildWorkItem(item *pb.CloudWorkItem, now time.Time) (WorkItem, error) {
	spaceID := strings.TrimSpace(item.GetSpaceId())
	if spaceID == "" {
		return WorkItem{}, fmt.Errorf("space_id is required")
	}
	ownerService := defaultString(item.GetOwnerService(), "unknown")
	payload := structToJSON(item.GetPayload())
	payloadHash := sha256Hex(payload)
	idempotencyKey := strings.Join([]string{
		ownerService,
		item.GetOwnerRef(),
		item.GetWorkloadType(),
		payloadHash,
	}, "|")
	leaseTimeout := item.GetLeaseTimeoutMs()
	if leaseTimeout <= 0 {
		leaseTimeout = defaultLeaseMillis
	}
	return WorkItem{
		SpaceID:        spaceID,
		WorkItemID:     "wi_" + sha256Hex(spaceID+"|"+idempotencyKey)[:32],
		IdempotencyKey: idempotencyKey,
		OwnerService:   ownerService,
		OwnerRef:       item.GetOwnerRef(),
		WorkloadType:   item.GetWorkloadType(),
		DeploymentID:   item.GetDeploymentId(),
		Payload:        payload,
		PayloadHash:    payloadHash,
		Priority:       int(item.GetPriority()),
		Status:         WorkItemStatusPending,
		LeaseTimeoutMs: leaseTimeout,
		MaxAttempts:    defaultMaxAttempts,
		Result:         "{}",
		CreateTime:     now,
		ModifyTime:     now,
	}, nil
}

func workItemStatusToDB(status pb.WorkItemStatus) (string, bool) {
	switch status {
	case pb.WorkItemStatus_WORK_ITEM_STATUS_SUCCESS:
		return WorkItemStatusSuccess, true
	case pb.WorkItemStatus_WORK_ITEM_STATUS_FAILED:
		return WorkItemStatusFailed, true
	case pb.WorkItemStatus_WORK_ITEM_STATUS_RUNNING:
		return WorkItemStatusRunning, true
	case pb.WorkItemStatus_WORK_ITEM_STATUS_LEASED:
		return WorkItemStatusLeased, true
	case pb.WorkItemStatus_WORK_ITEM_STATUS_PENDING:
		return WorkItemStatusPending, true
	default:
		return "", false
	}
}

func structToJSON(st *structpb.Struct) string {
	if st == nil {
		return "{}"
	}
	raw, err := protojson.Marshal(st)
	if err != nil {
		return "{}"
	}
	if len(raw) == 0 || string(raw) == "null" {
		return "{}"
	}
	return string(raw)
}

func jsonToStruct(raw string) *structpb.Struct {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return &structpb.Struct{}
	}
	st := &structpb.Struct{}
	if err := protojson.Unmarshal([]byte(raw), st); err != nil {
		var obj map[string]any
		if err := json.Unmarshal([]byte(raw), &obj); err == nil {
			st, _ = structpb.NewStruct(obj)
			return st
		}
		return &structpb.Struct{}
	}
	return st
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func sha256Hex(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
