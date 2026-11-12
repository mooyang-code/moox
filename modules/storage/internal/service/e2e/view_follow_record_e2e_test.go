//go:build cgo

package e2e

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	primarystore "github.com/mooyang-code/moox/modules/storage/internal/service/primarystore"
	viewservice "github.com/mooyang-code/moox/modules/storage/internal/service/view"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/proto"
)

type discardReadyPublisher struct{}

func (discardReadyPublisher) Publish(context.Context, events.Event, proto.Message, events.PublishOptions) (*jetstream.PublishAck, error) {
	return &jetstream.PublishAck{}, nil
}

// recordSnapshot 只登记一个记录型数据集。
type recordSnapshot struct{ dataset *pb.Dataset }

func (s recordSnapshot) GetDataset(spaceID, datasetID string) (*pb.Dataset, bool) {
	return s.dataset, spaceID == s.dataset.GetSpaceId() && datasetID == s.dataset.GetDatasetId()
}

func (s recordSnapshot) ListDatasetColumns(string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

// 记录型 View（Bleve）建成后为空，只跟随之后的写入：按记录键回读整条记录写进索引。
func TestRecordViewFollowsRecordWrites(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	snapshot := recordSnapshot{dataset: &pb.Dataset{SpaceId: readyBenchSpace, DatasetId: "notes", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"}}
	store, err := pebble.Open(pebble.Options{NodeID: "storage-primary", Path: filepath.Join(t.TempDir(), "pebble")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	primary, err := primarystore.New(primarystore.Options{Store: store, Snapshot: func() metadata.RequestSnapshot { return snapshot }})
	if err != nil {
		t.Fatal(err)
	}
	view, err := viewservice.New(filepath.Join(t.TempDir(), "view"), followEnvSecret)
	if err != nil {
		t.Fatal(err)
	}
	reader := primaryFieldReader{service: primary}
	view.SetPrimaryAuth(&pb.AuthInfo{AppId: "storage-primary"})
	view.SetPrimaryReader(reader)
	stop, err := view.StartFollowing(ctx, viewservice.FollowOptions{Changes: reader, Range: reader, Publisher: discardReadyPublisher{}, Wait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	columns := []*pb.ViewColumn{{SpaceId: readyBenchSpace, ViewId: "notes_view", ColumnName: "title", OriginId: "title", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN}}
	meta := &followEnvMetadata{views: []*pb.View{{SpaceId: readyBenchSpace, ViewId: "notes_view", DatasetId: "notes", Engine: "bleve", Columns: columns, Status: "active"}}}
	stopMaintainer, err := view.StartViewMaintainerAsync(ctx, viewservice.MaintenanceOptions{Metadata: meta, PrimaryRange: reader, Interval: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer stopMaintainer()
	deadline := time.Now().Add(20 * time.Second)
	for {
		rsp, _ := view.GetViewState(ctx, &pb.GetViewStateReq{AuthInfo: followEnvViewAuth(), SpaceId: readyBenchSpace, ViewId: "notes_view"})
		if rsp.GetIndexId() != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("记录型 View 没有建好")
		}
		time.Sleep(20 * time.Millisecond)
	}
	record := func(id, version, title string) *pb.RowFieldUpsert {
		return &pb.RowFieldUpsert{
			Key:    &pb.RowKey{SpaceId: readyBenchSpace, DatasetId: "notes", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: id, Version: version}}},
			Fields: []*pb.FieldValue{{FieldId: "title", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: title}}}},
		}
	}
	rsp, err := primary.UpsertFields(ctx, &pb.PrimaryUpsertFieldsReq{AuthInfo: &pb.AuthInfo{AppId: "storage-primary"}, SpaceId: readyBenchSpace, DatasetId: "notes",
		Rows: []*pb.RowFieldUpsert{record("r1", "1", "first note"), record("r2", "1", "second note")}})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("写入记录：rsp=%v err=%v", rsp, err)
	}
	applied, err := view.WaitViewApplied(ctx, &pb.WaitViewAppliedReq{AuthInfo: followEnvViewAuth(), SpaceId: readyBenchSpace, ViewId: "notes_view", WaitTimeoutMs: 10000})
	if err != nil || !applied.GetApplied() {
		t.Fatalf("等记录型 View 跟上：rsp=%v err=%v", applied, err)
	}
	search, err := view.SearchRecordRows(ctx, &pb.SearchRecordRowsReq{AuthInfo: followEnvViewAuth(), SpaceId: readyBenchSpace, ViewId: "notes_view", TextQuery: "note", Page: &pb.Page{Page: 1, Size: 10}})
	if err != nil || search.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(search.GetRows()) != 2 {
		t.Fatalf("检索记录：rsp=%v err=%v", search, err)
	}
}
