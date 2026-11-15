package command

import (
	"context"
	"fmt"
	"io"
	"strings"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"google.golang.org/protobuf/proto"
)

// collector task delete-result 经操作员 SSH 网关调 Storage 的 Metadata，删除采集结果的 View 与数据集。

type collectorStorageObjectRef struct {
	SpaceID  string
	ObjectID string
}

// collectorOwnedMetadata 是要删除的采集结果：View 先于数据集删除（数据集被 View 引用时不能先删）。
type collectorOwnedMetadata struct {
	views    []collectorStorageObjectRef
	datasets []collectorStorageObjectRef
}

type collectorStorageMetadataClient interface {
	DeleteDataset(context.Context, *storagepb.DeleteDatasetReq) (*storagepb.DeleteDatasetRsp, error)
	DeleteView(context.Context, *storagepb.DeleteViewReq) (*storagepb.DeleteViewRsp, error)
}

type collectorStorageMetadataProxy struct {
	gateway gatewayclient.Invoker
	auth    *storagepb.AuthInfo
}

var newCollectorStorageMetadataClient = func(ctx context.Context, file, authFile string) (collectorStorageMetadataClient, io.Closer, error) {
	if strings.TrimSpace(file) == "" {
		return nil, nil, fmt.Errorf("删除采集结果要用 --file 指定 moox.toml（经操作员 SSH 网关访问 Storage）")
	}
	auth, err := storagePrimaryRoleAuth(authFile, "moox-collector")
	if err != nil {
		return nil, nil, err
	}
	gateway, err := openCommandGateway(ctx, file, nil)
	if err != nil {
		return nil, nil, err
	}
	return &collectorStorageMetadataProxy{gateway: gateway, auth: auth}, gateway, nil
}

func (c *collectorStorageMetadataProxy) DeleteDataset(ctx context.Context, req *storagepb.DeleteDatasetReq) (*storagepb.DeleteDatasetRsp, error) {
	if c == nil || c.gateway == nil {
		return nil, fmt.Errorf("Storage 元数据客户端不可用")
	}
	request := &storagepb.DeleteDatasetReq{}
	if req != nil {
		request = proto.Clone(req).(*storagepb.DeleteDatasetReq)
	}
	request.AuthInfo = c.auth
	rsp := &storagepb.DeleteDatasetRsp{}
	err := postStorageRaw(ctx, c.gateway, metadataServiceName, "DeleteDataset", request, rsp)
	return rsp, err
}

func (c *collectorStorageMetadataProxy) DeleteView(ctx context.Context, req *storagepb.DeleteViewReq) (*storagepb.DeleteViewRsp, error) {
	if c == nil || c.gateway == nil {
		return nil, fmt.Errorf("Storage 元数据客户端不可用")
	}
	request := &storagepb.DeleteViewReq{}
	if req != nil {
		request = proto.Clone(req).(*storagepb.DeleteViewReq)
	}
	request.AuthInfo = c.auth
	rsp := &storagepb.DeleteViewRsp{}
	err := postStorageRaw(ctx, c.gateway, metadataServiceName, "DeleteView", request, rsp)
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

// deleteCollectorOwnedMetadata 先删 View 再删数据集；已经不存在的对象算删除成功，重跑同一条命令可以续做。
func deleteCollectorOwnedMetadata(ctx context.Context, metadata collectorStorageMetadataClient, objects collectorOwnedMetadata) error {
	if metadata == nil {
		return nil
	}
	for _, object := range objects.views {
		response, err := metadata.DeleteView(ctx, &storagepb.DeleteViewReq{
			SpaceId: object.SpaceID,
			ViewId:  object.ObjectID,
		})
		if err != nil || !collectorDeleteViewAccepted(response) {
			return fmt.Errorf("删除采集结果 View %s/%s 失败：%w", object.SpaceID, object.ObjectID, collectorDeleteResponseError(err, response))
		}
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

func collectorDeleteViewAccepted(response *storagepb.DeleteViewRsp) bool {
	if response == nil || response.GetRetInfo() == nil {
		return false
	}
	switch response.GetRetInfo().GetCode() {
	case storagepb.ErrorCode_SUCCESS, storagepb.ErrorCode_VIEW_NOT_FOUND, storagepb.ErrorCode_NOT_FOUND:
		return true
	default:
		return false
	}
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
