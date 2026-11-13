package test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	primarystore "github.com/mooyang-code/moox/modules/storage/internal/service/primarystore"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// 两个空间里的同名数据集互不干扰；记录写入必须带版本。
func TestStorageSeparatesSpacesAndRejectsEmptyRecordVersion(t *testing.T) {
	ctx := context.Background()
	store, err := pebble.Open(pebble.Options{Path: filepath.Join(t.TempDir(), "pebble")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	primary, err := primarystore.New(primarystore.Options{Store: store, Snapshot: activeRequestSnapshot})
	if err != nil {
		t.Fatal(err)
	}
	key := func(space, version string) *pb.RowKey {
		return &pb.RowKey{SpaceId: space, DatasetId: "shared", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "record", Version: version}}}
	}
	row := func(space, version string) *pb.RowFieldUpsert {
		return &pb.RowFieldUpsert{
			Key:    key(space, version),
			Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: space}}}},
		}
	}
	auth := &pb.AuthInfo{AppId: "storage-primary"}
	for _, space := range []string{"space-a", "space-b"} {
		rsp, err := primary.UpsertFields(ctx, fillDataset(&pb.PrimaryUpsertFieldsReq{AuthInfo: auth, Rows: []*pb.RowFieldUpsert{row(space, "1")}}))
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetKeys()) != 1 {
			t.Fatalf("write %s rsp=%v err=%v", space, rsp, err)
		}
	}
	for _, space := range []string{"space-a", "space-b"} {
		read, err := primary.ReadRows(ctx, &pb.PrimaryReadRowsReq{AuthInfo: auth, Keys: []*pb.RowKey{key(space, "1")}, ColumnNames: []string{"value"}})
		if err != nil || len(read.GetRows()) != 1 || read.GetRows()[0].GetFields()[0].GetValue().GetStringValue() != space {
			t.Fatalf("read %s rsp=%v err=%v", space, read, err)
		}
	}
	bad, err := primary.UpsertFields(ctx, fillDataset(&pb.PrimaryUpsertFieldsReq{AuthInfo: auth, Rows: []*pb.RowFieldUpsert{row("space-a", "")}}))
	if err != nil || bad.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("empty version rsp=%v err=%v", bad, err)
	}
}
