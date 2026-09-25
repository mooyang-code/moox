package marketfetch

import (
	"context"
	"testing"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

type deadlineRecordingStorage struct {
	remaining time.Duration
}

func (s *deadlineRecordingStorage) UpsertFields(ctx context.Context, _ []*storagepb.RowFieldUpsert) error {
	deadline, ok := ctx.Deadline()
	if ok {
		s.remaining = time.Until(deadline)
	}
	return ctx.Err()
}

func TestContextWithReserveEndsFetchBeforeStorageAndCLSWindow(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer parentCancel()
	work, workCancel := contextWithReserve(parent, 180*time.Millisecond)
	defer workCancel()
	deadline, ok := work.Deadline()
	require.True(t, ok)
	remaining := time.Until(deadline)
	require.Positive(t, remaining)
	require.LessOrEqual(t, remaining, 140*time.Millisecond)
}

func TestStorageAndPublishReservesColdEventBusConnection(t *testing.T) {
	commit, publish := storageAndPublishReserves(5*time.Second, 0, true)
	require.Equal(t, 8*time.Second, commit)
	require.Equal(t, 3*time.Second, publish)
}

func TestReservedDeadlineStorageUsesReservedParentBudget(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer parentCancel()
	expiredWork, workCancel := context.WithCancel(parent)
	workCancel()
	underlying := &deadlineRecordingStorage{}
	storage := &reservedDeadlineStorage{Storage: underlying, parent: parent, timeout: 120 * time.Millisecond}

	require.NoError(t, storage.UpsertFields(expiredWork, []*storagepb.RowFieldUpsert{{}}))
	require.Positive(t, underlying.remaining)
	require.LessOrEqual(t, underlying.remaining, 130*time.Millisecond)
}

func TestReservedDeadlineStorageForwardsInstrumentNames(t *testing.T) {
	underlying := &pipelineStorageWithInstrumentNames{names: map[string]string{"600000.XSHG": "浦发银行"}}
	storage := &reservedDeadlineStorage{Storage: underlying, parent: context.Background(), timeout: time.Second}

	names, err := storage.ListInstrumentNames(context.Background(), StockCNSpaceID, []string{"600000.XSHG"})
	require.NoError(t, err)
	require.Equal(t, "浦发银行", names["600000.XSHG"])
}
