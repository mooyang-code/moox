package pebble

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// 同一行的多个字段由一次 UpsertFields 原子写入；并发读取必须总是读到同一次提交的值，不能读到旧 high、新 close 这类混合值。
func TestReadRowsReturnsOneCommitUnderConcurrentUpdates(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-08-16T00:00:00Z"}}}
	write := func(v float64) error {
		value := func() *pb.TypedValue { return &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: v}} }
		return upsertTestRows(store, context.Background(), []*pb.RowFieldUpsert{{Key: key, Fields: []*pb.FieldValue{
			{FieldId: "high", Value: value()}, {FieldId: "close", Value: value()}, {FieldId: "low", Value: value()},
		}}})
	}
	if err := write(1); err != nil {
		t.Fatal(err)
	}
	var stop atomic.Bool
	var writers sync.WaitGroup
	writers.Add(1)
	go func() {
		defer writers.Done()
		for v := float64(2); !stop.Load(); v++ {
			if err := write(v); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	var failures atomic.Int64
	var readers sync.WaitGroup
	for r := 0; r < 4; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for i := 0; i < 400; i++ {
				rows, err := store.ReadRows(context.Background(), []*pb.RowKey{key}, []string{"high", "close", "low"}, nil)
				if err != nil || len(rows) != 1 || len(rows[0].GetFields()) != 3 {
					failures.Add(1)
					continue
				}
				first := rows[0].GetFields()[0].GetValue().GetDoubleValue()
				for _, field := range rows[0].GetFields() {
					if field.GetValue().GetDoubleValue() != first {
						failures.Add(1)
						t.Errorf("读到混合值：%s", fmt.Sprint(rows[0].GetFields()))
						break
					}
				}
			}
		}()
	}
	readers.Wait()
	stop.Store(true)
	writers.Wait()
	if failures.Load() != 0 {
		t.Fatalf("并发读取出现 %d 次不一致或失败", failures.Load())
	}
}
