package command

import (
	"context"
	"fmt"
	"io"
	"strings"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

// collector task delete-result 经操作员 SSH 网关调 Storage 的 Metadata，删除采集结果数据集（它的查询索引随之删除）。

type collectorStorageObjectRef struct {
	SpaceID  string
	ObjectID string
}

// collectorOwnedMetadata 是要删除的采集结果数据集。
type collectorOwnedMetadata struct {
	datasets []collectorStorageObjectRef
}

type collectorStorageMetadataClient interface {
	DeleteDataset(context.Context, *storagepb.DeleteDatasetReq) (*storagepb.DeleteDatasetRsp, error)
}

type collectorStorageMetadataProxy struct {
	gateway gatewayclient.Invoker
}

var newCollectorStorageMetadataClient = func(ctx context.Context, file string) (collectorStorageMetadataClient, io.Closer, error) {
	if strings.TrimSpace(file) == "" {
		return nil, nil, fmt.Errorf("删除采集结果要用 --file 指定 moox.toml（经操作员 SSH 网关访问 Storage）")
	}
	gateway, err := openCommandGateway(ctx, file, nil)
	if err != nil {
		return nil, nil, err
	}
	return &collectorStorageMetadataProxy{gateway: gateway}, gateway, nil
}

func (c *collectorStorageMetadataProxy) DeleteDataset(ctx context.Context, req *storagepb.DeleteDatasetReq) (*storagepb.DeleteDatasetRsp, error) {
	if c == nil || c.gateway == nil {
		return nil, fmt.Errorf("Storage 元数据客户端不可用")
	}
	if req == nil {
		req = &storagepb.DeleteDatasetReq{}
	}
	rsp := &storagepb.DeleteDatasetRsp{}
	err := postStorageRaw(ctx, c.gateway, metadataServiceName, "DeleteDataset", req, rsp)
	return rsp, err
}

func appendUniqueStorageObject(values []collectorStorageObjectRef, spaceID, objectID string) []collectorStorageObjectRef {
	spaceID, objectID = strings.TrimSpace(spaceID), strings.TrimSpace(objectID)
	if spaceID == "" || objectID == "" {
		return values
	}
	for _, existing := range values {
		if existing.SpaceID == spaceID && existing.ObjectID == objectID {
			return values
		}
	}
	return append(values, collectorStorageObjectRef{SpaceID: spaceID, ObjectID: objectID})
}

// deleteCollectorOwnedMetadata 删数据集；已经不存在的对象算删除成功，重跑同一条命令可以续做。
func deleteCollectorOwnedMetadata(ctx context.Context, metadata collectorStorageMetadataClient, objects collectorOwnedMetadata) error {
	if metadata == nil {
		return nil
	}
	for _, object := range objects.datasets {
		response, err := metadata.DeleteDataset(ctx, &storagepb.DeleteDatasetReq{
			SpaceId:   object.SpaceID,
			DatasetId: object.ObjectID,
		})
		if err != nil || !collectorDeleteDatasetAccepted(response) {
			return fmt.Errorf("删除采集结果数据集 %s/%s 失败：%w", object.SpaceID, object.ObjectID, collectorDeleteResponseError(err, response))
		}
	}
	return nil
}

func collectorDeleteDatasetAccepted(response *storagepb.DeleteDatasetRsp) bool {
	if response == nil || response.GetRetInfo() == nil {
		return false
	}
	switch response.GetRetInfo().GetCode() {
	case storagepb.ErrorCode_SUCCESS, storagepb.ErrorCode_DATASET_NOT_FOUND, storagepb.ErrorCode_NOT_FOUND:
		return true
	default:
		return false
	}
}

func collectorDeleteResponseError(callErr error, response interface{ GetRetInfo() *storagepb.RetInfo }) error {
	if callErr != nil {
		return callErr
	}
	if response == nil || response.GetRetInfo() == nil {
		return fmt.Errorf("响应为空")
	}
	return fmt.Errorf("%s", response.GetRetInfo().GetMsg())
}
