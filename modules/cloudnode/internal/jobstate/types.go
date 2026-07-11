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
	StatusRunning       = "running"
	StatusSuccess       = "success"
	StatusFailed        = "failed"
	StatusCanceled      = "canceled"
	StatusEnqueueFailed = "enqueue_failed"

	AttemptRunning  = "running"
	AttemptSuccess  = "success"
	AttemptFailed   = "failed"
	AttemptLost     = "lost"
	AttemptCanceled = "canceled"

	ErrorRetryable = "retryable"
	ErrorPermanent = "permanent"
)

var (
	ErrConflict     = errors.New("job item state conflict")
	ErrStaleAttempt = errors.New("stale job item attempt")
	ErrInactive     = errors.New("job item is not running")
	ErrInvalid      = errors.New("invalid job item")
	ErrNotFound     = errors.New("job item not found")
)

type QueueMeta struct {
	Subject    string `json:"subject,omitempty"`
	Stream     string `json:"stream,omitempty"`
	StreamSeq  uint64 `json:"stream_seq,omitempty"`
	AckSubject string `json:"ack_subject,omitempty"`
}

type Attempt struct {
	AttemptNo     int            `json:"attempt_no"`
	NodeID        string         `json:"node_id"`
	Status        string         `json:"status"`
	ErrorKind     string         `json:"error_kind,omitempty"`
	ErrorCode     string         `json:"error_code,omitempty"`
	ErrorMessage  string         `json:"error_message,omitempty"`
	ResultSummary map[string]any `json:"result_summary,omitempty"`
	StartedAt     time.Time      `json:"started_at"`
	FinishedAt    *time.Time     `json:"finished_at,omitempty"`
}

type State struct {
	SchemaVersion    int            `json:"schema_version"`
	SpaceID          string         `json:"space_id"`
	JobID            string         `json:"job_id"`
	JobItemID        string         `json:"job_item_id"`
	JobType          string         `json:"job_type"`
	CodePackageID    string         `json:"code_package_id"`
	Params           map[string]any `json:"params,omitempty"`
	Priority         int32          `json:"priority"`
	Status           string         `json:"status"`
	RunningNode      string         `json:"running_node,omitempty"`
	AttemptNo        int            `json:"attempt_no"`
	RecoverAt        *time.Time     `json:"recover_at,omitempty"`
	Queue            QueueMeta      `json:"queue,omitempty"`
	ResultSummary    map[string]any `json:"result_summary,omitempty"`
	LastErrorKind    string         `json:"last_error_kind,omitempty"`
	LastErrorCode    string         `json:"last_error_code,omitempty"`
	LastErrorMessage string         `json:"last_error_message,omitempty"`
	CancelReason     string         `json:"cancel_reason,omitempty"`
	HistorySynced    bool           `json:"history_synced,omitempty"`
	StartedAt        *time.Time     `json:"started_at,omitempty"`
	FinishedAt       *time.Time     `json:"finished_at,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
	Attempts         []Attempt      `json:"attempts,omitempty"`
}

type CreateResult struct {
	JobItemID    string
	Status       pb.JobItemAckStatus
	RejectReason string
	Created      bool
	Deduplicated bool
}

type RunningRequest struct {
	SpaceID    string
	JobItemID  string
	NodeID     string
	AckSubject string
	StreamSeq  uint64
}

type RunningState struct {
	AttemptNo  int
	AckSubject string
	RecoverAt  time.Time
}

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

func (s State) IsTerminal() bool {
	return s.Status == StatusSuccess || s.Status == StatusFailed || s.Status == StatusCanceled
}

func (s State) ToDetail() *pb.JobItemDetail {
	params := mapToStruct(s.Params)
	result := mapToStruct(s.ResultSummary)
	return &pb.JobItemDetail{
		SpaceId:          s.SpaceID,
		JobId:            s.JobID,
		JobItemId:        s.JobItemID,
		JobType:          s.JobType,
		CodePackageId:    s.CodePackageID,
		Params:           params,
		Priority:         s.Priority,
		Status:           statusToPB(s.Status),
		RunningNode:      s.RunningNode,
		AttemptNo:        int32(s.AttemptNo),
		RecoverAt:        timePtrToPB(s.RecoverAt),
		ResultSummary:    result,
		LastErrorKind:    errorKindToPB(s.LastErrorKind),
		LastErrorCode:    s.LastErrorCode,
		LastErrorMessage: s.LastErrorMessage,
		CreateTime:       timeToPB(s.CreatedAt),
		StartTime:        timePtrToPB(s.StartedAt),
		FinishTime:       timePtrToPB(s.FinishedAt),
	}
}

func (s State) ToJobItem() *pb.JobItem {
	return &pb.JobItem{
		SpaceId: s.SpaceID, JobId: s.JobID, JobItemId: s.JobItemID, JobType: s.JobType,
		CodePackageId: s.CodePackageID, Params: mapToStruct(s.Params), Priority: s.Priority,
	}
}

func (a Attempt) ToProto() *pb.JobItemAttempt {
	return &pb.JobItemAttempt{
		AttemptNo:     int32(a.AttemptNo),
		NodeId:        a.NodeID,
		Status:        attemptStatusToPB(a.Status),
		ErrorKind:     errorKindToPB(a.ErrorKind),
		ErrorCode:     a.ErrorCode,
		ErrorMessage:  a.ErrorMessage,
		ResultSummary: mapToStruct(a.ResultSummary),
		StartedAt:     timeToPB(a.StartedAt),
		FinishedAt:    timePtrToPB(a.FinishedAt),
	}
}

func statusToPB(status string) pb.JobItemStatus {
	switch status {
	case StatusPending:
		return pb.JobItemStatus_JOB_ITEM_STATUS_PENDING
	case StatusRunning:
		return pb.JobItemStatus_JOB_ITEM_STATUS_RUNNING
	case StatusSuccess:
		return pb.JobItemStatus_JOB_ITEM_STATUS_SUCCESS
	case StatusFailed:
		return pb.JobItemStatus_JOB_ITEM_STATUS_FAILED
	case StatusCanceled:
		return pb.JobItemStatus_JOB_ITEM_STATUS_CANCELED
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
	case pb.JobItemStatus_JOB_ITEM_STATUS_RUNNING:
		return StatusRunning
	case pb.JobItemStatus_JOB_ITEM_STATUS_SUCCESS:
		return StatusSuccess
	case pb.JobItemStatus_JOB_ITEM_STATUS_FAILED:
		return StatusFailed
	case pb.JobItemStatus_JOB_ITEM_STATUS_CANCELED:
		return StatusCanceled
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

func attemptStatusToPB(status string) pb.JobItemAttemptStatus {
	switch status {
	case AttemptRunning:
		return pb.JobItemAttemptStatus_JOB_ITEM_ATTEMPT_STATUS_RUNNING
	case AttemptSuccess:
		return pb.JobItemAttemptStatus_JOB_ITEM_ATTEMPT_STATUS_SUCCESS
	case AttemptFailed:
		return pb.JobItemAttemptStatus_JOB_ITEM_ATTEMPT_STATUS_FAILED
	case AttemptLost:
		return pb.JobItemAttemptStatus_JOB_ITEM_ATTEMPT_STATUS_LOST
	case AttemptCanceled:
		return pb.JobItemAttemptStatus_JOB_ITEM_ATTEMPT_STATUS_CANCELED
	default:
		return pb.JobItemAttemptStatus_JOB_ITEM_ATTEMPT_STATUS_UNSPECIFIED
	}
}

func timePtrToPB(t *time.Time) *timestamppb.Timestamp {
	if t == nil || t.IsZero() {
		return nil
	}
	return timestamppb.New(*t)
}

func timeToPB(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
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
