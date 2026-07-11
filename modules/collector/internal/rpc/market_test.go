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
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/modules/storage/testkit"
	"github.com/mooyang-code/moox/packages/marketmanifest"
	"google.golang.org/protobuf/encoding/protojson"
	"gorm.io/gorm"
)

func TestQueryMarketKlinesUsesLogicalMarketAndCompositeCursor(t *testing.T) {
	base := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	access, err := testkit.Open(t.TempDir(), []testkit.DatasetSchema{rpcKlineSchema("crypto_binance", "spot_kline")})
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	store := storageio.NewClientWithAccess(access, nil, []storageio.Binding{{SpaceID: "crypto_binance", DatasetID: "spot_kline", Role: storageio.RoleUnifiedData, Feed: "kline", RequiredVolume: true, RequiredAmount: true}})
	for _, subject := range []string{"BTC-USDT", "ETH-USDT"} {
		row := rpcKline(base)
		row.SubjectID, row.ProviderSymbol = subject, subject
		if err := store.WriteUnifiedKline(context.Background(), "spot_kline", marketdata.ResolvedKline{ProviderKline: row, SourceDatasetID: "binance_kline", QualityStatus: "confirmed", Revision: 1, ResolvedAt: base.Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	db, err := gorm.Open(sqlite.Open("file:market-query?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	manifest := marketmanifest.Manifest{MarketID: "crypto_binance", SpaceID: "crypto_binance", InstrumentTypes: []string{"spot"}, Feeds: []marketmanifest.Feed{{ID: "spot_kline", DatasetID: "spot_kline", InstrumentType: "spot", Frequencies: []string{"1m"}}}}
	service := New(db, Dependencies{MarketManifests: []marketmanifest.Manifest{manifest}})
	service.storageAccess = access
	service.now = func() time.Time { return base.Add(time.Hour) }
	req := &pb.QueryMarketKlinesReq{MarketId: "crypto_binance", SubjectIds: []string{"ETH-USDT", "BTC-USDT"}, InstrumentTypes: []string{"spot"}, Frequency: "1m", StartTime: base.Format(time.RFC3339), EndTime: base.Format(time.RFC3339), Order: "asc", PageSize: 1}
	first, err := service.QueryMarketKlines(context.Background(), req)
	if err != nil || first.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(first.GetRows()) != 1 || first.GetNextCursor() == "" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	req.Cursor = first.GetNextCursor()
	second, err := service.QueryMarketKlines(context.Background(), req)
	if err != nil || second.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(second.GetRows()) != 1 || second.GetRows()[0].GetSubjectId() == first.GetRows()[0].GetSubjectId() {
		t.Fatalf("second=%+v err=%v", second, err)
	}
}

func TestRefreshMarketKlinesPublishesExistingLogicalTask(t *testing.T) {
	now := time.Date(2026, 7, 11, 1, 0, 0, 0, time.UTC)
	submitted := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/api/service/cloudnode/SubmitJobItems":
			var req cloudpb.SubmitJobItemsReq
			if err := protojson.Unmarshal(body, &req); err != nil {
				t.Fatal(err)
			}
			submitted += len(req.GetItems())
			acks := []*cloudpb.JobItemAck{{JobItemId: req.GetItems()[0].GetJobItemId(), Status: cloudpb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED}}
			writeRPCProto(t, w, &cloudpb.SubmitJobItemsRsp{RetInfo: &cloudpb.RetInfo{Code: cloudpb.ErrorCode_SUCCESS}, Acks: acks})
		case "/api/service/cloudnode/GetNodeList":
			writeRPCProto(t, w, &cloudpb.GetNodeListRsp{RetInfo: &cloudpb.RetInfo{Code: cloudpb.ErrorCode_SUCCESS}})
		default:
			t.Fatalf("path=%s", r.URL.Path)
		}
	}))
	defer server.Close()
	db, _ := gorm.Open(sqlite.Open("file:market-refresh?mode=memory&cache=shared"), &gorm.Config{})
	if err := db.AutoMigrate(&domain.TaskInstance{}); err != nil {
		t.Fatal(err)
	}
	if err := repository.MigrateMarketControl(db); err != nil {
		t.Fatal(err)
	}
	params := map[string]any{"job_type": "collect.kline", "market_id": "crypto_binance", "space_id": "crypto_binance", "provider_id": "binance", "provider_symbol": "BTCUSDT", "source_dataset_id": "binance_kline", "unified_dataset_id": "spot_kline", "subject_id": "BTC-USDT", "frequency": "1m", "start_time": now.Add(-time.Hour).Format(time.RFC3339), "end_time": now.Format(time.RFC3339), "quota_scope_key": "ip", "quota_windows": []any{map[string]any{"window_seconds": 60, "limit": 6000}}}
	raw, _ := json.Marshal(params)
	instance := domain.TaskInstance{SpaceID: "crypto_binance", TaskID: "logical-task", RuleID: "rule", DataType: "kline", DatasetID: "spot_kline", SubjectID: "BTC-USDT", Interval: "1m", TaskParams: string(raw), Result: "{}", CreateTime: now, ModifyTime: now}
	if err := db.Create(&instance).Error; err != nil {
		t.Fatal(err)
	}
	manifest := marketmanifest.Manifest{MarketID: "crypto_binance", SpaceID: "crypto_binance", RuntimeEnabled: true, Readiness: marketmanifest.Readiness{CapabilityEnabled: true}, InstrumentTypes: []string{"spot"}, Feeds: []marketmanifest.Feed{{DatasetID: "spot_kline", InstrumentType: "spot", Frequencies: []string{"1m"}}}}
	service := New(db, Dependencies{ServiceGatewayTarget: server.URL, ServiceAuth: taskpublisher.AuthConfig{AccessKey: "ak", SecretKey: "sk"}, MarketManifests: []marketmanifest.Manifest{manifest}})
	service.now = func() time.Time { return now }
	rsp, err := service.RefreshMarketKlines(context.Background(), &pb.RefreshMarketKlinesReq{MarketId: "crypto_binance", InstrumentTypes: []string{"spot"}, SubjectIds: []string{"BTC-USDT"}, Frequency: "1m", StartTime: now.Add(-2 * time.Hour).Format(time.RFC3339), EndTime: now.Format(time.RFC3339)})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || submitted != 1 || len(rsp.GetTaskIds()) != 1 || rsp.GetTaskIds()[0] != "logical-task" {
		t.Fatalf("rsp=%+v submitted=%d err=%v", rsp, submitted, err)
	}
}

func TestQueryMarketKlinesRejectsChangedBoundary(t *testing.T) {
	base := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	access, err := testkit.Open(t.TempDir(), []testkit.DatasetSchema{rpcKlineSchema("crypto_binance", "spot_kline")})
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	store := storageio.NewClientWithAccess(access, nil, []storageio.Binding{{SpaceID: "crypto_binance", DatasetID: "spot_kline", Role: storageio.RoleUnifiedData, Feed: "kline", RequiredVolume: true, RequiredAmount: true}})
	write := func(revision int64, close string) {
		row := rpcKline(base)
		row.Close = marketdata.MustDecimal(close)
		row.High = row.Close
		if err := store.WriteUnifiedKline(context.Background(), "spot_kline", marketdata.ResolvedKline{ProviderKline: row, SourceDatasetID: "binance_kline", QualityStatus: "confirmed", Revision: revision, ResolvedAt: base.Add(time.Duration(revision) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	write(1, "2")
	db, _ := gorm.Open(sqlite.Open("file:market-query-change?mode=memory&cache=shared"), &gorm.Config{})
	manifest := marketmanifest.Manifest{MarketID: "crypto_binance", SpaceID: "crypto_binance", InstrumentTypes: []string{"spot"}, Feeds: []marketmanifest.Feed{{DatasetID: "spot_kline", InstrumentType: "spot", Frequencies: []string{"1m"}}}}
	service := New(db, Dependencies{MarketManifests: []marketmanifest.Manifest{manifest}})
	service.storageAccess, service.now = access, func() time.Time { return base.Add(time.Hour) }
	req := &pb.QueryMarketKlinesReq{MarketId: "crypto_binance", SubjectIds: []string{"BTC-USDT"}, InstrumentTypes: []string{"spot"}, Frequency: "1m", StartTime: base.Format(time.RFC3339), EndTime: base.Format(time.RFC3339), PageSize: 1}
	first, _ := service.QueryMarketKlines(context.Background(), req)
	// Add a second row so the first response carries a continuation boundary.
	other := rpcKline(base)
	other.SubjectID = "ETH-USDT"
	_ = store.WriteUnifiedKline(context.Background(), "spot_kline", marketdata.ResolvedKline{ProviderKline: other, SourceDatasetID: "binance_kline", QualityStatus: "confirmed", Revision: 1, ResolvedAt: base.Add(time.Minute)})
	req.SubjectIds = []string{"BTC-USDT", "ETH-USDT"}
	first, _ = service.QueryMarketKlines(context.Background(), req)
	write(2, "3")
	req.Cursor = first.GetNextCursor()
	changed, _ := service.QueryMarketKlines(context.Background(), req)
	if changed.GetRetInfo().GetMsg() != "data_changed_restart_query" {
		t.Fatalf("changed=%+v", changed)
	}
}

func TestQueryMarketKlinesRejectsInsertionBeforeBoundary(t *testing.T) {
	base := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	access, err := testkit.Open(t.TempDir(), []testkit.DatasetSchema{rpcKlineSchema("crypto_binance", "spot_kline")})
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	store := storageio.NewClientWithAccess(access, nil, []storageio.Binding{{SpaceID: "crypto_binance", DatasetID: "spot_kline", Role: storageio.RoleUnifiedData, Feed: "kline", RequiredVolume: true, RequiredAmount: true}})
	writeAt := func(at time.Time) {
		row := rpcKline(at)
		row.DataTime, row.CloseTime = at, at.Add(time.Minute)
		if err := store.WriteUnifiedKline(context.Background(), "spot_kline", marketdata.ResolvedKline{ProviderKline: row, SourceDatasetID: "binance_kline", QualityStatus: "confirmed", Revision: 1, ResolvedAt: at.Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	writeAt(base.Add(time.Minute))
	writeAt(base.Add(2 * time.Minute))
	db, _ := gorm.Open(sqlite.Open("file:market-query-insert?mode=memory&cache=shared"), &gorm.Config{})
	manifest := marketmanifest.Manifest{MarketID: "crypto_binance", SpaceID: "crypto_binance", InstrumentTypes: []string{"spot"}, Feeds: []marketmanifest.Feed{{DatasetID: "spot_kline", InstrumentType: "spot", Frequencies: []string{"1m"}}}}
	service := New(db, Dependencies{MarketManifests: []marketmanifest.Manifest{manifest}})
	service.storageAccess, service.now = access, func() time.Time { return base.Add(time.Hour) }
	req := &pb.QueryMarketKlinesReq{MarketId: "crypto_binance", SubjectIds: []string{"BTC-USDT"}, InstrumentTypes: []string{"spot"}, Frequency: "1m", StartTime: base.Format(time.RFC3339), EndTime: base.Add(2 * time.Minute).Format(time.RFC3339), PageSize: 1}
	first, _ := service.QueryMarketKlines(context.Background(), req)
	if first.GetNextCursor() == "" {
		t.Fatal("expected cursor")
	}
	writeAt(base)
	req.Cursor = first.GetNextCursor()
	changed, _ := service.QueryMarketKlines(context.Background(), req)
	if changed.GetRetInfo().GetMsg() != "data_changed_restart_query" {
		t.Fatalf("changed=%+v", changed)
	}
}
