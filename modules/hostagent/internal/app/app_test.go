package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/modules/hostagent/internal/collector"
	"github.com/mooyang-code/moox/modules/hostagent/internal/config"
	"github.com/mooyang-code/moox/modules/hostagent/internal/identity"
	hostagentpb "github.com/mooyang-code/moox/modules/hostagent/proto/hostagentgen"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mocker "github.com/tencent/goom"
	"trpc.group/trpc-go/trpc-go"
)

func writeEventBusConfig(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "eventbus.yaml")
	content := "urls:\n  - nats://127.0.0.1:4222\nusername: hostagent\neventbus_token: token\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func testAgent(t *testing.T) *Agent {
	t.Helper()
	dir := t.TempDir()
	return &Agent{
		cfg: &config.Config{
			Interval:       time.Millisecond,
			IdentityPath:   filepath.Join(dir, "identity.yaml"),
			EventBusConfig: writeEventBusConfig(t, dir),
		},
		id:        identity.File{AgentID: uuid.New().String()},
		collector: collector.New(),
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

func TestNew_NilConfig_ShouldReturnError(t *testing.T) {
	_, err := New(context.Background(), nil, "dev")
	assert.Error(t, err)
}

func TestNew_UnsupportedPlatform_ShouldReturnError(t *testing.T) {
	if runtime.GOOS == "linux" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64") {
		t.Skip("linux amd64/arm64 supports New")
	}
	_, err := New(context.Background(), &config.Config{
		IdentityPath:   filepath.Join(t.TempDir(), "identity.yaml"),
		EventBusConfig: writeEventBusConfig(t, t.TempDir()),
	}, "dev")
	assert.Error(t, err)
}

func TestAgent_GetStatus_ShouldExposeCountersAndLatest(t *testing.T) {
	a := testAgent(t)
	now := time.Now().UTC()
	a.latest = testSnapshot()
	a.lastCollect = now
	a.lastPublish = now
	a.collected.Store(3)
	a.published.Store(2)
	a.dropped.Store(1)
	a.skipped.Store(4)
	a.client = &jetstream.Client{}

	rsp, err := a.GetStatus(context.Background(), &hostagentpb.GetStatusReq{})
	require.NoError(t, err)
	assert.Equal(t, a.id.AgentID, rsp.GetAgentId())
	assert.Equal(t, a.version, rsp.GetVersion())
	assert.Equal(t, a.hostname, rsp.GetHostname())
	assert.Equal(t, a.bootID, rsp.GetBootId())
	assert.Equal(t, uint64(3), rsp.GetCollected())
	assert.Equal(t, uint64(2), rsp.GetPublished())
	assert.Equal(t, uint64(1), rsp.GetDropped())
	assert.Equal(t, uint64(4), rsp.GetSkipped())
	assert.True(t, rsp.GetEventbusConnected())
	assert.NotNil(t, rsp.GetLatest())
	assert.NotEmpty(t, rsp.GetLastCollectAt())
	assert.NotEmpty(t, rsp.GetLastPublishAt())
}

func TestAgent_GetSnapshot_ShouldReturnLatestSnapshot(t *testing.T) {
	a := testAgent(t)
	now := time.Now().UTC()
	a.latest = testSnapshot()
	a.lastCollect = now

	rsp, err := a.GetSnapshot(context.Background(), &hostagentpb.GetSnapshotReq{})
	require.NoError(t, err)
	assert.NotNil(t, rsp.GetSnapshot())
	assert.NotEmpty(t, rsp.GetCollectedAt())
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

func TestAgent_Close_NilClient_ShouldNoop(t *testing.T) {
	a := testAgent(t)
	require.NoError(t, a.Close())
}

func TestAgent_Close_WithClient_ShouldCloseAndClear(t *testing.T) {
	mock := mocker.Create()
	defer mock.Reset()

	client := &jetstream.Client{}
	mock.Struct(client).Method("Close").Return(nil)

	a := testAgent(t)
	a.client = client
	require.NoError(t, a.Close())
	assert.Nil(t, a.client)
}

func TestAgent_RunOnce_CollectError_ShouldIncrementDropped(t *testing.T) {
	a := testAgent(t)
	rsp, err := a.RunOnce(context.Background(), &hostagentpb.RunOnceReq{})
	assert.Error(t, err)
	assert.NotEmpty(t, rsp.GetPublishError())
	assert.Equal(t, uint64(1), a.collected.Load())
	assert.Equal(t, uint64(1), a.dropped.Load())
}

func TestAgent_RunOnceGuarded_ConcurrentCall_ShouldSkip(t *testing.T) {
	a := testAgent(t)
	a.running.Store(true)
	rsp, err := a.RunOnce(context.Background(), &hostagentpb.RunOnceReq{})
	assert.Error(t, err)
	assert.Equal(t, "collection already running", rsp.GetPublishError())
	assert.Equal(t, uint64(1), a.skipped.Load())
}

func TestAgent_RunOnce_PublishSuccess_ShouldUpdateCounters(t *testing.T) {
	mock := mocker.Create()
	defer mock.Reset()

	coll := &collector.Collector{}
	snapshot := testSnapshot()
	mock.Struct(coll).Method("Collect").Return(snapshot, nil, nil)

	client := &jetstream.Client{}
	mock.Struct(client).Method("Publish").Return(&jetstream.PublishAck{Stream: "MOOX", Sequence: 1}, nil)

	a := testAgent(t)
	a.collector = coll
	a.client = client

	rsp, err := a.RunOnce(context.Background(), &hostagentpb.RunOnceReq{})
	require.NoError(t, err)
	assert.True(t, rsp.GetPublished())
	assert.NotEmpty(t, rsp.GetMessageId())
	assert.NotNil(t, rsp.GetSnapshot())
	assert.Equal(t, uint64(1), a.published.Load())
	assert.Empty(t, a.lastErr)
}

func TestAgent_RunOnce_PublishError_ShouldRecordFailure(t *testing.T) {
	mock := mocker.Create()
	defer mock.Reset()

	coll := &collector.Collector{}
	mock.Struct(coll).Method("Collect").Return(testSnapshot(), nil, nil)

	client := &jetstream.Client{}
	mock.Struct(client).Method("Publish").Return(nil, errors.New("publish failed"))

	a := testAgent(t)
	a.collector = coll
	a.client = client

	rsp, err := a.RunOnce(context.Background(), &hostagentpb.RunOnceReq{})
	assert.Error(t, err)
	assert.Contains(t, rsp.GetPublishError(), "publish failed")
	assert.Equal(t, uint64(1), a.dropped.Load())
	assert.Contains(t, a.lastErr, "publish failed")
}

func TestAgent_Eventbus_InvalidConfig_ShouldReturnError(t *testing.T) {
	a := testAgent(t)
	a.cfg.EventBusConfig = filepath.Join(t.TempDir(), "missing.yaml")
	_, err := a.eventbus(context.Background())
	assert.Error(t, err)
}

func TestAgent_Run_CancelledContext_ShouldExit(t *testing.T) {
	a := testAgent(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		a.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after context cancellation")
	}
}

func TestRegister_InvalidInputs_ShouldReturnError(t *testing.T) {
	a := testAgent(t)
	assert.Error(t, Register(nil, a))
	assert.Error(t, Register(nil, nil))
}

func TestRegister_ConfiguredService_ShouldSucceed(t *testing.T) {
	wd, err := os.Getwd()
	require.NoError(t, err)
	configDir := filepath.Join(wd, "..", "..", "config")
	require.NoError(t, os.Chdir(configDir))
	t.Cleanup(func() { _ = os.Chdir(wd) })

	a := testAgent(t)
	require.NoError(t, Register(trpc.NewServer(), a))
}

func TestAgent_RunOnce_NilAgent_ShouldReturnError(t *testing.T) {
	var a *Agent
	_, err := a.runOnce(context.Background())
	assert.Error(t, err)
}

func TestAgent_Run_NilReceiver_ShouldNoop(t *testing.T) {
	var a *Agent
	a.Run(context.Background())
}

func TestAgent_RunOnce_EventBusLoadError_ShouldDrop(t *testing.T) {
	mock := mocker.Create()
	defer mock.Reset()

	coll := &collector.Collector{}
	mock.Struct(coll).Method("Collect").Return(testSnapshot(), nil, nil)

	a := testAgent(t)
	a.collector = coll
	a.cfg.EventBusConfig = filepath.Join(t.TempDir(), "missing.yaml")

	rsp, err := a.RunOnce(context.Background(), &hostagentpb.RunOnceReq{})
	assert.Error(t, err)
	assert.NotEmpty(t, rsp.GetPublishError())
	assert.Equal(t, uint64(1), a.dropped.Load())
}

func TestAgent_Eventbus_ReusesExistingClient(t *testing.T) {
	a := testAgent(t)
	client := &jetstream.Client{}
	a.client = client

	got, err := a.eventbus(context.Background())
	require.NoError(t, err)
	assert.Same(t, client, got)
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
