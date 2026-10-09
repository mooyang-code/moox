// Package storagegateway provides Monitor's typed Storage calls through the shared gateway.
package storagegateway

import (
	"context"
	"errors"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"trpc.group/trpc-go/trpc-go/client"
)

type Client struct{ gateway gatewayclient.Invoker }

func New(gateway gatewayclient.Invoker) *Client { return &Client{gateway: gateway} }

func invoke[T any](ctx context.Context, c *Client, service, method string, request any, options []client.Option) (*T, error) {
	if c == nil || c.gateway == nil {
		return nil, errors.New("monitor Storage gateway client is unavailable")
	}
	if len(options) != 0 {
		return nil, errors.New("Storage gateway calls use context and catalog settings instead of tRPC client options")
	}
	response := new(T)
	if err := c.gateway.Invoke(ctx, "trpc.moox.storage."+service, method, request, response); err != nil {
		return nil, err
	}
	return response, nil
}

func (c *Client) UpsertFields(ctx context.Context, request *storagepb.PrimaryUpsertFieldsReq, options ...client.Option) (*storagepb.PrimaryUpsertFieldsRsp, error) {
	return invoke[storagepb.PrimaryUpsertFieldsRsp](ctx, c, "PrimaryStore", "UpsertFields", request, options)
}

func (c *Client) ReadTimeSeriesRows(ctx context.Context, request *storagepb.ReadTimeSeriesRowsReq, options ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	return invoke[storagepb.ReadTimeSeriesRowsRsp](ctx, c, "PrimaryStore", "ReadTimeSeriesRows", request, options)
}

func (c *Client) ReadFields(ctx context.Context, request *storagepb.PrimaryReadFieldsReq, options ...client.Option) (*storagepb.PrimaryReadFieldsRsp, error) {
	return invoke[storagepb.PrimaryReadFieldsRsp](ctx, c, "PrimaryStore", "ReadFields", request, options)
}

func (c *Client) GetSpace(ctx context.Context, request *storagepb.GetSpaceReq, options ...client.Option) (*storagepb.GetSpaceRsp, error) {
	return invoke[storagepb.GetSpaceRsp](ctx, c, "Metadata", "GetSpace", request, options)
}

func (c *Client) GetDataset(ctx context.Context, request *storagepb.GetDatasetReq, options ...client.Option) (*storagepb.GetDatasetRsp, error) {
	return invoke[storagepb.GetDatasetRsp](ctx, c, "Metadata", "GetDataset", request, options)
}

func (c *Client) GetDataNode(ctx context.Context, request *storagepb.GetDataNodeReq, options ...client.Option) (*storagepb.GetDataNodeRsp, error) {
	return invoke[storagepb.GetDataNodeRsp](ctx, c, "Metadata", "GetDataNode", request, options)
}

func (c *Client) ListDatasetColumns(ctx context.Context, request *storagepb.ListDatasetColumnsReq, options ...client.Option) (*storagepb.ListDatasetColumnsRsp, error) {
	return invoke[storagepb.ListDatasetColumnsRsp](ctx, c, "Metadata", "ListDatasetColumns", request, options)
}

func (c *Client) ListDatasetSubjects(ctx context.Context, request *storagepb.ListDatasetSubjectsReq, options ...client.Option) (*storagepb.ListDatasetSubjectsRsp, error) {
	return invoke[storagepb.ListDatasetSubjectsRsp](ctx, c, "Metadata", "ListDatasetSubjects", request, options)
}

func (c *Client) ListTags(ctx context.Context, request *storagepb.ListTagsReq, options ...client.Option) (*storagepb.ListTagsRsp, error) {
	return invoke[storagepb.ListTagsRsp](ctx, c, "Metadata", "ListTags", request, options)
}
