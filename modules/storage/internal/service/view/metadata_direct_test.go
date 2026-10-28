package view

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

type authCapturingMetadata struct {
	pb.MetadataClientProxy
	auth *pb.AuthInfo
}

func (c *authCapturingMetadata) ListViews(_ context.Context, req *pb.ListViewsReq, _ ...client.Option) (*pb.ListViewsRsp, error) {
	c.auth = req.GetAuthInfo()
	return &pb.ListViewsRsp{}, nil
}

func (c *authCapturingMetadata) RecordViewSyncPoint(_ context.Context, req *pb.RecordViewSyncPointReq, _ ...client.Option) (*pb.RecordViewSyncPointRsp, error) {
	c.auth = req.GetAuthInfo()
	return &pb.RecordViewSyncPointRsp{}, nil
}

// 直连 Metadata 的客户端无论调用方传入什么身份，都改用 storage-view 的服务签名（View 密钥）。
func TestDirectMetadataClientSignsEveryRequestWithViewSecret(t *testing.T) {
	proxy := &authCapturingMetadata{}
	metadata := NewDirectMetadataClient(proxy, "view-secret")
	want := &pb.AuthInfo{AppId: "storage-view", AppKey: datanode.ServiceAuthKey("view-secret", "storage-view")}

	_, err := metadata.ListViews(context.Background(), &pb.ListViewsReq{AuthInfo: &pb.AuthInfo{AppId: "storage-view", AppKey: datanode.ServiceAuthKey("primary-secret", "storage-view")}})
	require.NoError(t, err)
	require.Equal(t, want.GetAppKey(), proxy.auth.GetAppKey(), "启动校验与调度循环不再使用 Primary 密钥")
	proxy.auth = nil
	_, err = metadata.RecordViewSyncPoint(context.Background(), &pb.RecordViewSyncPointReq{})
	require.NoError(t, err)
	require.Equal(t, want.GetAppId(), proxy.auth.GetAppId())
	require.Equal(t, want.GetAppKey(), proxy.auth.GetAppKey())
}
