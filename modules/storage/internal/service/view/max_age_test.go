package view

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/storagepolicy"
)

var maxAgeNow = time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)

func maxAgeWrite(space, dataset, freq string, dataTime time.Time) viewindex.RowWrite {
	return viewindex.RowWrite{Key: viewindex.RowKey{Key: &pb.RowKey{
		SpaceId: space, DatasetId: dataset,
		Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC", Freq: freq, DataTime: dataTime.Format(time.RFC3339Nano)}},
	}}}
}

func maxAgeService(t *testing.T) *Service {
	t.Helper()
	svc, err := New(t.TempDir(), "secret")
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return maxAgeNow }
	svc.SetMaxAge(storagepolicy.Default().View.MaxAge)
	return svc
}

func TestDropExpiredWritesBoundaryAndFrequencies(t *testing.T) {
	svc := maxAgeService(t)
	// 默认：1m 7d、5m 35d、1d 永久。
	writes := []viewindex.RowWrite{
		maxAgeWrite("crypto", "k", "5m", maxAgeNow.Add(-35*24*time.Hour)),                                                                             // 恰好等于期限：保留
		maxAgeWrite("crypto", "k", "5m", maxAgeNow.Add(-35*24*time.Hour-time.Nanosecond)),                                                             // 早 1ns：丢弃
		maxAgeWrite("crypto", "k", "1m", maxAgeNow.Add(-8*24*time.Hour)),                                                                              // 1m 期限更短：丢弃
		maxAgeWrite("crypto", "k", "1m", maxAgeNow.Add(-6*24*time.Hour)),                                                                              // 保留
		maxAgeWrite("crypto", "k", "1d", maxAgeNow.Add(-3000*24*time.Hour)),                                                                           // 永久：保留
		maxAgeWrite("crypto", "k", "45m", maxAgeNow.Add(-100*24*time.Hour)),                                                                           // 45m 回落到 30m（210d）：保留
		maxAgeWrite("mooxsys", "host", "1m", maxAgeNow.Add(-8*24*time.Hour)),                                                                          // 空间覆盖：7d，丢弃
		maxAgeWrite("stockcn", "k", "1m", maxAgeNow.Add(-30*24*time.Hour)),                                                                            // 股票 1m 60d：保留
		{Key: viewindex.RowKey{Key: &pb.RowKey{SpaceId: "crypto", DatasetId: "r", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "x"}}}}}, // 记录型行：保留
	}
	kept := svc.dropExpiredWrites("idx", writes)
	if len(kept) != 6 {
		t.Fatalf("保留 %d 行，期望 6", len(kept))
	}
	for _, write := range kept {
		if ts := write.Key.Key.GetTimeSeries(); ts != nil && ts.GetFreq() == "1m" && ts.GetDataTime() == maxAgeNow.Add(-8*24*time.Hour).Format(time.RFC3339Nano) {
			t.Fatalf("超期的 1m 行被保留: %v", write.Key.Key)
		}
	}
}

func TestDropExpiredWritesUnconfiguredKeepsEverything(t *testing.T) {
	svc, err := New(t.TempDir(), "secret")
	if err != nil {
		t.Fatal(err)
	}
	writes := []viewindex.RowWrite{maxAgeWrite("crypto", "k", "1m", maxAgeNow.Add(-1000*24*time.Hour))}
	if got := svc.dropExpiredWrites("idx", writes); len(got) != 1 {
		t.Fatalf("未配置最长保留期时不应丢行，保留 %d", len(got))
	}
}

// 因子结果集的 View 与 K 线 View 走同一条写入路径：writeIndex 在进入引擎前过滤，全部超期时不调用引擎。
func TestWriteIndexDropsExpiredRowsForKlineAndFactorResults(t *testing.T) {
	svc := maxAgeService(t)
	engine := &primaryHistoryBackfillEngine{}
	batch := func(dataset string) viewindex.ViewIndexWriteBatch {
		return viewindex.ViewIndexWriteBatch{RowWrites: []viewindex.RowWrite{
			maxAgeWrite("crypto", dataset, "5m", maxAgeNow.Add(-240*24*time.Hour)),
			maxAgeWrite("crypto", dataset, "5m", maxAgeNow.Add(-time.Hour)),
		}}
	}
	for _, dataset := range []string{"kline_5m", "factor_result_ema"} {
		engine.writeRows = 0
		if err := svc.writeIndex(context.Background(), "idx", engine, batch(dataset)); err != nil {
			t.Fatal(err)
		}
		if engine.writeRows != 1 {
			t.Fatalf("%s 写入 %d 行，期望只写未超期的 1 行", dataset, engine.writeRows)
		}
	}
	engine.writeRows = 0
	expiredOnly := viewindex.ViewIndexWriteBatch{RowWrites: []viewindex.RowWrite{maxAgeWrite("crypto", "kline_5m", "5m", maxAgeNow.Add(-240*24*time.Hour))}}
	if err := svc.writeIndex(context.Background(), "idx", engine, expiredOnly); err != nil || engine.writeRows != 0 {
		t.Fatalf("全部超期应直接返回: err=%v rows=%d", err, engine.writeRows)
	}
}
