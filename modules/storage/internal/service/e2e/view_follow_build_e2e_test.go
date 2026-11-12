//go:build cgo

package e2e

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	viewservice "github.com/mooyang-code/moox/modules/storage/internal/service/view"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
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

// 空闲数据集：别的数据集持续写入，空闲 View 的游标照样前进并落盘；日志清理（相当于 8 天没有写入）后重启不断档、不重建，之后的写入照常应用。
func TestIdleViewSurvivesChangeLogCleanup(t *testing.T) {
	ctx := context.Background()
	idle := readyBenchDataset{datasetID: "bench_idle_ds", viewID: "bench_idle_ds_view", subjects: []string{"BTC-USDT"}, columns: readyBenchKlineColumns}
	busy := readyBenchDataset{datasetID: "bench_busy_ds", viewID: "bench_busy_ds_view", subjects: []string{"ETH-USDT"}, columns: readyBenchKlineColumns}
	env := newFollowEnv(t, []readyBenchDataset{idle, busy}, followEnvOptions{})
	before := env.WaitBuilt(t, ctx, idle.viewID)
	env.WaitBuilt(t, ctx, busy.viewID)
	env.stopFollowing()
	env.follow.Wait, env.follow.PersistInterval = time.Second, 10*time.Millisecond
	var err error
	if env.stopFollowing, err = env.view.StartFollowing(ctx, env.follow); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		row := readyBenchRow(busy, busy.subjects[0], readyBenchBase.Add(time.Duration(i)*readyBenchStep), i)
		writeRowsInBatches(t, ctx, env, busy, []*pb.RowFieldUpsert{row}, "")
	}
	head, err := env.store.ChangeCursor()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for env.WaitBuilt(t, ctx, idle.viewID).GetAppliedCursor().GetSequence() < head.GetSequence() {
		if time.Now().After(deadline) {
			t.Fatal("空闲 View 的游标没有扫过别的数据集的条目")
		}
		time.Sleep(50 * time.Millisecond)
	}
	env.stopFollowing() // 停止时落盘游标
	if _, err := env.store.CleanupChangeLog(ctx, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if env.stopFollowing, err = env.view.StartFollowing(ctx, env.follow); err != nil {
		t.Fatal(err)
	}
	row := readyBenchRow(idle, idle.subjects[0], readyBenchBase, 0)
	writeRowsInBatches(t, ctx, env, idle, []*pb.RowFieldUpsert{row}, "")
	env.WaitApplied(t, ctx, idle.viewID)
	requireViewRows(t, ctx, env, idle.viewID, readyBenchBase, readyBenchBase.Add(readyBenchStep), 1)
	if after := env.WaitBuilt(t, ctx, idle.viewID); after.GetGeneration() != before.GetGeneration() {
		t.Fatalf("空闲 View 不应重建：建成序号 %d → %d", before.GetGeneration(), after.GetGeneration())
	}
}

// controlledRangeReader 控制建索引读存量（latest_per_series）：可以让接下来几次失败，或停住直到放行（调用方取消时返回）。
type controlledRangeReader struct {
	inner   viewservice.TimeSeriesRangeReader
	mu      sync.Mutex
	fail    int
	failed  int
	block   chan struct{}
	entered chan struct{}
}

func (r *controlledRangeReader) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq, opts ...client.Option) (*pb.ReadTimeSeriesRowsRsp, error) {
	if req.GetLatestPerSeries() > 0 {
		r.mu.Lock()
		if r.fail > 0 {
			r.fail--
			r.failed++
			r.mu.Unlock()
			return nil, errors.New("模拟读取存量失败")
		}
		block, entered := r.block, r.entered
		r.entered = nil
		r.mu.Unlock()
		if block != nil {
			if entered != nil {
				close(entered)
			}
			select {
			case <-block:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	return r.inner.ReadTimeSeriesRows(ctx, req, opts...)
}

func (r *controlledRangeReader) set(fn func(r *controlledRangeReader)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fn(r)
}

func (m *followEnvMetadata) setViews(views []*pb.View) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.views = views
}

func (m *followEnvMetadata) snapshotViews() []*pb.View {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*pb.View, len(m.views))
	for i, view := range m.views {
		out[i] = proto.Clone(view).(*pb.View)
	}
	return out
}

// newGapEnv 建好一个 K 线 View，写一期后停掉跟随、再写一期并清掉日志，制造断档；返回环境、读取控制与断档前的建成序号。
func newGapEnv(t *testing.T) (*followEnv, *controlledRangeReader, readyBenchDataset, uint64) {
	t.Helper()
	ctx := context.Background()
	kline := readyBenchDataset{datasetID: "bench_kline_gap", viewID: "bench_kline_gap_view", subjects: []string{"BTC-USDT", "ETH-USDT"}, columns: readyBenchKlineColumns}
	reader := &controlledRangeReader{}
	env := newFollowEnv(t, []readyBenchDataset{kline}, followEnvOptions{rangeReader: func(inner viewservice.TimeSeriesRangeReader) viewservice.TimeSeriesRangeReader {
		reader.inner = inner
		return reader
	}})
	env.WaitBuilt(t, ctx, kline.viewID)
	writeGapPeriod(t, env, kline, readyBenchBase, 0)
	env.WaitApplied(t, ctx, kline.viewID)
	before := env.WaitBuilt(t, ctx, kline.viewID).GetGeneration()
	env.stopFollowing()
	writeGapPeriod(t, env, kline, readyBenchBase.Add(readyBenchStep), 1)
	if _, err := env.store.CleanupChangeLog(ctx, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	return env, reader, kline, before
}

func writeGapPeriod(t *testing.T, env *followEnv, dataset readyBenchDataset, period time.Time, round int) {
	t.Helper()
	rows := make([]*pb.RowFieldUpsert, len(dataset.subjects))
	for i, subject := range dataset.subjects {
		rows[i] = readyBenchRow(dataset, subject, period, round)
	}
	writeRowsInBatches(t, context.Background(), env, dataset, rows, "")
}

// waitRebuilt 等 View 重建好（建成序号变化）并确认断档前后两期的行都在。
func waitRebuilt(t *testing.T, env *followEnv, dataset readyBenchDataset, before uint64) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(30 * time.Second)
	for {
		rsp, err := env.view.GetViewState(ctx, &pb.GetViewStateReq{AuthInfo: followEnvViewAuth(), SpaceId: readyBenchSpace, ViewId: dataset.viewID})
		if err == nil && rsp.GetIndexId() != "" && rsp.GetGeneration() != before {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("30 秒内没有重建：%v err=%v", rsp, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	requireViewRows(t, ctx, env, dataset.viewID, readyBenchBase, readyBenchBase.Add(2*readyBenchStep), uint64(2*len(dataset.subjects)))
}

// 重建失败后 View 不可读；进程重启（跟随器重新启动）后接着重建，成功后恢复可读并照常跟随。
func TestFollowRetriesFailedRebuild(t *testing.T) {
	ctx := context.Background()
	env, reader, kline, before := newGapEnv(t)
	reader.set(func(r *controlledRangeReader) { r.fail = 1000 })
	var err error
	if env.stopFollowing, err = env.view.StartFollowing(ctx, env.follow); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		reader.mu.Lock()
		failed := reader.failed
		reader.mu.Unlock()
		if failed >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("重建没有重试")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if rsp, _ := env.view.GetViewState(ctx, &pb.GetViewStateReq{AuthInfo: followEnvViewAuth(), SpaceId: readyBenchSpace, ViewId: kline.viewID}); rsp.GetIndexId() != "" {
		t.Fatalf("重建失败期间 View 不应可读：%v", rsp)
	}
	// 模拟进程重启：跟随器重新启动时，索引里没有进度、运行时仍是待重建。
	env.stopFollowing()
	reader.set(func(r *controlledRangeReader) { r.fail = 0 })
	if env.stopFollowing, err = env.view.StartFollowing(ctx, env.follow); err != nil {
		t.Fatal(err)
	}
	waitRebuilt(t, env, kline, before)
	next := readyBenchBase.Add(2 * readyBenchStep)
	writeGapPeriod(t, env, kline, next, 2)
	env.WaitApplied(t, ctx, kline.viewID)
	requireViewRows(t, ctx, env, kline.viewID, next, next.Add(readyBenchStep), uint64(len(kline.subjects)))
}

// 重建中途 View 被停用（跟随器被停掉），再启用后接着重建。
func TestFollowRebuildResumesAfterViewReenabled(t *testing.T) {
	ctx := context.Background()
	env, reader, kline, before := newGapEnv(t)
	entered := make(chan struct{})
	reader.set(func(r *controlledRangeReader) { r.block, r.entered = make(chan struct{}), entered })
	var err error
	if env.stopFollowing, err = env.view.StartFollowing(ctx, env.follow); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("没有开始重建")
	}
	views := env.metadata.snapshotViews()
	env.metadata.setViews(nil) // 停用：维护流程停掉它的跟随器，重建中途取消
	time.Sleep(500 * time.Millisecond)
	reader.set(func(r *controlledRangeReader) { r.block = nil })
	env.metadata.setViews(views) // 再启用
	waitRebuilt(t, env, kline, before)
}
