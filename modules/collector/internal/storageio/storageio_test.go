package storageio

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/coverage"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/pipeline"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/client"
)

func TestProviderAndUnifiedWritersEnforceDatasetRoles(t *testing.T) {
	access := &fakeAccess{}
	c := NewClientWithAccess(access, nil, []Binding{{SpaceID: "crypto_binance", DatasetID: "binance_kline", Role: RoleProviderData, Feed: "kline"}, {SpaceID: "crypto_binance", DatasetID: "spot_kline", Role: RoleUnifiedData, Feed: "kline"}})
	row := testKline()
	if err := c.WriteProviderKlines(context.Background(), "spot_kline", []marketdata.ProviderKline{row}); err == nil {
		t.Fatal("unified binding accepted as provider dataset")
	}
	if err := c.WriteUnifiedKline(context.Background(), "binance_kline", marketdata.ResolvedKline{ProviderKline: row}); err == nil {
		t.Fatal("provider binding accepted as unified dataset")
	}
	if err := c.WriteProviderKlines(context.Background(), "binance_kline", []marketdata.ProviderKline{row}); err != nil {
		t.Fatal(err)
	}
	if got := access.timeReq.GetWriteMode(); got != storagepb.RowWriteMode_ROW_WRITE_MODE_REPLACE {
		t.Fatalf("write mode = %s", got)
	}
	if len(access.timeReq.GetRows()[0].GetColumns()) < 12 {
		t.Fatalf("exact and numeric columns missing: %+v", access.timeReq.GetRows()[0])
	}
}

func TestQualityEventWriterUsesDeterministicRevisionKey(t *testing.T) {
	access := &fakeAccess{}
	c := NewClientWithAccess(access, nil, []Binding{{SpaceID: "crypto_binance", DatasetID: "kline_quality_event", Role: RoleQualityEvent, Feed: "quality_event"}})
	row := marketdata.ResolvedKline{ProviderKline: testKline(), SourceDatasetID: "binance_kline", Revision: 2, ResolvedAt: time.Date(2026, 7, 11, 1, 0, 0, 0, time.UTC)}
	events := []pipeline.QualityEvent{{Type: "kline_resolved", ProviderIDs: []marketdata.ProviderID{"binance", "okx"}, Reason: "confirmed"}}
	if err := c.WriteQualityEvents(context.Background(), "kline_quality_event", row, events); err != nil {
		t.Fatal(err)
	}
	first := access.recordReq.GetRows()[0].GetKey()
	if first.GetVersion() != "2" || first.GetRecordId() == "" || access.recordReq.GetWriteMode() != storagepb.RowWriteMode_ROW_WRITE_MODE_REPLACE {
		t.Fatalf("event key=%+v", first)
	}
	firstID := first.GetRecordId()
	if err := c.WriteQualityEvents(context.Background(), "kline_quality_event", row, events); err != nil {
		t.Fatal(err)
	}
	if access.recordReq.GetRows()[0].GetKey().GetRecordId() != firstID {
		t.Fatal("retry changed deterministic quality event key")
	}
}

func TestCalendarWriterUsesGenerationAsStableRecordVersion(t *testing.T) {
	generation := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	access := &fakeAccess{}
	c := NewClientWithAccess(access, nil, []Binding{{SpaceID: "crypto_binance", DatasetID: "calendar", Role: RoleUnifiedData, Feed: "calendar"}})
	day := markets.CalendarDay{ExchangeID: "BINANCE", TradeDate: "2026-07-11", Timezone: "UTC", Status: "open", Sessions: []markets.CalendarSession{{Open: generation, Close: generation.Add(24 * time.Hour)}}}
	if err := c.WriteCalendarDays(context.Background(), "calendar", generation, []markets.CalendarDay{day}); err != nil {
		t.Fatal(err)
	}
	row := access.recordReq.GetRows()[0]
	if row.GetKey().GetRecordId() != "BINANCE|2026-07-11" || row.GetKey().GetVersion() != generation.Format(time.RFC3339Nano) || access.recordReq.GetWriteMode() != storagepb.RowWriteMode_ROW_WRITE_MODE_REPLACE {
		t.Fatalf("row=%+v", row)
	}
}

func TestCoverageStoreReadsUnifiedRangeAndWritesDeterministicState(t *testing.T) {
	base := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	access := &fakeAccess{readRows: []*storagepb.TimeSeriesRow{{Key: &storagepb.TimeSeriesKey{SpaceId: "crypto_binance", DatasetId: "spot_kline", SubjectId: "BTC-USDT", Freq: "1m", DataTime: base.Format(time.RFC3339)}}}}
	c := NewClientWithAccess(access, nil, []Binding{{SpaceID: "crypto_binance", DatasetID: "spot_kline", Role: RoleUnifiedData, Feed: "kline"}, {SpaceID: "crypto_binance", DatasetID: "market_coverage", Role: RoleCoverageState, Feed: "coverage"}})
	buckets, err := c.PresentBuckets(context.Background(), "crypto_binance", "spot_kline", "BTC-USDT", marketdata.FrequencyMinute, base, base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 1 || access.readReq.GetKeys()[0].GetDataTime() != "" || access.readReq.GetTimeRange() == nil {
		t.Fatalf("buckets=%v request=%+v", buckets, access.readReq)
	}
	state := coverage.State{SpaceID: "crypto_binance", DatasetID: "spot_kline", SubjectID: "BTC-USDT", PartitionID: "2026-07-11", Frequency: marketdata.FrequencyMinute, Start: base, End: base.Add(time.Hour), Expected: 60, Present: 59, Missing: 1, MissingRanges: []coverage.Range{{Start: base.Add(time.Minute), End: base.Add(time.Minute), Buckets: 1}}, Status: "incomplete", CheckedAt: base.Add(time.Hour)}
	if err := c.WriteCoverageState(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if access.recordReq.GetWriteMode() != storagepb.RowWriteMode_ROW_WRITE_MODE_REPLACE || access.recordReq.GetRows()[0].GetKey().GetVersion() != base.Format(time.RFC3339Nano) {
		t.Fatalf("coverage request=%+v", access.recordReq)
	}
}

func TestReadCandidatesUsesExactKeys(t *testing.T) {
	access := &fakeAccess{readRows: []*storagepb.TimeSeriesRow{{Key: &storagepb.TimeSeriesKey{SpaceId: "crypto_binance", DatasetId: "binance_kline", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-11T00:00:00Z"}}}}
	c := NewClientWithAccess(access, nil, []Binding{{SpaceID: "crypto_binance", DatasetID: "binance_kline", Role: RoleProviderData, Feed: "kline"}})
	_, err := c.ReadCandidates(context.Background(), "crypto_binance", []string{"binance_kline"}, "BTC-USDT", marketdata.FrequencyMinute, time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(access.readReq.GetKeys()) != 1 || access.readReq.GetKeys()[0].GetDataTime() != "2026-07-11T00:00:00Z" {
		t.Fatalf("want exact read, got %+v", access.readReq)
	}
}

func testKline() marketdata.ProviderKline {
	return marketdata.ProviderKline{SubjectID: "BTC-USDT", ProviderID: "binance", ProviderSymbol: "BTCUSDT", Frequency: marketdata.FrequencyMinute, DataTime: time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC), CloseTime: time.Date(2026, 7, 11, 0, 1, 0, 0, time.UTC), TradeDate: "2026-07-11", FeedScope: "spot", VolumeUnit: "base", AmountUnit: "quote", Open: marketdata.MustDecimal("1"), High: marketdata.MustDecimal("2"), Low: marketdata.MustDecimal("1"), Close: marketdata.MustDecimal("2"), Volume: decimalPtr(marketdata.MustDecimal("3")), Amount: decimalPtr(marketdata.MustDecimal("6")), ProviderTimestamp: time.Date(2026, 7, 11, 0, 1, 0, 0, time.UTC), FetchedAt: time.Date(2026, 7, 11, 0, 1, 1, 0, time.UTC), RequestID: "req", Closed: true}
}
func decimalPtr(v marketdata.Decimal) *marketdata.Decimal { return &v }

type fakeAccess struct {
	timeReq   *storagepb.WriteTimeSeriesRowsReq
	readReq   *storagepb.ReadTimeSeriesRowsReq
	recordReq *storagepb.WriteRecordRowsReq
	readRows  []*storagepb.TimeSeriesRow
}

func (f *fakeAccess) WriteTimeSeriesRows(_ context.Context, req *storagepb.WriteTimeSeriesRowsReq, _ ...client.Option) (*storagepb.WriteTimeSeriesRowsRsp, error) {
	f.timeReq = req
	return &storagepb.WriteTimeSeriesRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}
func (f *fakeAccess) ReadTimeSeriesRows(_ context.Context, req *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	f.readReq = req
	return &storagepb.ReadTimeSeriesRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Rows: f.readRows}, nil
}
func (f *fakeAccess) WriteRecordRows(_ context.Context, req *storagepb.WriteRecordRowsReq, _ ...client.Option) (*storagepb.WriteRecordRowsRsp, error) {
	f.recordReq = req
	return &storagepb.WriteRecordRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}
func (f *fakeAccess) ReadRecordRows(context.Context, *storagepb.ReadRecordRowsReq, ...client.Option) (*storagepb.ReadRecordRowsRsp, error) {
	return &storagepb.ReadRecordRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}
