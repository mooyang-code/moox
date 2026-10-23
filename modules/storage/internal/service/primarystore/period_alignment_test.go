package primarystore

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestValidateSnapshotAlignmentFollowsDatasetAlignment(t *testing.T) {
	ctx := metadata.WithRequestSnapshot(context.Background(), attrsSnapshot{
		"epoch":   {"owner_module": "collector", "dataset_role": "raw_collection"},
		"session": {"owner_module": "collector", "dataset_role": "kline_resample_result", "alignment": "session"},
	})
	open := time.Date(2026, 10, 21, 1, 30, 0, 0, time.UTC).Unix() // 北京时间 09:30
	request := func(dataset, frequency string, at int64) *pb.PrimaryRegisterSeriesSnapshotReq {
		return &pb.PrimaryRegisterSeriesSnapshotReq{SpaceId: "space", DatasetId: dataset, Frequency: frequency, EffectiveFrom: at}
	}
	// epoch 数据集：1h 必须落在整点，09:30 开盘（01:30Z）不行。
	require.ErrorContains(t, validateSnapshotAlignment(ctx, request("epoch", "1h", open)), "aligned")
	require.NoError(t, validateSnapshotAlignment(ctx, request("epoch", "1h", open-30*60)))
	// session 数据集：桶从开盘起切分，只要求整分钟，由 DataNode 校验。
	require.NoError(t, validateSnapshotAlignment(ctx, request("session", "1h", open)))
	// 日线及以上的边界取决于时区，这一层不检查。
	require.NoError(t, validateSnapshotAlignment(ctx, request("epoch", "1d", open)))
}

func TestPeriodDatasetRolesIncludeResampleResult(t *testing.T) {
	require.True(t, periodDatasetRoles["raw_collection"])
	require.True(t, periodDatasetRoles["kline_resample_result"])
	require.False(t, periodDatasetRoles["factor_result"])
}
