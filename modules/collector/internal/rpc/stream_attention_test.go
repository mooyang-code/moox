package rpc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/attention"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// 心跳中断按库里的任务与心跳记录判断：有推送数据集且心跳超时为 1，没有推送数据集为 0，启动未满一个超时为 0；
// 新建一个判断对象（等于 Collector 重启）读库照样能判；同一次中断持续多久 detail 都不变。
func TestStreamHeartbeatAttention(t *testing.T) {
	_, db := newReregisterFixture(t)
	ctx := context.Background()
	attn := attention.New(prometheus.NewRegistry())
	now := time.Now().UTC()
	build := func(started time.Time, at time.Time) *StreamHeartbeatAttention {
		h := NewStreamHeartbeatAttention(db, 3*time.Minute, attn)
		h.startedAt, h.now = started, func() time.Time { return at }
		return h
	}
	refresh := func(h *StreamHeartbeatAttention) string {
		require.NoError(t, h.Refresh(ctx, []string{"crypto"}))
		return attn.Active()[attention.KindStreamHeartbeatLost]
	}
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "task_ds1", TaskName: "ds1", DataType: domain.DataTypeKline, Ingest: domain.IngestSCF,
		Enabled: true, TagIDs: []string{"binance_spot"}, CollectParams: `{"frequency":"1m"}`, ResultDatasetID: "ds1"}))
	require.NoError(t, db.StreamWorkers().Heartbeat(ctx, store.StreamWorker{SpaceID: "crypto", InstanceID: "hk-1", Applied: "{}", Status: "{}", HeartbeatAt: now.Add(-181 * time.Second)}))
	require.Empty(t, refresh(build(now.Add(-time.Hour), now)), "没有推送数据集不判断")

	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "task_ds2", TaskName: "ds2", DataType: domain.DataTypeKline, Ingest: domain.IngestStreaming,
		Enabled: true, TagIDs: []string{"binance_spot"}, CollectParams: `{"frequency":"1m"}`, ResultDatasetID: "ds2"}))
	require.Empty(t, refresh(build(now.Add(-time.Minute), now)), "启动未满一个超时不判断")
	detail := refresh(build(now.Add(-time.Hour), now))
	require.True(t, strings.HasPrefix(detail, "crypto 最后心跳 "), detail)
	require.Equal(t, detail, refresh(build(now.Add(-time.Hour), now.Add(10*time.Minute))), "同一次中断 detail 不变")

	require.NoError(t, db.StreamWorkers().Heartbeat(ctx, store.StreamWorker{SpaceID: "crypto", InstanceID: "hk-1", Applied: "{}", Status: "{}", HeartbeatAt: now}))
	require.Empty(t, refresh(build(now.Add(-time.Hour), now)), "心跳恢复后回到 0")
}

// 连续 3 期有失败标的才上报，期数增加 detail 不变；有一期正常即恢复。
func TestStreamPeriodsMissingAttention(t *testing.T) {
	_, db := newReregisterFixture(t)
	ctx := context.Background()
	attn := attention.New(prometheus.NewRegistry())
	stream := NewStreamService(db, StreamSettings{}, nil, nil)
	stream.SetAttention(attn)
	report := func(missing int32) string {
		_, err := stream.ReportStreamPeriods(ctx, &pb.ReportStreamPeriodsReq{SpaceId: "crypto", Periods: []*pb.StreamPeriodStat{
			{Market: "swap", Frequency: "1m", DatasetId: "ds_swap", Expected: 10, Arrived: 10 - missing, Missing: missing, Committed: true},
		}})
		require.NoError(t, err)
		return attn.Active()[attention.KindStreamPeriodsMissing]
	}
	require.Empty(t, report(1))
	require.Empty(t, report(1))
	first := report(1)
	require.Contains(t, first, "crypto/swap/ds_swap 1m")
	require.Equal(t, first, report(2), "期数增加 detail 不变")
	require.Empty(t, report(0))
}
