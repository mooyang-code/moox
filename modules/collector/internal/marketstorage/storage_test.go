package marketstorage

import (
	"context"
	"testing"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type storageGatewayFunc func(context.Context, string, string, any, any) error

func (f storageGatewayFunc) Invoke(ctx context.Context, service, method string, request, response any) error {
	return f(ctx, service, method, request, response)
}

// 写入请求原样发给 Storage 的 PrimaryStore，不需要任何存储口令配置。
func TestGatewayWriterCommitsPeriodWithoutStorageCredentials(t *testing.T) {
	calls := 0
	gateway := storageGatewayFunc(func(_ context.Context, service, method string, request, response any) error {
		calls++
		require.Equal(t, "trpc.moox.storage.PrimaryStore", service)
		require.Equal(t, "CommitTimeSeriesBatch", method)
		require.Equal(t, "bars", request.(*storagepb.PrimaryCommitTimeSeriesBatchReq).GetExpectation().GetDatasetId())
		proto.Merge(response.(proto.Message), &storagepb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: storageSuccess(), AcceptedSeriesIndexes: []uint32{0}})
		return nil
	})
	writer, err := NewGatewayBatchStorage(gateway)
	require.NoError(t, err)
	expectation := validStorageExpectation(time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC))
	require.NoError(t, writer.CommitTimeSeriesBatch(context.Background(), expectation, []*storagepb.TimeSeriesBatchRow{{SeriesIndex: 0}}))
	require.Equal(t, 1, calls)

	_, err = NewGatewayBatchStorage(nil)
	require.Error(t, err)
}

func TestInternalGatewayWriterDoesNotRepeatFailedCalls(t *testing.T) {
	expectation := validStorageExpectation(time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC))
	for _, operation := range []struct {
		name string
		call func(BatchStorage) error
	}{
		{"CommitTimeSeriesBatch", func(w BatchStorage) error {
			return w.CommitTimeSeriesBatch(t.Context(), expectation, []*storagepb.TimeSeriesBatchRow{{SeriesIndex: 0}})
		}},
		{"UpsertFields", func(w BatchStorage) error {
			row := &storagepb.Row{Key: &storagepb.RowKey{SpaceId: "crypto", DatasetId: "dataset"}}
			return w.UpsertFields(t.Context(), []*storagepb.Row{row})
		}},
	} {
		t.Run(operation.name, func(t *testing.T) {
			calls := 0
			gateway := storageGatewayFunc(func(_ context.Context, service, method string, request, response any) error {
				calls++
				require.Equal(t, "trpc.moox.storage.PrimaryStore", service)
				require.Equal(t, operation.name, method)
				return context.DeadlineExceeded
			})
			writer, err := NewGatewayBatchStorage(gateway)
			require.NoError(t, err)
			require.ErrorIs(t, operation.call(writer), context.DeadlineExceeded)
			require.Equal(t, 1, calls)
		})
	}
}
