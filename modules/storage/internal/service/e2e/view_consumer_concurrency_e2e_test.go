//go:build cgo

package e2e

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	viewservice "github.com/mooyang-code/moox/modules/storage/internal/service/view"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"trpc.group/trpc-go/trpc-go/client"
)

type concurrencyPrimaryReader struct{}

func (concurrencyPrimaryReader) ReadFields(_ context.Context, req *pb.PrimaryReadFieldsReq, _ ...client.Option) (*pb.PrimaryReadFieldsRsp, error) {
	rows := make([]*pb.RowFieldValues, 0, len(req.GetKeys()))
	for _, key := range req.GetKeys() {
		rows = append(rows, &pb.RowFieldValues{Key: key, Fields: []*pb.FieldValue{{
			FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}},
		}}})
	}
	return &pb.PrimaryReadFieldsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"}, Rows: rows}, nil
}

func TestViewEventConsumerProcessesIndependentDatasetLanesE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const secret = "view-concurrency-secret"
	service, err := viewservice.New(filepath.Join(t.TempDir(), "view"), secret)
	if err != nil {
		t.Fatal(err)
	}
	auth := &pb.AuthInfo{AppId: "e2e", AppKey: datanode.ServiceAuthKey(secret, "e2e")}
	service.SetPrimaryAuth(auth)
	service.SetPrimaryReader(concurrencyPrimaryReader{})
	for _, dataset := range []string{"prices_a", "prices_b"} {
		prepareConcurrencyView(t, ctx, service, auth, dataset)
	}

	server, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	go server.Start()
	if !server.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded NATS did not start")
	}
	defer server.Shutdown()
	nc, err := nats.Connect(server.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	const subject = "moox.storage.dataset.rows.upserted.v1.>"
	if _, err := js.AddStream(&nats.StreamConfig{Name: "MOOX_STORAGE", Subjects: []string{subject}, Storage: nats.MemoryStorage}); err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddConsumer("MOOX_STORAGE", &nats.ConsumerConfig{Name: "storage_view", Durable: "storage_view", FilterSubject: subject, AckPolicy: nats.AckExplicitPolicy, AckWait: time.Second, MaxDeliver: 3, MaxAckPending: 8, DeliverPolicy: nats.DeliverAllPolicy}); err != nil {
		t.Fatal(err)
	}
	client, err := jetstream.Connect(ctx, jetstream.ConfigFromEnv([]string{server.ClientURL()}, "storage-view-concurrency-e2e"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := events.NewPublisher(client, registry)
	if err != nil {
		t.Fatal(err)
	}
	blockedStarted := make(chan struct{})
	releaseBlocked := make(chan struct{})
	var blockOnce sync.Once
	stop, err := service.StartEventConsumer(ctx, client, viewservice.EventConsumerOptions{
		Consumer: "storage_view", FetchBatch: 2, MaxWorkers: 2, MaxAckPending: 8,
		BeforeProcess: func(hookCtx context.Context, delivery *jetstream.Delivery) error {
			_, payload, err := events.DecodeDatasetRowsUpsertedWithContentType(registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, delivery.ContentType)
			if err != nil || payload.GetDatasetId() != "prices_a" {
				return nil
			}
			blocked := false
			blockOnce.Do(func() {
				close(blockedStarted)
				blocked = true
			})
			if blocked {
				select {
				case <-releaseBlocked:
				case <-hookCtx.Done():
					return hookCtx.Err()
				}
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	for i, dataset := range []string{"prices_a", "prices_b"} {
		rowEvent := &storagepb.DatasetRowsUpserted{SpaceId: "quant", DatasetId: dataset, Rows: []*storagepb.RowUpsert{{
			Key:    &storagepb.RowKey{SpaceId: "quant", DatasetId: dataset, Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: "BTC", Freq: "1m", DataTime: "2026-07-20T00:00:00Z"}}},
			Fields: []*storagepb.FieldValue{{FieldId: "close", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: float64(100 + i)}}}},
		}}}
		if _, err := publisher.Publish(ctx, events.DatasetRowsUpserted, rowEvent, events.PublishOptions{EventID: "view-concurrency-" + dataset, OccurredAt: time.Now().UTC(), SpaceID: "quant", SubjectID: dataset}); err != nil {
			t.Fatal(err)
		}
	}

	select {
	case <-blockedStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("dataset prices_a did not enter the blocked lane")
	}
	waitForConcurrencyRows(t, ctx, service, auth, "prices_b")
	close(releaseBlocked)
	waitForConcurrencyRows(t, ctx, service, auth, "prices_a")
}

func waitForEitherConcurrencyRows(t *testing.T, ctx context.Context, service *viewservice.Service, auth *pb.AuthInfo) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, dataset := range []string{"prices_a", "prices_b"} {
			if concurrencyRowsReady(ctx, service, auth, dataset) {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the non-blocked dataset lane did not complete while another lane was blocked")
}

func concurrencyRowsReady(ctx context.Context, service *viewservice.Service, auth *pb.AuthInfo, dataset string) bool {
	viewID := dataset + "-view"
	result, err := service.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{AuthInfo: auth, SpaceId: "quant", ViewId: viewID, TimeRange: &pb.TimeRange{StartTime: "2026-07-20T00:00:00Z", EndTime: "2026-07-20T00:01:00Z"}, Page: &pb.Page{Page: 1, Size: 10}})
	return err == nil && result.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS && len(result.GetRows()) == 1
}

func waitForConcurrencyRows(t *testing.T, ctx context.Context, service *viewservice.Service, auth *pb.AuthInfo, dataset string) {
	t.Helper()
	viewID := dataset + "-view"
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result, err := service.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{AuthInfo: auth, SpaceId: "quant", ViewId: viewID, TimeRange: &pb.TimeRange{StartTime: "2026-07-20T00:00:00Z", EndTime: "2026-07-20T00:01:00Z"}, Page: &pb.Page{Page: 1, Size: 10}})
		if err == nil && result.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS && len(result.GetRows()) == 1 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("dataset %s did not become queryable", dataset)
}

func prepareConcurrencyView(t *testing.T, ctx context.Context, service *viewservice.Service, auth *pb.AuthInfo, dataset string) {
	t.Helper()
	viewID := dataset + "-view"
	if rsp, err := service.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: auth, IndexId: viewID, Schema: &pb.ViewIndexSchema{SpaceId: "quant", ViewId: viewID, PrimaryDatasetId: dataset, ViewVersion: 1, Engine: "duckdb", ViewSchemaHash: "schema-1", Columns: []*pb.ViewColumn{{ColumnName: "close", OriginId: dataset + ".close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("prepare %s: rsp=%v err=%v", dataset, rsp, err)
	}
	if err := service.AttachActiveView(&pb.View{SpaceId: "quant", ViewId: viewID, PrimaryDatasetId: dataset, Engine: "duckdb", ActiveIndexId: viewID, ActiveViewRevision: 1, ActiveViewSchemaHash: "schema-1", ActiveColumns: []*pb.ViewColumn{{ColumnName: "close", OriginId: dataset + ".close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}, Status: "active"}); err != nil {
		t.Fatal(err)
	}
}
