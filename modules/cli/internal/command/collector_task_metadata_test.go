package command

import (
	"context"
	"errors"
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

// 代理把删除请求原样发给 Storage 的 Metadata 服务。
func TestCollectorStorageMetadataProxyForwardsDeleteRequests(t *testing.T) {
	var seen []string
	transport := storageGatewayCall(func(_ context.Context, service, method string, request, _ any) error {
		require.Equal(t, metadataServiceName, service)
		req, ok := request.(*storagepb.DeleteDatasetReq)
		require.True(t, ok, "unexpected request: %T", request)
		require.Equal(t, "d1", req.GetDatasetId())
		seen = append(seen, method)
		return nil
	})
	proxy := &collectorStorageMetadataProxy{gateway: transport}

	_, err := proxy.DeleteDataset(context.Background(), &storagepb.DeleteDatasetReq{SpaceId: "crypto", DatasetId: "d1"})
	require.NoError(t, err)
	require.Equal(t, []string{"DeleteDataset"}, seen)
}

type collectorMetadataFake struct {
	calls       []string
	datasetCode storagepb.ErrorCode
	datasetErr  error
}

func (f *collectorMetadataFake) DeleteDataset(_ context.Context, req *storagepb.DeleteDatasetReq) (*storagepb.DeleteDatasetRsp, error) {
	f.calls = append(f.calls, "dataset:"+req.GetSpaceId()+"/"+req.GetDatasetId())
	if f.datasetErr != nil {
		return nil, f.datasetErr
	}
	return &storagepb.DeleteDatasetRsp{RetInfo: &storagepb.RetInfo{Code: f.datasetCode, Msg: "拒绝"}}, nil
}

// 删数据集一次调用即可（查询索引随之删除）；对象已经不存在算成功，重跑能续做；Storage 拒绝或调用失败时报错。
func TestDeleteCollectorOwnedMetadataDeletesDatasets(t *testing.T) {
	var objects collectorOwnedMetadata
	objects.datasets = appendUniqueStorageObject(objects.datasets, "crypto", " d1 ")
	objects.datasets = appendUniqueStorageObject(objects.datasets, "crypto", "d1")
	objects.datasets = appendUniqueStorageObject(objects.datasets, "crypto", "")
	require.Len(t, objects.datasets, 1, "去重并忽略空 ID")

	fake := &collectorMetadataFake{datasetCode: storagepb.ErrorCode_DATASET_NOT_FOUND}
	require.NoError(t, deleteCollectorOwnedMetadata(context.Background(), fake, objects))
	require.Equal(t, []string{"dataset:crypto/d1"}, fake.calls)

	fake = &collectorMetadataFake{datasetCode: storagepb.ErrorCode_INVALID_PARAM}
	err := deleteCollectorOwnedMetadata(context.Background(), fake, objects)
	require.ErrorContains(t, err, "删除采集结果数据集 crypto/d1 失败：拒绝")

	fake = &collectorMetadataFake{datasetErr: errors.New("网关不可达")}
	require.ErrorContains(t, deleteCollectorOwnedMetadata(context.Background(), fake, objects), "网关不可达")
}
