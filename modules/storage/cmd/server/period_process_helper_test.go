package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/sqlite"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

type periodProcessReady struct {
	PrimaryTarget  string `json:"primary_target"`
	MetadataTarget string `json:"metadata_target"`
	DataNodeTarget string `json:"data_node_target"`
	NodeID         string `json:"node_id"`
	SpaceID        string `json:"space_id"`
	DatasetID      string `json:"dataset_id"`
	Frequency      string `json:"frequency"`
	RetryDatasetID string `json:"retry_dataset_id"`
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
	service := server.New(server.WithTransport(transport.NewServerTransport()), server.WithServiceName(name), server.WithProtocol("trpc"), server.WithNetwork("tcp"), server.WithListener(listener), server.WithServerAsync(false))
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
			if message.GetEventName() != events.DatasetPeriodCompleted.Name() {
				continue
			}
			payload := &storageeventpb.DatasetPeriodCompleted{}
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
	// Minute-level snapshot cases and hourly retry cases must not share a
	// Dataset: production enforces one declared frequency per Dataset.
	for _, fixture := range []struct{ id, frequency, task string }{
		{ready.DatasetID, ready.Frequency, "period-e2e-task"},
		{ready.RetryDatasetID, "1h", "period-e2e-retry-task"},
	} {
		dataset, err := meta.CreateDataset(ctx, &pb.Dataset{
			SpaceId: ready.SpaceID, DatasetId: fixture.id, DataNodeId: ready.NodeID, Name: "Period E2E raw data " + fixture.frequency,
			DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: fixture.frequency,
			Attributes: map[string]string{"owner_module": "collector", "dataset_role": "raw_collection", "collector_task_id": fixture.task},
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"open", "high", "low", "close", "volume", "amount"} {
			if _, err := meta.UpsertDatasetColumn(ctx, &pb.DatasetColumn{
				SpaceId: ready.SpaceID, DatasetId: fixture.id, ColumnName: name,
				OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_SYSTEM, OriginId: name,
				ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active",
			}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := meta.CommitDatasetActivation(ctx, ready.SpaceID, fixture.id, dataset.GetRevision()); err != nil {
			t.Fatal(err)
		}
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
