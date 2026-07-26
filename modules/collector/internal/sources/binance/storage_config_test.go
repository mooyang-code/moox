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

	mooxsecurity "github.com/mooyang-code/moox/packages/security"
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

	binding := &StorageBinding{SubjectDatasetIDs: []string{"rec-1", "rec-1", "", "kline-1"}}
	applyBindingDefaults(binding, "spot")
	assert.Equal(t, "binance", binding.DataSourceID)
	assert.Equal(t, "crypto_pair", binding.SubjectType)
	assert.Equal(t, "spot", binding.SubjectMarket)
	assert.Equal(t, []string{"rec-1", "kline-1"}, binding.SubjectDatasetIDs)

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
	assert.Equal(t, "name", stringField("name", "v").GetFieldId())
	assert.Equal(t, int64(3), intField("n", 3).GetValue().GetIntValue())
	assert.Equal(t, 1.5, doubleField("d", 1.5).GetValue().GetDoubleValue())

	auth := storageAuthInfo(StorageBinding{AuthInfo: StorageAuthInfo{AppID: "app", AppKey: "key"}})
	assert.Equal(t, "app", auth.GetAppId())
}

func TestStorageAuthInfoUsesRuntimePrimarySecret(t *testing.T) {
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "runtime-primary-secret")

	auth := storageAuthInfo(StorageBinding{AuthInfo: StorageAuthInfo{
		AppID: "moox-collector", AppKey: "packaged-fallback",
	}})
	assert.Equal(t, "moox-collector", auth.GetAppId())
	assert.Equal(t,
		mooxsecurity.HMACSHA256Hex("runtime-primary-secret", []byte("moox-collector")),
		auth.GetAppKey(),
	)
}

func TestNormalizeStorageTarget(t *testing.T) {
	assert.Equal(t, "ip://127.0.0.1:20102", normalizeStorageTarget("", "20102"))
	assert.Equal(t, "ip://10.0.0.1:20102", normalizeStorageTarget("10.0.0.1:20102", "20102"))
	assert.Equal(t, "ip://host:20100", normalizeStorageTarget("ip://host:20100", "20100"))
	assert.Equal(t, "http://svc:8080", normalizeStorageTarget("http://svc:8080", "20102"))
}

func TestStorageGatewayTargetDoesNotUseHTTPCallbackGateway(t *testing.T) {
	t.Setenv("MOOX_SERVICE_GATEWAY_TARGET", "http://127.0.0.1:11002")

	assert.Equal(t, "ip://127.0.0.1:11003", storageGatewayTarget("ip://127.0.0.1:11003", ""))
	assert.Equal(t, "ip://metadata:20100", storageGatewayTarget("", "ip://metadata:20100"))
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

func TestStorageWriterListsAllDatasetSubjectPages(t *testing.T) {
	proxy := &datasetSubjectProxy{}
	writer := &storageWriter{
		metadata: proxy,
		authInfo: &storagepb.AuthInfo{AppId: "collector"},
	}

	got, err := writer.ListDatasetSubjects(context.Background(), "space-a", "dataset-a")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "BTC-USDT", got[0].GetSubjectId())
	assert.Equal(t, "OLD-USDT", got[1].GetSubjectId())
	assert.Equal(t, []uint32{1, 2}, proxy.pages)
	assert.Equal(t, []uint32{200, 200}, proxy.sizes)
	for _, req := range proxy.listRequests {
		assert.Equal(t, "collector", req.GetAuthInfo().GetAppId())
		assert.Equal(t, "space-a", req.GetSpaceId())
		assert.Equal(t, "dataset-a", req.GetDatasetId())
	}
}

func TestStorageWriterBindsCompleteDatasetSubject(t *testing.T) {
	proxy := &datasetSubjectProxy{}
	writer := &storageWriter{
		metadata: proxy,
		authInfo: &storagepb.AuthInfo{AppId: "collector"},
	}
	item := &storagepb.DatasetSubject{
		SpaceId: "space-a", DatasetId: "dataset-a", SubjectId: "OLD-USDT",
		SubjectRole: "benchmark", EffectiveStartTime: "2026-01-01T00:00:00Z",
		EffectiveEndTime: "2026-12-31T00:00:00Z", Status: "inactive",
		CreatedAt: "created", UpdatedAt: "updated", Attributes: map[string]string{"source": "binance"},
	}

	require.NoError(t, writer.BindDatasetSubject(context.Background(), item))
	require.NotNil(t, proxy.bindRequest)
	assert.Equal(t, "collector", proxy.bindRequest.GetAuthInfo().GetAppId())
	assert.Same(t, item, proxy.bindRequest.GetDatasetSubject())
}

type latestTimeSeriesProxy struct {
	storagepb.PrimaryStoreClientProxy
	req *storagepb.ReadTimeSeriesRowsReq
	rsp *storagepb.ReadTimeSeriesRowsRsp
}

func (p *latestTimeSeriesProxy) ReadTimeSeriesRows(_ context.Context, req *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	p.req = req
	return p.rsp, nil
}

type datasetSubjectProxy struct {
	storagepb.MetadataClientProxy
	pages        []uint32
	sizes        []uint32
	listRequests []*storagepb.ListDatasetSubjectsReq
	bindRequest  *storagepb.BindDatasetSubjectReq
}

func (p *datasetSubjectProxy) ListDatasetSubjects(
	_ context.Context,
	req *storagepb.ListDatasetSubjectsReq,
	_ ...client.Option,
) (*storagepb.ListDatasetSubjectsRsp, error) {
	p.pages = append(p.pages, req.GetPage().GetPage())
	p.sizes = append(p.sizes, req.GetPage().GetSize())
	p.listRequests = append(p.listRequests, req)
	switch req.GetPage().GetPage() {
	case 1:
		return &storagepb.ListDatasetSubjectsRsp{
			RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS},
			DatasetSubjects: []*storagepb.DatasetSubject{{
				SpaceId: req.GetSpaceId(), DatasetId: req.GetDatasetId(), SubjectId: "BTC-USDT", Status: "active",
			}},
			PageResult: &storagepb.PageResult{Page: 1, Size: 200, HasMore: true},
		}, nil
	default:
		return &storagepb.ListDatasetSubjectsRsp{
			RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS},
			DatasetSubjects: []*storagepb.DatasetSubject{{
				SpaceId: req.GetSpaceId(), DatasetId: req.GetDatasetId(), SubjectId: "OLD-USDT", Status: "active",
			}},
			PageResult: &storagepb.PageResult{Page: 2, Size: 200},
		}, nil
	}
}

func (p *datasetSubjectProxy) BindDatasetSubject(
	_ context.Context,
	req *storagepb.BindDatasetSubjectReq,
	_ ...client.Option,
) (*storagepb.BindDatasetSubjectRsp, error) {
	p.bindRequest = req
	return &storagepb.BindDatasetSubjectRsp{
		RetInfo:        &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS},
		DatasetSubject: req.GetDatasetSubject(),
	}, nil
}
