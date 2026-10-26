package localdata

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/stretchr/testify/require"
)

// 热路径：每个标的新增连续的 1 根，窗口先追加、写盘并行；落完之后窗口和磁盘一致（设计 5.2，决定 25）。
func TestHotPathAppendsWindowAndFlushesToDisk(t *testing.T) {
	fx := newFixture(t, mean3())
	fx.data.cfg.Lookback = func(seriesstore.Source) int { return 10 }
	fx.st.put("A", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	fx.st.put("B", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"A", "B"}))
	fx.now = at(11)

	// 新增一根（第 10 分钟）。
	fx.st.put("A", 100, 10)
	fx.st.put("B", 100, 10)
	hotBefore := fx.data.hotWrites.Load()
	outcome := fx.live(t, at(10), []string{"A", "B"})
	require.Equal(t, "complete", outcome.Status)
	require.Equal(t, int64(2), fx.data.hotWrites.Load()-hotBefore, "两个标的都走热路径")
	require.InDelta(t, (108+109+110)/3.0, fx.result("A", 10).Fields["mean"], 1e-9, "用最近 3 根的均值")

	window := fx.data.st(startupSrc).win.Load()
	for _, subject := range []string{"A", "B"} {
		latest, ok := window.Latest(subject)
		require.True(t, ok)
		require.Equal(t, at(10), latest, "窗口已追加新的一根")
		b, ok, err := fx.data.Store().Bounds(startupSrc, subject)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, at(10), b.Latest, "写盘落完之后磁盘也到了新的一根")
	}
}

// 落盘失败时窗口比磁盘多了几根：下一次操作先从磁盘重载这些标的，丢掉没落盘的行，之后的拉取会重新取回。
func TestSettleFlushRecoversWindowWhenFlushFailed(t *testing.T) {
	fx := newFixture(t, mean3())
	fx.data.cfg.Lookback = func(seriesstore.Source) int { return 10 }
	fx.st.put("A", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"A"}))
	st := fx.data.st(startupSrc)

	// 模拟：窗口追加了磁盘上没有的一根，随后落盘失败。
	fields, _, err := fx.data.Store().Schema(startupSrc)
	require.NoError(t, err)
	extra := seriesstore.NewColumns(fields, 1)
	values := make([]seriesstore.Value, len(fields))
	for i := range values {
		values[i] = seriesstore.Float(999)
	}
	extra.AppendRow(at(10), "", false, values...)
	require.NoError(t, st.window.Append("A", extra))
	latest, _ := st.window.Latest("A")
	require.Equal(t, at(10), latest)

	job := &flushJob{done: make(chan struct{}), subjects: []string{"A"}, err: errors.New("磁盘写满")}
	close(job.done)
	st.flushMu.Lock()
	st.flush = job
	st.flushMu.Unlock()

	fx.data.settleFlush(st)
	latest, _ = st.window.Latest("A")
	require.Equal(t, at(9), latest, "窗口回到磁盘上有的最新一根")
	require.Nil(t, st.awaitFlush(), "记录已清除")
}

// 落盘失败时先原样重试一次：重试成功则本地与已发布的结果一致，窗口不用回退。
func TestSettleFlushRetriesFailedFlush(t *testing.T) {
	fx := newFixture(t, mean3())
	fx.data.cfg.Lookback = func(seriesstore.Source) int { return 10 }
	fx.st.put("A", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"A"}))
	st := fx.data.st(startupSrc)
	fx.st.put("A", 100, 10)
	staged, err := fx.data.syncer.PullStaged(context.Background(), st.spec, []string{"A"}, at(11))
	require.NoError(t, err)
	require.Len(t, staged.Writes, 1)
	require.NoError(t, st.window.Append("A", staged.Writes[0].Rows))

	job := &flushJob{done: make(chan struct{}), subjects: []string{"A"}, err: errors.New("暂时失败"), staged: staged, writes: staged.Writes}
	close(job.done)
	st.flushMu.Lock()
	st.flush = job
	st.flushMu.Unlock()

	fx.data.settleFlush(st)
	b, ok, err := fx.data.Store().Bounds(startupSrc, "A")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, at(10), b.Latest, "重试把没落盘的一根写进了磁盘")
	latest, _ := st.window.Latest("A")
	require.Equal(t, at(10), latest, "窗口保持不变")
}

// 本地数据被导入覆盖之后，不能再用「拉取已反映」跳过变更。
func TestWriteLocalClearsPulledRecords(t *testing.T) {
	fx := newFixture(t, mean3())
	fx.data.cfg.Lookback = func(seriesstore.Source) int { return 10 }
	fx.st.put("A", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"A"}))
	st := fx.data.st(startupSrc)
	fx.st.put("A", 100, 10)
	staged, err := fx.data.syncer.PullStaged(context.Background(), st.spec, []string{"A"}, at(11))
	require.NoError(t, err)
	_, err = staged.Commit(context.Background(), staged.Writes)
	require.NoError(t, err)

	fields, _, err := fx.data.Store().Schema(startupSrc)
	require.NoError(t, err)
	old := seriesstore.NewColumns(fields, 1)
	values := make([]seriesstore.Value, len(fields))
	for i := range values {
		values[i] = seriesstore.Float(1)
	}
	old.AppendRow(at(10), "", false, values...)
	_, err = fx.data.WriteLocal(context.Background(), startupSrc, []seriesstore.SubjectWrite{{Subject: "A", From: at(10), To: at(11), Rows: old}}, seriesstore.WriteOptions{})
	require.NoError(t, err)
	require.False(t, fx.data.syncer.HasPulled(startupSrc, "A"), "WriteLocal 之后拉取记录应已清除")
}
