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
	"github.com/mooyang-code/moox/modules/hostagent/internal/identity"
	hostagentpb "github.com/mooyang-code/moox/modules/hostagent/proto/hostagentgen"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const hostTopic = "moox.metrics.host.reported.v1"

type Agent struct {
	cfg                                    *config.Config
	id                                     identity.File
	collector                              snapshotCollector
	clientMu                               sync.Mutex
	client                                 *jetstream.Client
	hostname, bootID, version              string
	latestMu                               sync.RWMutex
	latest                                 *hostmetricpb.HostSnapshot
	lastCollect, lastPublish               time.Time
	lastErr                                string
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

func (a *Agent) Run(ctx context.Context) {
	if a == nil || ctx == nil {
		return
	}
	ticker := time.NewTicker(a.cfg.Interval)
	defer ticker.Stop()
	_, _ = a.runOnceGuarded(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			go func() { _, _ = a.runOnceGuarded(ctx) }()
		}
	}
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
	now := time.Now().UTC()
	snapshot, _, err := a.collector.Collect(ctx)
	a.collected.Add(1)
	if err != nil {
		a.recordError(err)
		a.dropped.Add(1)
		return &hostagentpb.RunOnceRsp{PublishError: err.Error()}, err
	}
	a.latestMu.Lock()
	a.latest = proto.Clone(snapshot).(*hostmetricpb.HostSnapshot)
	a.lastCollect = now
	a.latestMu.Unlock()
	msgID, err := uuid.NewV7()
	if err != nil {
		a.dropped.Add(1)
		return nil, err
	}
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(&hostmetricpb.HostMetric{Snapshot: snapshot})
	if err != nil {
		a.dropped.Add(1)
		return nil, err
	}
	publishedAt := time.Now().UTC()
	msg := &messagepb.MooxMessage{ProtocolVersion: 1, MessageId: msgID.String(), Topic: hostTopic, Kind: messagepb.MessageKind_MESSAGE_KIND_SNAPSHOT, Producer: &messagepb.Producer{ServiceName: "moox-host-agent", InstanceId: a.id.AgentID, NodeId: a.hostname, BootId: a.bootID, Version: a.version}, SpaceId: "moox_system", Sequence: 0, OccurredAt: timestamppb.New(now), PublishedAt: timestamppb.New(publishedAt), ContentType: "application/x-protobuf; message=trpc.moox.hostagent.HostMetric", Payload: payload}
	client, err := a.eventbus(ctx)
	if err != nil {
		a.dropped.Add(1)
		a.recordError(err)
		return &hostagentpb.RunOnceRsp{MessageId: msg.GetMessageId(), PublishError: err.Error(), Snapshot: snapshot}, err
	}
	_, err = client.Publish(ctx, msg)
	if err != nil {
		a.dropped.Add(1)
		a.recordError(err)
		return &hostagentpb.RunOnceRsp{MessageId: msg.GetMessageId(), PublishError: err.Error(), Snapshot: snapshot}, err
	}
	a.published.Add(1)
	a.latestMu.Lock()
	a.lastPublish = publishedAt
	a.lastErr = ""
	a.latestMu.Unlock()
	return &hostagentpb.RunOnceRsp{MessageId: msg.GetMessageId(), Published: true, Snapshot: snapshot}, nil
}

func (a *Agent) eventbus(ctx context.Context) (*jetstream.Client, error) {
	a.clientMu.Lock()
	defer a.clientMu.Unlock()
	if a.client != nil {
		return a.client, nil
	}
	ecfg, err := config.LoadEventBus(a.cfg.EventBusConfig)
	if err != nil {
		return nil, err
	}
	client, err := jetstream.Connect(ctx, jetstream.Config{URLs: ecfg.URLs, Name: "moox-host-agent-" + a.id.AgentID, Username: ecfg.Username, Password: ecfg.EventBusToken, TLSCAFile: ecfg.CAFile, ReconnectBufferBytes: 0, ConnectTimeout: 5 * time.Second, MaxReconnects: -1})
	if err != nil {
		return nil, err
	}
	a.client = client
	return client, nil
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
	a.clientMu.Lock()
	defer a.clientMu.Unlock()
	if a.client == nil {
		return nil
	}
	err := a.client.Close()
	a.client = nil
	return err
}

func (a *Agent) GetStatus(context.Context, *hostagentpb.GetStatusReq) (*hostagentpb.GetStatusRsp, error) {
	a.latestMu.RLock()
	defer a.latestMu.RUnlock()
	rsp := &hostagentpb.GetStatusRsp{AgentId: a.id.AgentID, Version: a.version, Hostname: a.hostname, BootId: a.bootID, LastError: a.lastErr, Collected: a.collected.Load(), Published: a.published.Load(), Dropped: a.dropped.Load(), Skipped: a.skipped.Load(), EventbusConnected: a.client != nil}
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
