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

// activeRequestSnapshot 是任何数据集都存在且启用、但没有列定义的请求快照，只关心读写行的测试用它。
func activeRequestSnapshot() metadata.RequestSnapshot { return activeSnapshot{} }

type activeSnapshot struct{}

func (activeSnapshot) GetDataset(spaceID, datasetID string) (*pb.Dataset, bool) {
	return &pb.Dataset{SpaceId: spaceID, DatasetId: datasetID, Status: "active"}, true
}
func (activeSnapshot) ListDatasetColumns(string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}
