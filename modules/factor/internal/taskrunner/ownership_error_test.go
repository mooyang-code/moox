package taskrunner

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

func TestOwnershipConflictIsNotRetriedAsStorageFailure(t *testing.T) {
	base := time.Unix(60, 0)
	storage := &fakeStorage{chunks: []*storageio.RangeChunk{frameChunk([]time.Time{base})}, writeErr: domain.ErrOutputOwnershipConflict}
	observer := &recordingStorageWriteFailureObserver{}
	runner := NewService(1, storage, &fakeExecutor{}, WithStorageWriteFailureObserver(observer))
	err := runner.Run(context.Background(), oneBarTask("BTC", base))
	var terminal engine.NonRetryableError
	require.ErrorAs(t, err, &terminal)
	require.ErrorIs(t, err, domain.ErrOutputOwnershipConflict)
	require.Empty(t, observer.errors, "an output conflict is not a Storage outage")
}

type ownershipConflictBatchStorage struct {
	batchRecordingStorage
	written []string
}

func (s *ownershipConflictBatchStorage) WriteFactorPatches(context.Context, []storageio.FactorPatch) ([]uint64, error) {
	s.batchCalls++
	return nil, domain.ErrOutputOwnershipConflict
}

func (s *ownershipConflictBatchStorage) WriteFactorPatch(_ context.Context, task *engine.FactorTask, _ *engine.FactorResult) (uint64, error) {
	s.singleCalls++
	if task.Factor.FactorID == "conflict" {
		return 0, domain.ErrOutputOwnershipConflict
	}
	s.written = append(s.written, task.Factor.FactorID)
	return 1, nil
}

func TestBatchOwnershipConflictDoesNotFailHealthyFactor(t *testing.T) {
	storage := &ownershipConflictBatchStorage{}
	runner := NewService(2, storage, &recordingBatchExecutor{})
	first := oneBarTask("BTC", time.Unix(60, 0))
	first.PeriodTime, first.TaskID, first.Factor.FactorID = 60, "healthy-task", "healthy"
	second := first
	second.TaskID, second.Factor.FactorID = "conflict-task", "conflict"
	results := runner.RunAll(context.Background(), []Task{first, second})
	require.Len(t, results, 2)
	require.NoError(t, results[0].Err)
	require.ErrorIs(t, results[1].Err, domain.ErrOutputOwnershipConflict)
	require.Equal(t, []string{"healthy"}, storage.written)
	require.Equal(t, 1, storage.batchCalls)
	require.Equal(t, 2, storage.singleCalls)
}
