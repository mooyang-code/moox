package bench

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDiscoverKlineFilesFindsSpotAndSwapCSVs(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{
		"period_1h_kline/spot/BTC-USDT.csv",
		"period_1h_kline/swap/ETH-USDT.csv",
		"period_5m_kline/spot/BNB-USDT.csv",
		"other/ignored.csv",
	} {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir failed: %v", err)
		}
		if err := os.WriteFile(path, []byte("header\n"), 0o600); err != nil {
			t.Fatalf("write file failed: %v", err)
		}
	}

	files, err := DiscoverKlineFiles(root, "1h")
	if err != nil {
		t.Fatalf("DiscoverKlineFiles returned error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files len = %d, want 2: %+v", len(files), files)
	}
	if files[0].Market != "spot" || files[0].SubjectID != "BTC-USDT" {
		t.Fatalf("first file = %+v, want spot BTC-USDT", files[0])
	}
	if files[1].Market != "swap" || files[1].SubjectID != "ETH-USDT" {
		t.Fatalf("second file = %+v, want swap ETH-USDT", files[1])
	}
}

func TestReadKlineCSVSkipsBannerAndParsesRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "BTC-USDT.csv")
	content := "download banner\n" +
		"candle_begin_time,open,high,low,close,volume,quote_volume,trade_num,taker_buy_base_asset_volume,taker_buy_quote_asset_volume,symbol,avg_price_1m,avg_price_5m,fundingRate\n" +
		"2025-03-03 00:05:00,1.1,1.2,1.0,1.15,10.5,12.3,7,5.1,6.2,BTC-USDT,1.11,1.12,-5.5e-05\n" +
		"2025-03-03 01:05:00,2.1,2.2,2.0,2.15,20.5,22.3,8,6.1,7.2,BTC-USDT,2.11,2.12,\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write csv failed: %v", err)
	}

	rows, err := ReadKlineCSV(KlineFile{Path: path, Market: "swap", SubjectID: "BTC-USDT", Freq: "1h"}, 0)
	if err != nil {
		t.Fatalf("ReadKlineCSV returned error: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows len = %d, want 2", len(rows))
	}
	if rows[0].Time != time.Date(2025, 3, 3, 0, 5, 0, 0, time.UTC) {
		t.Fatalf("time = %s", rows[0].Time)
	}
	if rows[0].Close != 1.15 || rows[0].TradeNum != 7 || rows[0].FundingRate == nil || *rows[0].FundingRate != -5.5e-05 {
		t.Fatalf("row[0] parsed incorrectly: %+v", rows[0])
	}
	if rows[1].FundingRate != nil {
		t.Fatalf("empty fundingRate should remain nil: %+v", rows[1].FundingRate)
	}
}

func TestLatencyRecorderSummarizesPercentiles(t *testing.T) {
	var recorder LatencyRecorder
	for _, d := range []time.Duration{10, 20, 30, 40, 50} {
		recorder.Add(d * time.Millisecond)
	}
	got := recorder.Summary()
	if got.Count != 5 {
		t.Fatalf("count = %d, want 5", got.Count)
	}
	if got.MinMS != 10 || got.MaxMS != 50 || got.P50MS != 30 || got.P95MS != 50 {
		t.Fatalf("summary = %+v, want min=10 p50=30 p95=50 max=50", got)
	}
}
