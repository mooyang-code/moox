package storageio

import (
	"context"
	"errors"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"trpc.group/trpc-go/trpc-go/client"
)

type gatewayAdapter struct{ gateway gatewayclient.Invoker }

var (
	_ MetadataClient = (*gatewayAdapter)(nil)
	_ DataViewClient = (*gatewayAdapter)(nil)
)

// NewGatewayClient uses the shared process client for discovery, signing and retries.
func NewGatewayClient(gateway gatewayclient.Invoker) *RPCClient {
	adapter := &gatewayAdapter{gateway: gateway}
	return &RPCClient{Metadata: adapter, DataView: adapter}
}

func invokeGateway[T any](ctx context.Context, gateway gatewayclient.Invoker, service, method string, request any, options []client.Option) (*T, error) {
	if gateway == nil {
		return nil, errors.New("strategy gateway client is required")
	}
	if len(options) != 0 {
		return nil, errors.New("strategy gateway calls use context and catalog settings instead of tRPC client options")
	}
	response := new(T)
	if err := gateway.Invoke(ctx, service, method, request, response); err != nil {
		return nil, err
	}
	return response, nil
}

func (c *gatewayAdapter) GetView(ctx context.Context, request *storagepb.GetViewReq, options ...client.Option) (*storagepb.GetViewRsp, error) {
	return invokeGateway[storagepb.GetViewRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "GetView", request, options)
}

func (c *gatewayAdapter) ListViewColumns(ctx context.Context, request *storagepb.ListViewColumnsReq, options ...client.Option) (*storagepb.ListViewColumnsRsp, error) {
	return invokeGateway[storagepb.ListViewColumnsRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "ListViewColumns", request, options)
}

func (c *gatewayAdapter) ListDatasetSubjects(ctx context.Context, request *storagepb.ListDatasetSubjectsReq, options ...client.Option) (*storagepb.ListDatasetSubjectsRsp, error) {
	return invokeGateway[storagepb.ListDatasetSubjectsRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "ListDatasetSubjects", request, options)
}

func (c *gatewayAdapter) GetSubject(ctx context.Context, request *storagepb.GetSubjectReq, options ...client.Option) (*storagepb.GetSubjectRsp, error) {
	return invokeGateway[storagepb.GetSubjectRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "GetSubject", request, options)
}

func (c *gatewayAdapter) QueryTimeSeriesRows(ctx context.Context, request *storagepb.QueryTimeSeriesRowsReq, options ...client.Option) (*storagepb.QueryTimeSeriesRowsRsp, error) {
	return invokeGateway[storagepb.QueryTimeSeriesRowsRsp](ctx, c.gateway, "trpc.moox.storage.DataView", "QueryTimeSeriesRows", request, options)
}
