package marketfetch_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/markets/stockcn"
	"github.com/mooyang-code/moox/modules/collector/internal/marketstorage"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	collectorrpc "github.com/mooyang-code/moox/modules/collector/internal/rpc"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/modules/collector/schema"
	storagegen "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/jetstream/testkit"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/server"
)

const (
	periodE2EEnabledEnv       = "MOOX_PERIOD_E2E_RUN"
	periodE2EStorageBinaryEnv = "MOOX_PERIOD_E2E_STORAGE_HELPER_BINARY"
	periodE2EGatewayBinaryEnv = "MOOX_PERIOD_E2E_GATEWAY_HELPER_BINARY"
	periodE2EGatewayNodeID    = "period-e2e-gateway"
	periodE2EGatewayKeyID     = "period-e2e-collector"
	periodE2ESpotTagID        = "period_e2e"
	periodE2EWriteSource      = "collector-period-e2e"
	periodE2ESeriesTag        = "venue:binance|market:spot|source:spot_http"
	periodE2EStockTagID       = "stockcn_period_e2e"
	periodE2EStockProviderID  = "sina"
	periodE2EStockSourceID    = "stockcn_minute_http"
	periodE2EStockSubjectID   = "600000.XSHG"
	periodE2EStockSeriesTag   = "default"
)

type periodE2EStorageReady struct {
	PrimaryTarget  string `json:"primary_target"`
	MetadataTarget string `json:"metadata_target"`
	SpaceID        string `json:"space_id"`
	DatasetID      string `json:"dataset_id"`
	Frequency      string `json:"frequency"`
	StockSpaceID   string `json:"stock_space_id"`
	StockDatasetID string `json:"stock_dataset_id"`
	ClockFile      string `json:"clock_file"`
	AppID          string `json:"app_id"`
	PrimaryAppKey  string `json:"primary_app_key"`
	OutboxTarget   string `json:"outbox_target"`
}

type periodE2EOutbox struct {
	Count   uint64                  `json:"outbox_count"`
	Markers []periodE2EOutboxMarker `json:"collector_period_markers"`
}

type periodE2EOutboxMarker struct {
	OutboxID  uint64          `json:"outbox_id"`
	EventID   string          `json:"event_id"`
	SpaceID   string          `json:"space_id"`
	DatasetID string          `json:"dataset_id"`
	Frequency string          `json:"frequency"`
	Period    int64           `json:"period_time"`
	Payload   json.RawMessage `json:"payload"`
}

type periodE2EProcess struct {
	cmd     *exec.Cmd
	output  bytes.Buffer
	done    chan struct{}
	mu      sync.Mutex
	waitErr error
}

type periodE2ESchedulerDatasetSource struct {
	subject domain.Subject
}

func (s periodE2ESchedulerDatasetSource) GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error) {
	return storagesource.DatasetInfo{DataSourceID: "stockcn"}, nil
}

func (s periodE2ESchedulerDatasetSource) ResolveSubjects(context.Context, string, []string) ([]domain.Subject, error) {
	return []domain.Subject{s.subject}, nil
}

func TestPeriodStorageRPCE2E(t *testing.T) {
	if os.Getenv(periodE2EEnabledEnv) != "1" {
		t.Skip("set MOOX_PERIOD_E2E_RUN=1 through the E2E script to run native RPC coverage")
	}
	storageBinary := requirePeriodE2EFile(t, periodE2EStorageBinaryEnv)
	gatewayBinary := requirePeriodE2EFile(t, periodE2EGatewayBinaryEnv)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	root := t.TempDir()
	ready := startPeriodStorageHelper(t, ctx, root, storageBinary)

	secret := periodE2ERandomHex(t, 32)
	gatewayTarget := startPeriodStorageGateway(t, ctx, root, gatewayBinary, ready, secret)
	configurePeriodE2EClients(t, root, ready, secret)

	storage, err := marketstorage.NewBatchStorage(periodE2EStorageOptions(t, gatewayTarget), marketstorage.InstTypeSPOT, periodE2EWriteSource)
	require.NoError(t, err)
	metadata, err := marketstorage.NewResampleMetadataClient(periodE2EStorageOptions(t, gatewayTarget), marketstorage.InstTypeSPOT)
	require.NoError(t, err)

	// A known calendar session keeps this test independent of holidays and the
	// wall clock, including dates beyond the production calendar's coverage.
	clock := time.Date(2026, time.September, 30, 7, 30, 0, 0, time.UTC)
	require.NoError(t, replacePeriodE2EClock(ready.ClockFile, clock))
	initialItems := []*storagegen.TagSnapshotItem{
		{SubjectId: "BTC-USDT", SubjectType: "crypto_pair", Name: "Bitcoin / Tether", Market: "spot", Currency: "USDT", Timezone: "UTC"},
		{SubjectId: "ETH-USDT", SubjectType: "crypto_pair", Name: "Ether / Tether", Market: "spot", Currency: "USDT", Timezone: "UTC"},
	}
	applyPeriodE2ETagSnapshot(t, ctx, metadata, ready, initialItems, clock)
	require.Equal(t, []string{"BTC-USDT", "ETH-USDT"}, resolvePeriodE2ETagSubjects(t, ctx, metadata, ready))
	instrumentNames, ok := storage.(interface {
		ListInstrumentNames(context.Context, string, []string) (map[string]string, error)
	})
	require.True(t, ok, "production Storage writer must retain instrument-name Metadata access")
	names, err := instrumentNames.ListInstrumentNames(ctx, ready.SpaceID, []string{"BTC-USDT", "ETH-USDT"})
	require.NoError(t, err)
	require.Equal(t, "Bitcoin / Tether", names["BTC-USDT"])
	schedulerDB, scheduler := newPeriodE2ESnapshotScheduler(t, ctx, root, gatewayTarget, ready)
	frozen := periodE2ESchedulerSnapshot(t, ctx, schedulerDB, scheduler, ready, clock)
	require.Equal(t, uint32(2), frozen.ExpectedCount)
	require.Equal(t, []string{"BTC-USDT", "ETH-USDT"}, []string{frozen.Entries[0].SubjectID, frozen.Entries[1].SubjectID})

	deadline := clock.Add(30 * time.Minute)
	period := clock
	expectation := periodE2EStorageExpectation(frozen, deadline)
	require.Equal(t, periodE2EExpectation(ready, period, deadline, []string{"BTC-USDT", "ETH-USDT"}).SeriesHash, expectation.SeriesHash)
	state, err := storage.EnsureDatasetPeriod(ctx, expectation)
	require.NoError(t, err)
	require.Equal(t, domain.PeriodStatusWaiting, state.Status)
	require.Equal(t, deadline, state.DeadlineAt)
	status, err := storage.GetDatasetPeriodStatus(ctx, expectation)
	require.NoError(t, err)
	require.Equal(t, domain.PeriodStatusWaiting, status.Status)
	for _, invalid := range []*storagegen.TimeSeriesBatchRow{
		{SeriesIndex: 0, Row: periodE2ERow(ready, period, "BTC-USDT", "wrong-series", 1)},
		{SeriesIndex: 0, Row: periodE2ERow(ready, period, "ETH-USDT", periodE2ESeriesTag, 1)},
	} {
		require.Error(t, storage.CommitTimeSeriesBatch(ctx, expectation, []*storagegen.TimeSeriesBatchRow{invalid}, "period-e2e-wrong-binding"))
	}
	status, err = storage.GetDatasetPeriodStatus(ctx, expectation)
	require.NoError(t, err)
	require.Equal(t, domain.PeriodStatusWaiting, status.Status, "rejected rows must not complete any bitmap")

	row := periodE2ERow(ready, period, "BTC-USDT", periodE2ESeriesTag, 68000)
	require.NoError(t, storage.CommitTimeSeriesBatch(ctx, expectation, []*storagegen.TimeSeriesBatchRow{{SeriesIndex: 0, Row: row}}, "period-e2e-btc"))
	results, err := storage.RecordDatasetPeriodFailures(ctx, expectation, []uint32{1})
	require.NoError(t, err)
	require.Equal(t, storagegen.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_RECORDED, results[0].GetDisposition())

	updatedItems := []*storagegen.TagSnapshotItem{initialItems[0]}
	applyPeriodE2ETagSnapshot(t, ctx, metadata, ready, updatedItems, clock.Add(time.Second))
	require.Equal(t, []string{"BTC-USDT"}, resolvePeriodE2ETagSubjects(t, ctx, metadata, ready))
	reloaded := periodE2ESchedulerSnapshot(t, ctx, schedulerDB, scheduler, ready, clock)
	require.Equal(t, frozen, reloaded, "Scheduler must reload the immutable snapshot without re-resolving changed Metadata membership")
	nextFrozen := periodE2ESchedulerSnapshot(t, ctx, schedulerDB, scheduler, ready, clock.Add(time.Minute))
	require.Equal(t, uint32(1), nextFrozen.ExpectedCount)
	require.Equal(t, "BTC-USDT", nextFrozen.Entries[0].SubjectID)
	require.NotEqual(t, frozen.SeriesHash, nextFrozen.SeriesHash)
	t.Log("SCENARIO PASS frozen-current-next-universe")

	changedSnapshot := periodE2EExpectation(ready, period, deadline, []string{"BTC-USDT"})
	_, err = storage.EnsureDatasetPeriod(ctx, changedSnapshot)
	require.ErrorIs(t, err, marketstorage.ErrDatasetPeriodConflict, "an active period must keep its original metadata membership")

	nextExpectation := periodE2EStorageExpectation(nextFrozen, deadline)
	nextState, err := storage.EnsureDatasetPeriod(ctx, nextExpectation)
	require.NoError(t, err)
	require.Equal(t, domain.PeriodStatusWaiting, nextState.Status)
	require.Equal(t, uint32(1), nextExpectation.GetExpectedCount())
	runPeriodSeriesBindingRPCE2E(t, ctx, storage, ready, period.Add(3*time.Minute), deadline)
	runPeriodMetadataDualProviderRPCE2E(t, ctx, gatewayTarget, metadata, ready, initialItems[0], period.Add(6*time.Minute), deadline)
	t.Log("SCENARIO PASS snapshot-freeze")

	runStockCNPeriodTimerE2E(t, ctx, root, gatewayBinary, ready, secret, gatewayTarget)
	runPeriodRetryExhaustionRPCE2E(t, ctx, root, gatewayTarget, metadata, ready, initialItems, clock)

	thirdPeriod := period.Add(2 * time.Minute)
	thirdExpectation := periodE2EExpectation(ready, thirdPeriod, deadline, []string{"BTC-USDT", "ETH-USDT"})
	_, err = storage.EnsureDatasetPeriod(ctx, thirdExpectation)
	require.NoError(t, err)
	preDeadline, err := storage.RecordDatasetPeriodFailures(ctx, thirdExpectation, []uint32{0})
	require.NoError(t, err)
	require.Len(t, preDeadline, 1)
	require.Equal(t, storagegen.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_RECORDED, preDeadline[0].GetDisposition())
	require.NoError(t, replacePeriodE2EClock(ready.ClockFile, deadline))
	lateResults, err := storage.RecordDatasetPeriodFailures(ctx, expectation, []uint32{1})
	require.NoError(t, err)
	require.Equal(t, storagegen.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_RECORDED, lateResults[0].GetDisposition(), "replay after lost ACK preserves the recorded receipt")
	t.Log("SCENARIO PASS failure-recorded-replay-after-deadline")
	finalStatus, err := storage.GetDatasetPeriodStatus(ctx, expectation)
	require.NoError(t, err)
	require.Equal(t, domain.PeriodStatusDegraded, finalStatus.Status)
	require.NoError(t, storage.CommitTimeSeriesBatch(ctx, expectation, []*storagegen.TimeSeriesBatchRow{{SeriesIndex: 0, Row: row}}, "period-e2e-btc"), "a bit committed before deadline must replay successfully after terminalization")
	t.Log("SCENARIO PASS success-bit-replay-after-deadline")
	missedResults, err := storage.RecordDatasetPeriodFailures(ctx, thirdExpectation, []uint32{1})
	require.NoError(t, err)
	require.Equal(t, storagegen.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_MISSED_DEADLINE, missedResults[0].GetDisposition())
	t.Log("SCENARIO PASS failure-first-report-missed-deadline")
	runPeriodFailureReporterRPCE2E(t, ctx, root, gatewayTarget, ready, thirdExpectation)
	t.Log("SCENARIO PASS failure-receipts")
	runPeriodCleanupRPCE2E(t, ctx, root, gatewayTarget, ready, clock)
	t.Log("SCENARIO PASS cleanup-retention")
}

func requirePeriodE2EFile(t *testing.T, env string) string {
	t.Helper()
	path := strings.TrimSpace(os.Getenv(env))
	if !filepath.IsAbs(path) {
		t.Fatalf("%s must be an absolute path", env)
	}
	info, err := os.Stat(path)
	require.NoError(t, err, "%s", env)
	require.False(t, info.IsDir(), "%s must name a file", env)
	return path
}

func startPeriodStorageHelper(t *testing.T, ctx context.Context, root, binary string) periodE2EStorageReady {
	t.Helper()
	readyFile := filepath.Join(root, "storage-ready.json")
	process := startPeriodE2EProcess(t, binary, []string{"-test.run=^TestPeriodNativeProcessHelper$", "-test.v", "-test.timeout=2m"}, readyFile, map[string]string{
		"MOOX_PERIOD_E2E_HELPER":     "1",
		"MOOX_PERIOD_E2E_READY_FILE": readyFile,
	})
	var ready periodE2EStorageReady
	waitPeriodE2EReady(t, ctx, process, readyFile, func(raw []byte) error { return json.Unmarshal(raw, &ready) })
	require.NotEmpty(t, ready.PrimaryTarget)
	require.NotEmpty(t, ready.MetadataTarget)
	require.NotEmpty(t, ready.ClockFile)
	require.NotEmpty(t, ready.OutboxTarget)
	return ready
}

func inspectPeriodE2EOutbox(t *testing.T, ctx context.Context, ready periodE2EStorageReady) periodE2EOutbox {
	t.Helper()
	host, port, err := net.SplitHostPort(ready.OutboxTarget)
	require.NoError(t, err)
	ip := net.ParseIP(host)
	require.NotNil(t, ip)
	require.True(t, ip.IsLoopback(), "test-only outbox evidence must never be read from a remote host")
	require.NotEmpty(t, port)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+ready.OutboxTarget+"/__test/outbox", nil)
	require.NoError(t, err)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	var evidence periodE2EOutbox
	require.NoError(t, json.NewDecoder(response.Body).Decode(&evidence))
	return evidence
}

func requirePeriodE2EMarker(t *testing.T, evidence periodE2EOutbox, expectation *storagegen.DatasetPeriodExpectation) (periodE2EOutboxMarker, *storageeventpb.CollectorPeriodCompleted) {
	t.Helper()
	var matches []periodE2EOutboxMarker
	for _, marker := range evidence.Markers {
		if marker.SpaceID == expectation.GetSpaceId() && marker.DatasetID == expectation.GetDatasetId() && marker.Frequency == expectation.GetFrequency() && marker.Period == expectation.GetPeriodTime() {
			matches = append(matches, marker)
		}
	}
	require.Len(t, matches, 1, "the real Storage outbox must have exactly one matching terminal marker")
	marker := matches[0]
	require.NotZero(t, marker.OutboxID)
	require.NotEmpty(t, marker.EventID)
	payload := &storageeventpb.CollectorPeriodCompleted{}
	require.NoError(t, protojson.Unmarshal(marker.Payload, payload))
	require.Equal(t, expectation.GetDatasetId(), payload.GetDatasetId())
	require.Equal(t, expectation.GetFrequency(), payload.GetFrequency())
	require.Equal(t, expectation.GetPeriodTime(), payload.GetPeriodTime())
	require.Equal(t, expectation.GetSeriesHash(), payload.GetExpectedScopeRef())
	return marker, payload
}

func waitPeriodE2EFinalizerCycles(t *testing.T, ctx context.Context) {
	t.Helper()
	// The actual Storage maintenance loop ticks once per second. Let it revisit
	// the controlled deadline twice; this is not a replacement finalizer call.
	timer := time.NewTimer(2100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal("Storage finalizer did not get its bounded maintenance window")
	}
}

func startPeriodStorageGateway(t *testing.T, ctx context.Context, root, binary string, ready periodE2EStorageReady, secret string) string {
	t.Helper()
	readyFile := filepath.Join(root, "host-gateway-ready")
	nonceDir := filepath.Join(root, "gateway-nonces")
	primary := stripPeriodE2EURL(t, ready.PrimaryTarget)
	metadata := stripPeriodE2EURL(t, ready.MetadataTarget)
	args := []string{
		"-host-id=" + periodE2EGatewayNodeID,
		"-route=trpc.moox.storage.PrimaryStore=" + primary, "-route=trpc.moox.storage.Metadata=" + metadata,
		"-callers=collector", "-listen-addr=127.0.0.1:0", "-ready-file=" + readyFile, "-nonce-dir=" + nonceDir,
	}
	process := startPeriodE2EProcess(t, binary, args, readyFile, map[string]string{"MOOX_GATEWAY_E2E_KEYS": periodE2EGatewayKeys(secret)})
	var target string
	waitPeriodE2EReady(t, ctx, process, readyFile, func(raw []byte) error { target = strings.TrimSpace(string(raw)); return nil })
	parsed, err := url.Parse(target)
	require.NoError(t, err)
	require.Equal(t, "ip", parsed.Scheme)
	periodE2ESecrets.Store(target, secret)
	return target
}

// periodE2ESecrets 记录每个 e2e-helper 地址对应的 collector 签名密钥。
var periodE2ESecrets sync.Map

// periodE2EStorageOptions 返回以 collector 身份、经 e2e-helper（主机网关本机入口）访问 Storage 的
// gatewayclient 选项，与 Collector 进程的用法一致。
func periodE2EStorageOptions(t *testing.T, target string) []client.Option {
	t.Helper()
	return periodE2EGateway(t, target).ClientOptions()
}

// periodE2ERuntimeClient 返回经 e2e-helper 领取 Timer 批次的客户端。生产中 SCF 经外部接入领取，外部接入这一段
// 由 modules/access 的端到端测试覆盖；这里覆盖 Collector 与 Storage 之间的周期协议。
func periodE2ERuntimeClient(t *testing.T, target string) marketfetch.TimerRuntimeClient {
	t.Helper()
	return marketfetch.NewTimerRuntimeClient(periodE2EGateway(t, target))
}

// periodE2EStorageWriter 返回 SCF 处理器按市场类型创建 Storage 客户端的函数。
func periodE2EStorageWriter(t *testing.T, target string) func(string, string) (marketfetch.Storage, error) {
	t.Helper()
	options := periodE2EStorageOptions(t, target)
	return func(market, source string) (marketfetch.Storage, error) {
		return marketfetch.NewMarketStorageForMarket(options, market, source)
	}
}

// periodE2EGateway 返回以 collector 身份经 e2e-helper 访问的 gatewayclient。没有登记的地址（例如不可达地址）
// 使用无效密钥。
func periodE2EGateway(t *testing.T, target string) *gatewayclient.Client {
	t.Helper()
	secret := "unregistered-period-e2e-secret"
	if value, ok := periodE2ESecrets.Load(target); ok {
		secret = value.(string)
	}
	credentials := gatewayauth.Credentials{KeyID: periodE2EGatewayKeyID, Caller: "collector", Secret: secret}
	gateway, err := gatewayclient.New(gatewayclient.Options{
		Config: gatewayclient.Config{
			Mode: gatewayclient.ModeLocal, Caller: "collector", CAFile: "unused-moox-ca.crt", CacheDir: t.TempDir(),
			LocalAddress: stripPeriodE2EURL(t, target),
		},
		Credentials: &credentials,
	})
	require.NoError(t, err)
	t.Cleanup(gateway.Close)
	return gateway
}

// periodE2EStorageFactory 返回经 periodE2EStorageOptions 访问 Storage 的市场存储工厂。
func periodE2EStorageFactory(t *testing.T, target string) marketfetch.StorageFactory {
	t.Helper()
	options := periodE2EStorageOptions(t, target)
	return func(_, source string) (marketfetch.Storage, error) {
		return marketstorage.NewBatchStorage(options, marketstorage.InstTypeSPOT, source)
	}
}

func startPeriodE2EProcess(t *testing.T, binary string, args []string, readyFile string, overrides map[string]string) *periodE2EProcess {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Env = periodE2EEnvironment(os.Environ(), overrides)
	process := &periodE2EProcess{cmd: cmd, done: make(chan struct{})}
	cmd.Stdout = &process.output
	cmd.Stderr = &process.output
	require.NoError(t, cmd.Start())
	go func() {
		err := cmd.Wait()
		process.mu.Lock()
		process.waitErr = err
		process.mu.Unlock()
		close(process.done)
	}()
	t.Cleanup(func() { stopPeriodE2EProcess(t, process) })
	return process
}

func waitPeriodE2EReady(t *testing.T, ctx context.Context, process *periodE2EProcess, path string, decode func([]byte) error) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		raw, err := os.ReadFile(path)
		if err == nil {
			require.NoError(t, decode(raw))
			return
		}
		if !os.IsNotExist(err) {
			t.Fatalf("read helper readiness file: %v", err)
		}
		select {
		case <-process.done:
			t.Fatalf("helper exited before readiness: %s", periodE2EProcessFailure(process))
		case <-ctx.Done():
			t.Fatalf("helper readiness timeout: %s", periodE2EProcessFailure(process))
		case <-ticker.C:
		}
	}
}

func stopPeriodE2EProcess(t *testing.T, process *periodE2EProcess) {
	t.Helper()
	select {
	case <-process.done:
		if err := periodE2EProcessFailure(process); err != "" {
			t.Errorf("helper exited unsuccessfully before cleanup: %s", err)
		}
		return
	default:
	}
	if err := process.cmd.Process.Signal(os.Interrupt); err != nil {
		_ = process.cmd.Process.Kill()
	}
	select {
	case <-process.done:
	case <-time.After(5 * time.Second):
		_ = process.cmd.Process.Kill()
		<-process.done
		t.Errorf("helper did not stop after SIGINT: %s", periodE2EProcessFailure(process))
	}
	if err := periodE2EProcessFailure(process); err != "" {
		t.Errorf("helper exited unsuccessfully: %s", err)
	}
}

func periodE2EProcessFailure(process *periodE2EProcess) string {
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.waitErr != nil {
		return fmt.Sprintf("%v\n%s", process.waitErr, process.output.String())
	}
	return ""
}

// periodE2EGatewayKeys 是 e2e-helper 的校验密钥：只有 collector 一个调用方。
func periodE2EGatewayKeys(secret string) string {
	return "collector:" + periodE2EGatewayKeyID + ":" + secret
}

func periodE2EEnvironment(base []string, overrides map[string]string) []string {
	result := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if strings.HasPrefix(key, "MOOX_") {
			continue
		}
		if _, replaced := overrides[key]; replaced {
			continue
		}
		result = append(result, entry)
	}
	for key, value := range overrides {
		result = append(result, key+"="+value)
	}
	return result
}

func configurePeriodE2EClients(t *testing.T, root string, ready periodE2EStorageReady, secret string) {
	t.Helper()
	configPath := filepath.Join(root, "market-storage.yaml")
	config := "storage:\n  bindings:\n    spot:\n      data_source_id: binance\n      subject_type: crypto_pair\n      subject_market: spot\n      auth_info:\n        app_id: " + ready.AppID + "\n        app_key: \"\"\n"
	require.NoError(t, os.WriteFile(configPath, []byte(config), 0600))
	t.Setenv("MOOX_STORAGE_MARKET_CONFIG", configPath)
	appKeys, err := json.Marshal(map[string]string{ready.AppID: ready.PrimaryAppKey})
	require.NoError(t, err)
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON", string(appKeys))
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "")
	t.Setenv("MOOX_COLLECTOR_STORAGE_METADATA_TARGET", "")
	_ = os.Unsetenv("MOOX_COLLECTOR_STORAGE_METADATA_TARGET")
}

func applyPeriodE2ETagSnapshot(t *testing.T, ctx context.Context, metadata *marketstorage.ResampleMetadataClient, ready periodE2EStorageReady, items []*storagegen.TagSnapshotItem, at time.Time) {
	t.Helper()
	response, err := metadata.Client.ApplyTagSnapshot(ctx, &storagegen.ApplyTagSnapshotReq{
		AuthInfo: metadata.Auth, SpaceId: ready.SpaceID, TagId: periodE2ESpotTagID,
		RunAt: at.UTC().Format(time.RFC3339Nano), Items: items,
	})
	require.NoError(t, err)
	require.NotNil(t, response)
	require.NotNil(t, response.GetRetInfo())
	require.Equal(t, storagegen.ErrorCode_SUCCESS, response.GetRetInfo().GetCode(), response.GetRetInfo().GetMsg())
}

func resolvePeriodE2ETagSubjects(t *testing.T, ctx context.Context, metadata *marketstorage.ResampleMetadataClient, ready periodE2EStorageReady) []string {
	t.Helper()
	response, err := metadata.Client.ResolveSubjects(ctx, &storagegen.ResolveSubjectsReq{AuthInfo: metadata.Auth, SpaceId: ready.SpaceID, TagIds: []string{periodE2ESpotTagID}})
	require.NoError(t, err)
	require.NotNil(t, response)
	require.Equal(t, storagegen.ErrorCode_SUCCESS, response.GetRetInfo().GetCode(), response.GetRetInfo().GetMsg())
	ids := make([]string, 0, len(response.GetSubjects()))
	for _, subject := range response.GetSubjects() {
		if subject != nil {
			ids = append(ids, subject.GetSubjectId())
		}
	}
	return ids
}

func periodE2EExpectation(ready periodE2EStorageReady, period, deadline time.Time, subjects []string) *storagegen.DatasetPeriodExpectation {
	subjects = append([]string(nil), subjects...)
	keys := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		keys = append(keys, domain.CanonicalSeriesKey("binance", "spot_http", "spot", subject, periodE2ESeriesTag))
	}
	seriesHash := domain.SeriesSetHash(keys)
	snapshot := make([]*storagegen.DatasetPeriodSeries, 0, len(subjects))
	for index, subject := range subjects {
		snapshot = append(snapshot, &storagegen.DatasetPeriodSeries{SeriesIndex: uint32(index), SubjectId: subject, SeriesTag: periodE2ESeriesTag})
	}
	return &storagegen.DatasetPeriodExpectation{
		SpaceId: ready.SpaceID, DatasetId: ready.DatasetID, Frequency: "1m", PeriodTime: period.Unix(),
		SeriesHash: seriesHash, ExpectedCount: uint32(len(subjects)), DeadlineAt: deadline.Unix(), SeriesSnapshot: snapshot,
	}
}

func runPeriodSeriesBindingRPCE2E(t *testing.T, ctx context.Context, storage marketfetch.Storage, ready periodE2EStorageReady, period, deadline time.Time) {
	t.Helper()
	periodStorage, ok := storage.(interface {
		EnsureDatasetPeriod(context.Context, *storagegen.DatasetPeriodExpectation) (domain.PeriodStorageState, error)
		GetDatasetPeriodStatus(context.Context, *storagegen.DatasetPeriodExpectation) (domain.PeriodStorageState, error)
		CommitTimeSeriesBatch(context.Context, *storagegen.DatasetPeriodExpectation, []*storagegen.TimeSeriesBatchRow, string) error
	})
	require.True(t, ok)
	bound := periodE2EExpectation(ready, period, deadline, []string{"BTC-USDT", "ETH-USDT"})
	_, err := periodStorage.EnsureDatasetPeriod(ctx, bound)
	require.NoError(t, err)
	for _, invalid := range []*storagegen.TimeSeriesBatchRow{
		{SeriesIndex: 0, Row: periodE2ERow(ready, period, "BTC-USDT", "wrong-series", 1)},
		{SeriesIndex: 0, Row: periodE2ERow(ready, period, "ETH-USDT", periodE2ESeriesTag, 1)},
	} {
		require.Error(t, periodStorage.CommitTimeSeriesBatch(ctx, bound, []*storagegen.TimeSeriesBatchRow{invalid}, "binding-rejected"))
	}
	require.NoError(t, periodStorage.CommitTimeSeriesBatch(ctx, bound, []*storagegen.TimeSeriesBatchRow{{SeriesIndex: 1, Row: periodE2ERow(ready, period, "ETH-USDT", periodE2ESeriesTag, 1)}}, "binding-eth"))
	status, err := periodStorage.GetDatasetPeriodStatus(ctx, bound)
	require.NoError(t, err)
	require.Equal(t, domain.PeriodStatusWaiting, status.Status, "rejected index-zero rows must not have advanced the success bitmap")
	require.NoError(t, periodStorage.CommitTimeSeriesBatch(ctx, bound, []*storagegen.TimeSeriesBatchRow{{SeriesIndex: 0, Row: periodE2ERow(ready, period, "BTC-USDT", periodE2ESeriesTag, 1)}}, "binding-btc"))
	status, err = periodStorage.GetDatasetPeriodStatus(ctx, bound)
	require.NoError(t, err)
	require.Equal(t, domain.PeriodStatusComplete, status.Status)
	t.Log("SCENARIO PASS wrong-series-index-rejection")

	dualPeriod := period.Add(time.Minute)
	dual := periodE2EExpectation(ready, dualPeriod, deadline, []string{"BTC-USDT", "BTC-USDT"})
	otherTag := "venue:okx|market:spot|source:spot_http"
	dual.SeriesSnapshot[1].SeriesTag = otherTag
	dual.SeriesHash = domain.SeriesSetHash([]string{
		domain.CanonicalSeriesKey("binance", "spot_http", "spot", "BTC-USDT", periodE2ESeriesTag),
		domain.CanonicalSeriesKey("okx", "spot_http", "spot", "BTC-USDT", otherTag),
	})
	_, err = periodStorage.EnsureDatasetPeriod(ctx, dual)
	require.NoError(t, err)
	require.Equal(t, uint32(2), dual.GetExpectedCount(), "two Provider series are distinct despite one Subject")
	require.NoError(t, periodStorage.CommitTimeSeriesBatch(ctx, dual, []*storagegen.TimeSeriesBatchRow{{SeriesIndex: 0, Row: periodE2ERow(ready, dualPeriod, "BTC-USDT", periodE2ESeriesTag, 1)}}, "dual-binance"))
	status, err = periodStorage.GetDatasetPeriodStatus(ctx, dual)
	require.NoError(t, err)
	require.Equal(t, domain.PeriodStatusWaiting, status.Status, "Subject deduplication cannot collapse the series bitmap")
	require.NoError(t, periodStorage.CommitTimeSeriesBatch(ctx, dual, []*storagegen.TimeSeriesBatchRow{{SeriesIndex: 1, Row: periodE2ERow(ready, dualPeriod, "BTC-USDT", otherTag, 1)}}, "dual-okx"))
	status, err = periodStorage.GetDatasetPeriodStatus(ctx, dual)
	require.NoError(t, err)
	require.Equal(t, domain.PeriodStatusComplete, status.Status)
	t.Log("SCENARIO PASS duplicate-subject-two-series")
}

// The CloudNode transport is the only substituted Scheduler dependency. An
// empty external catalog stops dispatch after the real Metadata freeze phase.
type periodE2EEmptyCloudNode struct{}

func (periodE2EEmptyCloudNode) ListMarketFetchers(context.Context, string) ([]scfinvoker.Node, error) {
	return nil, nil
}

func (periodE2EEmptyCloudNode) ListTimerMarketFetchers(context.Context, string) ([]scfinvoker.Node, error) {
	return nil, nil
}

func (periodE2EEmptyCloudNode) Invoke(context.Context, string, string, map[string]any, cloudnodepb.ScfInvokeType) (scfinvoker.InvocationResult, error) {
	return scfinvoker.InvocationResult{}, fmt.Errorf("empty CloudNode catalog must not dispatch")
}

func newPeriodE2ESnapshotScheduler(t *testing.T, ctx context.Context, root, target string, ready periodE2EStorageReady) (*store.Store, *marketfetch.Scheduler) {
	t.Helper()
	db := openPeriodE2EDB(t, filepath.Join(root, "snapshot-scheduler.db"))
	params, err := json.Marshal(map[string]any{
		"market_id": "crypto", "instrument_type": "spot", "source_id": "spot_http",
		"target_dataset_id": ready.DatasetID, "frequency": "1m",
	})
	require.NoError(t, err)
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{
		SpaceID: ready.SpaceID, TaskID: "period-e2e-scheduler-task", TaskName: "Period freeze fixture",
		DataType: "kline", CollectParams: string(params), TagIDs: []string{periodE2ESpotTagID},
		Enabled: true, PrepareState: domain.PrepareStateReady,
	}))
	return db, &marketfetch.Scheduler{
		SpaceID: ready.SpaceID, Tasks: db.Tasks(), Batches: db.FetchBatches(),
		PeriodSeriesSnapshot: db.PeriodSeriesSnapshot(), Symbols: storagesource.NewDatasetSource(periodE2EStorageOptions(t, target)),
		Invoker: periodE2EEmptyCloudNode{},
	}
}

func periodE2ESchedulerSnapshot(t *testing.T, ctx context.Context, db *store.Store, scheduler *marketfetch.Scheduler, ready periodE2EStorageReady, period time.Time) domain.PeriodSeriesSnapshot {
	t.Helper()
	scheduler.Now = func() time.Time { return period.Add(time.Minute + 10*time.Second) }
	err := scheduler.Tick(ctx, ready.SpaceID)
	require.EqualError(t, err, "no active Invoke market fetcher nodes", "Metadata expansion must finish before the empty external catalog prevents dispatch")
	snapshot, found, err := db.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, domain.PeriodKey{
		SpaceID: ready.SpaceID, DatasetID: ready.DatasetID, Frequency: "1m", PeriodTime: period,
	})
	require.NoError(t, err)
	require.True(t, found, "production Scheduler must persist the Metadata-derived series snapshot")
	return snapshot
}

func runPeriodMetadataDualProviderRPCE2E(t *testing.T, ctx context.Context, target string, metadata *marketstorage.ResampleMetadataClient, ready periodE2EStorageReady, btc *storagegen.TagSnapshotItem, period, deadline time.Time) {
	t.Helper()
	const secondTag = "period_e2e_okx"
	// Fixture administration uses the real local Metadata RPC. Collector's
	// Gateway deliberately has no tag-creation permission; all membership reads,
	// subsequent snapshots and period writes still use its governed route.
	admin := storagegen.NewMetadataClientProxy(client.WithTarget(ready.MetadataTarget))
	source, err := admin.CreateDataSource(ctx, &storagegen.CreateDataSourceReq{AuthInfo: metadata.Auth, DataSource: &storagegen.DataSource{
		SpaceId: ready.SpaceID, DataSourceId: "okx", Name: "Period E2E OKX", Kind: "exchange", Market: "crypto", Timezone: "UTC", Status: "active",
	}})
	require.NoError(t, err)
	require.Equal(t, storagegen.ErrorCode_SUCCESS, source.GetRetInfo().GetCode(), source.GetRetInfo().GetMsg())
	created, err := admin.UpsertTag(ctx, &storagegen.UpsertTagReq{AuthInfo: metadata.Auth, CreateOnly: true, Tag: &storagegen.Tag{
		SpaceId: ready.SpaceID, TagId: secondTag, TagName: "Period E2E OKX", Mode: "auto", Source: "okx", MarketType: "spot",
	}})
	require.NoError(t, err)
	require.Equal(t, storagegen.ErrorCode_SUCCESS, created.GetRetInfo().GetCode(), created.GetRetInfo().GetMsg())
	applied, err := metadata.Client.ApplyTagSnapshot(ctx, &storagegen.ApplyTagSnapshotReq{
		AuthInfo: metadata.Auth, SpaceId: ready.SpaceID, TagId: secondTag, RunAt: period.Format(time.RFC3339Nano), Items: []*storagegen.TagSnapshotItem{btc},
	})
	require.NoError(t, err)
	require.Equal(t, storagegen.ErrorCode_SUCCESS, applied.GetRetInfo().GetCode(), applied.GetRetInfo().GetMsg())
	union, err := metadata.Client.ResolveSubjects(ctx, &storagegen.ResolveSubjectsReq{AuthInfo: metadata.Auth, SpaceId: ready.SpaceID, TagIds: []string{periodE2ESpotTagID, secondTag}})
	require.NoError(t, err)
	require.Equal(t, storagegen.ErrorCode_SUCCESS, union.GetRetInfo().GetCode())
	require.Len(t, union.GetSubjects(), 1, "Metadata subject union must deduplicate the shared BTC subject")
	db := openPeriodE2EDB(t, filepath.Join(t.TempDir(), "dual-provider.db"))
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{
		SpaceID: ready.SpaceID, TaskID: "dual-provider-task", TaskName: "Dual Provider fixture", DataType: "kline",
		CollectParams: fmt.Sprintf(`{"market_id":"crypto","instrument_type":"spot","source_id":"spot_http","target_dataset_id":%q,"frequency":"1m"}`, ready.DatasetID),
		TagIDs:        []string{periodE2ESpotTagID, secondTag}, Enabled: true, PrepareState: domain.PrepareStateReady,
	}))
	scheduler := &marketfetch.Scheduler{SpaceID: ready.SpaceID, Tasks: db.Tasks(), Batches: db.FetchBatches(), PeriodSeriesSnapshot: db.PeriodSeriesSnapshot(), Symbols: storagesource.NewDatasetSource(periodE2EStorageOptions(t, target)), Invoker: periodE2EEmptyCloudNode{}}
	snapshot := periodE2ESchedulerSnapshot(t, ctx, db, scheduler, ready, period)
	require.Equal(t, uint32(2), snapshot.ExpectedCount)
	require.Len(t, snapshot.Entries, 2)
	providers := make(map[string]bool)
	for _, entry := range snapshot.Entries {
		require.Equal(t, "BTC-USDT", entry.SubjectID)
		providers[entry.Provider] = true
	}
	require.Equal(t, map[string]bool{"binance": true, "okx": true}, providers)
	storage, err := marketstorage.NewBatchStorage(periodE2EStorageOptions(t, target), marketstorage.InstTypeSPOT, periodE2EWriteSource)
	require.NoError(t, err)
	expectation := periodE2EStorageExpectation(snapshot, deadline)
	_, err = storage.EnsureDatasetPeriod(ctx, expectation)
	require.NoError(t, err)
	for index, entry := range snapshot.Entries {
		require.NoError(t, storage.CommitTimeSeriesBatch(ctx, expectation, []*storagegen.TimeSeriesBatchRow{{SeriesIndex: entry.SeriesIndex, Row: periodE2ERow(ready, period, entry.SubjectID, entry.SeriesTag, float64(index+1))}}, fmt.Sprintf("metadata-dual-%d", index)))
		state, err := storage.GetDatasetPeriodStatus(ctx, expectation)
		require.NoError(t, err)
		if index == 0 {
			require.Equal(t, domain.PeriodStatusWaiting, state.Status)
		} else {
			require.Equal(t, domain.PeriodStatusComplete, state.Status)
		}
	}
	t.Log("SCENARIO PASS metadata-dual-provider-membership")
	_, marker := requirePeriodE2EMarker(t, inspectPeriodE2EOutbox(t, ctx, ready), expectation)
	require.Equal(t, "complete", marker.GetStatus())
	require.Equal(t, []string{"BTC-USDT"}, marker.GetUniverseSubjectIds(), "two expected Provider series must produce one Subject in the actual Marker payload")
	require.Empty(t, marker.GetFailedSubjects())
	t.Log("SCENARIO PASS metadata-dual-provider-marker-subject-dedup")
}

func openPeriodE2EDB(t *testing.T, path string) *store.Store {
	t.Helper()
	db, err := store.Open(&store.Options{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	return db
}

func unusedPeriodE2ETarget(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	return "ip://" + address
}

func runPeriodFailureReporterRPCE2E(t *testing.T, ctx context.Context, root, target string, ready periodE2EStorageReady, expectation *storagegen.DatasetPeriodExpectation) {
	t.Helper()
	db := openPeriodE2EDB(t, filepath.Join(root, "failure-reporter.db"))
	item := domain.CollectionItem{
		SubjectID: "ETH-USDT", Provider: "binance", SourceID: "spot_http", MarketType: "spot",
		DatasetID: ready.DatasetID, Frequency: "1m", TargetDataTime: time.Unix(expectation.GetPeriodTime(), 0).UTC().Format(time.RFC3339Nano),
	}
	itemJSON, err := json.Marshal(item)
	require.NoError(t, err)
	targets := []domain.WriteTarget{
		{ID: "recorded-target", SpaceID: ready.SpaceID, DatasetID: ready.DatasetID, SeriesIndex: 0, SeriesHash: expectation.GetSeriesHash(), ExpectedCount: expectation.GetExpectedCount()},
		{ID: "missed-target", SpaceID: ready.SpaceID, DatasetID: ready.DatasetID, SeriesIndex: 1, SeriesHash: expectation.GetSeriesHash(), ExpectedCount: expectation.GetExpectedCount()},
	}
	targetJSON, err := json.Marshal(targets)
	require.NoError(t, err)
	retry := domain.RetryItem{
		SpaceID: ready.SpaceID, RetryKey: "period-e2e-mixed-receipt", InstanceID: "period-e2e-failed-instance",
		SubjectID: item.SubjectID, Frequency: "1m", TargetDataTime: time.Unix(expectation.GetPeriodTime(), 0).UTC(),
		TaskJSON: string(itemJSON), FailureTargetsJSON: string(targetJSON), Attempt: 3, Status: "permanent_failed",
		PeriodFailureReportState: domain.PeriodFailureReportPending,
	}
	require.NoError(t, db.FetchRetries().Upsert(ctx, &retry))
	unreachable := marketfetch.NewPeriodFailureReporter(db.FetchRetries(), periodE2EStorageFactory(t, unusedPeriodE2ETarget(t)), ready.SpaceID)
	require.Error(t, unreachable.RunOnce(ctx, ready.SpaceID))
	pending, err := db.FetchRetries().Get(ctx, ready.SpaceID, retry.RetryKey)
	require.NoError(t, err)
	require.Equal(t, domain.PeriodFailureReportPending, pending.PeriodFailureReportState)
	require.NotEmpty(t, pending.PeriodFailureLastError)
	// Restore the actual Gateway client, not a success stub. The single response
	// contains a replayed receipt and a first-after-deadline receipt.
	reporter := marketfetch.NewPeriodFailureReporter(db.FetchRetries(), periodE2EStorageFactory(t, target), ready.SpaceID)
	require.NoError(t, reporter.RunOnce(ctx, ready.SpaceID))
	settled, err := db.FetchRetries().Get(ctx, ready.SpaceID, retry.RetryKey)
	require.NoError(t, err)
	require.Equal(t, domain.PeriodFailureReportMissedDeadline, settled.PeriodFailureReportState, "nil RPC error is not an all-target ACK")
	var results []domain.PeriodFailureTargetResult
	require.NoError(t, json.Unmarshal([]byte(settled.PeriodFailureResultsJSON), &results))
	require.Len(t, results, 2)
	byTarget := make(map[string]string)
	for _, result := range results {
		byTarget[result.WriteTargetID] = result.Disposition
	}
	require.Equal(t, map[string]string{"recorded-target": "recorded", "missed-target": "missed_deadline"}, byTarget)
	t.Log("SCENARIO PASS mixed-recorded-missed-receipts")
	t.Log("SCENARIO PASS failure-pending-network-recovery")
}

type periodE2EInvoke struct {
	handler *marketfetch.Handler
	results chan error
	ctx     context.Context
	mu      sync.Mutex
	calls   int
}

func (*periodE2EInvoke) ListMarketFetchers(context.Context, string) ([]scfinvoker.Node, error) {
	return []scfinvoker.Node{{NodeID: "period-e2e-invoke", FunctionName: "moox-period-e2e-invoke", Region: "ap-hongkong", TriggerType: "invoke"}}, nil
}

func (*periodE2EInvoke) ListTimerMarketFetchers(context.Context, string) ([]scfinvoker.Node, error) {
	return nil, nil
}

func (i *periodE2EInvoke) Invoke(_ context.Context, _, _ string, raw map[string]any, _ cloudnodepb.ScfInvokeType) (scfinvoker.InvocationResult, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return scfinvoker.InvocationResult{}, err
	}
	var event model.CloudFunctionEvent
	if err := json.Unmarshal(encoded, &event); err != nil {
		return scfinvoker.InvocationResult{}, err
	}
	i.mu.Lock()
	i.calls++
	requestID := fmt.Sprintf("period-e2e-invoke-%d", i.calls)
	i.mu.Unlock()
	event.RequestID = requestID
	// An accepted asynchronous CloudNode invocation runs with the SCF worker's
	// own budget, not the short RPC acceptance deadline.
	workerCtx, cancel := context.WithTimeout(i.ctx, time.Minute)
	defer cancel()
	_, err = i.handler.HandleWithFunctionName(workerCtx, event, "moox-period-e2e-invoke")
	i.results <- err
	// Market failures are worker results, not failed CloudNode acceptance.
	return scfinvoker.InvocationResult{RequestID: requestID}, nil
}

type periodE2ESpotFetcher struct {
	requests chan marketdata.KlineRequest
}

func (*periodE2ESpotFetcher) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: "binance", SourceID: "spot_http", DisplayName: "E2E Binance fixture", Hosts: []string{"fixture.invalid"}, Status: marketdata.SourceEnabled}
}

func (*periodE2ESpotFetcher) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		Markets: []string{"crypto"}, Instruments: []marketdata.InstrumentType{marketdata.InstrumentSpot}, Frequencies: []string{"1h"},
		CompleteOHLCV: true, HasAmount: true, MaxBarsPerRequest: 1, TimestampMode: marketdata.TimestampModeOpen,
		RateLimit: marketdata.RateLimitPolicy{RequestsPerSecond: 100, Burst: 10, MaxConcurrent: 2, Cooldown: time.Millisecond, RequestTimeout: time.Second},
		History:   marketdata.KlineHistoryCapability{MaxLookback: 30 * 24 * time.Hour},
	}
}

func (p *periodE2ESpotFetcher) FetchKlines(ctx context.Context, request marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	select {
	case p.requests <- request:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if request.SubjectID == "ETH-USDT" {
		return nil, fmt.Errorf("fixture external provider temporarily unavailable: %w", marketdata.ErrTimeout)
	}
	return []marketdata.NormalizedKline{{
		SubjectID: request.SubjectID, ProviderID: "binance", SourceID: "spot_http", ProviderSymbol: request.ProviderSymbol,
		Frequency: request.Frequency, BarStart: request.StartTime, BarEnd: request.EndTime,
		Open: 10, High: 11, Low: 9, Close: 10.5, VolumeShares: 100, AmountCNY: 1050, ProviderTimestamp: request.EndTime, FetchedAt: request.EndTime, RequestID: request.RequestID,
	}}, nil
}

func runPeriodRetryExhaustionRPCE2E(t *testing.T, ctx context.Context, root, target string, metadata *marketstorage.ResampleMetadataClient, ready periodE2EStorageReady, items []*storagegen.TagSnapshotItem, clock time.Time) {
	t.Helper()
	configurePeriodE2EEventBus(t)
	applyPeriodE2ETagSnapshot(t, ctx, metadata, ready, items, clock.Add(2*time.Second))
	db := openPeriodE2EDB(t, filepath.Join(root, "retry-exhaustion.db"))
	params, err := json.Marshal(map[string]any{
		"market_id": "crypto", "instrument_type": "spot", "source_id": "spot_http", "target_dataset_id": ready.DatasetID,
		"frequency": "1h", "output_fields": []string{"close"},
	})
	require.NoError(t, err)
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{
		SpaceID: ready.SpaceID, TaskID: "period-e2e-retry-task", TaskName: "Retry exhaustion fixture", DataType: "kline",
		CollectParams: string(params), TagIDs: []string{periodE2ESpotTagID}, Enabled: true, PrepareState: domain.PrepareStateReady,
	}))
	completionCtx, cancelCompletion := context.WithCancel(ctx)
	completionDone, err := marketfetch.StartCompletionConsumerWithDone(completionCtx, ready.SpaceID, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		cancelCompletion()
		select {
		case <-completionDone:
		case <-time.After(5 * time.Second):
			t.Error("retry Completion consumer did not stop")
		}
	})
	provider := &periodE2ESpotFetcher{requests: make(chan marketdata.KlineRequest, 10)}
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(provider))
	router, err := marketdata.NewRouter(registry, 1, nil, nil)
	require.NoError(t, err)
	// Scheduler deadline recovery compares against wall time. Keep this
	// invocation fixture on the current clock instead of the suite's historical
	// calendar date, or every persisted deadline would already be expired.
	now := time.Now().UTC().Add(2 * time.Minute).Truncate(time.Minute).Add(10 * time.Minute)
	require.NoError(t, replacePeriodE2EClock(ready.ClockFile, now))
	defer func() {
		if err := replacePeriodE2EClock(ready.ClockFile, clock); err != nil {
			t.Errorf("restore period E2E clock: %v", err)
		}
	}()
	handler := marketfetch.NewHandler()
	handler.NewStorage = periodE2EStorageWriter(t, target)
	handler.Now = func() time.Time { return now }
	publish := handler.Publish
	handler.Publish = func(publishCtx context.Context, request marketfetch.Request, payload proto.Message) error {
		if completion, ok := payload.(*marketfetchpb.MarketFetchBatchCompleted); ok {
			for _, item := range completion.GetItems() {
				t.Logf("worker outcome subject=%s outcome=%s error=%s targets=%v", item.GetSubjectId(), item.GetOutcome(), item.GetErrorSummary(), item.GetTargets())
			}
		}
		publishErr := publish(publishCtx, request, payload)
		t.Logf("completion publish returned batch=%s error=%v", request.BatchID, publishErr)
		return publishErr
	}
	handler.NewCryptoKlinePipeline = func(storage marketfetch.Storage, instrument marketdata.InstrumentType) (*marketfetch.KlinePipeline, error) {
		return &marketfetch.KlinePipeline{
			Router: router, Storage: storage, CandidateChain: []string{"binance"}, RouteID: "period-e2e-crypto",
			SpaceID: ready.SpaceID, MarketID: "crypto", InstrumentType: instrument, DatasetID: ready.DatasetID,
			SourceID: "spot_http", SeriesTag: periodE2ESeriesTag, Now: func() time.Time { return now },
		}, nil
	}
	invoker := &periodE2EInvoke{handler: handler, results: make(chan error, 5), ctx: ctx}
	newStorage := periodE2EStorageFactory(t, target)
	scheduler := &marketfetch.Scheduler{
		SpaceID: ready.SpaceID, Tasks: db.Tasks(), Batches: db.FetchBatches(), Instances: db.TaskInstances(), Retries: db.FetchRetries(),
		PeriodSeriesSnapshot: db.PeriodSeriesSnapshot(), PeriodStorageStates: db.PeriodStorageStates(),
		Symbols: storagesource.NewDatasetSource(periodE2EStorageOptions(t, target)), Invoker: invoker, Storage: newStorage,
		// Separate initial batches isolate the real provider session guard: an
		// ETH timeout should not turn the healthy BTC acquisition into a retry.
		BatchSize: 1, InvokeConcurrency: 1, MaxRetryAttempts: 3, Now: func() time.Time { return now },
	}
	t.Setenv("MOOX_SPACE_ID", ready.SpaceID)
	t.Setenv("MOOX_FETCH_MAX_RETRY_ATTEMPTS", "3")
	var retryKey string
	var initialBatchIDs []string
	for attempt := 0; attempt < 4; attempt++ {
		require.NoError(t, scheduler.Tick(ctx, ready.SpaceID))
		expectedInvocations := 1
		if attempt == 0 {
			expectedInvocations = 2
			planned, scanErr := db.FetchBatches().ListDue(ctx, ready.SpaceID, time.Now().UTC().Add(24*time.Hour), 10)
			require.NoError(t, scanErr)
			var plannedSubjects []string
			for _, batch := range planned {
				initialBatchIDs = append(initialBatchIDs, batch.BatchID)
				var request marketfetch.Request
				require.NoError(t, json.Unmarshal([]byte(batch.RequestJSON), &request))
				for _, item := range request.Items {
					plannedSubjects = append(plannedSubjects, item.SubjectID)
				}
			}
			t.Logf("initial period retry plan: batches=%d subjects=%v", len(planned), plannedSubjects)
			require.ElementsMatch(t, []string{"BTC-USDT", "ETH-USDT"}, plannedSubjects)
		}
		for range expectedInvocations {
			select {
			case invokeErr := <-invoker.results:
				require.NoError(t, invokeErr, "real worker must publish a failed-item Completion rather than fail invocation")
			case <-time.After(20 * time.Second):
				invoker.mu.Lock()
				calls := invoker.calls
				invoker.mu.Unlock()
				pending, scanErr := db.FetchRetries().ListDue(ctx, ready.SpaceID, now.Add(time.Hour), 10)
				batches, batchErr := db.FetchBatches().ListDue(ctx, ready.SpaceID, time.Now().UTC().Add(24*time.Hour), 10)
				batchStates := make([]string, 0, len(batches))
				for _, batch := range batches {
					batchStates = append(batchStates, fmt.Sprintf("%s:%s", batch.BatchID, batch.Status))
				}
				for _, batchID := range initialBatchIDs {
					batch, getErr := db.FetchBatches().Get(ctx, ready.SpaceID, batchID)
					if getErr != nil {
						batchStates = append(batchStates, fmt.Sprintf("%s:get_error=%v", batchID, getErr))
					} else {
						batchStates = append(batchStates, fmt.Sprintf("%s:%s", batchID, batch.Status))
					}
				}
				stack := make([]byte, 64*1024)
				n := runtime.Stack(stack, true)
				var activeStacks []string
				for _, goroutine := range strings.Split(string(stack[:n]), "\n\n") {
					if strings.Contains(goroutine, "periodE2EInvoke") || strings.Contains(goroutine, "dispatchPlanned") || strings.Contains(goroutine, "HandleWithFunctionName") || strings.Contains(goroutine, "publishCompletion") || strings.Contains(goroutine, "jetstream.Connect") || strings.Contains(goroutine, "PublishRaw") || strings.Contains(goroutine, "nats.") {
						activeStacks = append(activeStacks, goroutine)
					}
				}
				t.Fatalf("Scheduler did not dispatch retry invocation: generation=%d calls=%d queued_results=%d retries=%+v scan_error=%v batches=%v batch_error=%v\n%s", attempt, calls, len(invoker.results), pending, scanErr, batchStates, batchErr, strings.Join(activeStacks, "\n\n"))
			}
		}
		var retry *domain.RetryItem
		require.Eventually(t, func() bool {
			pending, scanErr := db.FetchRetries().ListDue(ctx, ready.SpaceID, now.Add(time.Hour), 10)
			if scanErr != nil {
				return false
			}
			if retryKey == "" {
				for _, candidate := range pending {
					if candidate.SubjectID == "ETH-USDT" {
						retryKey = candidate.RetryKey
					}
				}
			}
			if retryKey == "" {
				return false
			}
			retry, scanErr = db.FetchRetries().Get(ctx, ready.SpaceID, retryKey)
			return scanErr == nil && retry.Attempt == min(attempt+1, 3) && (attempt < 3 && retry.Status == "pending" || attempt == 3 && retry.Status == "permanent_failed")
		}, 5*time.Second, 20*time.Millisecond, "Completion must persist the expected retry generation")
		t.Logf("retry generation %d: attempt=%d status=%s error=%s", attempt, retry.Attempt, retry.Status, retry.LastErrorSummary)
		if attempt < 3 {
			require.NotNil(t, retry.NextRetryAt)
			now = retry.NextRetryAt.Add(time.Second)
		}
	}
	counts := make(map[string]int)
	for len(provider.requests) > 0 {
		counts[(<-provider.requests).SubjectID]++
	}
	require.Equal(t, map[string]int{"BTC-USDT": 1, "ETH-USDT": 4}, counts, "BTC is successful once; ETH executes initial plus exactly three retries")
	reporter := marketfetch.NewPeriodFailureReporter(db.FetchRetries(), newStorage, ready.SpaceID)
	require.NoError(t, reporter.RunOnce(ctx, ready.SpaceID))
	retry, err := db.FetchRetries().Get(ctx, ready.SpaceID, retryKey)
	require.NoError(t, err)
	require.Equal(t, domain.PeriodFailureReportAcknowledged, retry.PeriodFailureReportState)
	snapshot, found, err := db.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, domain.PeriodKey{
		SpaceID: ready.SpaceID, DatasetID: ready.DatasetID, Frequency: ready.Frequency, PeriodTime: now.Truncate(time.Hour).Add(-time.Hour),
	})
	require.NoError(t, err)
	require.True(t, found)
	storage, err := marketstorage.NewBatchStorage(periodE2EStorageOptions(t, target), marketstorage.InstTypeSPOT, periodE2EWriteSource)
	require.NoError(t, err)
	expectation := periodE2EStorageExpectation(snapshot, time.Time{})
	expectation.Frequency = ready.Frequency
	state, err := storage.GetDatasetPeriodStatus(ctx, expectation)
	require.NoError(t, err)
	require.Equal(t, domain.PeriodStatusWaiting, state.Status)
	require.NoError(t, replacePeriodE2EClock(ready.ClockFile, state.DeadlineAt))
	require.Eventually(t, func() bool {
		state, err = storage.GetDatasetPeriodStatus(ctx, expectation)
		return err == nil && state.Status == domain.PeriodStatusDegraded
	}, 5*time.Second, 20*time.Millisecond)
	_, marker := requirePeriodE2EMarker(t, inspectPeriodE2EOutbox(t, ctx, ready), expectation)
	require.Equal(t, "degraded", marker.GetStatus())
	require.Equal(t, []string{"BTC-USDT", "ETH-USDT"}, marker.GetUniverseSubjectIds())
	require.Equal(t, []string{"ETH-USDT"}, marker.GetFailedSubjects(), "only the exhausted series has an accepted failure receipt")
	t.Log("SCENARIO PASS initial-plus-three-retry-exhaustion")
}

func periodE2ESnapshot(expectation *storagegen.DatasetPeriodExpectation) domain.PeriodSeriesSnapshot {
	period := time.Unix(expectation.GetPeriodTime(), 0).UTC()
	snapshot := domain.PeriodSeriesSnapshot{
		Key:        domain.PeriodKey{SpaceID: expectation.GetSpaceId(), DatasetID: expectation.GetDatasetId(), Frequency: expectation.GetFrequency(), PeriodTime: period},
		SeriesHash: expectation.GetSeriesHash(), ExpectedCount: expectation.GetExpectedCount(),
	}
	for _, entry := range expectation.GetSeriesSnapshot() {
		snapshot.Entries = append(snapshot.Entries, domain.PeriodSeriesSnapshotEntry{
			SpaceID: snapshot.Key.SpaceID, DatasetID: snapshot.Key.DatasetID, Frequency: snapshot.Key.Frequency, PeriodTime: period,
			SeriesIndex: entry.GetSeriesIndex(), SubjectID: entry.GetSubjectId(), SeriesTag: entry.GetSeriesTag(),
			Provider: "binance", SourceID: "spot_http", MarketType: "spot", ProviderSymbol: "BTCUSDT",
			SeriesKey:  domain.CanonicalSeriesKey("binance", "spot_http", "spot", entry.GetSubjectId(), entry.GetSeriesTag()),
			SeriesHash: snapshot.SeriesHash, ExpectedCount: int(snapshot.ExpectedCount),
		})
	}
	return snapshot
}

func runPeriodCleanupRPCE2E(t *testing.T, ctx context.Context, root, target string, ready periodE2EStorageReady, clock time.Time) {
	t.Helper()
	db := openPeriodE2EDB(t, filepath.Join(root, "period-cleanup.db"))
	storage, err := marketstorage.NewBatchStorage(periodE2EStorageOptions(t, target), marketstorage.InstTypeSPOT, periodE2EWriteSource)
	require.NoError(t, err)
	period := clock.Add(-40 * 24 * time.Hour)
	deadline := clock.Add(time.Hour)
	waiting := periodE2EExpectation(ready, period, deadline, []string{"BTC-USDT"})
	missing := periodE2EExpectation(ready, period.Add(time.Minute), deadline, []string{"BTC-USDT"})
	complete := periodE2EExpectation(ready, period.Add(2*time.Minute), deadline, []string{"BTC-USDT"})
	for _, expectation := range []*storagegen.DatasetPeriodExpectation{waiting, missing, complete} {
		_, _, err := db.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, periodE2ESnapshot(expectation))
		require.NoError(t, err)
	}
	for _, expectation := range []*storagegen.DatasetPeriodExpectation{waiting, complete} {
		_, err := storage.EnsureDatasetPeriod(ctx, expectation)
		require.NoError(t, err)
	}
	require.NoError(t, storage.CommitTimeSeriesBatch(ctx, complete, []*storagegen.TimeSeriesBatchRow{{SeriesIndex: 0, Row: periodE2ERow(ready, time.Unix(complete.GetPeriodTime(), 0), "BTC-USDT", periodE2ESeriesTag, 1)}}, "cleanup-complete"))
	reconciler := marketfetch.NewPeriodStorageReconciler(db.PeriodSeriesSnapshot(), db.PeriodStorageStates(), storage, ready.SpaceID)
	// Cleanup retention is measured from Storage's terminal confirmation, not
	// only from the period timestamp. Advance the cleanup clock beyond that
	// retention window so the just-confirmed complete period is eligible.
	cleanupNow := time.Now().UTC().Add(30*24*time.Hour + time.Hour)
	deleted, err := reconciler.Reconcile(ctx, cleanupNow)
	require.Error(t, err, "NOT_FOUND must defer its snapshot rather than manufacture a period")
	require.Equal(t, int64(1), deleted, "only complete, expired and quiescent period may be deleted")
	for _, expectation := range []*storagegen.DatasetPeriodExpectation{waiting, missing} {
		_, found, err := db.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, periodE2ESnapshot(expectation).Key)
		require.NoError(t, err)
		require.True(t, found)
	}
	_, err = storage.GetDatasetPeriodStatus(ctx, missing)
	require.Error(t, err, "status query must not create a missing period")
	unreachable, err := marketstorage.NewBatchStorage(periodE2EStorageOptions(t, unusedPeriodE2ETarget(t)), marketstorage.InstTypeSPOT, periodE2EWriteSource)
	require.NoError(t, err)
	networkReconciler := marketfetch.NewPeriodStorageReconciler(db.PeriodSeriesSnapshot(), db.PeriodStorageStates(), unreachable, ready.SpaceID)
	deleted, err = networkReconciler.Reconcile(ctx, cleanupNow)
	require.Error(t, err)
	require.Zero(t, deleted, "network errors must not authorize cleanup")
	t.Log("SCENARIO PASS cleanup-waiting-not-found-network-retention")
	require.NoError(t, replacePeriodE2EClock(ready.ClockFile, deadline))
	var terminal domain.PeriodStorageState
	require.Eventually(t, func() bool {
		terminal, err = storage.GetDatasetPeriodStatus(ctx, waiting)
		return err == nil && terminal.Status == domain.PeriodStatusDegraded
	}, 5*time.Second, 20*time.Millisecond, "the real Storage finalizer must degrade the waiting period after the controlled deadline")
	waitPeriodE2EFinalizerCycles(t, ctx)
	baselineOutbox := inspectPeriodE2EOutbox(t, ctx, ready)
	baselineMarker, baselinePayload := requirePeriodE2EMarker(t, baselineOutbox, waiting)
	require.Equal(t, "degraded", baselinePayload.GetStatus())
	require.Equal(t, []string{"BTC-USDT"}, baselinePayload.GetUniverseSubjectIds())
	require.Empty(t, baselinePayload.GetFailedSubjects(), "deadline degradation does not invent recorded failure receipts")
	for replay := range 2 {
		_, err := storage.RecordDatasetPeriodFailures(ctx, waiting, []uint32{0})
		require.NoError(t, err)
		require.Error(t, storage.CommitTimeSeriesBatch(ctx, waiting, []*storagegen.TimeSeriesBatchRow{{SeriesIndex: 0, Row: periodE2ERow(ready, period, "BTC-USDT", periodE2ESeriesTag, 2)}}, "cleanup-late"))
		observed, err := storage.GetDatasetPeriodStatus(ctx, waiting)
		require.NoError(t, err)
		require.Equal(t, terminal.Key, observed.Key)
		require.Equal(t, terminal.SeriesHash, observed.SeriesHash)
		require.Equal(t, terminal.ExpectedCount, observed.ExpectedCount)
		require.Equal(t, terminal.DeadlineAt, observed.DeadlineAt)
		require.Equal(t, terminal.Status, observed.Status, "late replay must leave terminal authority unchanged; ConfirmedAt is a fresh client observation")
		require.NoError(t, replacePeriodE2EClock(ready.ClockFile, deadline.Add(time.Duration(replay+1)*time.Minute)))
		waitPeriodE2EFinalizerCycles(t, ctx)
		observedOutbox := inspectPeriodE2EOutbox(t, ctx, ready)
		observedMarker, observedPayload := requirePeriodE2EMarker(t, observedOutbox, waiting)
		require.Equal(t, baselineOutbox.Count, observedOutbox.Count, "late calls and real background-finalizer work must not append any outbox entry")
		require.Equal(t, baselineMarker.OutboxID, observedMarker.OutboxID)
		require.Equal(t, baselineMarker.EventID, observedMarker.EventID)
		require.JSONEq(t, string(baselineMarker.Payload), string(observedMarker.Payload))
		require.True(t, proto.Equal(baselinePayload, observedPayload), "persisted Marker payload, including timestamp and committed positions, must not change")
	}
	t.Log("SCENARIO PASS cleanup-marker-id-payload-outbox-immutable")
	item := domain.CollectionItem{SubjectID: "BTC-USDT", Provider: "binance", SourceID: "spot_http", MarketType: "spot", DatasetID: ready.DatasetID, Frequency: "1m", TargetDataTime: period.Format(time.RFC3339Nano)}
	itemJSON, err := json.Marshal(item)
	require.NoError(t, err)
	targetJSON, err := json.Marshal([]domain.WriteTarget{{ID: "cleanup-unresolved-target", SpaceID: ready.SpaceID, DatasetID: ready.DatasetID, SeriesIndex: 0, SeriesHash: waiting.GetSeriesHash(), ExpectedCount: 1}})
	require.NoError(t, err)
	unresolved := domain.RetryItem{
		SpaceID: ready.SpaceID, RetryKey: "cleanup-unresolved-failure", InstanceID: "cleanup-unresolved-instance", SubjectID: item.SubjectID,
		Frequency: "1m", TargetDataTime: period, TaskJSON: string(itemJSON), FailureTargetsJSON: string(targetJSON), Attempt: 3,
		Status: "permanent_failed", PeriodFailureReportState: domain.PeriodFailureReportPending,
	}
	require.NoError(t, db.FetchRetries().Upsert(ctx, &unresolved))
	blockedReconciler := marketfetch.NewPeriodStorageReconciler(db.PeriodSeriesSnapshot(), db.PeriodStorageStates(), storage, ready.SpaceID)
	deleted, err = blockedReconciler.Reconcile(ctx, clock)
	require.Error(t, err, "the missing independent period still defers reconciliation")
	require.Zero(t, deleted, "a terminal Storage period cannot release its snapshot while a failure receipt is unresolved")
	_, retained, err := db.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, periodE2ESnapshot(waiting).Key)
	require.NoError(t, err)
	require.True(t, retained)
	reporter := marketfetch.NewPeriodFailureReporter(db.FetchRetries(), periodE2EStorageFactory(t, target), ready.SpaceID)
	require.NoError(t, reporter.RunOnce(ctx, ready.SpaceID))
	settled, err := db.FetchRetries().Get(ctx, ready.SpaceID, unresolved.RetryKey)
	require.NoError(t, err)
	require.Equal(t, domain.PeriodFailureReportMissedDeadline, settled.PeriodFailureReportState)
	t.Log("SCENARIO PASS cleanup-unresolved-work-retention")
	// Fresh scan starts before the old cursor, including the newly degraded
	// period, while the still-missing period continues to defer cleanup.
	finalReconciler := marketfetch.NewPeriodStorageReconciler(db.PeriodSeriesSnapshot(), db.PeriodStorageStates(), storage, ready.SpaceID)
	deleted, err = finalReconciler.Reconcile(ctx, clock)
	require.Error(t, err)
	require.Equal(t, int64(1), deleted)
	_, found, err := db.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, periodE2ESnapshot(waiting).Key)
	require.NoError(t, err)
	require.False(t, found)
	_, found, err = db.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, periodE2ESnapshot(missing).Key)
	require.NoError(t, err)
	require.True(t, found)
	t.Log("SCENARIO PASS cleanup-complete-degraded-terminal-deletion")
}

func periodE2ERow(ready periodE2EStorageReady, period time.Time, subjectID, seriesTag string, close float64) *storagegen.RowFieldUpsert {
	return &storagegen.RowFieldUpsert{
		Key: &storagegen.RowKey{
			SpaceId: ready.SpaceID, DatasetId: ready.DatasetID,
			Kind: &storagegen.RowKey_TimeSeries{TimeSeries: &storagegen.TimeSeriesRowKey{
				SubjectId: subjectID, Freq: "1m", DataTime: period.UTC().Format(time.RFC3339Nano), SeriesTag: seriesTag,
			}},
		},
		Fields: []*storagegen.FieldValue{{FieldId: "close", Value: &storagegen.TypedValue{Value: &storagegen.TypedValue_DoubleValue{DoubleValue: close}}}},
	}
}

func replacePeriodE2EClock(path string, value time.Time) error {
	temp := path + ".next"
	if err := os.WriteFile(temp, []byte(value.UTC().Format(time.RFC3339Nano)), 0600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

func stripPeriodE2EURL(t *testing.T, target string) string {
	t.Helper()
	parsed, err := url.Parse(target)
	require.NoError(t, err)
	require.Equal(t, "ip", parsed.Scheme)
	return parsed.Host
}

func periodE2ERandomHex(t *testing.T, size int) string {
	t.Helper()
	raw := make([]byte, size)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	return hex.EncodeToString(raw)
}

func runStockCNPeriodTimerE2E(t *testing.T, ctx context.Context, root, gatewayBinary string, ready periodE2EStorageReady, secret, storageGatewayTarget string) {
	t.Helper()
	const (
		taskID     = "stockcn-period-timer-e2e-task"
		instanceID = "stockcn-period-timer-e2e-instance"
		function   = "moox-stockcn-period-e2e"
		runID      = "stockcn-period-timer-e2e-run"
	)

	metadata, err := marketstorage.NewResampleMetadataClient(periodE2EStorageOptions(t, storageGatewayTarget), marketstorage.InstTypeSPOT)
	require.NoError(t, err)
	clockNow := time.Date(2026, time.September, 30, 7, 30, 0, 0, time.UTC)
	require.NoError(t, replacePeriodE2EClock(ready.ClockFile, clockNow))
	stockTagItems := []*storagegen.TagSnapshotItem{{
		SubjectId: periodE2EStockSubjectID, SubjectType: "equity", Name: "浦发银行", Market: "XSHG", Currency: "CNY", Timezone: "Asia/Shanghai",
	}}
	response, err := metadata.Client.ApplyTagSnapshot(ctx, &storagegen.ApplyTagSnapshotReq{
		AuthInfo: metadata.Auth, SpaceId: ready.StockSpaceID, TagId: periodE2EStockTagID,
		RunAt: clockNow.UTC().Format(time.RFC3339Nano), Items: stockTagItems,
	})
	require.NoError(t, err)
	require.Equal(t, storagegen.ErrorCode_SUCCESS, response.GetRetInfo().GetCode(), response.GetRetInfo().GetMsg())
	subjectsResponse, err := metadata.Client.ResolveSubjects(ctx, &storagegen.ResolveSubjectsReq{
		AuthInfo: metadata.Auth, SpaceId: ready.StockSpaceID, TagIds: []string{periodE2EStockTagID},
	})
	require.NoError(t, err)
	require.Equal(t, storagegen.ErrorCode_SUCCESS, subjectsResponse.GetRetInfo().GetCode(), subjectsResponse.GetRetInfo().GetMsg())
	require.Len(t, subjectsResponse.GetSubjects(), 1)
	require.Equal(t, periodE2EStockSubjectID, subjectsResponse.GetSubjects()[0].GetSubjectId())

	calendarPath := periodE2EStockCalendarPath(t)
	calendar, err := stockcn.LoadCalendar(calendarPath)
	require.NoError(t, err)
	period, periodEnd, err := calendar.LatestClosedMinute(clockNow, 5*time.Second)
	require.NoError(t, err)
	expectedMinutes, err := calendar.ExpectedMinuteBars(period.In(calendar.Location()).Format("2006-01-02"))
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(expectedMinutes), 7, "the fixed session must supply independent valid targets for the Timer scenarios")
	require.Contains(t, expectedMinutes, period, "Timer target must be an actual StockCN trading minute")

	db, err := store.Open(&store.Options{Path: filepath.Join(root, "collector-period-e2e.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close E2E Collector database: %v", err)
		}
	})
	task := domain.CollectionTask{
		SpaceID: ready.StockSpaceID, TaskID: taskID, TaskName: "StockCN Timer period E2E", DataType: "kline",
		CollectParams: fmt.Sprintf(`{"market_id":%q,"instrument_type":"equity","source_id":%q,"target_dataset_id":%q,"frequency":"1m"}`,
			ready.StockSpaceID, periodE2EStockSourceID, ready.StockDatasetID),
		Enabled: true, PrepareState: domain.PrepareStateReady,
	}
	require.NoError(t, db.Tasks().Create(ctx, task))
	storedTask, err := db.Tasks().GetByTaskID(ctx, ready.StockSpaceID, taskID)
	require.NoError(t, err)
	task = *storedTask

	seriesKey := domain.CanonicalSeriesKey(periodE2EStockProviderID, periodE2EStockSourceID, "equity", periodE2EStockSubjectID, periodE2EStockSeriesTag)
	seriesHash := domain.SeriesSetHash([]string{seriesKey})
	assignment := marketfetch.NodeAssignment{
		NodeID: "stockcn-period-e2e-node", FunctionName: function, Region: "ap-shanghai",
		Provider: periodE2EStockProviderID, RouteProvider: "stockcn_multi", MarketType: "equity", MarketID: ready.StockSpaceID,
		InstrumentType: "equity", SourceID: periodE2EStockSourceID, DatasetID: ready.StockDatasetID, Frequency: "1m",
		RouteVersion: "stockcn-period-e2e-route-v1", GroupID: 0, GroupCount: 1, Enabled: true,
	}
	// Claim uses the repository's wall clock rather than TickTime. Its lease
	// stays live without requiring the historical market target to be current.
	deadline := time.Now().UTC().Truncate(time.Second).Add(30 * time.Minute)
	ensureStorage := func(ensureCtx context.Context, current domain.PeriodSeriesSnapshot) (domain.PeriodStorageState, error) {
		expectation := periodE2EStorageExpectation(current, deadline)
		storage, storageErr := marketstorage.NewBatchStorage(periodE2EStorageOptions(t, storageGatewayTarget), marketstorage.InstTypeSPOT, periodE2EWriteSource)
		if storageErr != nil {
			return domain.PeriodStorageState{}, storageErr
		}
		return storage.EnsureDatasetPeriod(ensureCtx, expectation)
	}
	makeTimerPlan := func(targetPeriod time.Time, runID string) marketfetch.TimerPeriodPlan {
		targetPeriod = targetPeriod.UTC()
		targetTime := targetPeriod
		planInstanceID := fmt.Sprintf("%s-%d", instanceID, targetPeriod.Unix())
		planTargetID := fmt.Sprintf("stockcn-period-e2e-write-target-%d", targetPeriod.Unix())
		snapshot := domain.PeriodSeriesSnapshot{
			Key:        domain.PeriodKey{SpaceID: ready.StockSpaceID, DatasetID: ready.StockDatasetID, Frequency: "1m", PeriodTime: targetPeriod},
			SeriesHash: seriesHash, ExpectedCount: 1,
			Entries: []domain.PeriodSeriesSnapshotEntry{{
				SpaceID: ready.StockSpaceID, DatasetID: ready.StockDatasetID, Frequency: "1m", PeriodTime: targetPeriod,
				SeriesIndex: 0, SeriesKey: seriesKey, SubjectID: periodE2EStockSubjectID,
				Provider: periodE2EStockProviderID, SourceID: periodE2EStockSourceID, MarketType: "equity",
				ProviderSymbol: "sh600000", SeriesTag: periodE2EStockSeriesTag, SeriesHash: seriesHash, ExpectedCount: 1,
			}},
		}
		item := domain.CollectionItem{
			InstanceID: planInstanceID, SubjectID: periodE2EStockSubjectID, Symbol: "sh600000", TargetDataTime: targetPeriod.Format(time.RFC3339Nano),
			Provider: periodE2EStockProviderID, SourceID: periodE2EStockSourceID, MarketID: ready.StockSpaceID,
			InstrumentType: "equity", MarketType: "equity", DataType: "kline", DatasetID: ready.StockDatasetID, Frequency: "1m",
			SeriesIndex: 0, SeriesHash: seriesHash, ExpectedCount: 1,
		}
		request := marketfetch.Request{
			SpaceID: ready.StockSpaceID, MarketID: ready.StockSpaceID, InstrumentType: "equity", DatasetID: ready.StockDatasetID,
			Frequency: "1m", Provider: periodE2EStockProviderID, SourceID: periodE2EStockSourceID, MarketType: "equity",
			Region: assignment.Region, NodeID: assignment.NodeID, FunctionName: assignment.FunctionName, GroupID: 0, GroupCount: 1,
			Items: []domain.CollectionItem{item},
		}
		return marketfetch.TimerPeriodPlan{
			Task: task, RunID: runID, RunCutoff: time.Now().UTC().Add(time.Minute), TaskModifyTime: task.ModifyTime,
			Snapshot: snapshot, Assignment: assignment, Request: request,
			Instances: []domain.TaskInstance{{
				SpaceID: ready.StockSpaceID, InstanceID: planInstanceID, RunID: runID, Provider: periodE2EStockProviderID,
				ProviderSymbol: "sh600000", MarketType: "equity", DataType: "kline", SubjectID: periodE2EStockSubjectID,
				Frequency: "1m", TargetDataTime: &targetTime, SourceID: periodE2EStockSourceID, SeriesTag: periodE2EStockSeriesTag, TaskParams: `{}`,
			}},
			Targets: []domain.WriteTarget{{
				ID: planTargetID, SpaceID: ready.StockSpaceID, InstanceID: planInstanceID,
				TaskID: taskID, DatasetID: ready.StockDatasetID, Status: "pending",
			}},
		}
	}
	planner := &marketfetch.TimerPeriodPlanner{
		Snapshots: db.PeriodSeriesSnapshot(), States: db.PeriodStorageStates(), Batches: db.TimerPeriodBatches(),
		EnsureStorage: ensureStorage, Now: time.Now,
		ValidTargetDataTime: func(frequency string, target time.Time) bool {
			if frequency != "1m" {
				return false
			}
			tradingMinutes, calendarErr := calendar.ExpectedMinuteBars(target.In(calendar.Location()).Format("2006-01-02"))
			if calendarErr != nil {
				return false
			}
			for _, minute := range tradingMinutes {
				if minute.Equal(target.UTC()) && !target.After(clockNow) {
					return true
				}
			}
			return false
		},
	}
	latestPlan := makeTimerPlan(period, runID)
	olderPeriod := expectedMinutes[len(expectedMinutes)-2]
	olderPlan := makeTimerPlan(olderPeriod, runID)
	for _, plan := range []marketfetch.TimerPeriodPlan{latestPlan, olderPlan} {
		planned, planErr := planner.Plan(ctx, plan)
		require.NoError(t, planErr)
		require.True(t, planned, "Timer plan must persist a claimable StockCN period")
		manifest, manifestErr := db.TimerPeriodBatches().GetByPeriod(ctx, plan.Snapshot.Key, 0)
		require.NoError(t, manifestErr)
		require.Equal(t, seriesHash, manifest.SeriesHash)
	}
	plannedOnSecondTick, err := planner.Plan(ctx, latestPlan)
	require.NoError(t, err)
	require.False(t, plannedOnSecondTick, "two ticks for the same period must not create a second initial batch")
	collectorDBPath := filepath.Join(root, "collector-period-e2e.db")
	require.NoError(t, db.Close())
	db, err = store.Open(&store.Options{Path: collectorDBPath})
	require.NoError(t, err)
	restartPlanner := &marketfetch.TimerPeriodPlanner{
		Snapshots: db.PeriodSeriesSnapshot(), States: db.PeriodStorageStates(), Batches: db.TimerPeriodBatches(),
		EnsureStorage: ensureStorage, Now: time.Now, ValidTargetDataTime: planner.ValidTargetDataTime,
	}
	restartPlan := latestPlan
	restartPlan.RunID = "stockcn-period-timer-e2e-restart-run"
	for index := range restartPlan.Instances {
		restartPlan.Instances[index].RunID = restartPlan.RunID
	}
	plannedAfterRestart, err := restartPlanner.Plan(ctx, restartPlan)
	require.NoError(t, err)
	require.False(t, plannedAfterRestart, "a restarted planner must reuse the existing period instead of creating a second initial batch")
	for _, plan := range []marketfetch.TimerPeriodPlan{latestPlan, olderPlan} {
		manifests, listErr := db.TimerPeriodBatches().ListByPeriod(ctx, plan.Snapshot.Key)
		require.NoError(t, listErr)
		require.Len(t, manifests, 1)
	}
	t.Log("SCENARIO PASS timer-restart-single-manifest")
	oldestManifest, err := db.TimerPeriodBatches().GetByPeriod(ctx, olderPlan.Snapshot.Key, 0)
	require.NoError(t, err)
	require.Equal(t, seriesHash, oldestManifest.SeriesHash)
	latestManifest, err := db.TimerPeriodBatches().GetByPeriod(ctx, latestPlan.Snapshot.Key, 0)
	require.NoError(t, err)

	collectorGatewayTarget := startPeriodCollectorRuntimeE2E(t, ctx, root, gatewayBinary, secret, db)
	configurePeriodE2EEventBus(t)
	completionCtx, stopCompletion := context.WithCancel(ctx)
	completionDone, err := marketfetch.StartCompletionConsumerWithDone(completionCtx, ready.StockSpaceID, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		stopCompletion()
		select {
		case <-completionDone:
		case <-time.After(5 * time.Second):
			t.Error("Completion consumer did not exit before dependency cleanup")
		}
	})

	fetched := make(chan marketdata.KlineRequest, 2)
	provider := &periodE2EStockFetcher{requests: fetched}
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(provider))
	router, err := marketdata.NewRouter(registry, 3, nil, nil)
	require.NoError(t, err)
	handler := marketfetch.NewHandler()
	handler.NewStorage = periodE2EStorageWriter(t, storageGatewayTarget)
	runtimeClient := periodE2ERuntimeClient(t, collectorGatewayTarget)
	handler.TimerRuntimeClient = runtimeClient
	handler.Now = func() time.Time { return clockNow }
	handler.NewStockKlinePipeline = func(storage marketfetch.Storage) (*marketfetch.KlinePipeline, error) {
		return &marketfetch.KlinePipeline{
			Router: router, Storage: storage, CandidateChain: []string{periodE2EStockProviderID}, RouteID: marketfetch.StockCNRouteID,
			SpaceID: ready.StockSpaceID, MarketID: ready.StockSpaceID, InstrumentType: marketdata.InstrumentEquity,
			DatasetID: ready.StockDatasetID, SourceID: periodE2EStockSourceID, SeriesTag: periodE2EStockSeriesTag,
			SettleDelay: 5 * time.Second, Calendar: calendar,
		}, nil
	}
	t.Setenv("MOOX_SPACE_ID", ready.StockSpaceID)
	t.Setenv("MOOX_MARKET_FETCH_GROUP_ID", "0")
	t.Setenv("MOOX_MARKET_FETCH_GROUP_COUNT", "1")
	t.Setenv("MOOX_MARKET_FETCH_BINDING_HASH", oldestManifest.BindingHash)
	t.Setenv("MOOX_FETCH_TIMEOUT_SECONDS", "60")
	t.Setenv("MOOX_FETCH_STORAGE_TIMEOUT_MS", "5000")
	t.Setenv("MOOX_FETCH_MAX_INFLIGHT_REQUESTS", "1")
	requestID := "stockcn-period-e2e-tick-1"
	concurrentClaimBatchID := claimPeriodE2EStockBatchConcurrently(t, ctx, collectorGatewayTarget, oldestManifest.BindingHash, function, requestID, clockNow)
	require.Equal(t, oldestManifest.BatchID, concurrentClaimBatchID, "the first Timer claim must select the oldest effective period")
	t.Log("SCENARIO PASS timer-oldest-first")
	timerResponse, err := handler.HandleTimerAt(ctx, requestID, function, clockNow)
	require.NoError(t, err)
	require.NotNil(t, timerResponse)
	require.True(t, timerResponse.Success, timerResponse.Message)
	require.Equal(t, "succeeded", timerResponse.Message)

	select {
	case providerRequest := <-fetched:
		require.Equal(t, olderPeriod, providerRequest.StartTime)
		require.Equal(t, olderPeriod.Add(time.Minute), providerRequest.EndTime)
		require.Equal(t, periodE2EStockSubjectID, providerRequest.SubjectID)
		require.Equal(t, "sh600000", providerRequest.ProviderSymbol)
	case <-ctx.Done():
		t.Fatal("StockCN Timer did not reach its configured provider")
	}

	var completed *domain.BatchInvocation
	require.Eventually(t, func() bool {
		completed, err = db.FetchBatches().Get(ctx, ready.StockSpaceID, oldestManifest.BatchID)
		return err == nil && completed.Status == domain.BatchStatusSucceeded
	}, 10*time.Second, 50*time.Millisecond, "completion event must reconcile the claimed Timer batch")
	secondTimerResponse, err := handler.HandleTimerAt(ctx, "stockcn-period-e2e-tick-2", function, clockNow)
	require.NoError(t, err)
	require.NotNil(t, secondTimerResponse)
	require.True(t, secondTimerResponse.Success, secondTimerResponse.Message)
	require.Equal(t, "succeeded", secondTimerResponse.Message)
	select {
	case providerRequest := <-fetched:
		require.Equal(t, period, providerRequest.StartTime)
		require.Equal(t, periodEnd, providerRequest.EndTime)
	case <-ctx.Done():
		t.Fatal("second StockCN Timer tick did not claim the next persisted period")
	}
	require.Eventually(t, func() bool {
		completed, err = db.FetchBatches().Get(ctx, ready.StockSpaceID, latestManifest.BatchID)
		return err == nil && completed.Status == domain.BatchStatusSucceeded
	}, 10*time.Second, 50*time.Millisecond, "second Timer completion must reconcile the next oldest period")
	oldPeriodExpectation := periodE2EStorageExpectation(olderPlan.Snapshot, deadline)
	latestPeriodExpectation := periodE2EStorageExpectation(latestPlan.Snapshot, deadline)
	periodState, err := marketstorage.NewBatchStorage(periodE2EStorageOptions(t, storageGatewayTarget), marketstorage.InstTypeSPOT, periodE2EWriteSource)
	require.NoError(t, err)
	oldStatus, err := periodState.GetDatasetPeriodStatus(ctx, oldPeriodExpectation)
	require.NoError(t, err)
	require.Equal(t, domain.PeriodStatusComplete, oldStatus.Status, "a real StockCN Timer Kline commit must complete the oldest Storage bitmap")
	require.Equal(t, deadline, oldStatus.DeadlineAt)
	latestStatus, err := periodState.GetDatasetPeriodStatus(ctx, latestPeriodExpectation)
	require.NoError(t, err)
	require.Equal(t, domain.PeriodStatusComplete, latestStatus.Status, "the next Timer tick must complete the next period")
	require.Equal(t, deadline, latestStatus.DeadlineAt)
	for _, manifest := range []*domain.TimerPeriodBatch{oldestManifest, latestManifest} {
		batch, err := db.FetchBatches().Get(ctx, ready.StockSpaceID, manifest.BatchID)
		require.NoError(t, err)
		var request marketfetch.Request
		require.NoError(t, json.Unmarshal([]byte(batch.RequestJSON), &request))
		for _, item := range request.Items {
			instance, err := db.TaskInstances().Get(ctx, ready.StockSpaceID, item.InstanceID)
			require.NoError(t, err)
			require.Equal(t, domain.InstanceStatusSuccess, instance.LastExecStatus)
		}
		for _, target := range request.Targets {
			persisted, err := db.TaskInstances().GetWriteTarget(ctx, ready.StockSpaceID, target.ID)
			require.NoError(t, err)
			require.Equal(t, "succeeded", persisted.Status)
		}
	}
	t.Log("SCENARIO PASS timer-completion-persistence")
	t.Log("SCENARIO PASS timer-nontrading-historical-commit")
	wrongPlan := makeTimerPlan(expectedMinutes[len(expectedMinutes)-3], runID)
	plannedWrong, err := restartPlanner.Plan(ctx, wrongPlan)
	require.NoError(t, err)
	require.True(t, plannedWrong)
	wrongManifest, err := db.TimerPeriodBatches().GetByPeriod(ctx, wrongPlan.Snapshot.Key, 0)
	require.NoError(t, err)
	wrongBatchID := claimPeriodE2EStockBatchConcurrently(t, ctx, collectorGatewayTarget, wrongManifest.BindingHash, function, "wrong-completion-claim", clockNow)
	rejectPeriodE2EWrongCompletion(t, ctx, db, ready.StockSpaceID, wrongBatchID)
	emptyPlan := makeTimerPlan(expectedMinutes[len(expectedMinutes)-4], runID)
	plannedEmpty, err := restartPlanner.Plan(ctx, emptyPlan)
	require.NoError(t, err)
	require.True(t, plannedEmpty)
	emptyManifest, err := db.TimerPeriodBatches().GetByPeriod(ctx, emptyPlan.Snapshot.Key, 0)
	require.NoError(t, err)
	provider.empty = true
	emptyResponse, err := handler.HandleTimerAt(ctx, "stockcn-period-e2e-empty-bars", function, clockNow)
	require.NoError(t, err)
	require.NotNil(t, emptyResponse)
	require.False(t, emptyResponse.Success, "an empty external-provider result cannot count as a successful Timer Commit")
	select {
	case request := <-fetched:
		require.Equal(t, emptyPlan.Snapshot.Key.PeriodTime, request.StartTime)
	case <-ctx.Done():
		t.Fatal("empty-bars scenario did not reach the external provider")
	}
	require.Eventually(t, func() bool {
		batch, err := db.FetchBatches().Get(ctx, ready.StockSpaceID, emptyManifest.BatchID)
		return err == nil && batch.Status.Terminal() && batch.SuccessCount == 0 && batch.RetryCount == 1
	}, 5*time.Second, 20*time.Millisecond, "the empty-bars Completion must persist failure effects")
	emptyStatus, err := periodState.GetDatasetPeriodStatus(ctx, periodE2EStorageExpectation(emptyPlan.Snapshot, deadline))
	require.NoError(t, err)
	require.Equal(t, domain.PeriodStatusWaiting, emptyStatus.Status)
	t.Log("SCENARIO PASS timer-empty-bars-not-success")
	provider.empty = false
	latePlan := makeTimerPlan(expectedMinutes[len(expectedMinutes)-5], runID)
	plannedLate, err := restartPlanner.Plan(ctx, latePlan)
	require.NoError(t, err)
	require.True(t, plannedLate)
	lateManifest, err := db.TimerPeriodBatches().GetByPeriod(ctx, latePlan.Snapshot.Key, 0)
	require.NoError(t, err)
	require.NoError(t, replacePeriodE2EClock(ready.ClockFile, deadline))
	lateResponse, err := handler.HandleTimerAt(ctx, "stockcn-period-e2e-first-late-commit", function, clockNow)
	require.NoError(t, err)
	require.NotNil(t, lateResponse)
	require.False(t, lateResponse.Success, "Storage rejecting the first Commit after deadline cannot produce worker success")
	select {
	case request := <-fetched:
		require.Equal(t, latePlan.Snapshot.Key.PeriodTime, request.StartTime)
	case <-ctx.Done():
		t.Fatal("late-commit scenario did not reach the external provider")
	}
	require.Eventually(t, func() bool {
		batch, err := db.FetchBatches().Get(ctx, ready.StockSpaceID, lateManifest.BatchID)
		return err == nil && batch.Status.Terminal() && batch.SuccessCount == 0
	}, 5*time.Second, 20*time.Millisecond)
	t.Log("SCENARIO PASS timer-first-late-commit-no-success")
	// Storage's controlled clock can remain before the lease while the real
	// Collector wall clock sees an expired unclaimed manifest.
	require.NoError(t, replacePeriodE2EClock(ready.ClockFile, clockNow))
	deadline = time.Now().UTC().Truncate(time.Second).Add(3 * time.Second)
	expiredPlan := makeTimerPlan(expectedMinutes[len(expectedMinutes)-6], runID)
	plannedExpired, err := restartPlanner.Plan(ctx, expiredPlan)
	require.NoError(t, err)
	require.True(t, plannedExpired)
	expiredManifest, err := db.TimerPeriodBatches().GetByPeriod(ctx, expiredPlan.Snapshot.Key, 0)
	require.NoError(t, err)
	// The Claim repository deliberately ignores caller TickTime for lease
	// authority. Its private wall clock requires one short real lease expiry;
	// market-session selection and all Storage deadlines remain controlled.
	leaseExpired := time.NewTimer(time.Until(deadline) + 10*time.Millisecond)
	defer leaseExpired.Stop()
	select {
	case <-leaseExpired.C:
	case <-ctx.Done():
		t.Fatal("short unclaimed Timer lease did not expire before the harness deadline")
	}
	expiredProxy := collectorpb.NewMarketFetchRuntimeClientProxy(periodE2EGateway(t, collectorGatewayTarget).ClientOptions()...)
	expiredClaim, err := expiredProxy.ClaimTimerBatch(ctx, &collectorpb.ClaimTimerBatchReq{
		SpaceId: ready.StockSpaceID, FunctionName: function, RequestId: "expired-unclaimed-request",
		GroupId: 0, GroupCount: 1, BindingHash: expiredManifest.BindingHash, TickTime: clockNow.Unix(),
	})
	require.NoError(t, err)
	require.Equal(t, collectorpb.ErrorCode_SUCCESS, expiredClaim.GetRetInfo().GetCode())
	require.False(t, expiredClaim.GetClaimed())
	recovery := &marketfetch.Scheduler{
		SpaceID: ready.StockSpaceID, Tasks: db.Tasks(), Batches: db.FetchBatches(), Retries: db.FetchRetries(),
		Instances: db.TaskInstances(), Invoker: periodE2EEmptyCloudNode{}, InvokeNonRealtimeOnly: true,
		Symbols: periodE2ESchedulerDatasetSource{subject: domain.Subject{
			SubjectID: periodE2EStockSubjectID, Name: periodE2EStockSubjectID, Status: "active",
		}},
		ResolveSymbol: func(_, _, _, _ string) (string, error) {
			return "sh600000", nil
		},
		Now: func() time.Time { return time.Now().UTC().Add(31 * time.Minute) },
	}
	require.NoError(t, recovery.Tick(ctx, ready.StockSpaceID))
	var expiredBatch *domain.BatchInvocation
	require.Eventually(t, func() bool {
		var getErr error
		expiredBatch, getErr = db.FetchBatches().Get(ctx, ready.StockSpaceID, expiredManifest.BatchID)
		return getErr == nil && expiredBatch.Status.Terminal()
	}, 5*time.Second, 20*time.Millisecond, "the maintenance pass must recover an expired unclaimed Timer batch")
	require.Equal(t, domain.BatchStatusTimedOut, expiredBatch.Status)
	require.Empty(t, expiredBatch.RequestID, "an expired batch must close without a fabricated Claim")
	t.Log("SCENARIO PASS timer-expired-unclaimed-recovery")
	storageCreated := 0
	newStorage := handler.NewStorage
	handler.NewStorage = func(market, source string) (marketfetch.Storage, error) {
		storageCreated++
		return newStorage(market, source)
	}
	handler.TimerRuntimeClient = periodE2ERuntimeClient(t, unusedPeriodE2ETarget(t))
	unreachableResponse, err := handler.HandleTimerAt(ctx, "stockcn-period-e2e-unreachable", function, clockNow)
	require.True(t, err != nil || unreachableResponse != nil && !unreachableResponse.Success, "Claim transport failure cannot return a successful worker response")
	if unreachableResponse != nil {
		require.False(t, unreachableResponse.Success)
	}
	require.Zero(t, storageCreated, "an unreachable Claim cannot construct a writer or fall back to ordinary Upsert")
	select {
	case <-fetched:
		t.Fatal("an unreachable Claim must not invoke the market-data provider")
	default:
	}
	t.Log("SCENARIO PASS timer-unreachable-claim-no-writer")
	handler.NewStorage = newStorage
	handler.TimerRuntimeClient = runtimeClient
	deadline = time.Now().UTC().Truncate(time.Second).Add(30 * time.Minute)
	publishPlan := makeTimerPlan(expectedMinutes[len(expectedMinutes)-7], "publish-failure-run")
	runPeriodTimerPublishRecoveryRPCE2E(t, ctx, gatewayBinary, ready, secret, storageGatewayTarget, handler, provider, fetched, publishPlan, deadline, clockNow)
	t.Log("SCENARIO PASS timer-restart-claim")
}

func runPeriodTimerPublishRecoveryRPCE2E(t *testing.T, ctx context.Context, gatewayBinary string, ready periodE2EStorageReady, secret, storageTarget string, handler *marketfetch.Handler, provider *periodE2EStockFetcher, fetched <-chan marketdata.KlineRequest, plan marketfetch.TimerPeriodPlan, deadline, clock time.Time) {
	t.Helper()
	root := t.TempDir()
	db := openPeriodE2EDB(t, filepath.Join(root, "publish-recovery.db"))
	require.NoError(t, db.Tasks().Create(ctx, plan.Task))
	task, err := db.Tasks().GetByTaskID(ctx, ready.StockSpaceID, plan.Task.TaskID)
	require.NoError(t, err)
	plan.Task = *task
	plan.TaskModifyTime = task.ModifyTime
	storage, err := marketstorage.NewBatchStorage(periodE2EStorageOptions(t, storageTarget), marketstorage.InstTypeSPOT, periodE2EWriteSource)
	require.NoError(t, err)
	planner := &marketfetch.TimerPeriodPlanner{
		Snapshots: db.PeriodSeriesSnapshot(), States: db.PeriodStorageStates(), Batches: db.TimerPeriodBatches(), Now: time.Now,
		ValidTargetDataTime: func(frequency string, target time.Time) bool {
			return frequency == "1m" && target.Equal(plan.Snapshot.Key.PeriodTime)
		},
		EnsureStorage: func(ctx context.Context, snapshot domain.PeriodSeriesSnapshot) (domain.PeriodStorageState, error) {
			return storage.EnsureDatasetPeriod(ctx, periodE2EStorageExpectation(snapshot, deadline))
		},
	}
	planned, err := planner.Plan(ctx, plan)
	require.NoError(t, err)
	require.True(t, planned)
	manifest, err := db.TimerPeriodBatches().GetByPeriod(ctx, plan.Snapshot.Key, 0)
	require.NoError(t, err)
	runtimeTarget := startPeriodCollectorRuntimeE2E(t, ctx, root, gatewayBinary, secret, db)
	configurePeriodE2EEventBus(t)
	completionCtx, cancel := context.WithCancel(ctx)
	done, err := marketfetch.StartCompletionConsumerWithDone(completionCtx, ready.StockSpaceID, db.FetchBatches(), db.FetchRetries(), db.TaskInstances(), nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("publish-recovery Completion consumer did not stop")
		}
	})
	handler.TimerRuntimeClient = periodE2ERuntimeClient(t, runtimeTarget)
	t.Setenv("MOOX_MARKET_FETCH_BINDING_HASH", manifest.BindingHash)
	busURL := os.Getenv("MOOX_EVENTBUS_NATS_URL")
	// Fail the actual JetStream publisher's transport, not a replacement Publish
	// callback. The already-connected real consumer remains idle until restore.
	t.Setenv("MOOX_EVENTBUS_NATS_URL", "nats://"+strings.TrimPrefix(unusedPeriodE2ETarget(t), "ip://"))
	response, err := handler.HandleTimerAt(ctx, "publish-failure-initial", plan.Assignment.FunctionName, clock)
	require.True(t, err != nil || response != nil && !response.Success, "a successful Commit with an unpublishable Completion must not claim worker success")
	select {
	case request := <-fetched:
		require.Equal(t, plan.Snapshot.Key.PeriodTime, request.StartTime)
	case <-ctx.Done():
		t.Fatal("publish-failure worker did not execute the provider")
	}
	initial, err := db.FetchBatches().Get(ctx, ready.StockSpaceID, manifest.BatchID)
	require.NoError(t, err)
	require.Equal(t, domain.BatchStatusDispatched, initial.Status, "without a Completion the real consumer must not settle the batch")
	state, err := storage.GetDatasetPeriodStatus(ctx, periodE2EStorageExpectation(plan.Snapshot, deadline))
	require.NoError(t, err)
	require.Equal(t, domain.PeriodStatusComplete, state.Status, "the commit really preceded the failed Completion publish")
	t.Setenv("MOOX_EVENTBUS_NATS_URL", busURL)
	provider.empty = true
	defer func() { provider.empty = false }()
	invoker := &periodE2EInvoke{handler: handler, results: make(chan error, 3), ctx: ctx}
	require.NotNil(t, initial.DeadlineAt)
	now := initial.DeadlineAt.Add(time.Second)
	scheduler := &marketfetch.Scheduler{
		SpaceID: ready.StockSpaceID, Tasks: db.Tasks(), Batches: db.FetchBatches(), Retries: db.FetchRetries(), Instances: db.TaskInstances(),
		Invoker: invoker, InvokeNonRealtimeOnly: true, InvokeConcurrency: 1, MaxRetryAttempts: 3, Now: func() time.Time { return now },
	}
	require.NoError(t, scheduler.Tick(ctx, ready.StockSpaceID))
	var timedOut *domain.BatchInvocation
	require.Eventually(t, func() bool {
		var getErr error
		timedOut, getErr = db.FetchBatches().Get(ctx, ready.StockSpaceID, manifest.BatchID)
		return getErr == nil && timedOut.Status.Terminal()
	}, 5*time.Second, 20*time.Millisecond, "the maintenance pass must recover the missed Completion")
	require.Equal(t, domain.BatchStatusTimedOut, timedOut.Status)
	pending, err := db.FetchRetries().ListDue(ctx, ready.StockSpaceID, now.Add(time.Hour), 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	retryKey := pending[0].RetryKey
	for attempt := 1; attempt <= 3; attempt++ {
		retry, err := db.FetchRetries().Get(ctx, ready.StockSpaceID, retryKey)
		require.NoError(t, err)
		now = retry.NextRetryAt.Add(time.Second)
		require.NoError(t, scheduler.Tick(ctx, ready.StockSpaceID))
		select {
		case err := <-invoker.results:
			require.NoError(t, err, "restored real publisher must deliver each failed retry Completion")
		case <-ctx.Done():
			t.Fatal("publish-failure timeout recovery did not dispatch its retry")
		}
		select {
		case request := <-fetched:
			require.Equal(t, plan.Snapshot.Key.PeriodTime, request.StartTime)
		case <-ctx.Done():
			t.Fatal("retry did not reach the real worker's external-provider seam")
		}
		require.Eventually(t, func() bool {
			retry, err = db.FetchRetries().Get(ctx, ready.StockSpaceID, retryKey)
			if err != nil {
				return false
			}
			if attempt == 3 {
				return retry.Status == "permanent_failed" && retry.Attempt == 3
			}
			return retry.Status == "pending" && retry.Attempt == attempt+1
		}, 5*time.Second, 20*time.Millisecond)
	}
	now = now.Add(time.Hour)
	require.NoError(t, scheduler.Tick(ctx, ready.StockSpaceID))
	invoker.mu.Lock()
	calls := invoker.calls
	invoker.mu.Unlock()
	require.Equal(t, 3, calls, "publish failure timeout recovery must cap Invoke retries at three")
	t.Log("SCENARIO PASS timer-publish-failure-timeout-three-retries")
}

func rejectPeriodE2EWrongCompletion(t *testing.T, ctx context.Context, db *store.Store, spaceID, batchID string) {
	t.Helper()
	before, err := db.FetchBatches().Get(ctx, spaceID, batchID)
	require.NoError(t, err)
	var original marketfetch.Request
	require.NoError(t, json.Unmarshal([]byte(before.RequestJSON), &original))
	instances := make(map[string]domain.TaskInstance)
	for _, item := range original.Items {
		instance, err := db.TaskInstances().Get(ctx, spaceID, item.InstanceID)
		require.NoError(t, err)
		instances[item.InstanceID] = instance
	}
	targets := make(map[string]domain.WriteTarget)
	for _, target := range original.Targets {
		persisted, err := db.TaskInstances().GetWriteTarget(ctx, spaceID, target.ID)
		require.NoError(t, err)
		targets[target.ID] = persisted
	}
	client, err := jetstream.Connect(ctx, jetstream.ConfigFromEnv(nil, "period-e2e-wrong-completion"))
	require.NoError(t, err)
	defer client.Close()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	publisher, err := events.NewPublisher(client, registry)
	require.NoError(t, err)
	payload := &marketfetchpb.MarketFetchBatchCompleted{
		BatchId: batchID, ScheduleId: before.ScheduleID, BatchKind: string(before.BatchKind),
		DatasetId: original.DatasetID, NodeId: original.NodeID, Frequency: before.Frequency, PlannedCount: int32(before.PlannedCount),
		RequestId: "wrong-runtime-request", Status: string(domain.BatchStatusSucceeded), SuccessCount: 1,
		CompletedAt: timestamppb.Now(),
	}
	for _, item := range original.Items {
		payload.Items = append(payload.Items, &marketfetchpb.MarketFetchItemResult{
			InstanceId: item.InstanceID, SubjectId: item.SubjectID, TargetDataTime: item.TargetDataTime, Outcome: "success",
		})
	}
	ack, err := publisher.Publish(ctx, events.MarketFetchBatchCompleted, payload,
		events.PublishOptions{EventID: batchID, SpaceID: spaceID, SubjectID: original.DatasetID, OccurredAt: time.Now().UTC()})
	require.NoError(t, err)
	nc, err := nats.Connect(os.Getenv("MOOX_EVENTBUS_NATS_URL"))
	require.NoError(t, err)
	defer nc.Close()
	js, err := nc.JetStream()
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		info, err := js.ConsumerInfo(events.MarketFetchBatchCompleted.Stream(), "collector-market-fetch-completion-v1-"+spaceID)
		return err == nil && info.AckFloor.Stream >= ack.Sequence
	}, 5*time.Second, 20*time.Millisecond, "the real Completion consumer must terminate the wrong-runtime event")
	after, err := db.FetchBatches().Get(ctx, spaceID, batchID)
	require.NoError(t, err)
	require.Equal(t, before, after, "wrong-runtime Completion must have no persistent batch effects")
	for id, before := range instances {
		after, err := db.TaskInstances().Get(ctx, spaceID, id)
		require.NoError(t, err)
		require.Equal(t, before, after)
	}
	for id, before := range targets {
		after, err := db.TaskInstances().GetWriteTarget(ctx, spaceID, id)
		require.NoError(t, err)
		require.Equal(t, before, after)
	}
	count, err := db.FetchRetries().CountPending(ctx, spaceID, "", "")
	require.NoError(t, err)
	require.Zero(t, count)
	t.Log("SCENARIO PASS timer-wrong-runtime-completion-no-effects")
}

func periodE2EStorageExpectation(snapshot domain.PeriodSeriesSnapshot, deadline time.Time) *storagegen.DatasetPeriodExpectation {
	expectation := &storagegen.DatasetPeriodExpectation{
		SpaceId: snapshot.Key.SpaceID, DatasetId: snapshot.Key.DatasetID, Frequency: snapshot.Key.Frequency,
		PeriodTime: snapshot.Key.PeriodTime.UTC().Unix(), SeriesHash: snapshot.SeriesHash,
		ExpectedCount: snapshot.ExpectedCount, DeadlineAt: deadline.UTC().Unix(),
	}
	for _, entry := range snapshot.Entries {
		expectation.SeriesSnapshot = append(expectation.SeriesSnapshot, &storagegen.DatasetPeriodSeries{
			SeriesIndex: entry.SeriesIndex, SubjectId: entry.SubjectID, SeriesTag: entry.SeriesTag,
		})
	}
	return expectation
}

func claimPeriodE2EStockBatchConcurrently(t *testing.T, ctx context.Context, target, bindingHash, functionName, requestID string, tick time.Time) string {
	t.Helper()
	type claimResult struct {
		response *collectorpb.ClaimTimerBatchRsp
		err      error
	}
	results := make(chan claimResult, 2)
	options := periodE2EGateway(t, target).ClientOptions()
	for range 2 {
		go func() {
			proxy := collectorpb.NewMarketFetchRuntimeClientProxy(options...)
			claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			response, err := proxy.ClaimTimerBatch(claimCtx, &collectorpb.ClaimTimerBatchReq{
				SpaceId: "stockcn", FunctionName: functionName, RequestId: requestID,
				GroupId: 0, GroupCount: 1, BindingHash: bindingHash, TickTime: tick.UTC().Unix(),
			})
			results <- claimResult{response: response, err: err}
		}()
	}
	// Drain both bounded RPC calls before asserting so even a failed Claim
	// does not leave a harness goroutine racing server or database teardown.
	claims := make([]claimResult, 0, 2)
	for range 2 {
		claims = append(claims, <-results)
	}
	var batchIDs []string
	for _, result := range claims {
		require.NoError(t, result.err)
		require.NotNil(t, result.response)
		require.Equal(t, collectorpb.ErrorCode_SUCCESS, result.response.GetRetInfo().GetCode(), result.response.GetRetInfo().GetMsg())
		require.True(t, result.response.GetClaimed())
		var request marketfetch.Request
		require.NoError(t, json.Unmarshal(result.response.GetRequestJson(), &request))
		batchIDs = append(batchIDs, request.BatchID)
	}
	require.Len(t, batchIDs, 2)
	require.Equal(t, batchIDs[0], batchIDs[1], "a repeated concurrent Timer request ID must replay its single bound batch")
	t.Log("SCENARIO PASS timer-concurrent-same-request-claim")
	return batchIDs[0]
}

func periodE2EStockCalendarPath(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "../../../.."))
	return filepath.Join(repoRoot, "modules", "collector", "config", "markets", "stockcn", "calendar.yaml")
}

func startPeriodCollectorRuntimeE2E(t *testing.T, ctx context.Context, root, gatewayBinary, secret string, db *store.Store) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	service := server.New(server.WithServiceName("trpc.moox.collector.MarketFetchRuntime"), server.WithProtocol("trpc"), server.WithNetwork("tcp"), server.WithListener(listener), server.WithServerAsync(false))
	collectorpb.RegisterMarketFetchRuntimeService(service, collectorrpc.NewMarketFetchRuntime(&marketfetch.TimerBatchClaimer{Batches: db.TimerPeriodBatches()}))
	done := make(chan error, 1)
	go func() { done <- service.Serve() }()
	t.Cleanup(func() {
		closed := make(chan struct{}, 1)
		if err := service.Close(closed); err != nil {
			t.Errorf("close Collector runtime E2E listener: %v", err)
		}
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Error("Collector runtime E2E listener shutdown timed out")
		}
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Collector runtime E2E Serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Collector runtime E2E Serve goroutine did not exit")
		}
	})

	readyFile := filepath.Join(root, "collector-runtime-gateway-ready")
	args := []string{
		"-host-id=" + periodE2EGatewayNodeID + "-runtime",
		// 生产中 SCF 经外部接入领取批次；这里直接以 collector 身份调用，所以显式放行 ClaimTimerBatch。
		"-route=trpc.moox.collector.MarketFetchRuntime=" + listener.Addr().String(),
		"-callers=collector", "-methods=ClaimTimerBatch", "-listen-addr=127.0.0.1:0", "-ready-file=" + readyFile,
		"-nonce-dir=" + filepath.Join(root, "runtime-gateway-nonces"),
	}
	process := startPeriodE2EProcess(t, gatewayBinary, args, readyFile, map[string]string{"MOOX_GATEWAY_E2E_KEYS": periodE2EGatewayKeys(secret)})
	var target string
	waitPeriodE2EReady(t, ctx, process, readyFile, func(raw []byte) error { target = strings.TrimSpace(string(raw)); return nil })
	parsed, err := url.Parse(target)
	require.NoError(t, err)
	require.Equal(t, "ip", parsed.Scheme)
	periodE2ESecrets.Store(target, secret)
	return target
}

func configurePeriodE2EEventBus(t *testing.T) {
	t.Helper()
	server := testkit.Start(t)
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	pattern, err := registry.FamilyPattern(events.MarketFetchBatchCompleted)
	require.NoError(t, err)
	server.AddStream(t, &nats.StreamConfig{
		Name: events.MarketFetchBatchCompleted.Stream(), Subjects: []string{pattern},
		Storage: nats.MemoryStorage, Retention: nats.LimitsPolicy,
	})
	t.Setenv("MOOX_EVENTBUS_NATS_URL", server.URL())
	for _, name := range []string{
		"MOOX_EVENTBUS_URL", "NATS_URL", "MOOX_EVENTBUS_NATS_USERNAME", "MOOX_EVENTBUS_USERNAME",
		"MOOX_EVENTBUS_NATS_PASSWORD", "MOOX_EVENTBUS_PASSWORD", "MOOX_EVENTBUS_NATS_CREDENTIALS",
		"MOOX_EVENTBUS_CREDENTIALS", "MOOX_EVENTBUS_NATS_TLS_CA_FILE", "MOOX_EVENTBUS_TLS_CA",
		"MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64", "MOOX_EVENTBUS_NATS_TLS_CERT_FILE", "MOOX_EVENTBUS_TLS_CERT",
		"MOOX_EVENTBUS_NATS_TLS_KEY_FILE", "MOOX_EVENTBUS_TLS_KEY", "MOOX_EVENTBUS_CREDENTIAL_FILE",
	} {
		t.Setenv(name, "")
	}
}

type periodE2EStockFetcher struct {
	requests chan<- marketdata.KlineRequest
	empty    bool
}

func (p *periodE2EStockFetcher) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{
		ID: periodE2EStockProviderID, SourceID: periodE2EStockSourceID, DisplayName: "E2E Sina fixture",
		Hosts: []string{"fixture.invalid"}, Status: marketdata.SourceEnabled,
	}
}

func (p *periodE2EStockFetcher) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{
		Markets: []string{"stockcn"}, Exchanges: []string{"XSHG"}, Instruments: []marketdata.InstrumentType{marketdata.InstrumentEquity},
		Frequencies: []string{"1m"}, CompleteOHLCV: true, HasAmount: true, MaxBarsPerRequest: 1,
		TimestampMode: marketdata.TimestampModeOpen,
		RateLimit:     marketdata.RateLimitPolicy{RequestsPerSecond: 10, Burst: 1, MaxConcurrent: 1, Cooldown: time.Second, RequestTimeout: 2 * time.Second},
		History:       marketdata.KlineHistoryCapability{MaxLookback: 30 * 24 * time.Hour},
	}
}

func (p *periodE2EStockFetcher) FetchKlines(ctx context.Context, request marketdata.KlineRequest) ([]marketdata.NormalizedKline, error) {
	select {
	case p.requests <- request:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if p.empty {
		return nil, nil
	}
	start := request.StartTime.UTC()
	end := request.EndTime.UTC()
	return []marketdata.NormalizedKline{{
		SubjectID: request.SubjectID, ProviderID: periodE2EStockProviderID, SourceID: periodE2EStockSourceID,
		ProviderSymbol: request.ProviderSymbol, Frequency: request.Frequency, BarStart: start, BarEnd: end,
		Open: 10, High: 11, Low: 9, Close: 10.5, VolumeShares: 100, AmountCNY: 1050,
		ProviderTimestamp: end, FetchedAt: time.Now().UTC(), RequestID: request.RequestID,
	}}, nil
}
