package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	metadataservice "github.com/mooyang-code/moox/modules/storage/internal/service/catalog"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/sqlite"
	primarystore "github.com/mooyang-code/moox/modules/storage/internal/service/primarystore"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/server"
)

type periodProcessReady struct {
	PrimaryTarget  string `json:"primary_target"`
	MetadataTarget string `json:"metadata_target"`
	DataNodeTarget string `json:"data_node_target"`
	NodeID         string `json:"node_id"`
	SpaceID        string `json:"space_id"`
	DatasetID      string `json:"dataset_id"`
	Frequency      string `json:"frequency"`
	StockSpaceID   string `json:"stock_space_id"`
	StockDatasetID string `json:"stock_dataset_id"`
	OutboxTarget   string `json:"outbox_target"`
	ClockFile      string `json:"clock_file"`
	DataDir        string `json:"data_dir"`
	AppID          string `json:"app_id"`
	PrimaryAppKey  string `json:"primary_app_key"`
	MetadataAppKey string `json:"metadata_app_key"`
	NodeAppKey     string `json:"node_app_key"`
	PrimarySecret  string `json:"primary_secret"`
	NodeSecret     string `json:"node_secret"`
}

type periodHelperOutboxSnapshot struct {
	OutboxCount            int                             `json:"outbox_count"`
	CollectorPeriodMarkers []periodHelperOutboxMarkerEntry `json:"collector_period_markers"`
}

type periodHelperOutboxMarkerEntry struct {
	OutboxID   uint64          `json:"outbox_id"`
	EventID    string          `json:"event_id"`
	SpaceID    string          `json:"space_id"`
	DatasetID  string          `json:"dataset_id"`
	Frequency  string          `json:"frequency"`
	PeriodTime int64           `json:"period_time"`
	Payload    json.RawMessage `json:"payload"`
}

// Compile with go test -c; the harness atomically replaces clock_file with a
// UTC RFC3339Nano timestamp to advance the real Pebble period finalizer.
func TestPeriodNativeProcessHelper(t *testing.T) {
	if os.Getenv("MOOX_PERIOD_E2E_HELPER") != "1" {
		t.Skip("native period process helper requires MOOX_PERIOD_E2E_HELPER=1")
	}
	readyPath := os.Getenv("MOOX_PERIOD_E2E_READY_FILE")
	if !filepath.IsAbs(readyPath) {
		t.Fatal("MOOX_PERIOD_E2E_READY_FILE must be an absolute path")
	}
	if _, err := os.Lstat(readyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ready file must not exist: %v", err)
	}
	signals, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	startup, cancelStartup := context.WithTimeout(signals, 15*time.Second)
	defer cancelStartup()

	root, err := os.MkdirTemp("", "moox-period-storage-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove helper data directory: %v", err)
		}
	})
	ready := periodProcessReady{
		NodeID: "period-e2e-node", SpaceID: "period-e2e-space", DatasetID: "period-e2e-dataset",
		Frequency: "1h", StockSpaceID: "stockcn", StockDatasetID: "dataset_stockcn_equity_kline_1m",
		AppID: "moox-collector", DataDir: root,
		ClockFile: filepath.Join(root, "clock"), PrimarySecret: periodHelperSecret(t), NodeSecret: periodHelperSecret(t),
	}
	ready.PrimaryAppKey = datanode.ServiceAuthKey(ready.PrimarySecret, ready.AppID)
	ready.MetadataAppKey = ready.PrimaryAppKey
	ready.NodeAppKey = datanode.ServiceAuthKey(ready.NodeSecret, ready.AppID)
	if err := os.WriteFile(ready.ClockFile, []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0600); err != nil {
		t.Fatal(err)
	}
	clockErrors := make(chan error, 1)
	clock := func() time.Time {
		now, err := periodHelperReadClock(ready.ClockFile)
		if err != nil {
			select {
			case clockErrors <- err:
			default:
			}
			// Invalid clock input cannot advance a deadline while shutdown runs.
			return time.Time{}
		}
		return now
	}
	if _, err := periodHelperReadClock(ready.ClockFile); err != nil {
		t.Fatal(err)
	}
	node, err := datanode.NewService(datanode.Options{
		NodeID: ready.NodeID, AuthSecret: ready.NodeSecret,
		Pebble: pebble.Options{NodeID: ready.NodeID, Path: filepath.Join(root, "pebble"), PeriodNow: clock},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := node.Close(); err != nil {
			t.Errorf("close helper Pebble: %v", err)
		}
	})
	listeners := make(chan error, 4)
	dn, target := periodHelperListener(t, "trpc.moox.storage.DataNodeRuntime")
	ready.DataNodeTarget = target
	pb.RegisterDataNodeRuntimeService(dn, node)
	pb.RegisterDataNodePeriodRuntimeService(dn, node)
	pb.RegisterDataNodeDatasetAdminRuntimeService(dn, node)
	pb.RegisterDataNodeMarkerRuntimeService(dn, node)
	pb.RegisterDataNodeHistoryRuntimeService(dn, node)
	periodHelperServe(t, dn, listeners)

	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate Storage schema")
	}
	meta, err := metasqlite.Open(startup, metasqlite.Options{
		Path: filepath.Join(root, "metadata.db"), SchemaPath: filepath.Join(filepath.Dir(sourcePath), "..", "..", "schema", "metadata.sql"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := meta.Close(); err != nil {
			t.Errorf("close helper Metadata SQLite: %v", err)
		}
	})
	if err := meta.InitSchema(startup); err != nil {
		t.Fatal(err)
	}
	if err := meta.ValidateSchemaVersion(startup); err != nil {
		t.Fatal(err)
	}
	periodHelperSeed(t, startup, meta, ready)
	cached, err := metacache.New(startup, meta, metacache.Options{RefreshInterval: metacache.RefreshDisabled})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cached.Close(); err != nil {
			t.Errorf("close helper Metadata cache: %v", err)
		}
	})
	primary, err := primarystore.New(primarystore.Options{
		Resolver:  newDataNodeResolver(cached.RequestSnapshot, nil),
		Validator: primarystore.NewMetadataValidator(cached), Snapshot: cached.RequestSnapshot,
		Authorizer: func(auth *pb.AuthInfo) error {
			if auth == nil || auth.GetAppId() == "" || !hmac.Equal([]byte(auth.GetAppKey()), []byte(datanode.ServiceAuthKey(ready.PrimarySecret, auth.GetAppId()))) {
				return errors.New("invalid helper primary auth")
			}
			return nil
		},
		AuthSigner: func(auth *pb.AuthInfo) (*pb.AuthInfo, error) {
			if auth == nil {
				return nil, errors.New("auth_info is required")
			}
			signed := proto.Clone(auth).(*pb.AuthInfo)
			signed.AppKey = datanode.ServiceAuthKey(ready.NodeSecret, signed.GetAppId())
			return signed, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	metadataRPC, err := metadataservice.NewMetadataService(meta, cached, metadataservice.Options{AuthSecret: ready.NodeSecret, OperatorAuthSecret: ready.PrimarySecret})
	if err != nil {
		t.Fatal(err)
	}
	ps, target := periodHelperListener(t, "trpc.moox.storage.PrimaryStore")
	ready.PrimaryTarget = target
	pb.RegisterPrimaryStoreService(ps, primary)
	periodHelperServe(t, ps, listeners)
	ms, target := periodHelperListener(t, "trpc.moox.storage.Metadata")
	ready.MetadataTarget = target
	pb.RegisterMetadataService(ms, metadataRPC)
	periodHelperServe(t, ms, listeners)
	outboxListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ready.OutboxTarget = outboxListener.Addr().String()
	periodHelperServeOutbox(t, outboxListener, node, listeners)
	if err := periodHelperProbe(startup, ready); err != nil {
		t.Fatalf("helper RPC readiness: %v", err)
	}
	encoded, err := json.Marshal(ready)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(readyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(readyPath) })
	if _, err := f.Write(encoded); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("period Storage helper ready: primary=%s metadata=%s node=%s", ready.PrimaryTarget, ready.MetadataTarget, ready.DataNodeTarget)
	select {
	case <-signals.Done():
		t.Log("period Storage helper stopping on signal")
	case err := <-clockErrors:
		t.Fatalf("invalid helper clock: %v", err)
	case err := <-listeners:
		t.Fatalf("helper listener stopped before shutdown: %v", err)
	}
}

func periodHelperSecret(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(raw)
}

func periodHelperReadClock(path string) (time.Time, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, err
	}
	now, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(raw)))
	if err != nil || now.IsZero() || now.Location() != time.UTC {
		return time.Time{}, fmt.Errorf("clock must contain a nonzero UTC RFC3339Nano timestamp: %q", raw)
	}
	return now, nil
}

func periodHelperListener(t *testing.T, name string) (server.Service, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	service := server.New(server.WithServiceName(name), server.WithProtocol("trpc"), server.WithNetwork("tcp"), server.WithListener(listener), server.WithServerAsync(false))
	return service, "ip://" + listener.Addr().String()
}

func periodHelperServe(t *testing.T, service server.Service, failures chan<- error) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		err := service.Serve()
		done <- err
		failures <- err
	}()
	t.Cleanup(func() {
		closed := make(chan struct{}, 1)
		if err := service.Close(closed); err != nil {
			t.Errorf("close helper listener: %v", err)
		}
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Error("helper listener shutdown timed out")
		}
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("helper listener Serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("helper listener goroutine did not exit")
		}
	})
}

func periodHelperServeOutbox(t *testing.T, listener net.Listener, node *datanode.Service, failures chan<- error) {
	t.Helper()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/__test/outbox" {
			http.NotFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		entries, err := node.Store().ListOutbox(ctx, 0, 10000)
		if err != nil {
			http.Error(w, "read outbox failed", http.StatusInternalServerError)
			return
		}
		if len(entries) == 10000 {
			http.Error(w, "outbox exceeds test inspection limit", http.StatusInsufficientStorage)
			return
		}
		snapshot := periodHelperOutboxSnapshot{OutboxCount: len(entries)}
		for _, entry := range entries {
			message := &eventpb.EventMessage{}
			if err := proto.Unmarshal(entry.Data, message); err != nil {
				http.Error(w, "decode outbox event failed", http.StatusInternalServerError)
				return
			}
			if message.GetEventName() != events.CollectorPeriodCompleted.Name() {
				continue
			}
			payload := &storageeventpb.CollectorPeriodCompleted{}
			if err := proto.Unmarshal(message.GetPayload(), payload); err != nil {
				http.Error(w, "decode collector period marker failed", http.StatusInternalServerError)
				return
			}
			encodedPayload, err := protojson.Marshal(payload)
			if err != nil {
				http.Error(w, "encode collector period marker failed", http.StatusInternalServerError)
				return
			}
			snapshot.CollectorPeriodMarkers = append(snapshot.CollectorPeriodMarkers, periodHelperOutboxMarkerEntry{
				OutboxID: entry.ID, EventID: message.GetEventId(), SpaceID: message.GetSpaceId(),
				DatasetID: payload.GetDatasetId(), Frequency: payload.GetFrequency(), PeriodTime: payload.GetPeriodTime(), Payload: encodedPayload,
			})
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snapshot)
	})}
	done := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		done <- err
		failures <- err
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			t.Errorf("shutdown helper outbox endpoint: %v", err)
			_ = server.Close()
		}
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("helper outbox endpoint: %v", err)
			}
		case <-ctx.Done():
			t.Error("helper outbox endpoint did not stop")
		}
	})
}

func periodHelperSeed(t *testing.T, ctx context.Context, meta *metasqlite.Store, ready periodProcessReady) {
	t.Helper()
	if _, err := meta.CreateSpace(ctx, &pb.Space{SpaceId: ready.SpaceID, Name: "Period E2E"}); err != nil {
		t.Fatal(err)
	}
	if _, err := meta.UpsertDataSource(ctx, &pb.DataSource{
		SpaceId: ready.SpaceID, DataSourceId: "binance", Name: "Period E2E source", Kind: "exchange",
		Market: "crypto", Timezone: "UTC", Status: "active",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := meta.CreateTag(ctx, &pb.Tag{
		SpaceId: ready.SpaceID, TagId: "period_e2e", TagName: "Period E2E", Mode: "auto",
		Source: "binance", MarketType: "spot",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := meta.RegisterDataNode(ctx, ready.NodeID, ready.DataNodeTarget, "Period E2E node"); err != nil {
		t.Fatal(err)
	}
	dataset, err := meta.CreateDataset(ctx, &pb.Dataset{
		SpaceId: ready.SpaceID, DatasetId: ready.DatasetID, DataNodeId: ready.NodeID, Name: "Period E2E raw data",
		DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: ready.Frequency,
		Attributes: map[string]string{"owner_module": "collector", "dataset_role": "raw_collection", "collector_task_id": "period-e2e-task"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"open", "high", "low", "close", "volume", "amount"} {
		if _, err := meta.UpsertDatasetColumn(ctx, &pb.DatasetColumn{
			SpaceId: ready.SpaceID, DatasetId: ready.DatasetID, ColumnName: name,
			OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_SYSTEM, OriginId: name,
			ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := meta.CommitDatasetActivation(ctx, ready.SpaceID, ready.DatasetID, dataset.GetRevision()); err != nil {
		t.Fatal(err)
	}
	periodHelperSeedStockCN(t, ctx, meta, ready)
}

func periodHelperSeedStockCN(t *testing.T, ctx context.Context, meta *metasqlite.Store, ready periodProcessReady) {
	t.Helper()
	if _, err := meta.CreateSpace(ctx, &pb.Space{SpaceId: ready.StockSpaceID, Name: "StockCN Period E2E"}); err != nil {
		t.Fatal(err)
	}
	if _, err := meta.UpsertDataSource(ctx, &pb.DataSource{
		SpaceId: ready.StockSpaceID, DataSourceId: "sina", Name: "StockCN Period E2E source", Kind: "exchange",
		Market: "stockcn", Timezone: "Asia/Shanghai", Status: "active",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := meta.CreateTag(ctx, &pb.Tag{
		SpaceId: ready.StockSpaceID, TagId: "stockcn_period_e2e", TagName: "StockCN Period E2E", Mode: "auto",
		Source: "sina", MarketType: "equity",
	}); err != nil {
		t.Fatal(err)
	}
	dataset, err := meta.CreateDataset(ctx, &pb.Dataset{
		SpaceId: ready.StockSpaceID, DatasetId: ready.StockDatasetID, DataNodeId: ready.NodeID,
		Name: "StockCN Period E2E Kline", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1m",
		Attributes: map[string]string{"owner_module": "collector", "dataset_role": "raw_collection", "collector_task_id": "stockcn-period-e2e-task"},
	})
	if err != nil {
		t.Fatal(err)
	}
	columns := map[pb.FieldValueType][]string{
		pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE: {"open", "high", "low", "close", "volume", "amount"},
		pb.FieldValueType_FIELD_VALUE_TYPE_STRING: {"instrument_name", "volume_unit", "amount_unit", "provider_symbol", "request_id", "route_id", "source_provider", "quality_status", "amount_quality", "provider_id", "source_id"},
		pb.FieldValueType_FIELD_VALUE_TYPE_TIME:   {"trade_date", "close_time", "provider_timestamp", "fetched_at"},
		pb.FieldValueType_FIELD_VALUE_TYPE_INT:    {"route_rank"},
	}
	for valueType, names := range columns {
		for _, name := range names {
			if _, err := meta.UpsertDatasetColumn(ctx, &pb.DatasetColumn{
				SpaceId: ready.StockSpaceID, DatasetId: ready.StockDatasetID, ColumnName: name,
				OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_SYSTEM, OriginId: name,
				ValueType: valueType, Status: "active",
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := meta.CommitDatasetActivation(ctx, ready.StockSpaceID, ready.StockDatasetID, dataset.GetRevision()); err != nil {
		t.Fatal(err)
	}
}

func periodHelperProbe(ctx context.Context, ready periodProcessReady) error {
	opts := func(target string) []client.Option {
		return []client.Option{client.WithTarget(target), client.WithNetwork("tcp"), client.WithProtocol("trpc"), client.WithTimeout(time.Second)}
	}
	expectation := &pb.DatasetPeriodExpectation{SpaceId: ready.SpaceID, DatasetId: ready.DatasetID, Frequency: ready.Frequency, PeriodTime: 1, SeriesHash: "readiness", ExpectedCount: 1, DeadlineAt: time.Now().Add(time.Hour).Unix()}
	node := pb.NewDataNodePeriodRuntimeClientProxy(opts(ready.DataNodeTarget)...)
	primary := pb.NewPrimaryStoreClientProxy(opts(ready.PrimaryTarget)...)
	meta := pb.NewMetadataClientProxy(opts(ready.MetadataTarget)...)
	for {
		probeCtx, cancel := context.WithTimeout(ctx, time.Second)
		dn, dnErr := node.GetDatasetPeriodStatus(probeCtx, &pb.GetDatasetPeriodStatusReq{NodeId: ready.NodeID, AuthInfo: &pb.AuthInfo{AppId: ready.AppID, AppKey: ready.NodeAppKey}, Expectation: expectation})
		ps, psErr := primary.GetDatasetPeriodStatus(probeCtx, &pb.PrimaryGetDatasetPeriodStatusReq{AuthInfo: &pb.AuthInfo{AppId: ready.AppID, AppKey: ready.PrimaryAppKey}, Expectation: expectation})
		md, mdErr := meta.GetDataset(probeCtx, &pb.GetDatasetReq{AuthInfo: &pb.AuthInfo{AppId: ready.AppID, AppKey: ready.MetadataAppKey}, SpaceId: ready.SpaceID, DatasetId: ready.DatasetID})
		cancel()
		if dnErr == nil && psErr == nil && mdErr == nil && dn.GetRetInfo().GetCode() == pb.ErrorCode_NOT_FOUND && ps.GetRetInfo().GetCode() == pb.ErrorCode_NOT_FOUND && md.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS && md.GetDataset().GetStatus() == "active" {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w; node=%v/%v primary=%v/%v metadata=%v/%v", ctx.Err(), dn, dnErr, ps, psErr, md, mdErr)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestPeriodHelperClockValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clock")
	if _, err := periodHelperReadClock(path); err == nil {
		t.Fatal("missing clock accepted")
	}
	for _, value := range []string{"", "broken", "0001-01-01T00:00:00Z", "2026-10-01T00:00:00+08:00"} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := periodHelperReadClock(path); err == nil {
			t.Fatalf("invalid clock %q accepted", value)
		}
	}
	want := time.Date(2026, 10, 1, 0, 0, 0, 123, time.UTC)
	if err := os.WriteFile(path, []byte(want.Format(time.RFC3339Nano)), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := periodHelperReadClock(path); err != nil || !got.Equal(want) {
		t.Fatalf("clock=%v err=%v, want %v", got, err, want)
	}
}

func TestPeriodNativeProcessHelperLifecycle(t *testing.T) {
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) { periodHelperLifecycle(t, sig) })
	}
	t.Run("missing-clock-fails", func(t *testing.T) { periodHelperLifecycle(t, nil) })
}

func periodHelperLifecycle(t *testing.T, sig os.Signal) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(t.TempDir(), "ready.json")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestPeriodNativeProcessHelper$", "-test.v", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), "MOOX_PERIOD_E2E_HELPER=1", "MOOX_PERIOD_E2E_READY_FILE="+readyPath)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	completed := make(chan error, 1)
	go func() { completed <- cmd.Wait() }()
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = cmd.Process.Kill()
			<-completed
		}
	})
	var ready periodProcessReady
	for {
		raw, err := os.ReadFile(readyPath)
		if err == nil && json.Unmarshal(raw, &ready) == nil {
			break
		}
		select {
		case err := <-completed:
			waited = true
			t.Fatalf("helper exited before readiness: %v", err)
		case <-ctx.Done():
			t.Fatal("helper readiness timed out")
		case <-time.After(20 * time.Millisecond):
		}
	}
	info, err := os.Stat(readyPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("ready file is not private: info=%v err=%v", info, err)
	}
	if err := periodHelperProbe(ctx, ready); err != nil {
		t.Fatal(err)
	}
	if sig == nil {
		if err := os.Remove(ready.ClockFile); err != nil {
			t.Fatal(err)
		}
	} else {
		periodHelperProbeClock(t, ctx, ready)
		if err := cmd.Process.Signal(sig); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-completed:
		waited = true
		if sig == nil && err == nil {
			t.Fatal("helper accepted a missing clock")
		}
		if sig != nil && err != nil {
			t.Fatalf("helper graceful shutdown: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("helper did not exit after %v", sig)
	}
	if _, err := os.Stat(ready.DataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("helper data directory retained after exit: %v", err)
	}
}

func periodHelperProbeClock(t *testing.T, ctx context.Context, ready periodProcessReady) {
	t.Helper()
	now, err := periodHelperReadClock(ready.ClockFile)
	if err != nil {
		t.Fatal(err)
	}
	exp := &pb.DatasetPeriodExpectation{
		SpaceId: ready.SpaceID, DatasetId: ready.DatasetID, Frequency: ready.Frequency,
		PeriodTime: now.Truncate(time.Hour).Unix(), SeriesHash: "clock-smoke", ExpectedCount: 1,
		DeadlineAt:     now.Add(time.Minute).Unix(),
		SeriesSnapshot: []*pb.DatasetPeriodSeries{{SeriesIndex: 0, SubjectId: "BTC", SeriesTag: "source:e2e"}},
	}
	proxy := pb.NewPrimaryStoreClientProxy(client.WithTarget(ready.PrimaryTarget), client.WithNetwork("tcp"), client.WithProtocol("trpc"), client.WithTimeout(time.Second))
	auth := &pb.AuthInfo{AppId: ready.AppID, AppKey: ready.PrimaryAppKey}
	ensured, err := proxy.EnsureDatasetPeriod(ctx, &pb.PrimaryEnsureDatasetPeriodReq{AuthInfo: auth, Expectation: exp})
	if err != nil || ensured.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || ensured.GetStatus() != "waiting" {
		t.Fatalf("clock smoke Ensure via production adapter: rsp=%v err=%v", ensured, err)
	}
	replacement := ready.ClockFile + ".next"
	if err := os.WriteFile(replacement, []byte(now.Add(2*time.Minute).Format(time.RFC3339Nano)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, ready.ClockFile); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	for {
		status, err := proxy.GetDatasetPeriodStatus(ctx, &pb.PrimaryGetDatasetPeriodStatusReq{AuthInfo: auth, Expectation: exp})
		if err != nil || status.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("clock smoke status: rsp=%v err=%v", status, err)
		}
		if status.GetStatus() == "degraded" {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-deadline.C:
			t.Fatalf("controlled clock did not finalize period: %v", status)
		case <-time.After(20 * time.Millisecond):
		}
	}
}
