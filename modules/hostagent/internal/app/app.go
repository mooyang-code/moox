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
	"github.com/mooyang-code/moox/modules/hostagent/internal/eventpublisher"
	"github.com/mooyang-code/moox/modules/hostagent/internal/identity"
	hostagentpb "github.com/mooyang-code/moox/modules/hostagent/proto/hostagentgen"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"google.golang.org/protobuf/proto"
)

type Agent struct {
	cfg       *config.Config
	id        identity.File
	collector snapshotCollector
	// connectMu 串行化建连（可能阻塞到超时）；stateMu 只保护 publisher 指针，持锁期间不做网络操作，
	// 健康检查只读 stateMu，EventBus 建连停滞不会拖慢 /healthz。
	connectMu                              sync.Mutex
	stateMu                                sync.Mutex
	publisher                              eventpublisher.Publisher
	newPublisher                           func(context.Context) (eventpublisher.Publisher, error)
	hostname, bootID, version              string
	latestMu                               sync.RWMutex
	latest                                 *hostmetricpb.HostSnapshot
	lastCollect, lastPublish               time.Time
	lastErr                                string
	// collectErr 只记最近一次采集的错误，发布错误不会写入这里；/readyz 只看它（发布状态另见 lastErr 与 EventBus 连接）。
	collectErr                             string
	collected, published, dropped, skipped atomic.Uint64
	running                                atomic.Bool
}

type snapshotCollector interface {
	Collect(context.Context) (*hostmetricpb.HostSnapshot, []*hostmetricpb.CollectorStatus, error)
}

func New(ctx context.Context, cfg *config.Config, version string) (*Agent, error) {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return nil, fmt.Errorf("moox-host-agent supports linux amd64/arm64 only")
	}
	if cfg == nil {
		return nil, fmt.Errorf("hostagent config is nil")
	}
	id, err := identity.LoadOrCreate(cfg.IdentityPath)
	if err != nil {
		return nil, err
	}
	hostname, _ := os.Hostname()
	hostname = resolveHostName(hostname, cfg.HostName)
	boot := ""
	if b, readErr := os.ReadFile("/proc/sys/kernel/random/boot_id"); readErr == nil {
		boot = strings.TrimSpace(string(b))
	}
	return &Agent{cfg: cfg, id: id, collector: collector.New(), hostname: hostname, bootID: boot, version: version}, nil
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
		return &hostagentpb.RunOnceRsp{PublishError: "collection already running"}, fmt.Errorf("collection already running")
	}
	defer a.running.Store(false)
	return a.runOnce(ctx)
}

func (a *Agent) runOnce(ctx context.Context) (*hostagentpb.RunOnceRsp, error) {
	if a == nil {
		return nil, fmt.Errorf("agent is nil")
	}
	snapshot, _, err := a.collector.Collect(ctx)
	a.collected.Add(1)
	if err != nil {
		a.recordCollectError(err)
		a.dropped.Add(1)
		return &hostagentpb.RunOnceRsp{PublishError: err.Error()}, err
	}
	occurredAt := time.Now().UTC()
	a.latestMu.Lock()
	a.latest = proto.Clone(snapshot).(*hostmetricpb.HostSnapshot)
	a.lastCollect = occurredAt
	a.collectErr = ""
	a.latestMu.Unlock()
	msgID, err := uuid.NewV7()
	if err != nil {
		a.dropped.Add(1)
		return nil, err
	}
	publisher, err := a.eventPublisher(ctx)
	if err != nil {
		a.dropped.Add(1)
		a.recordError(err)
		return &hostagentpb.RunOnceRsp{MessageId: msgID.String(), PublishError: err.Error(), Snapshot: snapshot}, err
	}
	err = publisher.PublishHostMetric(ctx, msgID.String(), &hostmetricpb.HostMetric{HostId: a.cfg.HostID, AgentId: a.id.AgentID, Hostname: a.hostname, BootId: a.bootID, AgentVersion: a.version, Snapshot: snapshot}, occurredAt)
	if err != nil {
		a.dropped.Add(1)
		a.recordError(err)
		return &hostagentpb.RunOnceRsp{MessageId: msgID.String(), PublishError: err.Error(), Snapshot: snapshot}, err
	}
	publishedAt := time.Now().UTC()
	a.published.Add(1)
	a.latestMu.Lock()
	a.lastPublish = publishedAt
	a.lastErr = ""
	a.latestMu.Unlock()
	return &hostagentpb.RunOnceRsp{MessageId: msgID.String(), Published: true, Snapshot: snapshot}, nil
}

func (a *Agent) eventPublisher(ctx context.Context) (eventpublisher.Publisher, error) {
	a.connectMu.Lock()
	defer a.connectMu.Unlock()
	a.stateMu.Lock()
	current := a.publisher
	a.stateMu.Unlock()
	if current != nil && current.Ready() {
		return current, nil
	}
	if current != nil {
		a.stateMu.Lock()
		a.publisher = nil
		a.stateMu.Unlock()
		_ = current.Close()
	}
	factory := a.newPublisher
	if factory == nil {
		factory = func(ctx context.Context) (eventpublisher.Publisher, error) {
			return eventpublisher.New(ctx, a.cfg.EventBusConfig, a.id.AgentID)
		}
	}
	publisher, err := factory(ctx)
	if err != nil {
		return nil, err
	}
	a.stateMu.Lock()
	a.publisher = publisher
	a.stateMu.Unlock()
	return publisher, nil
}

// recordCollectError 同时记入采集错误（决定 /readyz）和 lastErr（状态详情）。
func (a *Agent) recordCollectError(err error) {
	a.latestMu.Lock()
	a.collectErr = truncate(err.Error(), 512)
	a.lastErr = a.collectErr
	a.latestMu.Unlock()
}

// collectionState 返回采集是否正常：至少成功采集过一次，并且最近一次采集没有出错。
// 就绪不依赖 EventBus：EventBus 不可达只影响发布，不能让组件的启动流程反复杀掉它。
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
func (a *Agent) Close() error {
	a.stateMu.Lock()
	publisher := a.publisher
	a.publisher = nil
	a.stateMu.Unlock()
	if publisher == nil {
		return nil
	}
	return publisher.Close()
}

func (a *Agent) publisherReady() bool {
	a.stateMu.Lock()
	publisher := a.publisher
	a.stateMu.Unlock()
	return publisher != nil && publisher.Ready()
}

func (a *Agent) GetStatus(context.Context, *hostagentpb.GetStatusReq) (*hostagentpb.GetStatusRsp, error) {
	a.latestMu.RLock()
	defer a.latestMu.RUnlock()
	rsp := &hostagentpb.GetStatusRsp{AgentId: a.id.AgentID, Version: a.version, Hostname: a.hostname, BootId: a.bootID, LastError: a.lastErr, Collected: a.collected.Load(), Published: a.published.Load(), Dropped: a.dropped.Load(), Skipped: a.skipped.Load(), EventbusConnected: a.publisherReady()}
	if !a.lastCollect.IsZero() {
		rsp.LastCollectAt = a.lastCollect.Format(time.RFC3339Nano)
	}
	if !a.lastPublish.IsZero() {
		rsp.LastPublishAt = a.lastPublish.Format(time.RFC3339Nano)
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
