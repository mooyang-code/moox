package view

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// 写索引期间一直持有 runtime.mu（大批因子写入要几秒）：查询要能解析出活动索引与缓存的统计，不等它。
func TestQueryPathDoesNotWaitForRuntimeLock(t *testing.T) {
	svc := newTestService(map[string]viewindex.Engine{"duckdb": &queryEngine{}})
	runtime := testRuntime(svc, timeSeriesSpec("crypto", "factors", "1m", "close"))
	runtime.mu.Lock()
	runtime.active = runtime.indexID
	runtime.statsIndexID = runtime.indexID
	runtime.stats = viewindex.ViewIndexStats{Exists: true, EntryCount: 42}
	runtime.publishReadStateLocked()
	runtime.mu.Unlock()

	runtime.mu.Lock() // 一次索引写入正在进行
	defer runtime.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if indexID, got := svc.activeIndex("crypto", "factors"); indexID != runtime.indexID || got != runtime {
			t.Errorf("activeIndex = %q, %p", indexID, got)
		}
		if stats, ok := cachedActiveIndexStats(runtime, runtime.indexID); !ok || stats.EntryCount != 42 {
			t.Errorf("cachedActiveIndexStats = %+v, %v", stats, ok)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("query path blocked on the runtime lock")
	}
}

// 并发 100 个查询的同时有一次很长的索引写入（持有运行时锁与索引写锁）：查询的 p99 不随写入时长增加。
func TestQueriesDoNotWaitForLongIndexWrite(t *testing.T) {
	stats := viewindex.ViewIndexStats{Exists: true, EntryCount: 1, IndexedFrom: "2026-07-29T00:00:00Z", IndexedTo: "2026-07-29T00:01:00Z"}
	engine := &queryEngine{rows: []*pb.RowFieldValues{{Key: timeSeriesTestRowKey("")}}, stats: stats}
	svc := queryTestService(engine, true)
	runtime := svc.runtimeOf(marketRef)
	const writeHold = 2 * time.Second
	writing := make(chan struct{})
	go func() {
		runtime.mu.Lock()
		release, err := runtime.gate.lock(context.Background())
		if err != nil {
			runtime.mu.Unlock()
			return
		}
		close(writing)
		time.Sleep(writeHold)
		release()
		runtime.mu.Unlock()
	}()
	<-writing
	var mu sync.Mutex
	durations := make([]time.Duration, 0, 100)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			started := time.Now()
			rsp, err := svc.QueryTimeSeriesRows(context.Background(), &pb.QueryTimeSeriesRowsReq{
				SpaceId: "space", DatasetId: "market",
				Selectors: []*pb.TimeSeriesSelector{{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m"}},
				TimeRange: &pb.TimeRange{StartTime: "2026-07-29T00:00:00Z", EndTime: "2026-07-29T00:01:00Z"},
			})
			elapsed := time.Since(started)
			if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
				t.Errorf("rsp=%v err=%v", rsp, err)
			}
			mu.Lock()
			durations = append(durations, elapsed)
			mu.Unlock()
		}()
	}
	wg.Wait()
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	if p99 := durations[len(durations)*99/100-1]; p99 >= writeHold/2 {
		t.Fatalf("写索引期间查询的 p99 = %s，不应等写入（%s）", p99, writeHold)
	}
}
