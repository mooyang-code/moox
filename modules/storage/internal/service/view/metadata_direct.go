package view

import (
	"context"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/client"
)

// storageViewAppID 是 storage-view 调用 Storage 其他组件时的服务身份。
const storageViewAppID = "storage-view"

// directMetadataClient 让 storage-view 直连同机的 Metadata（与直连 PrimaryStore 一致，不经网关）。
// 每个请求都带上按 storage-view 密钥签出的身份，Metadata 对 storage-view 专用的方法据此验签。
type directMetadataClient struct {
	proxy      pb.MetadataClientProxy
	authSecret string
}

var (
	_ MetadataClient           = (*directMetadataClient)(nil)
	_ PeriodMetadataClient     = (*directMetadataClient)(nil)
	_ ViewInventoryClient      = (*directMetadataClient)(nil)
	_ rebuildLogMetadataClient = (*directMetadataClient)(nil)
)

// NewDirectMetadataClient 包装 Metadata 的 tRPC 客户端，请求体里统一补上 storage-view 的服务签名。
func NewDirectMetadataClient(proxy pb.MetadataClientProxy, authSecret string) *directMetadataClient {
	return &directMetadataClient{proxy: proxy, authSecret: authSecret}
}

func (c *directMetadataClient) auth() *pb.AuthInfo {
	return &pb.AuthInfo{AppId: storageViewAppID, AppKey: datanode.ServiceAuthKey(c.authSecret, storageViewAppID)}
}

func (c *directMetadataClient) ListViews(ctx context.Context, request *pb.ListViewsReq, options ...client.Option) (*pb.ListViewsRsp, error) {
	request.AuthInfo = c.auth()
	return c.proxy.ListViews(ctx, request, options...)
}

func (c *directMetadataClient) GetDataset(ctx context.Context, request *pb.GetDatasetReq, options ...client.Option) (*pb.GetDatasetRsp, error) {
	request.AuthInfo = c.auth()
	return c.proxy.GetDataset(ctx, request, options...)
}

func (c *directMetadataClient) ListDatasetSubjects(ctx context.Context, request *pb.ListDatasetSubjectsReq, options ...client.Option) (*pb.ListDatasetSubjectsRsp, error) {
	request.AuthInfo = c.auth()
	return c.proxy.ListDatasetSubjects(ctx, request, options...)
}

func (c *directMetadataClient) ListDatasetColumns(ctx context.Context, request *pb.ListDatasetColumnsReq, options ...client.Option) (*pb.ListDatasetColumnsRsp, error) {
	request.AuthInfo = c.auth()
	return c.proxy.ListDatasetColumns(ctx, request, options...)
}

func (c *directMetadataClient) ListSubjects(ctx context.Context, request *pb.ListSubjectsReq, options ...client.Option) (*pb.ListSubjectsRsp, error) {
	request.AuthInfo = c.auth()
	return c.proxy.ListSubjects(ctx, request, options...)
}

func (c *directMetadataClient) ClaimViewIndexBuild(ctx context.Context, request *pb.ClaimViewIndexBuildReq, options ...client.Option) (*pb.ClaimViewIndexBuildRsp, error) {
	request.AuthInfo = c.auth()
	return c.proxy.ClaimViewIndexBuild(ctx, request, options...)
}

func (c *directMetadataClient) UpdateViewIndexBuild(ctx context.Context, request *pb.UpdateViewIndexBuildReq, options ...client.Option) (*pb.UpdateViewIndexBuildRsp, error) {
	request.AuthInfo = c.auth()
	return c.proxy.UpdateViewIndexBuild(ctx, request, options...)
}

func (c *directMetadataClient) ActivateViewIndex(ctx context.Context, request *pb.ActivateViewIndexReq, options ...client.Option) (*pb.ActivateViewIndexRsp, error) {
	request.AuthInfo = c.auth()
	return c.proxy.ActivateViewIndex(ctx, request, options...)
}

func (c *directMetadataClient) CommitViewSchemaExtension(ctx context.Context, request *pb.CommitViewSchemaExtensionReq, options ...client.Option) (*pb.CommitViewSchemaExtensionRsp, error) {
	request.AuthInfo = c.auth()
	return c.proxy.CommitViewSchemaExtension(ctx, request, options...)
}

func (c *directMetadataClient) FailViewIndexBuild(ctx context.Context, request *pb.FailViewIndexBuildReq, options ...client.Option) (*pb.FailViewIndexBuildRsp, error) {
	request.AuthInfo = c.auth()
	return c.proxy.FailViewIndexBuild(ctx, request, options...)
}

func (c *directMetadataClient) ListViewRebuildLogs(ctx context.Context, request *pb.ListViewRebuildLogsReq, options ...client.Option) (*pb.ListViewRebuildLogsRsp, error) {
	request.AuthInfo = c.auth()
	return c.proxy.ListViewRebuildLogs(ctx, request, options...)
}

func (c *directMetadataClient) CreateViewRebuildLog(ctx context.Context, request *pb.CreateViewRebuildLogReq, options ...client.Option) (*pb.CreateViewRebuildLogRsp, error) {
	request.AuthInfo = c.auth()
	return c.proxy.CreateViewRebuildLog(ctx, request, options...)
}

func (c *directMetadataClient) UpdateViewRebuildLog(ctx context.Context, request *pb.UpdateViewRebuildLogReq, options ...client.Option) (*pb.UpdateViewRebuildLogRsp, error) {
	request.AuthInfo = c.auth()
	return c.proxy.UpdateViewRebuildLog(ctx, request, options...)
}

func (c *directMetadataClient) UpsertSkippedViewRebuildLog(ctx context.Context, request *pb.UpsertSkippedViewRebuildLogReq, options ...client.Option) (*pb.UpsertSkippedViewRebuildLogRsp, error) {
	request.AuthInfo = c.auth()
	return c.proxy.UpsertSkippedViewRebuildLog(ctx, request, options...)
}

func (c *directMetadataClient) UpsertViewPeriodDatasetState(ctx context.Context, request *pb.UpsertViewPeriodDatasetStateReq, options ...client.Option) (*pb.UpsertViewPeriodDatasetStateRsp, error) {
	request.AuthInfo = c.auth()
	return c.proxy.UpsertViewPeriodDatasetState(ctx, request, options...)
}

func (c *directMetadataClient) ListViewPeriodDatasetStates(ctx context.Context, request *pb.ListViewPeriodDatasetStatesReq, options ...client.Option) (*pb.ListViewPeriodDatasetStatesRsp, error) {
	request.AuthInfo = c.auth()
	return c.proxy.ListViewPeriodDatasetStates(ctx, request, options...)
}

func (c *directMetadataClient) RecordViewSyncPoint(ctx context.Context, request *pb.RecordViewSyncPointReq, options ...client.Option) (*pb.RecordViewSyncPointRsp, error) {
	request.AuthInfo = c.auth()
	return c.proxy.RecordViewSyncPoint(ctx, request, options...)
}
