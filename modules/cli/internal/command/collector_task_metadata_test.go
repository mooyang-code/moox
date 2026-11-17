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
		switch req := request.(type) {
		case *storagepb.DeleteViewReq:
			require.Equal(t, "v1", req.GetViewId())
		case *storagepb.DeleteDatasetReq:
			require.Equal(t, "d1", req.GetDatasetId())
		default:
			t.Fatalf("unexpected request: %T", req)
		}
		seen = append(seen, method)
		return nil
	})
	proxy := &collectorStorageMetadataProxy{gateway: transport}

	_, err := proxy.DeleteView(context.Background(), &storagepb.DeleteViewReq{SpaceId: "crypto", ViewId: "v1"})
	require.NoError(t, err)
	_, err = proxy.DeleteDataset(context.Background(), &storagepb.DeleteDatasetReq{SpaceId: "crypto", DatasetId: "d1"})
	require.NoError(t, err)
	require.Equal(t, []string{"DeleteView", "DeleteDataset"}, seen)
}

type collectorMetadataFake struct {
	calls       []string
	viewCode    storagepb.ErrorCode
	datasetCode storagepb.ErrorCode
	datasetErr  error
}

func (f *collectorMetadataFake) DeleteView(_ context.Context, req *storagepb.DeleteViewReq) (*storagepb.DeleteViewRsp, error) {
	f.calls = append(f.calls, "view:"+req.GetSpaceId()+"/"+req.GetViewId())
	return &storagepb.DeleteViewRsp{RetInfo: &storagepb.RetInfo{Code: f.viewCode}}, nil
}

func (f *collectorMetadataFake) DeleteDataset(_ context.Context, req *storagepb.DeleteDatasetReq) (*storagepb.DeleteDatasetRsp, error) {
	f.calls = append(f.calls, "dataset:"+req.GetSpaceId()+"/"+req.GetDatasetId())
	if f.datasetErr != nil {
		return nil, f.datasetErr
	}
	return &storagepb.DeleteDatasetRsp{RetInfo: &storagepb.RetInfo{Code: f.datasetCode, Msg: "拒绝"}}, nil
}

// 先删 View 再删数据集；对象已经不存在算成功，重跑能续做；Storage 拒绝或调用失败时报错。
func TestDeleteCollectorOwnedMetadataDeletesViewsFirst(t *testing.T) {
	var objects collectorOwnedMetadata
	objects.datasets = appendUniqueStorageObject(objects.datasets, "crypto", "d1")
	objects.views = appendUniqueStorageObject(objects.views, "crypto", " v1 ")
	objects.views = appendUniqueStorageObject(objects.views, "crypto", "v1")
	objects.views = appendUniqueStorageObject(objects.views, "crypto", "")
	require.Len(t, objects.views, 1, "去重并忽略空 ID")

	fake := &collectorMetadataFake{viewCode: storagepb.ErrorCode_VIEW_NOT_FOUND, datasetCode: storagepb.ErrorCode_SUCCESS}
	require.NoError(t, deleteCollectorOwnedMetadata(context.Background(), fake, objects))
	require.Equal(t, []string{"view:crypto/v1", "dataset:crypto/d1"}, fake.calls)

	fake = &collectorMetadataFake{viewCode: storagepb.ErrorCode_SUCCESS, datasetCode: storagepb.ErrorCode_INVALID_PARAM}
	err := deleteCollectorOwnedMetadata(context.Background(), fake, objects)
	require.ErrorContains(t, err, "删除采集结果数据集 crypto/d1 失败：拒绝")

	fake = &collectorMetadataFake{viewCode: storagepb.ErrorCode_SUCCESS, datasetErr: errors.New("网关不可达")}
	require.ErrorContains(t, deleteCollectorOwnedMetadata(context.Background(), fake, objects), "网关不可达")
}
