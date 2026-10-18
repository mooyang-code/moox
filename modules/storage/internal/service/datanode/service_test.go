package datanode

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func dataNodePeriodRow(exp *pb.DatasetPeriodExpectation, subject, tag string) *pb.RowFieldUpsert {
	return &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: exp.GetSpaceId(), DatasetId: exp.GetDatasetId(), Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: subject, SeriesTag: tag, Freq: exp.GetFrequency(), DataTime: time.Unix(exp.GetPeriodTime(), 0).UTC().Format(time.RFC3339Nano)}}}, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}}}
}

func TestNewServiceRequiresAuthSecret(t *testing.T) {
	_, err := NewService(Options{
		NodeID: "node-a",
		Pebble: pebble.Options{
			NodeID: "node-a",
			Path:   filepath.Join(t.TempDir(), "node"),
		},
	})
	if err == nil {
		t.Fatal("expected missing auth secret to be rejected")
	}
}

func TestErrorCodeUsesTypedValidationErrors(t *testing.T) {
	if got := errorCode(errors.New("required backend is unavailable")); got != pb.ErrorCode_INNER_ERR {
		t.Fatalf("plain error classified as %s", got)
	}
	_, validationErr := pebble.NormalizeRowKey(nil)
	if got := errorCode(validationErr); got != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("validation error classified as %s", got)
	}
}

func TestGetNodeStateRequiresSignedIdentityAndReturnsConfiguredNode(t *testing.T) {
	node, err := NewService(Options{
		NodeID:     "node-a",
		AuthSecret: "secret",
		Pebble: pebble.Options{
			NodeID: "node-a",
			Path:   filepath.Join(t.TempDir(), "node"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()

	ctx := context.Background()
	wrongSignature, err := node.GetNodeState(ctx, &pb.GetNodeStateReq{
		NodeId:   "node-a",
		AuthInfo: &pb.AuthInfo{AppId: "storage-metadata", AppKey: "bad"},
	})
	if err != nil || wrongSignature.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("wrong signature: rsp=%v err=%v", wrongSignature, err)
	}

	mismatchedNode, err := node.GetNodeState(ctx, &pb.GetNodeStateReq{
		NodeId:   "node-b",
		AuthInfo: &pb.AuthInfo{AppId: "storage-metadata", AppKey: ServiceAuthKey("secret", "storage-metadata")},
	})
	if err != nil || mismatchedNode.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("mismatched node: rsp=%v err=%v", mismatchedNode, err)
	}

	ready, err := node.GetNodeState(ctx, &pb.GetNodeStateReq{
		NodeId:   "node-a",
		AuthInfo: &pb.AuthInfo{AppId: "storage-metadata", AppKey: ServiceAuthKey("secret", "storage-metadata")},
	})
	if err != nil || ready.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("valid identity: rsp=%v err=%v", ready, err)
	}
	if ready.GetNodeId() != "node-a" || ready.GetStatus() != "READY" {
		t.Fatalf("node state = (%q, %q), want (node-a, READY)", ready.GetNodeId(), ready.GetStatus())
	}
}
