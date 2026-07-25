package jobstate

import (
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestStateConverters_ShouldMapStatuses(t *testing.T) {
	now := time.Now().UTC()
	state := State{
		SpaceID: "crypto", JobID: "job-1", JobItemID: "ji-1", JobType: "collect.kline",
		CodePackageID: "pkg", Status: StatusSuccess, AttemptNo: 1,
		Params: map[string]any{"k": "v"}, ResultSummary: map[string]any{"ok": true},
		CreatedAt: now, StartedAt: &now, FinishedAt: &now,
		LastErrorKind: ErrorPermanent, LastErrorCode: "E1", LastErrorMessage: "boom",
	}
	assert.True(t, state.IsTerminal())
	detail := state.ToDetail()
	require.NotNil(t, detail)
	assert.Equal(t, pb.JobItemStatus_JOB_ITEM_STATUS_SUCCESS, detail.GetStatus())
	assert.Equal(t, "ji-1", detail.GetJobItemId())

	attempt := Attempt{
		AttemptNo: 1, NodeID: "n1", Status: AttemptSuccess,
		ErrorKind: ErrorRetryable, StartedAt: now, FinishedAt: &now,
		ResultSummary: map[string]any{"n": float64(1)},
	}
	proto := attempt.ToProto()
	assert.Equal(t, pb.JobItemAttemptStatus_JOB_ITEM_ATTEMPT_STATUS_SUCCESS, proto.GetStatus())
	assert.Equal(t, pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_RETRYABLE, proto.GetErrorKind())
}

func TestStatusAndErrorKindHelpers(t *testing.T) {
	assert.Equal(t, pb.JobItemStatus_JOB_ITEM_STATUS_PENDING, statusToPB(StatusPending))
	assert.Equal(t, pb.JobItemStatus_JOB_ITEM_STATUS_RUNNING, statusToPB(StatusRunning))
	assert.Equal(t, pb.JobItemStatus_JOB_ITEM_STATUS_FAILED, statusToPB(StatusFailed))
	assert.Equal(t, pb.JobItemStatus_JOB_ITEM_STATUS_ENQUEUE_FAILED, statusToPB(StatusEnqueueFailed))
	assert.Equal(t, pb.JobItemStatus_JOB_ITEM_STATUS_UNSPECIFIED, statusToPB("x"))

	assert.Equal(t, StatusPending, StatusFromPB(pb.JobItemStatus_JOB_ITEM_STATUS_PENDING))
	assert.Equal(t, StatusRunning, StatusFromPB(pb.JobItemStatus_JOB_ITEM_STATUS_RUNNING))
	assert.Equal(t, StatusSuccess, StatusFromPB(pb.JobItemStatus_JOB_ITEM_STATUS_SUCCESS))
	assert.Equal(t, "", StatusFromPB(pb.JobItemStatus_JOB_ITEM_STATUS_UNSPECIFIED))

	assert.Equal(t, pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_RETRYABLE, errorKindToPB(ErrorRetryable))
	assert.Equal(t, pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_PERMANENT, errorKindToPB(ErrorPermanent))
	assert.Equal(t, ErrorRetryable, ErrorKindFromPB(pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_RETRYABLE))
	assert.Equal(t, ErrorPermanent, ErrorKindFromPB(pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_PERMANENT))

	assert.Equal(t, pb.JobItemAttemptStatus_JOB_ITEM_ATTEMPT_STATUS_RUNNING, attemptStatusToPB(AttemptRunning))
	assert.Equal(t, pb.JobItemAttemptStatus_JOB_ITEM_ATTEMPT_STATUS_FAILED, attemptStatusToPB(AttemptFailed))
	assert.Equal(t, pb.JobItemAttemptStatus_JOB_ITEM_ATTEMPT_STATUS_LOST, attemptStatusToPB(AttemptLost))

	assert.Nil(t, timePtrToPB(nil))
	assert.Nil(t, timeToPB(time.Time{}))
	assert.NotNil(t, mapToStruct(nil))
	assert.NotNil(t, mapToStruct(map[string]any{"a": "b"}))
}

func TestStateAndAttemptConvertersCoverDefaults(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	recoverAt := now.Add(time.Minute)
	state := State{
		SpaceID: "crypto", JobID: "job-1", JobItemID: "item-1",
		Status: StatusRunning, AttemptNo: 2, RecoverAt: &recoverAt,
		Params:        map[string]any{"bad": make(chan int)},
		ResultSummary: map[string]any{"bad": make(chan int)},
		CreatedAt:     now,
	}
	detail := state.ToDetail()
	require.NotNil(t, detail)
	assert.Equal(t, pb.JobItemStatus_JOB_ITEM_STATUS_RUNNING, detail.GetStatus())
	assert.NotNil(t, detail.GetRecoverAt())
	assert.Empty(t, detail.GetParams().GetFields())
	assert.Empty(t, detail.GetResultSummary().GetFields())

	attempt := Attempt{AttemptNo: 3, Status: "unknown", StartedAt: now}
	got := attempt.ToProto()
	assert.Equal(t, pb.JobItemAttemptStatus_JOB_ITEM_ATTEMPT_STATUS_UNSPECIFIED, got.GetStatus())
	assert.Equal(t, pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_UNSPECIFIED, got.GetErrorKind())
	assert.Nil(t, got.GetFinishedAt())
}

func TestStatusErrorAndAttemptMappingsCoverAllBranches(t *testing.T) {
	assert.Equal(t, pb.JobItemStatus_JOB_ITEM_STATUS_SUCCESS, statusToPB(StatusSuccess))
	assert.Equal(t, StatusFailed, StatusFromPB(pb.JobItemStatus_JOB_ITEM_STATUS_FAILED))
	assert.Equal(t, StatusEnqueueFailed, StatusFromPB(pb.JobItemStatus_JOB_ITEM_STATUS_ENQUEUE_FAILED))

	assert.Equal(t, pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_UNSPECIFIED, errorKindToPB("unknown"))
	assert.Equal(t, "", ErrorKindFromPB(pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_UNSPECIFIED))

	assert.Equal(t, pb.JobItemAttemptStatus_JOB_ITEM_ATTEMPT_STATUS_SUCCESS, attemptStatusToPB(AttemptSuccess))
	assert.Equal(t, pb.JobItemAttemptStatus_JOB_ITEM_ATTEMPT_STATUS_UNSPECIFIED, attemptStatusToPB("unknown"))
}
