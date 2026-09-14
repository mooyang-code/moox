package primarystore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestInputCommitSpoofedMergeSourceCannotSetReady(t *testing.T) {
	writes := 0
	svc, err := New(Options{Resolver: func(context.Context, string, string) (DataNodeClient, error) {
		writes++
		return &recordingNode{write: func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
			t.Fatal("spoofed merge upsert reached DataNode")
			return nil, nil
		}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	row := inputCommitRow("BTC-USDT", map[string]float64{"close": 1})
	row.Attributes = map[string]*pb.TypedValue{"moox.input_ready": {Value: &pb.TypedValue_BoolValue{BoolValue: true}}}
	rsp, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "web"}, WriteSource: "merge", Rows: []*pb.RowFieldUpsert{row},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("spoofed merge upsert rsp=%v err=%v", rsp, err)
	}
	if writes != 0 {
		t.Fatalf("DataNode resolver called %d times", writes)
	}
}

func TestInputCommitSpoofedFactorSourceCannotPatch(t *testing.T) {
	writes := 0
	svc, err := New(Options{Resolver: func(context.Context, string, string) (DataNodeClient, error) {
		writes++
		return &recordingNode{write: func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
			t.Fatal("spoofed factor upsert reached DataNode")
			return nil, nil
		}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "web"}, WriteSource: "factor",
		Rows: []*pb.RowFieldUpsert{inputCommitRow("ETH-USDT", map[string]float64{"ma": 1.2})},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("spoofed factor upsert rsp=%v err=%v", rsp, err)
	}
	if writes != 0 {
		t.Fatalf("DataNode resolver called %d times", writes)
	}
}

func TestInputCommitUnauthorizedClientCannotCallCommitOrPatch(t *testing.T) {
	svc, err := New(Options{Resolver: func(context.Context, string, string) (DataNodeClient, error) {
		t.Fatal("unauthorized commit resolved DataNode")
		return nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	row := inputCommitRow("SOL-USDT", map[string]float64{"close": 10})
	auth := &pb.AuthInfo{AppId: "web"}
	commit, err := svc.CommitInput(context.Background(), &pb.PrimaryCommitInputReq{
		AuthInfo: auth, CommitId: "commit-web", RequiredFields: []string{"close"}, Row: row,
	})
	if err != nil || commit.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("unauthorized commit rsp=%v err=%v", commit, err)
	}
	patch, err := svc.PatchFactor(context.Background(), &pb.PrimaryPatchFactorReq{
		AuthInfo: auth, CommitId: "patch-web", BindingVersion: "bind-1", OwnedFields: []string{"ma"},
		Row: inputCommitRow("SOL-USDT", map[string]float64{"ma": 1}),
	})
	if err != nil || patch.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("unauthorized patch rsp=%v err=%v", patch, err)
	}
}

func TestInputCommitAuthorizedMergeAndFactorDoNotClobber(t *testing.T) {
	node, err := datanode.NewService(datanode.Options{NodeID: "node-a", AuthSecret: "node-secret", Pebble: pebble.Options{NodeID: "node-a", Path: filepath.Join(t.TempDir(), "node")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = node.Close() })
	svc, err := New(Options{Node: node, AuthSigner: func(auth *pb.AuthInfo) (*pb.AuthInfo, error) {
		return &pb.AuthInfo{AppId: auth.GetAppId(), AppKey: datanode.ServiceAuthKey("node-secret", auth.GetAppId())}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	row := inputCommitRow("BNB-USDT", map[string]float64{"close": 4})
	commit, err := svc.CommitInput(context.Background(), &pb.PrimaryCommitInputReq{
		AuthInfo: &pb.AuthInfo{AppId: "merge"}, CommitId: "commit-base", RequiredFields: []string{"close"}, Row: row,
	})
	if err != nil || commit.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || commit.GetReceipt().GetPosition().GetSequence() == 0 || !commit.GetReceipt().GetInputReady() {
		t.Fatalf("merge commit rsp=%v err=%v", commit, err)
	}
	lookup, err := svc.LookupWriteReceipt(context.Background(), &pb.PrimaryLookupWriteReceiptReq{
		AuthInfo: &pb.AuthInfo{AppId: "merge"}, SpaceId: "space", DatasetId: "mdataset_crypto_kline_1m", CommitId: "commit-base",
	})
	if err != nil || lookup.GetReceipt().GetPosition().GetSequence() != commit.GetReceipt().GetPosition().GetSequence() {
		t.Fatalf("lookup rsp=%v err=%v", lookup, err)
	}
	patch, err := svc.PatchFactor(context.Background(), &pb.PrimaryPatchFactorReq{
		AuthInfo: &pb.AuthInfo{AppId: "factor"}, CommitId: "patch-ma", BindingVersion: "bind-ma-1", OwnedFields: []string{"ma"},
		Row: inputCommitRow("BNB-USDT", map[string]float64{"ma": 4.1}),
	})
	if err != nil || patch.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("factor patch rsp=%v err=%v", patch, err)
	}
	got, err := svc.ReadFields(context.Background(), &pb.PrimaryReadFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "merge"}, Keys: []*pb.RowKey{row.GetKey()}, FieldIds: []string{"close", "ma"}, AttributeKeys: []string{"moox.input_ready"},
	})
	if err != nil || got.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(got.GetRows()) != 1 {
		t.Fatalf("read rsp=%v err=%v", got, err)
	}
	values := map[string]float64{}
	for _, field := range got.GetRows()[0].GetFields() {
		values[field.GetFieldId()] = field.GetValue().GetDoubleValue()
	}
	if values["close"] != 4 || values["ma"] != 4.1 {
		t.Fatalf("fields=%v", values)
	}
	if !got.GetRows()[0].GetAttributes()["moox.input_ready"].GetBoolValue() {
		t.Fatal("factor patch cleared input_ready")
	}
}

func TestCommitInputAllowsMergeOnFactorResultDataset(t *testing.T) {
	node, err := datanode.NewService(datanode.Options{NodeID: "node-a", AuthSecret: "node-secret", Pebble: pebble.Options{NodeID: "node-a", Path: filepath.Join(t.TempDir(), "node")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = node.Close() })
	svc, err := New(Options{
		Node: node,
		Snapshot: func() metadata.RequestSnapshot {
			return factorResultSnapshot{}
		},
		AuthSigner: func(auth *pb.AuthInfo) (*pb.AuthInfo, error) {
			return &pb.AuthInfo{AppId: auth.GetAppId(), AppKey: datanode.ServiceAuthKey("node-secret", auth.GetAppId())}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	row := inputCommitRow("BTC-USDT", map[string]float64{"close": 4})
	commit, err := svc.CommitInput(context.Background(), &pb.PrimaryCommitInputReq{
		AuthInfo: &pb.AuthInfo{AppId: "moox-merge"}, CommitId: "commit-mdataset", RequiredFields: []string{"close"}, Row: row,
	})
	if err != nil || commit.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || commit.GetReceipt().GetPosition().GetSequence() == 0 {
		t.Fatalf("merge commit on factor_result dataset rsp=%v err=%v", commit, err)
	}
	denied, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "moox-merge"}, Rows: []*pb.RowFieldUpsert{row},
	})
	if err != nil || denied.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("merge generic upsert must stay denied rsp=%v err=%v", denied, err)
	}
}

func TestPatchFactorAcceptsEngineAppID(t *testing.T) {
	node, err := datanode.NewService(datanode.Options{NodeID: "node-a", AuthSecret: "node-secret", Pebble: pebble.Options{NodeID: "node-a", Path: filepath.Join(t.TempDir(), "node")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = node.Close() })
	svc, err := New(Options{Node: node, AuthSigner: func(auth *pb.AuthInfo) (*pb.AuthInfo, error) {
		return &pb.AuthInfo{AppId: auth.GetAppId(), AppKey: datanode.ServiceAuthKey("node-secret", auth.GetAppId())}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	row := inputCommitRow("BNB-USDT", map[string]float64{"ma": 4.1})
	upsert, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "moox-factor-engine"}, WriteSource: "factor",
		Rows: []*pb.RowFieldUpsert{row},
	})
	if err != nil || upsert.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("engine upsert rsp=%v err=%v", upsert, err)
	}
	patch, err := svc.PatchFactor(context.Background(), &pb.PrimaryPatchFactorReq{
		AuthInfo: &pb.AuthInfo{AppId: "moox-factor-engine"}, CommitId: "patch-engine", BindingVersion: "bind-ma-1",
		OwnedFields: []string{"ma"}, Row: row,
	})
	if err != nil || patch.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || patch.GetReceipt().GetPosition().GetSequence() == 0 {
		t.Fatalf("engine patch rsp=%v err=%v", patch, err)
	}
}

func inputCommitRow(subject string, fields map[string]float64) *pb.RowFieldUpsert {
	row := &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: "space", DatasetId: "mdataset_crypto_kline_1m", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
		SubjectId: subject, Freq: "1m", DataTime: "2026-09-13T16:00:00Z",
	}}}}
	for id, value := range fields {
		row.Fields = append(row.Fields, &pb.FieldValue{FieldId: id, Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}}})
	}
	return row
}
