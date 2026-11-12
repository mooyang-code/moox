//go:build cgo

package e2e

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	viewservice "github.com/mooyang-code/moox/modules/storage/internal/service/view"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/client"
)

// blockingRangeReader 在首次建索引读存量时停住，等测试放行：用来在 H_start 与 H_done 之间插入写入。
type blockingRangeReader struct {
	inner   viewservice.TimeSeriesRangeReader
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (r *blockingRangeReader) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq, opts ...client.Option) (*pb.ReadTimeSeriesRowsRsp, error) {
	if req.GetLatestPerSeries() > 0 {
		r.once.Do(func() {
			close(r.entered)
			<-r.release
		})
	}
	return r.inner.ReadTimeSeriesRows(ctx, req, opts...)
}

// received 报告 within 之内有没有收到就绪事件。
func (r *readyWaiter) received(within time.Duration) bool {
	select {
	case <-r.ch:
		return true
	case <-time.After(within):
		return false
	}
}

// 建索引期间写入的周期完成：行照常可查，建成后不补发就绪事件；建成之后的周期照常发布。
func TestFollowSkipsReadyDuringInitialBuild(t *testing.T) {
	ctx := context.Background()
	factor := readyBenchDataset{
		datasetID: "bench_factor_build", viewID: "bench_factor_build_view", subjects: []string{"BTC-USDT", "ETH-USDT"},
		columns: []readyBenchColumn{{"factor_000", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}},
	}
	blocking := &blockingRangeReader{entered: make(chan struct{}), release: make(chan struct{})}
	env := newFollowEnv(t, []readyBenchDataset{factor}, followEnvOptions{rangeReader: func(inner viewservice.TimeSeriesRangeReader) viewservice.TimeSeriesRangeReader {
		blocking.inner = inner
		return blocking
	}})
	select {
	case <-blocking.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("30 秒内没有开始建索引")
	}
	writePeriod := func(period time.Time, round int) {
		rows := make([]*pb.RowFieldUpsert, len(factor.subjects))
		for i, subject := range factor.subjects {
			rows[i] = readyBenchRow(factor, subject, period, round)
		}
		writeRowsInBatches(t, ctx, env, factor, rows, fmt.Sprintf("build-%d", period.Unix()))
		reportProducerCompleted(t, ctx, env, factor, period, factor.subjects, fmt.Sprintf("build-cause-%d", period.Unix()))
	}
	during := env.Watcher().Expect(factor.viewID, readyBenchBase.Unix())
	writePeriod(readyBenchBase, 0)
	close(blocking.release)
	state := env.WaitBuilt(t, ctx, factor.viewID)
	env.WaitApplied(t, ctx, factor.viewID)
	requireViewRows(t, ctx, env, factor.viewID, readyBenchBase, readyBenchBase.Add(readyBenchStep), uint64(len(factor.subjects)))
	if during.received(500 * time.Millisecond) {
		t.Fatal("建索引期间的周期完成不应在建成后发布就绪事件")
	}
	if state.GetGeneration() == 0 {
		t.Fatalf("建成序号应记下建成时的日志头部：%v", state)
	}

	after := readyBenchBase.Add(readyBenchStep)
	waiter := env.Watcher().Expect(factor.viewID, after.Unix())
	writePeriod(after, 1)
	waiter.Wait(t, ctx)
	requireViewRows(t, ctx, env, factor.viewID, after, after.Add(readyBenchStep), uint64(len(factor.subjects)))
}

// View 停机期间的日志被清理（相当于停机超过保留期）：重启后断档，索引按原契约重建，停机期间的写入照样可查，之后照常跟随。
func TestFollowRebuildsAfterGap(t *testing.T) {
	ctx := context.Background()
	kline := readyBenchDataset{datasetID: "bench_kline_gap", viewID: "bench_kline_gap_view", subjects: []string{"BTC-USDT", "ETH-USDT"}, columns: readyBenchKlineColumns}
	env := newFollowEnv(t, []readyBenchDataset{kline}, followEnvOptions{})
	env.WaitBuilt(t, ctx, kline.viewID)
	write := func(period time.Time, round int) {
		rows := make([]*pb.RowFieldUpsert, len(kline.subjects))
		for i, subject := range kline.subjects {
			rows[i] = readyBenchRow(kline, subject, period, round)
		}
		writeRowsInBatches(t, ctx, env, kline, rows, "")
	}
	write(readyBenchBase, 0)
	env.WaitApplied(t, ctx, kline.viewID)
	before := env.WaitBuilt(t, ctx, kline.viewID)

	env.stopFollowing()
	stopped := readyBenchBase.Add(readyBenchStep)
	write(stopped, 1)
	if _, err := env.store.CleanupChangeLog(ctx, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var err error
	if env.stopFollowing, err = env.view.StartFollowing(ctx, env.follow); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		state := env.WaitBuilt(t, ctx, kline.viewID)
		if state.GetGeneration() != before.GetGeneration() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("30 秒内没有重建：%v", state)
		}
		time.Sleep(20 * time.Millisecond)
	}
	requireViewRows(t, ctx, env, kline.viewID, readyBenchBase, stopped.Add(readyBenchStep), uint64(2*len(kline.subjects)))
	next := stopped.Add(readyBenchStep)
	write(next, 2)
	env.WaitApplied(t, ctx, kline.viewID)
	requireViewRows(t, ctx, env, kline.viewID, next, next.Add(readyBenchStep), uint64(len(kline.subjects)))
}
