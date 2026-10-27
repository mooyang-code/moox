package test

import (
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// fillDataset 让只关心行的测试请求带上顶层数据集：空着时取第一行的 space_id、dataset_id。
func fillDataset(req *pb.PrimaryUpsertFieldsReq) *pb.PrimaryUpsertFieldsReq {
	if req.GetSpaceId() == "" && req.GetDatasetId() == "" && len(req.GetRows()) > 0 && req.GetRows()[0].GetKey() != nil {
		req.SpaceId, req.DatasetId = req.GetRows()[0].GetKey().GetSpaceId(), req.GetRows()[0].GetKey().GetDatasetId()
	}
	return req
}

// emptyRequestSnapshot 是没有任何元数据条目的请求快照：主库要求每个写请求都带快照，只关心路由与行的测试用它。
func emptyRequestSnapshot() metadata.RequestSnapshot { return emptySnapshot{} }

type emptySnapshot struct{}

func (emptySnapshot) GetDataset(string, string) (*pb.Dataset, bool) { return nil, false }
func (emptySnapshot) GetDataNode(string) (*pb.DataNode, bool)       { return nil, false }
func (emptySnapshot) ListDatasetColumns(string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}
