package view

import (
	"context"
	"errors"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"trpc.group/trpc-go/trpc-go/client"
)

type gatewayMetadataClient struct{ gateway gatewayclient.Invoker }

var (
	_ MetadataClient           = (*gatewayMetadataClient)(nil)
	_ PeriodMetadataClient     = (*gatewayMetadataClient)(nil)
	_ ViewInventoryClient      = (*gatewayMetadataClient)(nil)
	_ subjectCatalogClient     = (*gatewayMetadataClient)(nil)
	_ rebuildLogMetadataClient = (*gatewayMetadataClient)(nil)
)

// NewGatewayMetadataClient preserves Storage role authentication in each body.
// Signing, service discovery and read-only retries belong to gatewayclient.
func NewGatewayMetadataClient(gateway gatewayclient.Invoker) *gatewayMetadataClient {
	return &gatewayMetadataClient{gateway: gateway}
}

func invokeMetadata[T any](ctx context.Context, gateway gatewayclient.Invoker, method string, request any, options []client.Option) (*T, error) {
	if gateway == nil {
		return nil, errors.New("storage-view gateway client is required")
	}
	if len(options) != 0 {
		return nil, errors.New("Metadata gateway calls use context and catalog settings instead of tRPC client options")
	}
	response := new(T)
	if err := gateway.Invoke(ctx, "trpc.moox.storage.Metadata", method, request, response); err != nil {
		return nil, err
	}
	return response, nil
}

func (c *gatewayMetadataClient) ListViews(ctx context.Context, request *pb.ListViewsReq, options ...client.Option) (*pb.ListViewsRsp, error) {
	return invokeMetadata[pb.ListViewsRsp](ctx, c.gateway, "ListViews", request, options)
}

func (c *gatewayMetadataClient) GetDataset(ctx context.Context, request *pb.GetDatasetReq, options ...client.Option) (*pb.GetDatasetRsp, error) {
	return invokeMetadata[pb.GetDatasetRsp](ctx, c.gateway, "GetDataset", request, options)
}

func (c *gatewayMetadataClient) ListDatasetSubjects(ctx context.Context, request *pb.ListDatasetSubjectsReq, options ...client.Option) (*pb.ListDatasetSubjectsRsp, error) {
	return invokeMetadata[pb.ListDatasetSubjectsRsp](ctx, c.gateway, "ListDatasetSubjects", request, options)
}

func (c *gatewayMetadataClient) ListDatasetColumns(ctx context.Context, request *pb.ListDatasetColumnsReq, options ...client.Option) (*pb.ListDatasetColumnsRsp, error) {
	return invokeMetadata[pb.ListDatasetColumnsRsp](ctx, c.gateway, "ListDatasetColumns", request, options)
}

func (c *gatewayMetadataClient) ListSubjects(ctx context.Context, request *pb.ListSubjectsReq, options ...client.Option) (*pb.ListSubjectsRsp, error) {
	return invokeMetadata[pb.ListSubjectsRsp](ctx, c.gateway, "ListSubjects", request, options)
}

func (c *gatewayMetadataClient) ClaimViewIndexBuild(ctx context.Context, request *pb.ClaimViewIndexBuildReq, options ...client.Option) (*pb.ClaimViewIndexBuildRsp, error) {
	return invokeMetadata[pb.ClaimViewIndexBuildRsp](ctx, c.gateway, "ClaimViewIndexBuild", request, options)
}

func (c *gatewayMetadataClient) UpdateViewIndexBuild(ctx context.Context, request *pb.UpdateViewIndexBuildReq, options ...client.Option) (*pb.UpdateViewIndexBuildRsp, error) {
	return invokeMetadata[pb.UpdateViewIndexBuildRsp](ctx, c.gateway, "UpdateViewIndexBuild", request, options)
}

func (c *gatewayMetadataClient) ActivateViewIndex(ctx context.Context, request *pb.ActivateViewIndexReq, options ...client.Option) (*pb.ActivateViewIndexRsp, error) {
	return invokeMetadata[pb.ActivateViewIndexRsp](ctx, c.gateway, "ActivateViewIndex", request, options)
}

func (c *gatewayMetadataClient) CommitViewSchemaExtension(ctx context.Context, request *pb.CommitViewSchemaExtensionReq, options ...client.Option) (*pb.CommitViewSchemaExtensionRsp, error) {
	return invokeMetadata[pb.CommitViewSchemaExtensionRsp](ctx, c.gateway, "CommitViewSchemaExtension", request, options)
}

func (c *gatewayMetadataClient) FailViewIndexBuild(ctx context.Context, request *pb.FailViewIndexBuildReq, options ...client.Option) (*pb.FailViewIndexBuildRsp, error) {
	return invokeMetadata[pb.FailViewIndexBuildRsp](ctx, c.gateway, "FailViewIndexBuild", request, options)
}

func (c *gatewayMetadataClient) ListViewRebuildLogs(ctx context.Context, request *pb.ListViewRebuildLogsReq, options ...client.Option) (*pb.ListViewRebuildLogsRsp, error) {
	return invokeMetadata[pb.ListViewRebuildLogsRsp](ctx, c.gateway, "ListViewRebuildLogs", request, options)
}

func (c *gatewayMetadataClient) CreateViewRebuildLog(ctx context.Context, request *pb.CreateViewRebuildLogReq, options ...client.Option) (*pb.CreateViewRebuildLogRsp, error) {
	return invokeMetadata[pb.CreateViewRebuildLogRsp](ctx, c.gateway, "CreateViewRebuildLog", request, options)
}

func (c *gatewayMetadataClient) UpdateViewRebuildLog(ctx context.Context, request *pb.UpdateViewRebuildLogReq, options ...client.Option) (*pb.UpdateViewRebuildLogRsp, error) {
	return invokeMetadata[pb.UpdateViewRebuildLogRsp](ctx, c.gateway, "UpdateViewRebuildLog", request, options)
}

func (c *gatewayMetadataClient) UpsertSkippedViewRebuildLog(ctx context.Context, request *pb.UpsertSkippedViewRebuildLogReq, options ...client.Option) (*pb.UpsertSkippedViewRebuildLogRsp, error) {
	return invokeMetadata[pb.UpsertSkippedViewRebuildLogRsp](ctx, c.gateway, "UpsertSkippedViewRebuildLog", request, options)
}

func (c *gatewayMetadataClient) UpsertViewPeriodDatasetState(ctx context.Context, request *pb.UpsertViewPeriodDatasetStateReq, options ...client.Option) (*pb.UpsertViewPeriodDatasetStateRsp, error) {
	return invokeMetadata[pb.UpsertViewPeriodDatasetStateRsp](ctx, c.gateway, "UpsertViewPeriodDatasetState", request, options)
}

func (c *gatewayMetadataClient) ListViewPeriodDatasetStates(ctx context.Context, request *pb.ListViewPeriodDatasetStatesReq, options ...client.Option) (*pb.ListViewPeriodDatasetStatesRsp, error) {
	return invokeMetadata[pb.ListViewPeriodDatasetStatesRsp](ctx, c.gateway, "ListViewPeriodDatasetStates", request, options)
}

func (c *gatewayMetadataClient) RecordViewSyncPoint(ctx context.Context, request *pb.RecordViewSyncPointReq, options ...client.Option) (*pb.RecordViewSyncPointRsp, error) {
	return invokeMetadata[pb.RecordViewSyncPointRsp](ctx, c.gateway, "RecordViewSyncPoint", request, options)
}
