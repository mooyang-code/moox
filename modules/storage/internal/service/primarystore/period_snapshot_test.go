package primarystore

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/stretchr/testify/require"
)

func TestPrimarySnapshotRegistrationLazyPeriodAndKeyStatus(t *testing.T) {
	ctx := context.Background()
	period := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	var now atomic.Int64
	// 首次登记同样要求 P 晚于 Storage 当前时间：先在 P 之前登记，之后再把时钟推到 P 之后。
	now.Store(period.Add(-time.Minute).UnixNano())
	const secret = "snapshot-secret"
	node, err := datanode.NewService(datanode.Options{NodeID: "node-a", AuthSecret: secret, Pebble: pebble.Options{
		NodeID: "node-a", Path: filepath.Join(t.TempDir(), "node"), PeriodNow: func() time.Time { return time.Unix(0, now.Load()).UTC() },
	}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, node.Close()) })
	dataset := &pb.Dataset{SpaceId: "crypto", DatasetId: "bars", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1m",
		Attributes: map[string]string{"owner_module": "collector", "dataset_role": "raw_collection", "allowed_lateness_seconds": "30"}}
	service, err := New(Options{
		Node: primaryPeriodNodeIDAdapter{Service: node},
		Snapshot: func() metadata.RequestSnapshot {
			return periodAuthorizationSnapshot{datasets: map[routeKey]*pb.Dataset{{spaceID: "crypto", datasetID: "bars"}: dataset}}
		},
		AuthSigner: func(*pb.AuthInfo) (*pb.AuthInfo, error) {
			return &pb.AuthInfo{AppId: "primary", AppKey: datanode.ServiceAuthKey(secret, "primary")}, nil
		},
	})
	require.NoError(t, err)
	auth := &pb.AuthInfo{AppId: "collector", AppKey: "caller-key"}

	set, err := seriesset.Canonicalize([]seriesset.Series{
		{Provider: "binance", SourceID: "spot_http", MarketType: "spot", SubjectID: "BTC-USDT"},
		{Provider: "binance", SourceID: "spot_http", MarketType: "spot", SubjectID: "ETH-USDT"},
	})
	require.NoError(t, err)
	entries := make([]*pb.SeriesSnapshotEntry, 0, len(set.Series))
	for _, item := range set.Series {
		entries = append(entries, &pb.SeriesSnapshotEntry{Provider: item.Provider, SourceId: item.SourceID, MarketType: item.MarketType, SubjectId: item.SubjectID, SeriesTag: item.SeriesTag})
	}
	registered, err := service.RegisterSeriesSnapshot(ctx, &pb.PrimaryRegisterSeriesSnapshotReq{AuthInfo: auth, SpaceId: "crypto", DatasetId: "bars", Frequency: "1m", Series: entries, EffectiveFrom: period.Unix()})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, registered.GetRetInfo().GetCode(), "%v", registered.GetRetInfo())
	require.Equal(t, set.Hash, registered.GetSeriesHash(), "Storage 重算的 hash 必须与共享包一致")
	now.Store(period.Add(61 * time.Second).UnixNano())

	// 周期还不存在：按周期键查询返回 found=false，而不是错误。
	key := &pb.DatasetPeriodExpectation{SpaceId: "crypto", DatasetId: "bars", Frequency: "1m", PeriodTime: period.Unix()}
	status, err := service.GetDatasetPeriodStatus(ctx, &pb.PrimaryGetDatasetPeriodStatusReq{AuthInfo: auth, Expectation: key, IncludeProgress: true})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, status.GetRetInfo().GetCode())
	require.False(t, status.GetFound())

	exp := &pb.DatasetPeriodExpectation{SpaceId: "crypto", DatasetId: "bars", Frequency: "1m", PeriodTime: period.Unix(), SeriesHash: set.Hash, ExpectedCount: 2}
	committed, err := service.CommitTimeSeriesBatch(ctx, &pb.PrimaryCommitTimeSeriesBatchReq{AuthInfo: auth, Expectation: exp, WriteSource: "collector",
		Items: []*pb.TimeSeriesBatchRow{{SeriesIndex: 0, Row: primaryPeriodRowAt(period, "BTC-USDT", "")}}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, committed.GetRetInfo().GetCode(), "%v", committed.GetRetInfo())
	require.Equal(t, "waiting", committed.GetPeriodStatus())

	status, err = service.GetDatasetPeriodStatus(ctx, &pb.PrimaryGetDatasetPeriodStatusReq{AuthInfo: auth, Expectation: key, IncludeProgress: true})
	require.NoError(t, err)
	require.True(t, status.GetFound())
	require.Equal(t, period.Add(time.Minute+30*time.Second).Unix(), status.GetDeadlineAt(), "迟到时间来自数据集属性 allowed_lateness_seconds")
	require.Equal(t, uint32(1), status.GetSuccessfulCount())
	require.Equal(t, []uint32{1}, status.GetMissingSeriesIndexes())

	// 非 Collector 身份不能登记快照。
	denied, err := service.RegisterSeriesSnapshot(ctx, &pb.PrimaryRegisterSeriesSnapshotReq{AuthInfo: &pb.AuthInfo{AppId: "admin", AppKey: "k"}, SpaceId: "crypto", DatasetId: "bars", Frequency: "1m", Series: entries, EffectiveFrom: period.Unix() + 600})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NO_PERMISSION, denied.GetRetInfo().GetCode())
}

func primaryPeriodRowAt(period time.Time, subject, tag string) *pb.RowFieldUpsert {
	return &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: "crypto", DatasetId: "bars", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: subject, SeriesTag: tag, Freq: "1m", DataTime: period.Format(time.RFC3339Nano)}}},
		Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}}}
}
