//go:build cgo

package e2e

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
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

// 记录型 View（Bleve）建成后为空，只跟随之后的写入：按记录键回读整条记录写进索引。
func TestRecordViewFollowsRecordWrites(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notesDataset := &pb.Dataset{SpaceId: readyBenchSpace, DatasetId: "notes", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active", CreatedAt: followEnvDatasetCreatedAt}
	titleColumn := &pb.DatasetColumn{SpaceId: readyBenchSpace, DatasetId: "notes", ColumnName: "title", DisplayName: "标题", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Status: "active"}
	datasets := newTestDatasetCache(t, []*pb.Dataset{notesDataset}, []*pb.DatasetColumn{titleColumn})
	store, err := pebble.Open(pebble.Options{Path: filepath.Join(t.TempDir(), "pebble")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	primary, err := primarystore.New(primarystore.Options{Store: store, Datasets: datasets})
	if err != nil {
		t.Fatal(err)
	}
	view, err := viewservice.New(filepath.Join(t.TempDir(), "view"))
	if err != nil {
		t.Fatal(err)
	}
	reader := primaryFieldReader{service: primary}
	view.SetPrimaryReader(reader)
	stop, err := view.StartFollowing(ctx, viewservice.FollowOptions{Changes: reader, Range: reader, Publisher: discardReadyPublisher{}, Wait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	// 记录型数据集建好后不调任何 View 接口：维护流程为它建 Bleve 索引。
	meta := &followEnvMetadata{}
	meta.addDataset(notesDataset, titleColumn)
	stopMaintainer, err := view.StartViewMaintainerAsync(ctx, viewservice.MaintenanceOptions{Metadata: meta, PrimaryRange: reader, Interval: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer stopMaintainer()
	deadline := time.Now().Add(20 * time.Second)
	for {
		rsp, _ := view.GetViewState(ctx, &pb.GetViewStateReq{SpaceId: readyBenchSpace, DatasetId: "notes"})
		if rsp.GetIndexId() != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("记录型 View 没有建好")
		}
		time.Sleep(20 * time.Millisecond)
	}
	record := func(id, version, title string) *pb.Row {
		return &pb.Row{
			Key:    &pb.RowKey{SpaceId: readyBenchSpace, DatasetId: "notes", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: id, Version: version}}},
			Fields: []*pb.FieldValue{{FieldId: "title", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: title}}}},
		}
	}
	rsp, err := primary.UpsertFields(ctx, &pb.PrimaryUpsertFieldsReq{SpaceId: readyBenchSpace, DatasetId: "notes",
		Rows: []*pb.Row{record("r1", "1", "first note"), record("r2", "1", "second note")}})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("写入记录：rsp=%v err=%v", rsp, err)
	}
	applied, err := view.WaitViewApplied(ctx, &pb.WaitViewAppliedReq{SpaceId: readyBenchSpace, DatasetId: "notes", WaitTimeoutMs: 10000})
	if err != nil || !applied.GetApplied() {
		t.Fatalf("等记录型 View 跟上：rsp=%v err=%v", applied, err)
	}
	search, err := view.SearchRecordRows(ctx, &pb.SearchRecordRowsReq{SpaceId: readyBenchSpace, DatasetId: "notes", TextQuery: "note", Page: &pb.Page{Page: 1, Size: 10}})
	if err != nil || search.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(search.GetRows()) != 2 {
		t.Fatalf("检索记录：rsp=%v err=%v", search, err)
	}
}
