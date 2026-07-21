package test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	primarystorev2 "github.com/mooyang-code/moox/modules/storage/internal/service/primarystorev2"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestStorageV2RoutesSpaceDatasetAndRejectsEmptyRecordVersion(t *testing.T) {
	ctx := context.Background()
	const secret = "contract-secret"
	newNode := func(id string) *datanode.Service {
		node, err := datanode.NewService(datanode.Options{
			NodeID:     id,
			AuthSecret: secret,
			Pebble:     pebble.Options{NodeID: id, Path: filepath.Join(t.TempDir(), id)},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = node.Close() })
		return node
	}
	nodes := map[string]pb.DataNodeService{
		"space-a/shared": newNode("node-a"),
		"space-b/shared": newNode("node-b"),
	}
	primary, err := primarystorev2.New(primarystorev2.Options{
		Resolver: func(_ context.Context, spaceID, datasetID string) (pb.DataNodeService, error) {
			return nodes[spaceID+"/"+datasetID], nil
		},
		AuthSigner: func(auth *pb.AuthInfo) (*pb.AuthInfo, error) {
			return &pb.AuthInfo{AppId: auth.GetAppId(), AppKey: datanode.ServiceAuthKey(secret, auth.GetAppId())}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	row := func(space, version string) *pb.RowFieldUpsert {
		return &pb.RowFieldUpsert{
			Key: &pb.RowKey{
				SpaceId: space, DatasetId: "shared",
				Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "record", Version: version}},
			},
			Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: space}}}},
		}
	}
	auth := &pb.AuthInfo{AppId: "storage-primary"}
	rsp, err := primary.WriteFields(ctx, &pb.PrimaryWriteFieldsReq{AuthInfo: auth, Rows: []*pb.RowFieldUpsert{row("space-a", "1"), row("space-b", "1")}})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetKeys()) != 2 {
		t.Fatalf("write rsp=%v err=%v", rsp, err)
	}
	bad, err := primary.WriteFields(ctx, &pb.PrimaryWriteFieldsReq{AuthInfo: auth, Rows: []*pb.RowFieldUpsert{row("space-a", "")}})
	if err != nil || bad.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("empty version rsp=%v err=%v", bad, err)
	}
}
