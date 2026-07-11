package rpc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	cloudpb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/repository"
	"github.com/mooyang-code/moox/modules/collector/internal/storageio"
	"github.com/mooyang-code/moox/modules/collector/internal/taskpublisher"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/mooyang-code/moox/modules/storage/testkit"
	"github.com/mooyang-code/moox/packages/marketmanifest"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func TestCoverageCoordinatorPlansControlledGapRepair(t *testing.T) {
	base := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	access, err := testkit.Open(t.TempDir(), []testkit.DatasetSchema{rpcKlineSchema("crypto_binance", "spot_kline"), rpcCoverageSchema("crypto_binance", "market_coverage")})
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	store := storageio.NewClientWithAccess(access, nil, []storageio.Binding{{SpaceID: "crypto_binance", DatasetID: "spot_kline", Role: storageio.RoleUnifiedData, Feed: "kline", RequiredVolume: true, RequiredAmount: true}, {SpaceID: "crypto_binance", DatasetID: "market_coverage", Role: storageio.RoleCoverageState, Feed: "coverage"}})
	for _, offset := range []time.Duration{0, 2 * time.Minute} {
		row := rpcKline(base.Add(offset))
		if err := store.WriteUnifiedKline(context.Background(), "spot_kline", marketdata.ResolvedKline{ProviderKline: row, SourceDatasetID: "binance_kline", QualityStatus: "accepted", Revision: 1, ResolvedAt: row.CloseTime}); err != nil {
			t.Fatal(err)
		}
	}
	submitted := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/service/cloudnode/SubmitJobItems":
			var req cloudpb.SubmitJobItemsReq
			if err := protojson.Unmarshal(readRPCBody(t, r), &req); err != nil {
				t.Fatal(err)
			}
			submitted += len(req.GetItems())
			acks := make([]*cloudpb.JobItemAck, 0, len(req.GetItems()))
			for _, item := range req.GetItems() {
				acks = append(acks, &cloudpb.JobItemAck{JobItemId: item.GetJobItemId(), Status: cloudpb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED})
			}
			writeRPCProto(t, w, &cloudpb.SubmitJobItemsRsp{RetInfo: &cloudpb.RetInfo{Code: cloudpb.ErrorCode_SUCCESS}, Acks: acks})
		case "/api/service/cloudnode/GetNodeList":
			writeRPCProto(t, w, &cloudpb.GetNodeListRsp{RetInfo: &cloudpb.RetInfo{Code: cloudpb.ErrorCode_SUCCESS}})
		default:
			t.Fatalf("path=%s", r.URL.Path)
		}
	}))
	defer server.Close()
	db, err := gorm.Open(sqlite.Open("file:coverage-coordinator?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&domain.TaskInstance{}); err != nil {
		t.Fatal(err)
	}
	if err := repository.MigrateMarketControl(db); err != nil {
		t.Fatal(err)
	}
	manifest := marketmanifest.Manifest{MarketID: "crypto_binance", SpaceID: "crypto_binance", RuntimeEnabled: true, Readiness: marketmanifest.Readiness{CapabilityEnabled: true}, Timezone: "UTC", Exchange: marketmanifest.Exchange{ID: "BINANCE"}, ProductTypes: []string{"spot"}, InstrumentTypes: []string{"spot"}, Providers: []marketmanifest.Provider{{ID: "binance", Quotas: []marketmanifest.Quota{{Scope: "ip", WindowSeconds: 60, Limit: 6000}}}}, Datasets: []marketmanifest.Dataset{{ID: "binance_kline", Role: "provider_data", Feed: "kline", ProviderID: "binance"}, {ID: "spot_kline", Role: "unified_data", Feed: "kline"}, {ID: "market_coverage", Role: "coverage_state", Feed: "coverage"}}}
	service := New(db, Dependencies{ServiceGatewayTarget: server.URL, ServiceAuth: taskpublisher.AuthConfig{AccessKey: "ak", SecretKey: "sk"}, MarketManifests: []marketmanifest.Manifest{manifest}})
	service.storageAccess = access
	service.now = func() time.Time { return base.Add(time.Hour) }
	params := map[string]any{"job_type": "collect.kline", "market_id": "crypto_binance", "space_id": "crypto_binance", "exchange_id": "BINANCE", "product_type": "spot", "instrument_type": "spot", "provider_id": "binance", "provider_symbol": "BTCUSDT", "source_dataset_id": "binance_kline", "unified_dataset_id": "spot_kline", "frequency": "1m", "start_time": base.Format(time.RFC3339), "end_time": base.Add(3 * time.Minute).Format(time.RFC3339), "limit": 1000, "quota_scope_key": "ip"}
	raw, _ := json.Marshal(params)
	instance := domain.TaskInstance{SpaceID: "crypto_binance", TaskID: "task", RuleID: "rule", Market: "crypto_binance", DataType: "kline", DatasetID: "spot_kline", SubjectID: "BTC-USDT", Interval: "1m", TaskParams: string(raw), Result: "{}", CreateTime: base, ModifyTime: base}
	if err := db.Create(&instance).Error; err != nil {
		t.Fatal(err)
	}
	count, err := service.ReconcileMarketCoverage(context.Background(), 10)
	if err != nil || count != 1 || submitted != 1 {
		t.Fatalf("count=%d submitted=%d err=%v", count, submitted, err)
	}
}

func rpcKline(base time.Time) marketdata.ProviderKline {
	v := marketdata.MustDecimal("2")
	a := marketdata.MustDecimal("4")
	return marketdata.ProviderKline{SubjectID: "BTC-USDT", ProviderID: "binance", ProviderSymbol: "BTCUSDT", Frequency: marketdata.FrequencyMinute, DataTime: base, CloseTime: base.Add(time.Minute), TradeDate: "2026-07-11", FeedScope: "spot", VolumeUnit: "base", AmountUnit: "quote", Open: marketdata.MustDecimal("1"), High: marketdata.MustDecimal("2"), Low: marketdata.MustDecimal("1"), Close: marketdata.MustDecimal("2"), Volume: &v, Amount: &a, ProviderTimestamp: base.Add(time.Minute), FetchedAt: base.Add(time.Minute), RequestID: "req", Closed: true}
}
func rpcKlineSchema(space, dataset string) testkit.DatasetSchema {
	columns := map[string]storagepb.FieldValueType{}
	for _, name := range []string{"open", "high", "low", "close", "volume", "amount"} {
		columns[name] = storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE
		columns[name+"_exact"] = storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING
	}
	for _, name := range []string{"trade_date", "feed_scope", "volume_unit", "amount_unit", "request_id", "source_provider", "source_dataset_id", "quality_status"} {
		columns[name] = storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING
	}
	for _, name := range []string{"close_time", "provider_timestamp", "fetched_at", "source_fetched_at", "resolved_at"} {
		columns[name] = storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME
	}
	columns["is_closed"] = storagepb.FieldValueType_FIELD_VALUE_TYPE_BOOL
	columns["revision"] = storagepb.FieldValueType_FIELD_VALUE_TYPE_INT
	return testkit.DatasetSchema{SpaceID: space, DatasetID: dataset, Columns: columns}
}
func rpcCoverageSchema(space, dataset string) testkit.DatasetSchema {
	columns := map[string]storagepb.FieldValueType{}
	for _, name := range []string{"unified_dataset_id", "subject_id", "frequency", "partition_id", "missing_ranges", "coverage_status"} {
		columns[name] = storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING
	}
	for _, name := range []string{"range_start", "range_end", "checked_at"} {
		columns[name] = storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME
	}
	for _, name := range []string{"expected_count", "present_count", "missing_count"} {
		columns[name] = storagepb.FieldValueType_FIELD_VALUE_TYPE_INT
	}
	return testkit.DatasetSchema{SpaceID: space, DatasetID: dataset, Columns: columns}
}
func readRPCBody(t *testing.T, r *http.Request) []byte {
	t.Helper()
	defer r.Body.Close()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func writeRPCProto(t *testing.T, w http.ResponseWriter, value proto.Message) {
	t.Helper()
	raw, err := protojson.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}
