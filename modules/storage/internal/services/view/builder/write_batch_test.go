package builder

import (
	"context"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestMergeTimeSeriesRowsLatestWinsByMergeKey(t *testing.T) {
	rows := mergeTimeSeriesRowsLatestWins([]*pb.TimeSeriesRow{
		builderTestTSRow("crypto", "kline", "BTC-USDT", "2026-07-08T10:00:00Z", builderTestValue("close", 1)),
		builderTestTSRow("crypto", "kline", "ETH-USDT", "2026-07-08T10:00:00Z", builderTestValue("close", 10)),
		builderTestTSRow("crypto", "kline", "BTC-USDT", "2026-07-08T10:00:00Z", builderTestValue("close", 2), builderTestValue("volume", 20)),
	})

	if len(rows) != 2 {
		t.Fatalf("merged rows = %d, want BTC and ETH", len(rows))
	}
	btc := rows[0]
	if btc.GetKey().GetSubjectId() != "BTC-USDT" {
		t.Fatalf("first row subject = %q, want BTC-USDT", btc.GetKey().GetSubjectId())
	}
	if got := builderColumnDouble(btc, "close"); got != 2 {
		t.Fatalf("BTC close = %v, want latest value 2", got)
	}
	if got := builderColumnDouble(btc, "volume"); got != 20 {
		t.Fatalf("BTC volume = %v, want merged value 20", got)
	}
	if rows[1].GetKey().GetSubjectId() != "ETH-USDT" {
		t.Fatalf("second row subject = %q, want ETH-USDT preserved", rows[1].GetKey().GetSubjectId())
	}
}

func TestBatcherFlushesOnSizeAndWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := newBatcher[int](BatchOptions{BatchSize: 2, BatchWait: 20 * time.Millisecond})
	out := make(chan []int, 2)
	go b.run(ctx, out)

	if err := b.add(ctx, 1); err != nil {
		t.Fatalf("add 1: %v", err)
	}
	if err := b.add(ctx, 2); err != nil {
		t.Fatalf("add 2: %v", err)
	}
	got := <-out
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("size flush = %v, want [1 2]", got)
	}

	if err := b.add(ctx, 3); err != nil {
		t.Fatalf("add 3: %v", err)
	}
	select {
	case got = <-out:
		if len(got) != 1 || got[0] != 3 {
			t.Fatalf("wait flush = %v, want [3]", got)
		}
	case <-time.After(time.Second):
		t.Fatalf("batch_wait did not flush tail batch")
	}
}
