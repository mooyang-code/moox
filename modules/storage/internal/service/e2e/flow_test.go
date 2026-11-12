//go:build cgo

package e2e

import (
	"context"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	primarystore "github.com/mooyang-code/moox/modules/storage/internal/service/primarystore"
	viewservice "github.com/mooyang-code/moox/modules/storage/internal/service/view"
	"github.com/mooyang-code/moox/modules/storage/internal/serviceauth"
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

func exactDatasetEventSubjects(t *testing.T, registry *events.Registry, spaceID, datasetID string) []string {
	t.Helper()
	filters := make([]string, 0, 5)
	for _, event := range []events.Event{events.DatasetRowsUpserted, events.DatasetPeriodCompleted, events.DatasetSyncPoint} {
		filter, err := registry.RenderSubject(event, spaceID, datasetID)
		if err != nil {
			t.Fatal(err)
		}
		filters = append(filters, filter)
	}
	sort.Strings(filters)
	return filters
}

func (r primaryFieldReader) ReadRows(ctx context.Context, req *pb.PrimaryReadRowsReq, _ ...client.Option) (*pb.PrimaryReadRowsRsp, error) {
	return r.service.ReadRows(ctx, req)
}

func (r primaryFieldReader) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq, _ ...client.Option) (*pb.ReadTimeSeriesRowsRsp, error) {
	return r.service.ReadTimeSeriesRows(ctx, req)
}

func (r primaryFieldReader) ReadDatasetChanges(ctx context.Context, req *pb.ReadDatasetChangesReq, _ ...client.Option) (*pb.ReadDatasetChangesRsp, error) {
	return r.service.ReadDatasetChanges(ctx, req)
}

func TestSeriesTagPrimaryEventActiveViewAndBackfillFlow(t *testing.T) {
	ctx := context.Background()
	const secret = "e2e-secret"
	store, err := pebble.Open(pebble.Options{NodeID: "node-a", Path: filepath.Join(t.TempDir(), "node-a")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	primary, err := primarystore.New(primarystore.Options{Snapshot: activeRequestSnapshot, Store: store})
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
	if rsp, err := primary.UpsertFields(ctx, fillDataset(&pb.PrimaryUpsertFieldsReq{AuthInfo: auth, Rows: writes})); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetKeys()) != 3 {
		t.Fatalf("primary write: rsp=%v err=%v", rsp, err)
	}
	read, err := primary.ReadRows(ctx, &pb.PrimaryReadRowsReq{AuthInfo: auth, Keys: keys, ColumnNames: []string{"close", "volume"}})
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
	if message.GetEventVersion() != 2 || len(eventRows.GetRows()) != 3 {
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
	viewAuth := &pb.AuthInfo{AppId: "storage-primary", AppKey: serviceauth.Key(secret, "storage-primary")}
	view.SetPrimaryAuth(auth)
	view.SetPrimaryReader(primaryFieldReader{service: primary})
	columns := []*pb.ViewColumn{
		{ColumnName: "close", OriginId: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "volume", OriginId: "volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}
	if rsp, err := view.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: viewAuth, IndexId: "prices-view", Schema: &pb.ViewIndexSchema{SpaceId: "quant", ViewId: "prices-view", DatasetId: "prices", ViewVersion: 1, Engine: "duckdb", ViewSchemaHash: "schema-1", Columns: columns}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("view prepare: rsp=%v err=%v", rsp, err)
	}
	if err := view.AttachActiveView(context.Background(), &pb.View{SpaceId: "quant", ViewId: "prices-view", DatasetId: "prices", Engine: "duckdb", Freq: "1m", ActiveIndexId: "prices-view", ViewRevision: 1, ActiveViewSchemaHash: "schema-1", ActiveColumns: columns, Status: "active"}); err != nil {
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
	const storageSubject = "moox.event.storage.>"
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
	publisher, err := events.NewPublisher(eventClient, registry)
	if err != nil {
		t.Fatal(err)
	}
	// 索引没有跟随进度：跟随器先重建（读存量），之后跟随变更日志应用新的写入。
	stopFollowing, err := view.StartFollowing(ctx, viewservice.FollowOptions{Changes: primaryFieldReader{service: primary}, Range: primaryFieldReader{service: primary}, Publisher: publisher, Wait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer stopFollowing()
	waitForSelectorStates(t, ctx, view, viewAuth, closes)

	patch := &pb.RowFieldUpsert{Key: keys[1], Fields: []*pb.FieldValue{doubleField("close", 111)}}
	if rsp, err := primary.UpsertFields(ctx, fillDataset(&pb.PrimaryUpsertFieldsReq{AuthInfo: auth, Rows: []*pb.RowFieldUpsert{patch}})); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("field patch: rsp=%v err=%v", rsp, err)
	}
	closes[1] = 111
	waitForSelectorStates(t, ctx, view, viewAuth, closes)
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
	t.Fatal("写入没有在 View 的活动索引里变得可查")
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
