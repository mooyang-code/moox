package primarystore

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type factorDatasetAdminNode struct {
	deleted  int
	restored int
}

func (n *factorDatasetAdminNode) UpsertFields(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
	return nil, nil
}

func (*factorDatasetAdminNode) CommitInput(context.Context, *pb.CommitInputReq) (*pb.CommitInputRsp, error) {
	return nil, nil
}

func (*factorDatasetAdminNode) WriteFactorRows(context.Context, *pb.WriteFactorRowsReq) (*pb.WriteFactorRowsRsp, error) {
	return nil, nil
}

func (*factorDatasetAdminNode) LookupWriteReceipt(context.Context, *pb.LookupWriteReceiptReq) (*pb.LookupWriteReceiptRsp, error) {
	return nil, nil
}

func (n *factorDatasetAdminNode) ReadFields(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
	return nil, nil
}

func (*factorDatasetAdminNode) CleanupExpiredBuckets(context.Context, *pb.CleanupExpiredBucketsReq) (*pb.CleanupExpiredBucketsRsp, error) {
	return nil, nil
}

func (*factorDatasetAdminNode) GetNodeState(context.Context, *pb.GetNodeStateReq) (*pb.GetNodeStateRsp, error) {
	return nil, nil
}

func (n *factorDatasetAdminNode) DeleteDatasetRows(_ context.Context, req *pb.DeleteDatasetRowsReq) (*pb.DeleteDatasetRowsRsp, error) {
	n.deleted++
	if req.GetDatasetId() != "dataset_factor_prices_1m" || req.GetAuthInfo().GetAppId() != "factor" {
		return &pb.DeleteDatasetRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("unexpected factor delete request"))}, nil
	}
	return &pb.DeleteDatasetRowsRsp{RetInfo: successRetInfo(), DeletedRanges: 1}, nil
}

func (n *factorDatasetAdminNode) RestoreDatasetRows(_ context.Context, req *pb.RestoreDatasetRowsReq) (*pb.RestoreDatasetRowsRsp, error) {
	n.restored++
	if req.GetDatasetId() != "dataset_factor_prices_1m" || req.GetAuthInfo().GetAppId() != "factor" {
		return &pb.RestoreDatasetRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("unexpected factor restore request"))}, nil
	}
	return &pb.RestoreDatasetRowsRsp{RetInfo: successRetInfo()}, nil
}

type factorDatasetAdminSnapshot struct{ dataset *pb.Dataset }

func (s factorDatasetAdminSnapshot) GetDataset(string, string) (*pb.Dataset, bool) {
	return s.dataset, s.dataset != nil
}

func (factorDatasetAdminSnapshot) GetDataNode(string) (*pb.DataNode, bool) { return nil, false }

func (factorDatasetAdminSnapshot) ListDatasetColumns(string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return nil, nil, nil
}

func newFactorDatasetAdminService(t *testing.T, node *factorDatasetAdminNode, appID string) *Service {
	t.Helper()
	dataset := &pb.Dataset{
		SpaceId: "crypto", DatasetId: "dataset_factor_prices_1m", DataNodeId: "factor-node",
		Attributes: map[string]string{"dataset_role": "factor_result", "write_owner": "factor"},
	}
	service, err := New(Options{
		Resolver: func(_ context.Context, spaceID, datasetID string) (pb.DataNodeRuntimeService, error) {
			if spaceID != "crypto" || datasetID != "dataset_factor_prices_1m" {
				t.Errorf("resolved unexpected dataset %s/%s", spaceID, datasetID)
			}
			return node, nil
		},
		Snapshot: func() metadata.RequestSnapshot { return factorDatasetAdminSnapshot{dataset: dataset} },
		Authorizer: func(auth *pb.AuthInfo) error {
			if auth.GetAppId() != appID {
				return errors.New("unexpected caller")
			}
			return nil
		},
		AuthSigner: func(auth *pb.AuthInfo) (*pb.AuthInfo, error) {
			return &pb.AuthInfo{AppId: auth.GetAppId(), AppKey: "signed"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestDeleteDatasetRowsAllowsFactorResultOwner(t *testing.T) {
	node := &factorDatasetAdminNode{}
	service := newFactorDatasetAdminService(t, node, "factor")
	rsp, err := service.DeleteDatasetRows(context.Background(), &pb.PrimaryDeleteDatasetRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: "factor"}, SpaceId: "crypto", DatasetId: "dataset_factor_prices_1m",
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || rsp.GetDeletedRanges() != 1 {
		t.Fatalf("response=%v err=%v", rsp, err)
	}
	if node.deleted != 1 {
		t.Fatalf("DataNode delete count = %d, want 1", node.deleted)
	}
}

func TestDeleteDatasetRowsRejectsNonFactorCallerForFactorResult(t *testing.T) {
	node := &factorDatasetAdminNode{}
	service := newFactorDatasetAdminService(t, node, "collector")
	rsp, err := service.DeleteDatasetRows(context.Background(), &pb.PrimaryDeleteDatasetRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: "collector"}, SpaceId: "crypto", DatasetId: "dataset_factor_prices_1m",
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("response=%v err=%v", rsp, err)
	}
	if node.deleted != 0 {
		t.Fatal("unauthorized caller reached DataNode deletion")
	}
}

func TestRestoreDatasetRowsAllowsFactorResultOwner(t *testing.T) {
	node := &factorDatasetAdminNode{}
	service := newFactorDatasetAdminService(t, node, "factor")
	rsp, err := service.RestoreDatasetRows(context.Background(), &pb.PrimaryRestoreDatasetRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: "factor"}, SpaceId: "crypto", DatasetId: "dataset_factor_prices_1m",
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("response=%v err=%v", rsp, err)
	}
	if node.restored != 1 {
		t.Fatalf("DataNode restore count = %d, want 1", node.restored)
	}
}
