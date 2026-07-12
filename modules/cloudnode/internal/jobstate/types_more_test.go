package jobstate

import (
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	assert.Equal(t, StatusCanceled, StatusFromPB(pb.JobItemStatus_JOB_ITEM_STATUS_CANCELED))
	assert.Equal(t, StatusEnqueueFailed, StatusFromPB(pb.JobItemStatus_JOB_ITEM_STATUS_ENQUEUE_FAILED))

	assert.Equal(t, pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_UNSPECIFIED, errorKindToPB("unknown"))
	assert.Equal(t, "", ErrorKindFromPB(pb.JobItemErrorKind_JOB_ITEM_ERROR_KIND_UNSPECIFIED))

	assert.Equal(t, pb.JobItemAttemptStatus_JOB_ITEM_ATTEMPT_STATUS_SUCCESS, attemptStatusToPB(AttemptSuccess))
	assert.Equal(t, pb.JobItemAttemptStatus_JOB_ITEM_ATTEMPT_STATUS_UNSPECIFIED, attemptStatusToPB("unknown"))
}
