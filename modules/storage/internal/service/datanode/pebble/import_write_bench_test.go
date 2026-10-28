package pebble

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// TestImportUpsertThroughput 复现历史导入时 DataNode 的写入：每次请求 1000 行、每行 7 个字段，带 rows.upserted 事件，
// 同步落盘（与线上一致）。按不同并发数各写一遍，看串行锁下并发是否有收益。
// 默认跳过；MOOX_PEBBLE_BENCH=1 时运行，MOOX_PEBBLE_BENCH_REQUESTS 指定每种并发写多少个请求。
func TestImportUpsertThroughput(t *testing.T) {
	if os.Getenv("MOOX_PEBBLE_BENCH") != "1" {
		t.Skip("设置 MOOX_PEBBLE_BENCH=1 运行写入基准")
	}
	requests := 64
	if raw := os.Getenv("MOOX_PEBBLE_BENCH_REQUESTS"); raw != "" {
		requests, _ = strconv.Atoi(raw)
	}
	const rowsPerRequest = 1000
	columns := []string{"open", "high", "low", "close", "volume", "quote_volume", "trade_num"}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	buildRequest := func(n int) []*pb.RowFieldUpsert {
		rows := make([]*pb.RowFieldUpsert, 0, rowsPerRequest)
		subject := fmt.Sprintf("SYM%04d-USDT", n)
		for i := 0; i < rowsPerRequest; i++ {
			fields := make([]*pb.FieldValue, 0, len(columns))
			for j, name := range columns {
				fields = append(fields, &pb.FieldValue{FieldId: name, Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: float64(i) + float64(j)/10}}})
			}
			rows = append(rows, &pb.RowFieldUpsert{
				Key: &pb.RowKey{SpaceId: "crypto", DatasetId: "dataset_kline_5m", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
					SubjectId: subject, Freq: "5m", DataTime: start.Add(time.Duration(i) * 5 * time.Minute).Format(time.RFC3339), SeriesTag: "tag"}}},
				Fields: fields,
			})
		}
		return rows
	}
	for _, workers := range []int{1, 4, 8} {
		store, err := Open(Options{Path: t.TempDir(), NodeID: "bench"})
		if err != nil {
			t.Fatal(err)
		}
		prepared := make([][]*pb.RowFieldUpsert, requests)
		for n := range prepared {
			prepared[n] = buildRequest(n)
		}
		queue := make(chan int, requests)
		for n := 0; n < requests; n++ {
			queue <- n
		}
		close(queue)
		began := time.Now()
		var wg sync.WaitGroup
		var failed error
		var mu sync.Mutex
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for n := range queue {
					if _, err := upsertTestRowsEntries(store, context.Background(), prepared[n]); err != nil {
						mu.Lock()
						failed = err
						mu.Unlock()
						return
					}
				}
			}()
		}
		wg.Wait()
		took := time.Since(began)
		if failed != nil {
			t.Fatal(failed)
		}
		t.Logf("并发 %d：%d 个请求 × %d 行，%s，%.0f 行/秒", workers, requests, rowsPerRequest, took.Round(time.Millisecond),
			float64(requests*rowsPerRequest)/took.Seconds())
		_ = store.Close()
	}
}
