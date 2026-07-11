package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/storageio"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/modules/storage/testkit"
	"github.com/mooyang-code/moox/packages/marketmanifest"
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
