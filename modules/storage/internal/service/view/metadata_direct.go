package view

import (
	"context"

	"github.com/mooyang-code/moox/modules/storage/internal/serviceauth"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/client"
)

// storageViewAppID 是 storage-view 调用 Storage 其他组件时的服务身份。
const storageViewAppID = "storage-view"

// directMetadataClient 让 storage-view 直连同机的 Metadata（与直连 PrimaryStore 一致，不经网关），
// 每个请求都带上按 storage-view 密钥签出的身份。
type directMetadataClient struct {
	proxy      pb.MetadataClientProxy
	authSecret string
}

var _ MetadataClient = (*directMetadataClient)(nil)

// NewDirectMetadataClient 包装 Metadata 的 tRPC 客户端，请求体里统一补上 storage-view 的服务签名。
func NewDirectMetadataClient(proxy pb.MetadataClientProxy, authSecret string) *directMetadataClient {
	return &directMetadataClient{proxy: proxy, authSecret: authSecret}
}

func (c *directMetadataClient) auth() *pb.AuthInfo {
	return &pb.AuthInfo{AppId: storageViewAppID, AppKey: serviceauth.Key(c.authSecret, storageViewAppID)}
}

func (c *directMetadataClient) ListViews(ctx context.Context, request *pb.ListViewsReq, options ...client.Option) (*pb.ListViewsRsp, error) {
	request.AuthInfo = c.auth()
	return c.proxy.ListViews(ctx, request, options...)
}
