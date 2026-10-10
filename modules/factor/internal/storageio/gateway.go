package storageio

import (
	"context"
	"errors"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"trpc.group/trpc-go/trpc-go/client"
)

type gatewayStorageClient struct{ gateway gatewayclient.Invoker }

var (
	_ PrimaryStoreClient = (*gatewayStorageClient)(nil)
	_ MetadataClient     = (*gatewayStorageClient)(nil)
)

// NewGatewayClient preserves Storage role authentication while the shared client
// owns service discovery, request signing, timeouts and read-only retries.
func NewGatewayClient(gateway gatewayclient.Invoker, auth *commonpb.AuthInfo) *Client {
	adapter := &gatewayStorageClient{gateway: gateway}
	return NewClient(adapter, adapter, auth)
}

func invokeStorage[T any](ctx context.Context, gateway gatewayclient.Invoker, service, method string, request any, options []client.Option) (*T, error) {
	if gateway == nil {
		return nil, errors.New("factor Storage gateway client is required")
	}
	if len(options) != 0 {
		return nil, errors.New("Storage gateway calls use context and catalog settings instead of tRPC client options")
	}
	response := new(T)
	if err := gateway.Invoke(ctx, "trpc.moox.storage."+service, method, request, response); err != nil {
		return nil, err
	}
	return response, nil
}

func (c *gatewayStorageClient) ReadTimeSeriesRows(ctx context.Context, request *storagepb.ReadTimeSeriesRowsReq, options ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	return invokeStorage[storagepb.ReadTimeSeriesRowsRsp](ctx, c.gateway, "PrimaryStore", "ReadTimeSeriesRows", request, options)
}

func (c *gatewayStorageClient) WriteFactorRows(ctx context.Context, request *storagepb.PrimaryWriteFactorRowsReq, options ...client.Option) (*storagepb.PrimaryWriteFactorRowsRsp, error) {
	return invokeStorage[storagepb.PrimaryWriteFactorRowsRsp](ctx, c.gateway, "PrimaryStore", "WriteFactorRows", request, options)
}

func (c *gatewayStorageClient) ReportFactorPeriodComputed(ctx context.Context, request *storagepb.ReportFactorPeriodComputedReq, options ...client.Option) (*storagepb.ReportFactorPeriodComputedRsp, error) {
	return invokeStorage[storagepb.ReportFactorPeriodComputedRsp](ctx, c.gateway, "PrimaryStore", "ReportFactorPeriodComputed", request, options)
}

func (c *gatewayStorageClient) GetFactorPeriodComputed(ctx context.Context, request *storagepb.GetFactorPeriodComputedReq, options ...client.Option) (*storagepb.GetFactorPeriodComputedRsp, error) {
	return invokeStorage[storagepb.GetFactorPeriodComputedRsp](ctx, c.gateway, "PrimaryStore", "GetFactorPeriodComputed", request, options)
}

func (c *gatewayStorageClient) DeleteDatasetRows(ctx context.Context, request *storagepb.PrimaryDeleteDatasetRowsReq, options ...client.Option) (*storagepb.PrimaryDeleteDatasetRowsRsp, error) {
	return invokeStorage[storagepb.PrimaryDeleteDatasetRowsRsp](ctx, c.gateway, "PrimaryStore", "DeleteDatasetRows", request, options)
}

func (c *gatewayStorageClient) RestoreDatasetRows(ctx context.Context, request *storagepb.PrimaryRestoreDatasetRowsReq, options ...client.Option) (*storagepb.PrimaryRestoreDatasetRowsRsp, error) {
	return invokeStorage[storagepb.PrimaryRestoreDatasetRowsRsp](ctx, c.gateway, "PrimaryStore", "RestoreDatasetRows", request, options)
}

func (c *gatewayStorageClient) GetDataset(ctx context.Context, request *storagepb.GetDatasetReq, options ...client.Option) (*storagepb.GetDatasetRsp, error) {
	return invokeStorage[storagepb.GetDatasetRsp](ctx, c.gateway, "Metadata", "GetDataset", request, options)
}

func (c *gatewayStorageClient) ListDatasetColumns(ctx context.Context, request *storagepb.ListDatasetColumnsReq, options ...client.Option) (*storagepb.ListDatasetColumnsRsp, error) {
	return invokeStorage[storagepb.ListDatasetColumnsRsp](ctx, c.gateway, "Metadata", "ListDatasetColumns", request, options)
}

func (c *gatewayStorageClient) CreateDataset(ctx context.Context, request *storagepb.CreateDatasetReq, options ...client.Option) (*storagepb.CreateDatasetRsp, error) {
	return invokeStorage[storagepb.CreateDatasetRsp](ctx, c.gateway, "Metadata", "CreateDataset", request, options)
}

func (c *gatewayStorageClient) UpsertDatasetColumn(ctx context.Context, request *storagepb.UpsertDatasetColumnReq, options ...client.Option) (*storagepb.UpsertDatasetColumnRsp, error) {
	return invokeStorage[storagepb.UpsertDatasetColumnRsp](ctx, c.gateway, "Metadata", "UpsertDatasetColumn", request, options)
}

func (c *gatewayStorageClient) ActivateDataset(ctx context.Context, request *storagepb.ActivateDatasetReq, options ...client.Option) (*storagepb.ActivateDatasetRsp, error) {
	return invokeStorage[storagepb.ActivateDatasetRsp](ctx, c.gateway, "Metadata", "ActivateDataset", request, options)
}

func (c *gatewayStorageClient) DeleteDataset(ctx context.Context, request *storagepb.DeleteDatasetReq, options ...client.Option) (*storagepb.DeleteDatasetRsp, error) {
	return invokeStorage[storagepb.DeleteDatasetRsp](ctx, c.gateway, "Metadata", "DeleteDataset", request, options)
}

func (c *gatewayStorageClient) UpdateDataset(ctx context.Context, request *storagepb.UpdateDatasetReq, options ...client.Option) (*storagepb.UpdateDatasetRsp, error) {
	return invokeStorage[storagepb.UpdateDatasetRsp](ctx, c.gateway, "Metadata", "UpdateDataset", request, options)
}

func (c *gatewayStorageClient) ListDatasetSubjects(ctx context.Context, request *storagepb.ListDatasetSubjectsReq, options ...client.Option) (*storagepb.ListDatasetSubjectsRsp, error) {
	return invokeStorage[storagepb.ListDatasetSubjectsRsp](ctx, c.gateway, "Metadata", "ListDatasetSubjects", request, options)
}
