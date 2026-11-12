package primarystore

import pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"

// singleDatasetSnapshot 是只含一个数据集的元数据快照，供测试使用。
type singleDatasetSnapshot struct{ dataset *pb.Dataset }

func (s singleDatasetSnapshot) GetDataset(string, string) (*pb.Dataset, bool) {
	return s.dataset, s.dataset != nil
}

func (singleDatasetSnapshot) ListDatasetColumns(string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return nil, nil, nil
}
