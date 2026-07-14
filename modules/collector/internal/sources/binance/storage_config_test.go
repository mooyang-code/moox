package binance

import (
	"context"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"
	"trpc.group/trpc-go/trpc-go/client"
)

func TestResolveBinanceSourceConfigPathFindsDeployedCollectorConfigs(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "collector", "configs", "sources", "market", "binance.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("api: {}\nstorage:\n  bindings: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Chdir(root)

	got, err := resolveBinanceSourceConfigPath()
	if err != nil {
		t.Fatalf("resolveBinanceSourceConfigPath() error = %v", err)
	}
	if got != configPath {
		t.Fatalf("resolveBinanceSourceConfigPath() = %q, want %q", got, configPath)
	}
}

func TestStorageBindingHelpers(t *testing.T) {
	market, subject, err := storageBindingKey(InstTypeSPOT)
	require.NoError(t, err)
	assert.Equal(t, "spot", market)
	assert.Equal(t, "spot", subject)

	_, _, err = storageBindingKey("UNKNOWN")
	assert.Error(t, err)

	binding := &StorageBinding{RecordDatasetID: "rec-1", KlineDatasetID: "kline-1"}
	applyBindingDefaults(binding, "spot")
	assert.Equal(t, "binance", binding.DataSourceID)
	assert.Equal(t, "crypto_pair", binding.SubjectType)
	assert.Equal(t, "spot", binding.SubjectMarket)
	assert.ElementsMatch(t, []string{"rec-1", "kline-1"}, binding.BindDatasetIDs)

	ids := appendMissingDatasetIDs([]string{"a", "a", ""}, "b", "a", "c")
	assert.Equal(t, []string{"a", "b", "c"}, ids)

	assert.Equal(t, []string{"x", "y"}, dedupeStrings([]string{"x", "x", "y"}))
}

func TestEnsureStorageOK(t *testing.T) {
	assert.Error(t, ensureStorageOK("act", nil))
	err := ensureStorageOK("act", &storagepb.RetInfo{Code: storagepb.ErrorCode_INNER_ERR, Msg: "fail"})
	assert.ErrorContains(t, err, "fail")
	assert.NoError(t, ensureStorageOK("act", &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}))
}

func TestStorageFieldBuilders(t *testing.T) {
	assert.Equal(t, "name", stringField("name", "v").GetColumnName())
	assert.Equal(t, int64(3), intField("n", 3).GetValue().GetIntValue())
	assert.Equal(t, 1.5, doubleField("d", 1.5).GetValue().GetDoubleValue())

	auth := storageAuthInfo(StorageBinding{AuthInfo: StorageAuthInfo{AppID: "app", AppKey: "key"}})
	assert.Equal(t, "app", auth.GetAppId())
}

func TestNormalizeStorageTarget(t *testing.T) {
	assert.Equal(t, "ip://127.0.0.1:20102", normalizeStorageTarget("", "20102"))
	assert.Equal(t, "ip://10.0.0.1:20102", normalizeStorageTarget("10.0.0.1:20102", "20102"))
	assert.Equal(t, "ip://host:20100", normalizeStorageTarget("ip://host:20100", "20100"))
	assert.Equal(t, "http://svc:8080", normalizeStorageTarget("http://svc:8080", "20102"))
}

func TestLatestTimeSeriesTimeReadsNewestStorageRow(t *testing.T) {
	proxy := &latestTimeSeriesProxy{rsp: &storagepb.ReadTimeSeriesRowsRsp{
		RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS},
		Rows:    []*storagepb.TimeSeriesRow{{Key: &storagepb.TimeSeriesKey{DataTime: "2026-07-10T12:00:00Z"}}},
	}}
	writer := &storageWriter{access: proxy}

	got, found, err := writer.LatestTimeSeriesTime(context.Background(), &storagepb.TimeSeriesKey{
		SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC-USDT", Freq: "1m",
	})
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC), got)
	require.NotNil(t, proxy.req)
	assert.Equal(t, storagepb.SortOrder_SORT_ORDER_DESC, proxy.req.GetOrder())
	assert.Equal(t, uint32(1), proxy.req.GetPage().GetSize())
}

type latestTimeSeriesProxy struct {
	storagepb.AccessClientProxy
	req *storagepb.ReadTimeSeriesRowsReq
	rsp *storagepb.ReadTimeSeriesRowsRsp
}

func (p *latestTimeSeriesProxy) ReadTimeSeriesRows(_ context.Context, req *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	p.req = req
	return p.rsp, nil
}
