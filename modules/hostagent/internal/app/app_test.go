package app

import (
	"context"
	"errors"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/hostagent/internal/collector"
	"github.com/mooyang-code/moox/modules/hostagent/internal/config"
	hostagentpb "github.com/mooyang-code/moox/modules/hostagent/proto/hostagentgen"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

// fakeReporter 记下每次随拉取主机策略带去的快照。
type fakeReporter struct {
	mu      sync.Mutex
	reports []*monitorpb.HostReport
	err     error
}

func (f *fakeReporter) report(_ context.Context, report *monitorpb.HostReport) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, report)
	return f.err
}

func (f *fakeReporter) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeReporter) last() *monitorpb.HostReport {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reports) == 0 {
		return nil
	}
	return f.reports[len(f.reports)-1]
}

func testAgent(t *testing.T) *Agent {
	t.Helper()
	return &Agent{
		cfg:       &config.Config{HostID: "control"},
		collector: collector.New(),
		report:    (&fakeReporter{}).report,
		hostname:  "test-host",
		bootID:    "boot-id",
		version:   "test-version",
	}
}

func testSnapshot() *hostmetricpb.HostSnapshot {
	return &hostmetricpb.HostSnapshot{
		Cpu:    &hostmetricpb.CpuMetric{LogicalCores: 2},
		Memory: &hostmetricpb.MemoryMetric{TotalBytes: 1024, AvailableBytes: 512},
	}
}

type fakeSnapshotCollector struct {
	snapshot *hostmetricpb.HostSnapshot
	err      error
}

func (f fakeSnapshotCollector) Collect(context.Context) (*hostmetricpb.HostSnapshot, []*hostmetricpb.CollectorStatus, error) {
	return f.snapshot, nil, f.err
}

func TestNew_NilConfig_ShouldReturnError(t *testing.T) {
	_, err := New(context.Background(), nil, "dev", (&fakeReporter{}).report)
	assert.Error(t, err)
}

func TestResolveHostName_ShouldPreferConfiguredName(t *testing.T) {
	assert.Equal(t, "腾讯云-香港", resolveHostName("VM-32-13-ubuntu", "腾讯云-香港"))
	assert.Equal(t, "VM-32-13-ubuntu", resolveHostName("VM-32-13-ubuntu", ""))
}

func TestNew_UnsupportedPlatform_ShouldReturnError(t *testing.T) {
	if runtime.GOOS == "linux" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64") {
		t.Skip("linux amd64/arm64 supports New")
	}
	_, err := New(context.Background(), &config.Config{HostID: "control"}, "dev", (&fakeReporter{}).report)
	assert.Error(t, err)
}

func TestAgent_GetStatus_ShouldExposeCountersAndLatest(t *testing.T) {
	a := testAgent(t)
	now := time.Now().UTC()
	a.latest = testSnapshot()
	a.lastCollect = now
	a.lastReport = now
	a.collected.Store(3)
	a.reported.Store(2)
	a.dropped.Store(1)
	a.skipped.Store(4)

	rsp, err := a.GetStatus(context.Background(), &hostagentpb.GetStatusReq{})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, a.cfg.HostID, rsp.GetHostId())
	assert.Equal(t, a.version, rsp.GetVersion())
	assert.Equal(t, a.hostname, rsp.GetHostname())
	assert.Equal(t, a.bootID, rsp.GetBootId())
	assert.Equal(t, uint64(3), rsp.GetCollected())
	assert.Equal(t, uint64(2), rsp.GetReported())
	assert.Equal(t, uint64(1), rsp.GetDropped())
	assert.Equal(t, uint64(4), rsp.GetSkipped())
	assert.NotNil(t, rsp.GetLatest())
	assert.NotEmpty(t, rsp.GetLastCollectAt())
	assert.NotEmpty(t, rsp.GetLastReportAt())
}

func TestTruncate_ShouldCapLongStrings(t *testing.T) {
	assert.Equal(t, "abc", truncate("abc", 5))
	assert.Equal(t, "hello", truncate("hello-world", 5))
}

func TestAgent_RecordError_ShouldPersistTruncatedMessage(t *testing.T) {
	a := testAgent(t)
	a.recordError(errors.New("x" + string(make([]byte, 600))))
	a.latestMu.RLock()
	defer a.latestMu.RUnlock()
	assert.Len(t, a.lastErr, 512)
}

// 采集失败也照常拉主机策略（不带快照）：主机策略与“控制机或 Monitor 不可达”的判断不能跟着采集一起停。
func TestAgent_RunOnce_CollectError_StillPullsPolicy(t *testing.T) {
	a := testAgent(t)
	reporter := &fakeReporter{}
	a.report = reporter.report
	a.collector = fakeSnapshotCollector{err: errors.New("collect failed")}
	rsp, err := a.RunOnce(context.Background(), &hostagentpb.RunOnceReq{})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
	assert.Contains(t, rsp.GetRetInfo().GetMsg(), "collect failed")
	assert.NotEmpty(t, rsp.GetReportError())
	assert.Equal(t, uint64(1), a.collected.Load())
	assert.Equal(t, uint64(1), a.dropped.Load())
	require.Len(t, reporter.reports, 1)
	assert.Nil(t, reporter.reports[0])
}

func TestAgent_RunOnceGuarded_ConcurrentCall_ShouldSkip(t *testing.T) {
	a := testAgent(t)
	a.running.Store(true)
	rsp, err := a.RunOnce(context.Background(), &hostagentpb.RunOnceReq{})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_CONFLICT, rsp.GetRetInfo().GetCode())
	assert.Equal(t, "collection already running", rsp.GetReportError())
	assert.Equal(t, uint64(1), a.skipped.Load())
}

func TestAgent_RunOnce_ReportSuccess_ShouldUpdateCounters(t *testing.T) {
	a := testAgent(t)
	reporter := &fakeReporter{}
	a.report = reporter.report
	a.collector = fakeSnapshotCollector{snapshot: testSnapshot()}

	rsp, err := a.RunOnce(context.Background(), &hostagentpb.RunOnceReq{})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.True(t, rsp.GetReported())
	assert.NotEmpty(t, rsp.GetReportId())
	assert.NotNil(t, rsp.GetSnapshot())
	assert.Equal(t, uint64(1), a.reported.Load())
	assert.Empty(t, a.lastErr)
	report := reporter.last()
	require.Equal(t, rsp.GetReportId(), report.GetReportId())
	require.Equal(t, "control", report.GetMetric().GetHostId())
	require.Equal(t, "test-host", report.GetMetric().GetHostname())
	require.Equal(t, a.lastCollect.UnixMilli(), report.GetCollectedAt())
}

func TestAgent_RunOnce_UsesCollectionCompletionAsCollectedAt(t *testing.T) {
	a := testAgent(t)
	reporter := &fakeReporter{}
	a.report = reporter.report
	var completedAt time.Time
	a.collector = completionSnapshotCollector{snapshot: testSnapshot(), completedAt: &completedAt}

	_, err := a.RunOnce(context.Background(), &hostagentpb.RunOnceReq{})
	require.NoError(t, err)
	// 上报的采集时刻是毫秒，比较时把完成时刻也截到毫秒。
	collectedAt := time.UnixMilli(reporter.last().GetCollectedAt())
	assert.False(t, collectedAt.Before(completedAt.Truncate(time.Millisecond)), "collected_at=%s completion=%s", collectedAt, completedAt)
}

func TestAgent_RunOnce_ReportError_ShouldRecordFailure(t *testing.T) {
	a := testAgent(t)
	a.report = (&fakeReporter{err: errors.New("monitor unreachable")}).report
	a.collector = fakeSnapshotCollector{snapshot: testSnapshot()}

	rsp, err := a.RunOnce(context.Background(), &hostagentpb.RunOnceReq{})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
	assert.Contains(t, rsp.GetRetInfo().GetMsg(), "monitor unreachable")
	assert.Contains(t, rsp.GetReportError(), "monitor unreachable")
	assert.NotNil(t, rsp.GetSnapshot())
	assert.Equal(t, uint64(1), a.dropped.Load())
	assert.Contains(t, a.lastErr, "monitor unreachable")
}

func TestAgent_RunOnce_NilAgent_ShouldReturnError(t *testing.T) {
	var a *Agent
	_, err := a.runOnce(context.Background())
	assert.Error(t, err)
}

func TestAgent_RunOnceGuarded_ReleasesRunningFlag(t *testing.T) {
	a := testAgent(t)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = a.RunOnce(context.Background(), &hostagentpb.RunOnceReq{})
	}()
	wg.Wait()
	assert.False(t, a.running.Load())
}

type completionSnapshotCollector struct {
	snapshot    *hostmetricpb.HostSnapshot
	completedAt *time.Time
}

func (c completionSnapshotCollector) Collect(context.Context) (*hostmetricpb.HostSnapshot, []*hostmetricpb.CollectorStatus, error) {
	time.Sleep(time.Millisecond)
	*c.completedAt = time.Now().UTC()
	return c.snapshot, nil, nil
}

func TestHostAgentNativeRPCPreservesSnapshot(t *testing.T) {
	agent := testAgent(t)
	agent.collector = fakeSnapshotCollector{snapshot: testSnapshot()}
	agent.report = (&fakeReporter{err: errors.New("monitor unreachable")}).report
	agent.latest = testSnapshot()
	agent.lastCollect = time.Now().UTC()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	service := server.New(server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithTransport(transport.NewServerTransport()))
	hostagentpb.RegisterHostAgentMgrService(service, agent)
	done := make(chan error, 1)
	go func() { done <- service.Serve() }()
	t.Cleanup(func() {
		_ = service.Close(nil)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("HostAgent native listener did not stop")
		}
	})
	for _, serialization := range []int{codec.SerializationTypePB, codec.SerializationTypeJSON} {
		proxy := hostagentpb.NewHostAgentMgrClientProxy(client.WithTarget("ip://"+listener.Addr().String()), client.WithProtocol("trpc"), client.WithNetwork("tcp"), client.WithSerializationType(serialization), client.WithTimeout(3*time.Second), client.WithTransport(transport.NewClientTransport()), client.WithDisableConnectionPool())
		status, err := proxy.GetStatus(t.Context(), &hostagentpb.GetStatusReq{})
		require.NoError(t, err)
		require.Equal(t, commonpb.ErrorCode_SUCCESS, status.GetRetInfo().GetCode())
		require.Equal(t, agent.cfg.HostID, status.GetHostId())
		require.Equal(t, agent.hostname, status.GetHostname())
		require.Equal(t, agent.latest.GetCpu().GetLogicalCores(), status.GetLatest().GetCpu().GetLogicalCores())

		// 上报失败时 RunOnce 通过 RetInfo 返回错误且回 nil Go error，响应体中的快照与错误信息不会被 tRPC 丢弃。
		runRsp, err := proxy.RunOnce(t.Context(), &hostagentpb.RunOnceReq{})
		require.NoError(t, err)
		require.Equal(t, commonpb.ErrorCode_INNER_ERR, runRsp.GetRetInfo().GetCode())
		require.Contains(t, runRsp.GetReportError(), "monitor unreachable")
		require.NotEmpty(t, runRsp.GetReportId())
		require.Equal(t, uint32(2), runRsp.GetSnapshot().GetCpu().GetLogicalCores())
	}
}
