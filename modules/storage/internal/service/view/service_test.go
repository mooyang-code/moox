//go:build cgo

package view

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
)

type fakeFieldReader struct{}

func (fakeFieldReader) ReadFields(_ context.Context, req *pb.PrimaryReadFieldsReq, _ ...client.Option) (*pb.PrimaryReadFieldsRsp, error) {
	rows := make([]*pb.RowFieldValues, 0, len(req.GetKeys()))
	for _, key := range req.GetKeys() {
		rows = append(rows, &pb.RowFieldValues{
			Key: key,
			Fields: []*pb.FieldValue{{
				FieldId: "factor",
				Value:   &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.5}},
			}},
		})
	}
	return &pb.PrimaryReadFieldsRsp{RetInfo: successRetInfo(), Rows: rows}, nil
}

type recoveryFieldReader struct{ primaryPresent bool }

func (r recoveryFieldReader) ReadFields(_ context.Context, req *pb.PrimaryReadFieldsReq, _ ...client.Option) (*pb.PrimaryReadFieldsRsp, error) {
	rows := make([]*pb.RowFieldValues, 0, len(req.GetKeys()))
	for _, key := range req.GetKeys() {
		row := &pb.RowFieldValues{Key: key}
		if key.GetDatasetId() == "primary" && r.primaryPresent {
			row.Fields = append(row.Fields, &pb.FieldValue{FieldId: "base", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 10}}})
		}
		if key.GetDatasetId() == "secondary" {
			row.Fields = append(row.Fields, &pb.FieldValue{FieldId: "factor", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.5}}})
		}
		rows = append(rows, row)
	}
	return &pb.PrimaryReadFieldsRsp{RetInfo: successRetInfo(), Rows: rows}, nil
}

type sparseRecoveryFieldReader struct{ secondaryPresent bool }

func (r sparseRecoveryFieldReader) ReadFields(_ context.Context, req *pb.PrimaryReadFieldsReq, _ ...client.Option) (*pb.PrimaryReadFieldsRsp, error) {
	rows := make([]*pb.RowFieldValues, 0, len(req.GetKeys()))
	for _, key := range req.GetKeys() {
		row := &pb.RowFieldValues{Key: key}
		if key.GetDatasetId() == "primary" {
			row.Fields = append(row.Fields, &pb.FieldValue{FieldId: "base", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 10}}})
		}
		if key.GetDatasetId() == "secondary" && r.secondaryPresent {
			row.Fields = append(row.Fields, &pb.FieldValue{FieldId: "factor", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.5}}})
		}
		rows = append(rows, row)
	}
	return &pb.PrimaryReadFieldsRsp{RetInfo: successRetInfo(), Rows: rows}, nil
}

func TestViewIndexAndDataViewExplicitKeyFlow(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	if rsp, err := svc.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: auth, IndexId: "prices-view", Schema: &pb.ViewIndexSchema{SpaceId: "quant", ViewId: "prices-view", ViewVersion: 1, Engine: "duckdb", ViewSchemaHash: "schema-1", Columns: []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("prepare: rsp=%v err=%v", rsp, err)
	}
	key := &pb.RowKey{SpaceId: "quant", DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-20T00:00:00Z"}}}
	value := &pb.FieldValue{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}}}
	if rsp, err := svc.ApplyViewIndex(ctx, &pb.ApplyViewIndexReq{AuthInfo: auth, IndexId: "prices-view", Batch: &pb.ViewIndexWriteBatch{ViewRevision: 1, ViewSchemaHash: "schema-1", WriteMode: "LIVE_WRITE", RowWrites: []*pb.ViewIndexRowWrite{{Key: &pb.ViewIndexRowKey{RowKey: key}, Fields: []*pb.FieldValue{value}}}}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("apply: rsp=%v err=%v", rsp, err)
	}
	rsp, err := svc.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{AuthInfo: auth, ViewId: "prices-view", Keys: []*pb.TimeSeriesKey{{SpaceId: "quant", DatasetId: "prices", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-20T00:00:00Z"}}, ColumnNames: []string{"close"}})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetRows()) != 1 || len(rsp.GetRows()[0].GetFields()) != 1 {
		t.Fatalf("query: rsp=%v err=%v", rsp, err)
	}
}

func TestViewServiceRequiresSecretAndAuth(t *testing.T) {
	if _, err := New(filepath.Join(t.TempDir(), "views"), ""); err == nil {
		t.Fatal("expected missing view auth secret to be rejected")
	}
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := svc.PrepareViewIndex(context.Background(), &pb.PrepareViewIndexReq{
		IndexId: "idx",
		Schema:  &pb.ViewIndexSchema{SpaceId: "s", ViewId: "v", ViewVersion: 1, Engine: "bleve", ViewSchemaHash: "hash"},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("rsp=%v err=%v", rsp, err)
	}
}

func TestMalformedDeliveryIsPermanent(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	err = svc.applyDelivery(context.Background(), &jetstream.Delivery{
		Subject: "malformed",
		RawData: []byte("not-protobuf"), ContentType: events.ContentType, RawMessageID: "malformed",
	})
	if !isPermanentDeliveryError(err) {
		t.Fatalf("err=%v", err)
	}
	err = svc.applyDelivery(context.Background(), &jetstream.Delivery{DecodeError: errors.New("decode failed")})
	if !isPermanentDeliveryError(err) {
		t.Fatalf("decode err=%v", err)
	}
}

func TestApplyDeliveryRejectsInvalidStorageEvent(t *testing.T) {
	svc := svcForDeliveryTest(t)
	if err := svc.applyDelivery(context.Background(), &jetstream.Delivery{RawData: []byte("legacy"), ContentType: "application/x-protobuf"}); !isPermanentDeliveryError(err) {
		t.Fatalf("legacy event error = %v, want permanent error", err)
	}
	encoded, raw := validRawStorageDelivery(t)
	if err := svc.applyDelivery(context.Background(), &jetstream.Delivery{Subject: encoded.Subject, RawData: raw, RawMessageID: "wrong-id", ContentType: events.ContentType}); !isPermanentDeliveryError(err) {
		t.Fatalf("event id mismatch = %v, want permanent error", err)
	}
}

func TestApplyDeliveryAcceptsGovernedRawEventMessage(t *testing.T) {
	svc := svcForDeliveryTest(t)
	encoded, raw := validRawStorageDelivery(t)
	if err := svc.applyDelivery(context.Background(), &jetstream.Delivery{Subject: encoded.Subject, RawData: raw, RawMessageID: encoded.Message.GetEventId(), ContentType: events.ContentType}); err != nil {
		t.Fatalf("apply governed raw event: %v", err)
	}
}

func validRawStorageDelivery(t *testing.T) (events.EncodedEvent, []byte) {
	t.Helper()
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := registry.Encode(events.DatasetRowsUpserted, &storagepb.DatasetRowsUpserted{SpaceId: "foo", DatasetId: "bar", Rows: []*storagepb.RowUpsert{{Key: &storagepb.RowKey{SpaceId: "foo", DatasetId: "bar", Kind: &storagepb.RowKey_Record{Record: &storagepb.RecordRowKey{RecordId: "record-1", Version: "v1"}}}}}}, events.PublishOptions{EventID: "storage-test-1", OccurredAt: time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC), SpaceID: "foo", SubjectID: "bar"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := proto.Marshal(encoded.Message)
	if err != nil {
		t.Fatal(err)
	}
	return encoded, raw
}

func svcForDeliveryTest(t *testing.T) *Service {
	t.Helper()
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestProcessDeliveryBalancesLiveWork(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}

	svc.processDelivery(context.Background(), &jetstream.Delivery{DecodeError: errors.New("decode failed")})
	if got := svc.liveWork.Load(); got != 0 {
		t.Fatalf("live work count = %d, want 0", got)
	}
}

func TestDataViewSupportsTimeRangeAndTextOnlyQueries(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	prepare := func(id string) {
		t.Helper()
		rsp, err := svc.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{
			AuthInfo: auth, IndexId: id,
			Schema: &pb.ViewIndexSchema{SpaceId: "space", ViewId: id, ViewVersion: 1, Engine: map[string]string{"times": "duckdb", "records": "bleve"}[id], ViewSchemaHash: "hash", Columns: []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {ColumnName: "title", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING}}},
		})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("prepare rsp=%v err=%v", rsp, err)
		}
		if err := svc.AttachActiveView(&pb.View{SpaceId: "space", ViewId: id, ActiveIndexId: id, ActiveViewRevision: 1, ActiveViewSchemaHash: "hash", Engine: map[string]string{"times": "duckdb", "records": "bleve"}[id], ActiveColumns: []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, {ColumnName: "title", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING}}, Status: "active"}); err != nil {
			t.Fatalf("attach %s: %v", id, err)
		}
	}
	prepare("times")
	timeRows := []*pb.ViewIndexRowWrite{}
	for _, at := range []string{"2026-07-19T00:00:00Z", "2026-07-20T00:00:00Z"} {
		timeRows = append(timeRows, &pb.ViewIndexRowWrite{
			Key:    &pb.ViewIndexRowKey{RowKey: &pb.RowKey{SpaceId: "space", DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1d", DataTime: at}}}},
			Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}}}},
		})
	}
	if rsp, err := svc.ApplyViewIndex(ctx, &pb.ApplyViewIndexReq{AuthInfo: auth, IndexId: "times", Batch: &pb.ViewIndexWriteBatch{ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: "LIVE_WRITE", RowWrites: timeRows}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("apply time rows rsp=%v err=%v", rsp, err)
	}
	timeRsp, err := svc.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{
		AuthInfo: auth, SpaceId: "space", ViewId: "times",
		TimeRange: &pb.TimeRange{StartTime: "2026-07-20T00:00:00Z", EndTime: "2026-07-20T23:59:59Z"},
	})
	if err != nil || timeRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(timeRsp.GetRows()) != 1 {
		t.Fatalf("time rsp=%v err=%v", timeRsp, err)
	}

	prepare("records")
	recordKey := &pb.RowKey{SpaceId: "space", DatasetId: "records", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}
	if rsp, err := svc.ApplyViewIndex(ctx, &pb.ApplyViewIndexReq{AuthInfo: auth, IndexId: "records", Batch: &pb.ViewIndexWriteBatch{
		ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: "LIVE_WRITE",
		RowWrites: []*pb.ViewIndexRowWrite{{Key: &pb.ViewIndexRowKey{RowKey: recordKey}, Fields: []*pb.FieldValue{{FieldId: "title", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "market research"}}}}}},
	}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("apply record rsp=%v err=%v", rsp, err)
	}
	recordRsp, err := svc.SearchRecordRows(ctx, &pb.SearchRecordRowsReq{AuthInfo: auth, SpaceId: "space", ViewId: "records", TextQuery: "research"})
	if err != nil || recordRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(recordRsp.GetRows()) != 1 {
		t.Fatalf("record rsp=%v err=%v", recordRsp, err)
	}
}

func TestViewDatasetMappingIncludesSpace(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	for _, space := range []string{"space-a", "space-b"} {
		rsp, err := svc.PrepareViewIndex(context.Background(), &pb.PrepareViewIndexReq{
			AuthInfo: auth,
			IndexId:  space + "-view",
			Schema: &pb.ViewIndexSchema{
				SpaceId:        space,
				ViewId:         "view",
				ViewVersion:    1,
				Engine:         "bleve",
				ViewSchemaHash: "hash",
				Columns:        []*pb.ViewColumn{{OriginId: "shared.value", ColumnName: "value"}},
			},
		})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("space=%s rsp=%v err=%v", space, rsp, err)
		}
	}
	if len(svc.byData[datasetRef{spaceID: "space-a", datasetID: "shared"}]) != 1 ||
		len(svc.byData[datasetRef{spaceID: "space-b", datasetID: "shared"}]) != 1 {
		t.Fatalf("byData=%v", svc.byData)
	}
}

func TestSecondaryDatasetEventMapsToPrimaryViewGrain(t *testing.T) {
	writes := eventWrites(viewindex.ViewIndexSchema{
		PrimaryDatasetID: "primary",
		Columns: []*pb.ViewColumn{{
			OriginId: "secondary.factor", ColumnName: "secondary.factor",
		}},
	}, "secondary", []*pb.RowFieldUpsert{{
		Key: &pb.RowKey{SpaceId: "space", DatasetId: "secondary", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}},
		Fields: []*pb.FieldValue{{
			FieldId: "factor", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.5}},
		}},
	}})
	if len(writes) != 1 || writes[0].Key.Key.GetDatasetId() != "primary" {
		t.Fatalf("writes=%v", writes)
	}
}

func TestSecondaryEventRecoversCompleteMissingViewRow(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	if rsp, err := svc.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: auth, IndexId: "multi", Schema: &pb.ViewIndexSchema{
		SpaceId: "space", ViewId: "multi", PrimaryDatasetId: "primary", ViewVersion: 1, Engine: "bleve", ViewSchemaHash: "hash",
		Columns: []*pb.ViewColumn{{OriginId: "primary.base", ColumnName: "base"}, {OriginId: "secondary.factor", ColumnName: "factor"}},
	}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("prepare rsp=%v err=%v", rsp, err)
	}
	svc.SetPrimaryAuth(&pb.AuthInfo{AppId: "storage-view"})
	svc.SetPrimaryReader(recoveryFieldReader{primaryPresent: true})
	event := &pb.RowFieldUpsert{
		Key:    &pb.RowKey{SpaceId: "space", DatasetId: "secondary", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}},
		Fields: []*pb.FieldValue{{FieldId: "factor", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 2}}}},
	}
	engine, err := svc.engineFor("multi")
	if err != nil {
		t.Fatal(err)
	}
	schema := svc.schemas["multi"]
	initial := eventWrites(schema, "secondary", []*pb.RowFieldUpsert{event})
	recovered, err := svc.recoverMissingRows(ctx, engine, "multi", schema, "secondary", []*pb.RowFieldUpsert{event}, initial)
	if err != nil || len(recovered) != 1 || len(recovered[0].Fields) != 2 {
		t.Fatalf("recover failed recovered=%v err=%v", recovered, err)
	}
	if err := svc.applyDatasetEvent(ctx, "space", "secondary", []*pb.RowFieldUpsert{event}); err != nil {
		t.Fatal(err)
	}
	key := &pb.RowKey{SpaceId: "space", DatasetId: "primary", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}
	rows, err := svc.query(ctx, "multi", []*pb.RowKey{key}, nil)
	if err != nil || len(rows) != 1 || len(rows[0].GetFields()) != 2 {
		t.Fatalf("recovered rows=%v err=%v", rows, err)
	}
	svc.SetPrimaryReader(recoveryFieldReader{primaryPresent: false})
	if err := svc.applyDatasetEvent(ctx, "space", "secondary", []*pb.RowFieldUpsert{event}); err != nil {
		t.Fatal(err)
	}
	rows, err = svc.query(ctx, "multi", []*pb.RowKey{key}, nil)
	if err != nil || len(rows) != 1 {
		t.Fatalf("row unexpectedly disappeared: rows=%v err=%v", rows, err)
	}
}

func TestCompleteEventCreatesMissingViewRowWithoutRecovery(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	if rsp, err := svc.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: auth, IndexId: "single", Schema: &pb.ViewIndexSchema{
		SpaceId: "space", ViewId: "single", PrimaryDatasetId: "market", ViewVersion: 1, Engine: "bleve", ViewSchemaHash: "hash",
		Columns: []*pb.ViewColumn{{OriginId: "market.close", ColumnName: "close"}},
	}}); err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("prepare rsp=%v err=%v", rsp, err)
	}
	event := &pb.RowFieldUpsert{
		Key:    &pb.RowKey{SpaceId: "space", DatasetId: "market", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}},
		Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 3}}}},
	}
	if err := svc.applyDatasetEvent(ctx, "space", "market", []*pb.RowFieldUpsert{event}); err != nil {
		t.Fatal(err)
	}
	rows, err := svc.query(ctx, "single", []*pb.RowKey{event.GetKey()}, nil)
	if err != nil || len(rows) != 1 || len(rows[0].GetFields()) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
}

func TestViewEventUsesDatasetDots(t *testing.T) {
	schema := viewindex.ViewIndexSchema{SpaceID: "space", ViewID: "dots", PrimaryDatasetID: "primary", ViewVersion: 1, SchemaHash: "hash", Columns: []*pb.ViewColumn{
		{OriginId: "market.v2.close", ColumnName: "close"},
	}}
	event := &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: "space", DatasetId: "market.v2", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 3}}}}}
	writes := eventWrites(schema, "market.v2", []*pb.RowFieldUpsert{event})
	if len(writes) != 1 || len(writes[0].Fields) != 1 || writes[0].Fields[0].GetFieldId() != "close" {
		t.Fatalf("dataset with dot was not mapped: %v", writes)
	}
}

func TestViewBuildBackfillDoesNotOverwriteLiveAndSwitchesAtomically(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	schema := func() *pb.ViewIndexSchema {
		return &pb.ViewIndexSchema{
			SpaceId: "space", ViewId: "logical", PrimaryDatasetId: "shared", ViewVersion: 1, Engine: "bleve", ViewSchemaHash: "hash",
			Columns: []*pb.ViewColumn{{OriginId: "shared.value", ColumnName: "shared.value"}},
		}
	}
	prepare := func(id string) {
		t.Helper()
		rsp, err := svc.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{AuthInfo: auth, IndexId: id, Schema: schema()})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("prepare %s rsp=%v err=%v", id, rsp, err)
		}
	}
	key := &pb.RowKey{SpaceId: "space", DatasetId: "shared", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}
	apply := func(id, value, mode string) {
		t.Helper()
		rsp, err := svc.ApplyViewIndex(ctx, &pb.ApplyViewIndexReq{
			AuthInfo: auth, IndexId: id,
			Batch: &pb.ViewIndexWriteBatch{
				ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: mode,
				RowWrites: []*pb.ViewIndexRowWrite{{
					Key:    &pb.ViewIndexRowKey{RowKey: key},
					Fields: []*pb.FieldValue{{FieldId: "shared.value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: value}}}},
				}},
			},
		})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("apply %s rsp=%v err=%v", id, rsp, err)
		}
	}
	prepare("idx-a")
	apply("idx-a", "old", "LIVE_WRITE")
	prepare("idx-b")
	apply("idx-b", "live", "LIVE_WRITE")
	if err := svc.BackfillView(ctx, "space", "logical", 100); err != nil {
		t.Fatal(err)
	}
	if err := svc.SwitchView(ctx, "space", "logical", time.Hour); err != nil {
		t.Fatal(err)
	}
	rsp, err := svc.SearchRecordRows(ctx, &pb.SearchRecordRowsReq{
		AuthInfo: auth, SpaceId: "space", ViewId: "logical",
		Keys:        []*pb.RecordKey{{SpaceId: "space", DatasetId: "shared", RecordId: "r", Version: "1"}},
		ColumnNames: []string{"shared.value"},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetRows()) != 1 ||
		rsp.GetRows()[0].GetFields()[0].GetValue().GetStringValue() != "live" {
		t.Fatalf("rsp=%v err=%v", rsp, err)
	}
}

func TestBackfillReadsNewDatasetColumnsByExistingGrain(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	svc.SetPrimaryAuth(&pb.AuthInfo{AppId: "storage-view", AppKey: "primary-key"})
	ctx := context.Background()
	auth := &pb.AuthInfo{AppId: "caller", AppKey: datanode.ServiceAuthKey("view-secret", "caller")}
	prepare := func(id string, columns ...*pb.ViewColumn) {
		t.Helper()
		rsp, err := svc.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{
			AuthInfo: auth, IndexId: id,
			Schema: &pb.ViewIndexSchema{
				SpaceId: "space", ViewId: "joined", PrimaryDatasetId: "primary", ViewVersion: 1, Engine: "bleve",
				ViewSchemaHash: "hash", Columns: columns,
			},
		})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("prepare %s rsp=%v err=%v", id, rsp, err)
		}
	}
	primaryColumn := &pb.ViewColumn{OriginId: "primary.close", ColumnName: "primary.close"}
	secondaryColumn := &pb.ViewColumn{OriginId: "secondary.factor", ColumnName: "secondary.factor"}
	prepare("join-a", primaryColumn)
	key := &pb.RowKey{SpaceId: "space", DatasetId: "primary", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}
	rsp, err := svc.ApplyViewIndex(ctx, &pb.ApplyViewIndexReq{
		AuthInfo: auth, IndexId: "join-a",
		Batch: &pb.ViewIndexWriteBatch{
			ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: "LIVE_WRITE",
			RowWrites: []*pb.ViewIndexRowWrite{{
				Key:    &pb.ViewIndexRowKey{RowKey: key},
				Fields: []*pb.FieldValue{{FieldId: "primary.close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}}}},
			}},
		},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("apply rsp=%v err=%v", rsp, err)
	}
	if err := svc.AttachActiveView(&pb.View{SpaceId: "space", ViewId: "joined", ActiveIndexId: "join-a", ActiveViewRevision: 1, ActiveViewSchemaHash: "hash", Engine: "bleve", ActiveColumns: []*pb.ViewColumn{primaryColumn}, Status: "active"}); err != nil {
		t.Fatal(err)
	}
	prepare("join-b", primaryColumn, secondaryColumn)
	if err := svc.BackfillViewWithReader(ctx, "space", "joined", 100, fakeFieldReader{}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SwitchView(ctx, "space", "joined", time.Hour); err != nil {
		t.Fatal(err)
	}
	result, err := svc.SearchRecordRows(ctx, &pb.SearchRecordRowsReq{
		AuthInfo: auth, SpaceId: "space", ViewId: "joined",
		Keys:        []*pb.RecordKey{{SpaceId: "space", DatasetId: "primary", RecordId: "r", Version: "1"}},
		ColumnNames: []string{"primary.close", "secondary.factor"},
	})
	if err != nil || result.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(result.GetRows()) != 1 || len(result.GetRows()[0].GetFields()) != 2 {
		t.Fatalf("result=%v err=%v", result, err)
	}
}

func TestBackfillWaitsForLiveWorkToDrain(t *testing.T) {
	svc, err := New(filepath.Join(t.TempDir(), "views"), "view-secret")
	if err != nil {
		t.Fatal(err)
	}
	svc.liveWork.Store(1)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := svc.waitForLiveIdle(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	svc.liveWork.Store(0)
	if err := svc.waitForLiveIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func successRetInfo() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}
}
