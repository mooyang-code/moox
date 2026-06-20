package bench

import (
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestKlineRowsToDataRowsMapsColumnsAndSkipsEmptyFundingRate(t *testing.T) {
	fundingRate := -0.0001
	rows := []KlineRow{
		{
			Time: time.Date(2025, 3, 3, 0, 5, 0, 0, time.UTC), Open: 1, High: 2, Low: 0.5, Close: 1.5,
			Volume: 10, QuoteVolume: 20, TradeNum: 7, Symbol: "BTC-USDT", AvgPrice1M: 1.4, AvgPrice5M: 1.3,
			TakerBuyBaseAssetVolume: 4, TakerBuyQuoteAssetVolume: 5, FundingRate: &fundingRate,
		},
		{
			Time: time.Date(2025, 3, 3, 1, 5, 0, 0, time.UTC), Open: 2, High: 3, Low: 1.5, Close: 2.5,
			Volume: 11, QuoteVolume: 21, TradeNum: 8, Symbol: "BTC-USDT", AvgPrice1M: 2.4, AvgPrice5M: 2.3,
			TakerBuyBaseAssetVolume: 6, TakerBuyQuoteAssetVolume: 7,
		},
	}

	got := KlineRowsToDataRows("crypto", "bench_swap_kline_1h", "BTC-USDT", "1h", rows)
	if len(got) != 2 {
		t.Fatalf("rows len = %d, want 2", len(got))
	}
	if got[0].GetKey().GetDataTime() != "2025-03-03T00:05:00Z" {
		t.Fatalf("data_time = %q", got[0].GetKey().GetDataTime())
	}
	if got[0].GetKey().GetRowId() != "2025-03-03T00:05:00Z" {
		t.Fatalf("row_id = %q", got[0].GetKey().GetRowId())
	}
	if !hasColumn(got[0], "fundingRate") {
		t.Fatalf("first row should include fundingRate: %+v", got[0].GetColumns())
	}
	if hasColumn(got[1], "fundingRate") {
		t.Fatalf("second row should omit empty fundingRate: %+v", got[1].GetColumns())
	}
	if valueType(got[0], "trade_num") != pb.FieldValueType_FIELD_VALUE_TYPE_INT {
		t.Fatalf("trade_num should be int")
	}
}

func hasColumn(row *pb.DataRow, name string) bool {
	for _, column := range row.GetColumns() {
		if column.GetColumnName() == name {
			return true
		}
	}
	return false
}

func valueType(row *pb.DataRow, name string) pb.FieldValueType {
	for _, column := range row.GetColumns() {
		if column.GetColumnName() == name {
			return column.GetValueType()
		}
	}
	return pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED
}
