package jobstate

import (
	"errors"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	StatusPending       = "pending"
	StatusSuccess       = "success"
	StatusFailed        = "failed"
	StatusEnqueueFailed = "enqueue_failed"

	ErrorRetryable = "retryable"
	ErrorPermanent = "permanent"
)

var (
	ErrConflict = errors.New("job item state conflict")
	ErrInvalid  = errors.New("invalid job item")
	ErrNotFound = errors.New("job item not found")
)

type State struct {
	SchemaVersion    int            `json:"schema_version"`
	SpaceID          string         `json:"space_id"`
	JobID            string         `json:"job_id"`
	JobItemID        string         `json:"job_item_id"`
	JobType          string         `json:"job_type"`
	CodePackageID    string         `json:"code_package_id"`
	Params           map[string]any `json:"params,omitempty"`
	Priority         int32          `json:"priority"`
	ExecuteAt        *time.Time     `json:"execute_at,omitempty"`
	Status           string         `json:"status"`
	ResultSummary    map[string]any `json:"result_summary,omitempty"`
	LastErrorKind    string         `json:"last_error_kind,omitempty"`
	LastErrorCode    string         `json:"last_error_code,omitempty"`
	LastErrorMessage string         `json:"last_error_message,omitempty"`
	DurationMS       int64          `json:"duration_ms,omitempty"`
	ExecutionNode    string         `json:"execution_node,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
	FinishedAt       *time.Time     `json:"finished_at,omitempty"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

type CreateResult struct {
	JobItemID     string
	Status        pb.JobItemAckStatus
	RejectReason  string
	Created       bool
	Deduplicated  bool
	ShouldPublish bool
}

type ReportEvent struct {
	SpaceID       string
	JobItemID     string
	NodeID        string
	Status        string
	ErrorKind     string
	ErrorCode     string
	ErrorMessage  string
	ResultSummary map[string]any
	DurationMS    int64
	Time          time.Time
}

func (s State) IsTerminal() bool {
	return s.Status == StatusSuccess || s.Status == StatusFailed
}

func (s State) ToDetail() *pb.JobItemDetail {
	return &pb.JobItemDetail{
		SpaceId: s.SpaceID, JobId: s.JobID, JobItemId: s.JobItemID, JobType: s.JobType,
		CodePackageId: s.CodePackageID, Params: mapToStruct(s.Params), Priority: s.Priority,
		Status: statusToPB(s.Status), ResultSummary: mapToStruct(s.ResultSummary),
		LastErrorKind: errorKindToPB(s.LastErrorKind), LastErrorCode: s.LastErrorCode,
		LastErrorMessage: s.LastErrorMessage, DurationMs: s.DurationMS, ExecutionNode: s.ExecutionNode,
		CreateTime: timeToPB(s.CreatedAt), FinishTime: timePtrToPB(s.FinishedAt), ExecuteAt: timePtrToPB(s.ExecuteAt),
	}
}

func statusToPB(status string) pb.JobItemStatus {
	switch status {
	case StatusPending:
		return pb.JobItemStatus_JOB_ITEM_STATUS_PENDING
	case StatusSuccess:
		return pb.JobItemStatus_JOB_ITEM_STATUS_SUCCESS
	case StatusFailed:
		return pb.JobItemStatus_JOB_ITEM_STATUS_FAILED
	case StatusEnqueueFailed:
		return pb.JobItemStatus_JOB_ITEM_STATUS_ENQUEUE_FAILED
	default:
		return pb.JobItemStatus_JOB_ITEM_STATUS_UNSPECIFIED
	}
}

func StatusFromPB(status pb.JobItemStatus) string {
	switch status {
	case pb.JobItemStatus_JOB_ITEM_STATUS_PENDING:
		return StatusPending
	case pb.JobItemStatus_JOB_ITEM_STATUS_SUCCESS:
		return StatusSuccess
	case pb.JobItemStatus_JOB_ITEM_STATUS_FAILED:
		return StatusFailed
	case pb.JobItemStatus_JOB_ITEM_STATUS_ENQUEUE_FAILED:
		return StatusEnqueueFailed
	default:
		return ""
	}
}

func errorKindToPB(kind string) pb.JobItemErrorKind {
	switch kind {
	case ErrorRetryable:
		return pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_RETRYABLE
	case ErrorPermanent:
		return pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_PERMANENT
	default:
		return pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_UNSPECIFIED
	}
}

func ErrorKindFromPB(kind pb.JobItemErrorKind) string {
	switch kind {
	case pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_RETRYABLE:
		return ErrorRetryable
	case pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_PERMANENT:
		return ErrorPermanent
	default:
		return ""
	}
}

func timePtrToPB(value *time.Time) *timestamppb.Timestamp {
	if value == nil || value.IsZero() {
		return nil
	}
	return timestamppb.New(*value)
}

func timeToPB(value time.Time) *timestamppb.Timestamp {
	if value.IsZero() {
		return nil
	}
	return timestamppb.New(value)
}

func mapToStruct(values map[string]any) *structpb.Struct {
	if values == nil {
		values = map[string]any{}
	}
	out, err := structpb.NewStruct(values)
	if err != nil {
		return &structpb.Struct{Fields: map[string]*structpb.Value{}}
	}
	return out
}
