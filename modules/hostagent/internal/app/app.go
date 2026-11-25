package app

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/modules/hostagent/internal/collector"
	"github.com/mooyang-code/moox/modules/hostagent/internal/config"
	hostagentpb "github.com/mooyang-code/moox/modules/hostagent/proto/hostagentgen"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"google.golang.org/protobuf/proto"
)

// Reporter 把一次采集结果随拉取主机策略那次调用报给 Monitor；report 为空表示这次采集失败，只拉策略。
type Reporter func(context.Context, *monitorpb.HostReport) error

type Agent struct {
	cfg                       *config.Config
	collector                 snapshotCollector
	report                    Reporter
	hostname, bootID, version string
	latestMu                  sync.RWMutex
	latest                    *hostmetricpb.HostSnapshot
	lastCollect, lastReport   time.Time
	lastErr                   string
	// collectErr 只记最近一次采集的错误，上报错误不会写入这里；/readyz 只看它（上报状态另见 lastErr）。
	collectErr                            string
	collected, reported, dropped, skipped atomic.Uint64
	running                               atomic.Bool
}

type snapshotCollector interface {
	Collect(context.Context) (*hostmetricpb.HostSnapshot, []*hostmetricpb.CollectorStatus, error)
}

func New(ctx context.Context, cfg *config.Config, version string, report Reporter) (*Agent, error) {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return nil, fmt.Errorf("moox-host-agent supports linux amd64/arm64 only")
	}
	if cfg == nil || report == nil {
		return nil, fmt.Errorf("hostagent config and reporter are required")
	}
	hostname, _ := os.Hostname()
	hostname = resolveHostName(hostname, cfg.HostName)
	boot := ""
	if b, readErr := os.ReadFile("/proc/sys/kernel/random/boot_id"); readErr == nil {
		boot = strings.TrimSpace(string(b))
	}
	return &Agent{cfg: cfg, collector: collector.New(), report: report, hostname: hostname, bootID: boot, version: version}, nil
}

func resolveHostName(systemName, configuredName string) string {
	if name := strings.TrimSpace(configuredName); name != "" {
		return name
	}
	return systemName
}

func (a *Agent) runOnceGuarded(ctx context.Context) (*hostagentpb.RunOnceRsp, error) {
	if !a.running.CompareAndSwap(false, true) {
		a.skipped.Add(1)
		return &hostagentpb.RunOnceRsp{ReportError: "collection already running"}, fmt.Errorf("collection already running")
	}
	defer a.running.Store(false)
	return a.runOnce(ctx)
}

// runOnce 采集一次，再随拉取主机策略把快照报给 Monitor。采集失败也照常拉策略（不带快照），
// 否则主机策略与“控制机或 Monitor 不可达”的判断会跟着采集一起停掉。
func (a *Agent) runOnce(ctx context.Context) (*hostagentpb.RunOnceRsp, error) {
	if a == nil {
		return nil, fmt.Errorf("agent is nil")
	}
	snapshot, _, collectErr := a.collector.Collect(ctx)
	a.collected.Add(1)
	if collectErr != nil {
		a.recordCollectError(collectErr)
		a.dropped.Add(1)
		if err := a.report(ctx, nil); err != nil {
			a.recordError(err)
		}
		return &hostagentpb.RunOnceRsp{ReportError: collectErr.Error()}, collectErr
	}
	collectedAt := time.Now().UTC()
	a.latestMu.Lock()
	a.latest = proto.Clone(snapshot).(*hostmetricpb.HostSnapshot)
	a.lastCollect = collectedAt
	a.collectErr = ""
	a.latestMu.Unlock()
	reportID, err := uuid.NewV7()
	if err != nil {
		a.dropped.Add(1)
		return nil, err
	}
	report := &monitorpb.HostReport{
		ReportId: reportID.String(), CollectedAt: collectedAt.UnixMilli(),
		Metric: &hostmetricpb.HostMetric{HostId: a.cfg.HostID, Hostname: a.hostname, BootId: a.bootID, AgentVersion: a.version, Snapshot: snapshot},
	}
	if err := a.report(ctx, report); err != nil {
		a.dropped.Add(1)
		a.recordError(err)
		return &hostagentpb.RunOnceRsp{ReportId: reportID.String(), ReportError: err.Error(), Snapshot: snapshot}, err
	}
	a.reported.Add(1)
	a.latestMu.Lock()
	a.lastReport = time.Now().UTC()
	a.lastErr = ""
	a.latestMu.Unlock()
	return &hostagentpb.RunOnceRsp{ReportId: reportID.String(), Reported: true, Snapshot: snapshot}, nil
}

// recordCollectError 同时记入采集错误（决定 /readyz）和 lastErr（状态详情）。
func (a *Agent) recordCollectError(err error) {
	a.latestMu.Lock()
	a.collectErr = truncate(err.Error(), 512)
	a.lastErr = a.collectErr
	a.latestMu.Unlock()
}

// collectionState 返回采集是否正常：至少成功采集过一次，并且最近一次采集没有出错。
// 就绪不依赖上报：Monitor 不可达只影响上报，不能让组件的启动流程反复杀掉它。
func (a *Agent) collectionState() (ready bool, collectErr string) {
	a.latestMu.RLock()
	defer a.latestMu.RUnlock()
	return !a.lastCollect.IsZero() && a.collectErr == "", a.collectErr
}

func (a *Agent) recordError(err error) {
	a.latestMu.Lock()
	a.lastErr = truncate(err.Error(), 512)
	a.latestMu.Unlock()
}
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func (a *Agent) GetStatus(context.Context, *hostagentpb.GetStatusReq) (*hostagentpb.GetStatusRsp, error) {
	a.latestMu.RLock()
	defer a.latestMu.RUnlock()
	rsp := &hostagentpb.GetStatusRsp{HostId: a.cfg.HostID, Version: a.version, Hostname: a.hostname, BootId: a.bootID, LastError: a.lastErr, Collected: a.collected.Load(), Reported: a.reported.Load(), Dropped: a.dropped.Load(), Skipped: a.skipped.Load()}
	if !a.lastCollect.IsZero() {
		rsp.LastCollectAt = a.lastCollect.Format(time.RFC3339Nano)
	}
	if !a.lastReport.IsZero() {
		rsp.LastReportAt = a.lastReport.Format(time.RFC3339Nano)
	}
	if a.latest != nil {
		rsp.Latest = proto.Clone(a.latest).(*hostmetricpb.HostSnapshot)
	}
	return rsp, nil
}
func (a *Agent) GetSnapshot(context.Context, *hostagentpb.GetSnapshotReq) (*hostagentpb.GetSnapshotRsp, error) {
	a.latestMu.RLock()
	defer a.latestMu.RUnlock()
	rsp := &hostagentpb.GetSnapshotRsp{}
	if a.latest != nil {
		rsp.Snapshot = proto.Clone(a.latest).(*hostmetricpb.HostSnapshot)
	}
	if !a.lastCollect.IsZero() {
		rsp.CollectedAt = a.lastCollect.Format(time.RFC3339Nano)
	}
	return rsp, nil
}
func (a *Agent) RunOnce(ctx context.Context, _ *hostagentpb.RunOnceReq) (*hostagentpb.RunOnceRsp, error) {
	return a.runOnceGuarded(ctx)
}
