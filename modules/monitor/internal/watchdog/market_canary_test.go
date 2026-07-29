package watchdog

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

type canaryReader struct {
	rows    []*storagepb.TimeSeriesRow
	request *storagepb.ReadTimeSeriesRowsReq
	retInfo *storagepb.RetInfo
}

func (r *canaryReader) ReadTimeSeriesRows(_ context.Context, request *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	r.request = request
	retInfo := r.retInfo
	if retInfo == nil {
		retInfo = &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}
	}
	return &storagepb.ReadTimeSeriesRowsRsp{
		RetInfo: retInfo,
		Rows:    r.rows,
	}, nil
}

func TestMarketCanaryReadsRealStorageScopeAndEvaluatesClosedBars(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	reader := &canaryReader{rows: []*storagepb.TimeSeriesRow{
		marketCanaryRow(now.Add(-2*time.Minute), 100, 10),
		marketCanaryRow(now.Add(-time.Minute), 101, 12),
	}}
	config := MarketCanaryConfig{
		SpaceID: "crypto", DatasetID: "market_kline", SubjectID: "BTC-USDT", Frequency: "1m",
		Freshness: 3 * time.Minute, ReturnThreshold: 0.05, VolumeRatioThreshold: 5,
	}
	auth := &commonpb.AuthInfo{AppId: "monitor-market-canary", AppKey: "derived-key"}
	result := (MarketCanary{Reader: reader, AuthInfo: auth, Config: config, Now: func() time.Time { return now }}).Run(t.Context())
	require.True(t, result.Success)
	require.Equal(t, "market_canary:market_kline:BTC-USDT:1m", result.CheckID)
	require.Equal(t, "crypto", reader.request.GetSpaceId())
	require.Equal(t, "market_kline", reader.request.GetDatasetId())
	require.Equal(t, "BTC-USDT", reader.request.GetKeys()[0].GetSubjectId())
	require.Equal(t, uint32(2), reader.request.GetPage().GetSize())
	require.Equal(t, auth, reader.request.GetAuthInfo())

	reader.rows = []*storagepb.TimeSeriesRow{
		marketCanaryRow(now.Add(-2*time.Minute), 100, 10),
		marketCanaryRow(now.Add(-time.Minute), 110, 12),
	}
	result = (MarketCanary{Reader: reader, AuthInfo: auth, Config: config, Now: func() time.Time { return now }}).Run(t.Context())
	require.False(t, result.Success)
	require.Equal(t, "threshold_exceeded", result.ErrorMessage)
}

func TestMarketCanaryPreservesStorageRejectionDetail(t *testing.T) {
	reader := &canaryReader{retInfo: &storagepb.RetInfo{Code: 7, Msg: "dataset disabled"}}
	result := (MarketCanary{
		Reader: reader,
		Config: MarketCanaryConfig{
			SpaceID: "crypto", DatasetID: "binance_spot_kline",
			SubjectID: "BTC-USDT", Frequency: "1m",
			Freshness: time.Minute, ReturnThreshold: 0.05, VolumeRatioThreshold: 5,
		},
	}).Run(t.Context())

	require.Equal(t, "storage_rejected_query:7:dataset disabled", result.ErrorMessage)
}

func TestMarketCanaryStorageRejectionKeepsValidUTF8(t *testing.T) {
	message := strings.Repeat("数据集不可用", 40)
	got := storageRejectionError(&storagepb.RetInfo{Code: 7, Msg: message})
	require.True(t, utf8.ValidString(got))
	require.LessOrEqual(t, len([]rune(got)), len([]rune("storage_rejected_query:7:"))+160)
}

func TestMarketCanaryStorageRejectionRedactsSensitiveDetail(t *testing.T) {
	for _, message := range []string{
		"query failed token=super-secret-value",
		"query failed secret: super-secret-value",
		"open /home/ubuntu/moox/storage/data/private.db: permission denied",
		"open /root/moox/storage/private.db: permission denied",
		`open C:\moox\storage\private.db: permission denied`,
	} {
		got := storageRejectionError(&storagepb.RetInfo{Code: 7, Msg: message})
		require.NotContains(t, got, "super-secret-value")
		require.NotContains(t, got, "/home/ubuntu")
		require.Contains(t, got, "details_redacted")
	}
}

func marketCanaryRow(at time.Time, closeValue, volumeValue float64) *storagepb.TimeSeriesRow {
	return &storagepb.TimeSeriesRow{
		Key: &storagepb.TimeSeriesKey{DataTime: at.UTC().Format(time.RFC3339Nano)},
		Fields: []*storagepb.FieldValue{
			{FieldId: "close", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: closeValue}}},
			{FieldId: "volume", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: volumeValue}}},
		},
	}
}
