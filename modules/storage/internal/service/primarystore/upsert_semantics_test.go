package primarystore

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestGenericUpsertRejectsFactorWriteSource(t *testing.T) {
	resolved := false
	svc, err := New(Options{
		Resolver: func(context.Context, string, string) (DataNodeClient, error) {
			resolved = true
			return &factorRowsNode{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "web"}, WriteSource: "factor",
		Rows: []*pb.RowFieldUpsert{factorRowsRecord("value")},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("factor write source rsp=%v err=%v", rsp, err)
	}
	if resolved {
		t.Fatal("generic factor write source reached DataNode")
	}
}

func TestGenericUpsertRequiresFactorRowsForFactorResultDataset(t *testing.T) {
	resolved := false
	svc, err := New(Options{
		Resolver: func(context.Context, string, string) (DataNodeClient, error) {
			resolved = true
			return &factorRowsNode{}, nil
		},
		Snapshot: factorRowsMetadata,
	})
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "factor"}, Rows: []*pb.RowFieldUpsert{factorRowsRecord("value")},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("factor result generic upsert rsp=%v err=%v", rsp, err)
	}
	if resolved {
		t.Fatal("generic factor result upsert reached DataNode")
	}
}
