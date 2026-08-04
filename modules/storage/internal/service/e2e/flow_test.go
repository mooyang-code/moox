//go:build cgo

package e2e

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	storageoutbox "github.com/mooyang-code/moox/modules/storage/internal/service/datanode/outbox"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	primarystore "github.com/mooyang-code/moox/modules/storage/internal/service/primarystore"
	viewservice "github.com/mooyang-code/moox/modules/storage/internal/service/view"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
)

type primaryFieldReader struct {
	service *primarystore.Service
}

func (r primaryFieldReader) ReadFields(ctx context.Context, req *pb.PrimaryReadFieldsReq, _ ...client.Option) (*pb.PrimaryReadFieldsRsp, error) {
	return r.service.ReadFields(ctx, req)
}

func TestSeriesTagPrimaryEventActiveViewAndBackfillFlow(t *testing.T) {
	ctx := context.Background()
	const secret = "e2e-secret"
	store, err := pebble.Open(pebble.Options{NodeID: "node-a", Path: filepath.Join(t.TempDir(), "node-a")})
	if err != nil {
		t.Fatal(err)
	}
	node, err := datanode.NewService(datanode.Options{NodeID: "node-a", AuthSecret: secret, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	primary, err := primarystore.New(primarystore.Options{
		Resolver: func(_ context.Context, _, dataset string) (pb.DataNodeRuntimeService, error) {
			return node, nil
		},
		AuthSigner: func(in *pb.AuthInfo) (*pb.AuthInfo, error) {
			clone := proto.Clone(in).(*pb.AuthInfo)
			clone.AppKey = datanode.ServiceAuthKey(secret, clone.AppId)
			return clone, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	auth := &pb.AuthInfo{AppId: "storage-primary"}
	tags := []string{"", "venue:binance", "venue:okx"}
	closes := []float64{100, 101, 102}
	keys := make([]*pb.RowKey, 0, len(tags))
	writes := make([]*pb.RowFieldUpsert, 0, len(tags))
	for i, tag := range tags {
		key := seriesTagKey(tag)
		keys = append(keys, key)
		writes = append(writes, &pb.RowFieldUpsert{Key: key, Fields: []*pb.FieldValue{
			doubleField("close", closes[i]),
			doubleField("volume", float64(10+i)),
		}})
	}
	if rsp, err := primary.UpsertFields(ctx, &pb.PrimaryUpsertFieldsReq{AuthInfo: auth, Rows: writes, WriteSource: "scf:e2e-fetcher"}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetKeys()) != 3 {
		t.Fatalf("primary write: rsp=%v err=%v", rsp, err)
	}
	read, err := primary.ReadFields(ctx, &pb.PrimaryReadFieldsReq{AuthInfo: auth, Keys: keys, FieldIds: []string{"close", "volume"}})
	if err != nil || read.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(read.GetRows()) != 3 {
		t.Fatalf("primary read: rsp=%v err=%v", read, err)
	}
	assertSeriesTagRows(t, read.GetRows(), tags, closes)

	entries, err := store.ListOutbox(ctx, 0, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("outbox entries=%v err=%v", entries, err)
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	subject, err := registry.RenderSubject(events.DatasetRowsUpserted, "quant", "prices")
	if err != nil {
		t.Fatal(err)
	}
	envelope := new(eventpb.EventMessage)
	if err := proto.Unmarshal(entries[0].Data, envelope); err != nil {
		t.Fatal(err)
	}
	message, eventRows, err := events.DecodeDatasetRowsUpserted(registry, entries[0].Data, subject, envelope.GetEventId())
	if err != nil {
		t.Fatal(err)
	}
	if message.GetEventVersion() != 2 || eventRows.GetWriteSource() != "scf:e2e-fetcher" || len(eventRows.GetRows()) != 3 {
		t.Fatalf("DatasetRowsUpserted@2 message=%v rows=%v", message, eventRows.GetRows())
	}
	for i, row := range eventRows.GetRows() {
		if row.GetKey().GetTimeSeries().GetSeriesTag() != tags[i] {
			t.Fatalf("event row %d series_tag=%q want=%q", i, row.GetKey().GetTimeSeries().GetSeriesTag(), tags[i])
		}
	}

	view, err := viewservice.New(filepath.Join(t.TempDir(), "view"), secret)
	if err != nil {
		t.Fatal(err)
	}
	viewAuth := &pb.AuthInfo{AppId: "storage-primary", AppKey: datanode.ServiceAuthKey(secret, "storage-primary")}
	view.SetPrimaryAuth(auth)
	view.SetPrimaryReader(primaryFieldReader{service: primary})
	columns := []*pb.ViewColumn{
		{ColumnName: "close", OriginId: "prices.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "volume", OriginId: "prices.volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}
	if rsp, err := view.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: viewAuth, IndexId: "prices-view", Schema: &pb.ViewIndexSchema{SpaceId: "quant", ViewId: "prices-view", PrimaryDatasetId: "prices", ViewVersion: 1, Engine: "duckdb", ViewSchemaHash: "schema-1", Columns: columns}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("view prepare: rsp=%v err=%v", rsp, err)
	}
	if err := view.AttachActiveView(&pb.View{SpaceId: "quant", ViewId: "prices-view", PrimaryDatasetId: "prices", Engine: "duckdb", ActiveIndexId: "prices-view", ActiveViewRevision: 1, ActiveViewSchemaHash: "schema-1", ActiveColumns: columns, Status: "active"}); err != nil {
		t.Fatal(err)
	}

	const (
		eventUsername = "storage-e2e"
		eventPassword = "storage-e2e-password"
	)
	server, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(),
		Username: eventUsername, Password: eventPassword,
	})
	if err != nil {
		t.Fatal(err)
	}
	go server.Start()
	if !server.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded authenticated NATS did not start")
	}
	defer server.Shutdown()
	if unauthenticated, err := nats.Connect(server.ClientURL(), nats.Timeout(time.Second)); err == nil {
		unauthenticated.Close()
		t.Fatal("embedded NATS unexpectedly accepted an unauthenticated client")
	}
	nc, err := nats.Connect(server.ClientURL(), nats.UserInfo(eventUsername, eventPassword))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	const storageSubject = "moox.storage.dataset.rows.upserted.v2.>"
	if _, err := js.AddStream(&nats.StreamConfig{
		Name: "MOOX_STORAGE", Subjects: []string{storageSubject}, Storage: nats.MemoryStorage,
	}); err != nil {
		t.Fatal(err)
	}
	eventClient, err := jetstream.Connect(ctx, jetstream.Config{
		URLs: []string{server.ClientURL()}, Name: "storage-series-tag-e2e",
		Username: eventUsername, Password: eventPassword,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eventClient.Close()
	stopConsumer, err := view.StartEventConsumer(ctx, eventClient, viewservice.EventConsumerOptions{
		Consumer: "storage_view", FetchBatch: 2, MaxWorkers: 2, MaxAckPending: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stopConsumer()
	consumerInfo, err := js.ConsumerInfo("MOOX_STORAGE", "storage_view")
	if err != nil {
		t.Fatal(err)
	}
	if consumerInfo.Config.FilterSubject != storageSubject {
		t.Fatalf("storage_view filter=%q want=%q", consumerInfo.Config.FilterSubject, storageSubject)
	}
	relayErrors := make(chan error, 1)
	relay, err := storageoutbox.NewRelay(
		store,
		storageoutbox.NewJetStreamPublisher(eventClient),
		storageoutbox.RelayOptions{
			PollInterval: 10 * time.Millisecond,
			ErrorReporter: func(err error) {
				select {
				case relayErrors <- err:
				default:
				}
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	relay.Start(ctx)
	defer relay.Close()
	waitForOutboxEmpty(t, ctx, store, relayErrors)
	waitForSelectorStates(t, ctx, view, viewAuth, closes)

	patch := &pb.RowFieldUpsert{Key: keys[1], Fields: []*pb.FieldValue{doubleField("close", 111)}}
	if rsp, err := primary.UpsertFields(ctx, &pb.PrimaryUpsertFieldsReq{AuthInfo: auth, Rows: []*pb.RowFieldUpsert{patch}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("field patch: rsp=%v err=%v", rsp, err)
	}
	closes[1] = 111
	waitForOutboxEmpty(t, ctx, store, relayErrors)
	waitForSelectorStates(t, ctx, view, viewAuth, closes)

	if rsp, err := view.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: viewAuth, IndexId: "prices-view-next", Schema: &pb.ViewIndexSchema{SpaceId: "quant", ViewId: "prices-view", PrimaryDatasetId: "prices", ViewVersion: 2, Engine: "duckdb", ViewSchemaHash: "schema-2", Columns: columns}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("prepare replacement view: rsp=%v err=%v", rsp, err)
	}
	if err := view.BackfillViewWithReader(ctx, "quant", "prices-view", 2, primaryFieldReader{service: primary}); err != nil {
		t.Fatalf("backfill replacement view: %v", err)
	}
	if err := view.SwitchView(ctx, "quant", "prices-view", time.Hour); err != nil {
		t.Fatalf("switch replacement view: %v", err)
	}
	assertSelectorStates(t, ctx, view, viewAuth, closes)
}

func waitForOutboxEmpty(t *testing.T, ctx context.Context, store *pebble.Store, relayErrors <-chan error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-relayErrors:
			t.Fatalf("outbox relay: %v", err)
		default:
		}
		entries, err := store.ListOutbox(ctx, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("outbox was not relayed and deleted")
}

func waitForSelectorStates(t *testing.T, ctx context.Context, view *viewservice.Service, auth *pb.AuthInfo, closes []float64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rsp, err := view.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{
			AuthInfo: auth, SpaceId: "quant", ViewId: "prices-view",
			Selectors: []*pb.TimeSeriesSelector{{
				SpaceId: "quant", DatasetId: "prices", SubjectId: "BTC-USDT", Freq: "1m",
			}},
			TimeRange: &pb.TimeRange{
				StartTime: "2026-07-20T00:00:00Z",
				EndTime:   "2026-07-20T00:01:00Z",
			},
			ColumnNames: []string{"close", "volume"},
			Page:        &pb.Page{Page: 1, Size: 10},
		})
		if err == nil && rsp.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS && len(rsp.GetRows()) == len(closes) {
			matches := true
			for i, row := range rsp.GetRows() {
				if len(row.GetFields()) == 0 || row.GetFields()[0].GetValue().GetDoubleValue() != closes[i] {
					matches = false
					break
				}
			}
			if matches {
				assertSelectorStates(t, ctx, view, auth, closes)
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("relayed rows did not become queryable through the active view")
}

func seriesTagKey(tag string) *pb.RowKey {
	return &pb.RowKey{SpaceId: "quant", DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
		SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-20T00:00:00Z", SeriesTag: tag,
	}}}
}

func doubleField(id string, value float64) *pb.FieldValue {
	return &pb.FieldValue{FieldId: id, Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}}}
}

func assertSeriesTagRows(t *testing.T, rows []*pb.RowFieldValues, tags []string, closes []float64) {
	t.Helper()
	if len(rows) != len(tags) {
		t.Fatalf("row count=%d want=%d", len(rows), len(tags))
	}
	for i, row := range rows {
		if row.GetKey().GetTimeSeries().GetSeriesTag() != tags[i] {
			t.Fatalf("row %d series_tag=%q want=%q", i, row.GetKey().GetTimeSeries().GetSeriesTag(), tags[i])
		}
		if len(row.GetFields()) == 0 || row.GetFields()[0].GetValue().GetDoubleValue() != closes[i] {
			t.Fatalf("row %d fields=%v want close=%v", i, row.GetFields(), closes[i])
		}
	}
}

func assertSelectorStates(t *testing.T, ctx context.Context, view *viewservice.Service, auth *pb.AuthInfo, closes []float64) {
	t.Helper()
	query := func(selector *pb.TimeSeriesSelector) []*pb.TimeSeriesRow {
		t.Helper()
		rsp, err := view.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{
			AuthInfo: auth, SpaceId: "quant", ViewId: "prices-view",
			Selectors:   []*pb.TimeSeriesSelector{selector},
			TimeRange:   &pb.TimeRange{StartTime: "2026-07-20T00:00:00Z", EndTime: "2026-07-20T00:01:00Z"},
			ColumnNames: []string{"close", "volume"},
			Page:        &pb.Page{Page: 1, Size: 10},
		})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("view query: rsp=%v err=%v", rsp, err)
		}
		return rsp.GetRows()
	}
	base := &pb.TimeSeriesSelector{SpaceId: "quant", DatasetId: "prices", SubjectId: "BTC-USDT", Freq: "1m"}
	all := query(base)
	if len(all) != 3 {
		t.Fatalf("unset selector rows=%v", all)
	}
	for i, tag := range []string{"", "venue:binance", "venue:okx"} {
		if all[i].GetKey().GetSeriesTag() != tag || all[i].GetFields()[0].GetValue().GetDoubleValue() != closes[i] {
			t.Fatalf("stable row %d=%v", i, all[i])
		}
	}
	if len(all[1].GetFields()) != 2 || all[1].GetFields()[1].GetValue().GetDoubleValue() != 11 {
		t.Fatalf("field patch replaced untouched volume: %v", all[1])
	}
	empty := ""
	defaultRows := query(&pb.TimeSeriesSelector{SpaceId: "quant", DatasetId: "prices", SubjectId: "BTC-USDT", Freq: "1m", SeriesTag: &empty})
	if len(defaultRows) != 1 || defaultRows[0].GetKey().GetSeriesTag() != "" {
		t.Fatalf("explicit empty selector rows=%v", defaultRows)
	}
	okx := "venue:okx"
	okxRows := query(&pb.TimeSeriesSelector{SpaceId: "quant", DatasetId: "prices", SubjectId: "BTC-USDT", Freq: "1m", SeriesTag: &okx})
	if len(okxRows) != 1 || okxRows[0].GetKey().GetSeriesTag() != okx {
		t.Fatalf("exact selector rows=%v", okxRows)
	}
}
