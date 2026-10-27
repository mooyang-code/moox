package pebble

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// 读取 benchmark 的数据规模：500 个标的 × 3000 根 1m × 8 列，与改造前后共用同一份数据构造。
// 运行：go test -run '^$' -bench BenchmarkRead -benchmem -benchtime 5x ./internal/service/datanode/pebble
const (
	benchSubjects = 500
	benchBars     = 3000
)

var (
	benchColumns = []string{"open", "high", "low", "close", "volume", "amount", "trade_num", "vwap"}
	benchStart   = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	benchOnce    sync.Once
	benchStore   *Store
	benchDir     string
)

func benchData(b *testing.B) *Store {
	b.Helper()
	benchOnce.Do(func() {
		dir, err := os.MkdirTemp("", "pebble-read-bench-")
		if err != nil {
			b.Fatal(err)
		}
		benchDir = dir
		store, err := Open(Options{Path: dir, NodeID: "bench", DisableSyncWrites: true})
		if err != nil {
			b.Fatal(err)
		}
		batch := make([]*pb.RowFieldUpsert, 0, 4000)
		flush := func() {
			if len(batch) == 0 {
				return
			}
			if err := upsertTestRows(store, context.Background(), batch); err != nil {
				b.Fatal(err)
			}
			batch = batch[:0]
		}
		for s := 0; s < benchSubjects; s++ {
			subject := fmt.Sprintf("S%04d", s)
			for i := 0; i < benchBars; i++ {
				fields := make([]*pb.FieldValue, 0, len(benchColumns))
				for _, c := range benchColumns {
					fields = append(fields, &pb.FieldValue{FieldId: c, Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: float64(i)}}})
				}
				batch = append(batch, &pb.RowFieldUpsert{
					Key: &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
						SubjectId: subject, Freq: "1m", DataTime: benchStart.Add(time.Duration(i) * time.Minute).Format(time.RFC3339),
					}}},
					Fields: fields,
				})
				if len(batch) == cap(batch) {
					flush()
				}
			}
		}
		flush()
		benchStore = store
	})
	return benchStore
}

func TestMain(m *testing.M) {
	code := m.Run()
	if benchStore != nil {
		_ = benchStore.Close()
	}
	if benchDir != "" {
		_ = os.RemoveAll(benchDir)
	}
	os.Exit(code)
}

func benchSelectors(n int) []*pb.TimeSeriesSelector {
	out := make([]*pb.TimeSeriesSelector, 0, n)
	for s := 0; s < n; s++ {
		out = append(out, &pb.TimeSeriesSelector{SpaceId: "s", DatasetId: "d", SubjectId: fmt.Sprintf("S%04d", s*7%benchSubjects), Freq: "1m"})
	}
	return out
}

// 最近 N 根，不指定标的：View 重建的读法，读完整个数据集每个序列最近 100 根。
func BenchmarkReadLatestAllSubjects(b *testing.B) {
	store := benchData(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, cursor := 0, ""
		for {
			rsp, err := store.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{
				SpaceId: "s", DatasetId: "d", ColumnNames: benchColumns, LatestPerSeries: 100, Page: &pb.Page{Size: 20000, Cursor: cursor},
			})
			if err != nil {
				b.Fatal(err)
			}
			rows += len(rsp.GetRows())
			if cursor = rsp.GetPageResult().GetNextCursor(); cursor == "" {
				break
			}
		}
		if rows != benchSubjects*100 {
			b.Fatalf("rows=%d", rows)
		}
	}
}

// 50 个标的的一天区间（1440 根）。
func BenchmarkReadRange50SubjectsOneDay(b *testing.B) {
	store := benchData(b)
	selectors := benchSelectors(50)
	tr := &pb.TimeRange{StartTime: benchStart.Add(300 * time.Minute).Format(time.RFC3339), EndTime: benchStart.Add(300*time.Minute + 24*time.Hour).Format(time.RFC3339)}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, cursor := 0, ""
		for {
			rsp, err := store.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{
				SpaceId: "s", DatasetId: "d", Selectors: selectors, TimeRange: tr, ColumnNames: benchColumns, Page: &pb.Page{Size: 2000, Cursor: cursor},
			})
			if err != nil {
				b.Fatal(err)
			}
			rows += len(rsp.GetRows())
			if cursor = rsp.GetPageResult().GetNextCursor(); cursor == "" {
				break
			}
		}
		if rows != 50*1440 {
			b.Fatalf("rows=%d", rows)
		}
	}
}

// 单标的窗口：最近 500 根。
func BenchmarkReadSingleSubjectWindow(b *testing.B) {
	store := benchData(b)
	selectors := benchSelectors(1)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rsp, err := store.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{
			SpaceId: "s", DatasetId: "d", Selectors: selectors, ColumnNames: benchColumns, LatestPerSeries: 500, Page: &pb.Page{Size: 2000},
		})
		if err != nil {
			b.Fatal(err)
		}
		if len(rsp.GetRows()) != 500 {
			b.Fatalf("rows=%d", len(rsp.GetRows()))
		}
	}
}
