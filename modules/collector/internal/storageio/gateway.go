// Package storageio adapts Collector's Storage capabilities to the shared gateway client.
package storageio

import (
	"context"
	"errors"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/wirecompress"
	"trpc.group/trpc-go/trpc-go/client"
)

// Metadata is the Storage surface used by Collector's internal workers.
type Metadata interface {
	GetTag(context.Context, *storagepb.GetTagReq, ...client.Option) (*storagepb.GetTagRsp, error)
	GetDataset(context.Context, *storagepb.GetDatasetReq, ...client.Option) (*storagepb.GetDatasetRsp, error)
	CreateDataset(context.Context, *storagepb.CreateDatasetReq, ...client.Option) (*storagepb.CreateDatasetRsp, error)
	UpdateDataset(context.Context, *storagepb.UpdateDatasetReq, ...client.Option) (*storagepb.UpdateDatasetRsp, error)
	DeleteDataset(context.Context, *storagepb.DeleteDatasetReq, ...client.Option) (*storagepb.DeleteDatasetRsp, error)
	GetView(context.Context, *storagepb.GetViewReq, ...client.Option) (*storagepb.GetViewRsp, error)
	CreateView(context.Context, *storagepb.CreateViewReq, ...client.Option) (*storagepb.CreateViewRsp, error)
	UpdateView(context.Context, *storagepb.UpdateViewReq, ...client.Option) (*storagepb.UpdateViewRsp, error)
	DeleteView(context.Context, *storagepb.DeleteViewReq, ...client.Option) (*storagepb.DeleteViewRsp, error)
	ListViews(context.Context, *storagepb.ListViewsReq, ...client.Option) (*storagepb.ListViewsRsp, error)
	UpsertDatasetColumn(context.Context, *storagepb.UpsertDatasetColumnReq, ...client.Option) (*storagepb.UpsertDatasetColumnRsp, error)
	UpsertViewColumn(context.Context, *storagepb.UpsertViewColumnReq, ...client.Option) (*storagepb.UpsertViewColumnRsp, error)
	CheckDatasetActivation(context.Context, *storagepb.CheckDatasetActivationReq, ...client.Option) (*storagepb.CheckDatasetActivationRsp, error)
	ActivateDataset(context.Context, *storagepb.ActivateDatasetReq, ...client.Option) (*storagepb.ActivateDatasetRsp, error)
	ResolveSubjects(context.Context, *storagepb.ResolveSubjectsReq, ...client.Option) (*storagepb.ResolveSubjectsRsp, error)
	ListDatasetColumns(context.Context, *storagepb.ListDatasetColumnsReq, ...client.Option) (*storagepb.ListDatasetColumnsRsp, error)
	ListTags(context.Context, *storagepb.ListTagsReq, ...client.Option) (*storagepb.ListTagsRsp, error)
	ApplyTagSnapshot(context.Context, *storagepb.ApplyTagSnapshotReq, ...client.Option) (*storagepb.ApplyTagSnapshotRsp, error)
	ReportTagRunFailure(context.Context, *storagepb.ReportTagRunFailureReq, ...client.Option) (*storagepb.ReportTagRunFailureRsp, error)
	UpdateSubjectAttributes(context.Context, *storagepb.UpdateSubjectAttributesReq, ...client.Option) (*storagepb.UpdateSubjectAttributesRsp, error)
	GetDataSource(context.Context, *storagepb.GetDataSourceReq, ...client.Option) (*storagepb.GetDataSourceRsp, error)
	UpdateDataSource(context.Context, *storagepb.UpdateDataSourceReq, ...client.Option) (*storagepb.UpdateDataSourceRsp, error)
	ListSubjects(context.Context, *storagepb.ListSubjectsReq, ...client.Option) (*storagepb.ListSubjectsRsp, error)
}

// Primary is the Storage surface used by Collector's internal workers.
type Primary interface {
	RegisterSeriesSnapshot(context.Context, *storagepb.PrimaryRegisterSeriesSnapshotReq, ...client.Option) (*storagepb.PrimaryRegisterSeriesSnapshotRsp, error)
	ReadTimeSeriesRows(context.Context, *storagepb.ReadTimeSeriesRowsReq, ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error)
	CommitTimeSeriesBatch(context.Context, *storagepb.PrimaryCommitTimeSeriesBatchReq, ...client.Option) (*storagepb.PrimaryCommitTimeSeriesBatchRsp, error)
	UpsertFields(context.Context, *storagepb.PrimaryUpsertFieldsReq, ...client.Option) (*storagepb.PrimaryUpsertFieldsRsp, error)
	ReportCollectorPeriodCompleted(context.Context, *storagepb.ReportCollectorPeriodCompletedReq, ...client.Option) (*storagepb.ReportCollectorPeriodCompletedRsp, error)
	ReadRows(context.Context, *storagepb.PrimaryReadRowsReq, ...client.Option) (*storagepb.PrimaryReadRowsRsp, error)
	WaitViewSyncPoint(context.Context, *storagepb.WaitViewSyncPointReq, ...client.Option) (*storagepb.WaitViewSyncPointRsp, error)
	DeleteDatasetRows(context.Context, *storagepb.PrimaryDeleteDatasetRowsReq, ...client.Option) (*storagepb.PrimaryDeleteDatasetRowsRsp, error)
	RestoreDatasetRows(context.Context, *storagepb.PrimaryRestoreDatasetRowsReq, ...client.Option) (*storagepb.PrimaryRestoreDatasetRowsRsp, error)
}

type Client struct{ gateway gatewayclient.Invoker }

// NewGatewayClient shares its caller-owned gateway; it never owns or closes it.
func NewGatewayClient(gateway gatewayclient.Invoker) *Client { return &Client{gateway: gateway} }

func invoke[T any](ctx context.Context, gateway gatewayclient.Invoker, service, method string, request any, options []client.Option) (*T, error) {
	if gateway == nil {
		return nil, errors.New("collector gateway client is required")
	}
	if len(options) != 0 {
		return nil, errors.New("collector gateway calls use context and catalog settings instead of tRPC client options")
	}
	// 整分钟写入突发等请求顶层没有 space_id，空间在 Expectation 里：这里统一取出，让 tRPC 压缩按空间选字典。
	if carrier, ok := request.(interface {
		GetExpectation() *storagepb.DatasetPeriodExpectation
	}); ok {
		if space := carrier.GetExpectation().GetSpaceId(); space != "" {
			ctx = wirecompress.WithSpace(ctx, space)
		}
	}
	response := new(T)
	if err := gateway.Invoke(ctx, service, method, request, response); err != nil {
		return nil, err
	}
	return response, nil
}

func (c *Client) GetTag(ctx context.Context, request *storagepb.GetTagReq, options ...client.Option) (*storagepb.GetTagRsp, error) {
	return invoke[storagepb.GetTagRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "GetTag", request, options)
}

func (c *Client) GetDataset(ctx context.Context, request *storagepb.GetDatasetReq, options ...client.Option) (*storagepb.GetDatasetRsp, error) {
	return invoke[storagepb.GetDatasetRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "GetDataset", request, options)
}

func (c *Client) CreateDataset(ctx context.Context, request *storagepb.CreateDatasetReq, options ...client.Option) (*storagepb.CreateDatasetRsp, error) {
	return invoke[storagepb.CreateDatasetRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "CreateDataset", request, options)
}

func (c *Client) UpdateDataset(ctx context.Context, request *storagepb.UpdateDatasetReq, options ...client.Option) (*storagepb.UpdateDatasetRsp, error) {
	return invoke[storagepb.UpdateDatasetRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "UpdateDataset", request, options)
}

func (c *Client) DeleteDataset(ctx context.Context, request *storagepb.DeleteDatasetReq, options ...client.Option) (*storagepb.DeleteDatasetRsp, error) {
	return invoke[storagepb.DeleteDatasetRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "DeleteDataset", request, options)
}

func (c *Client) GetView(ctx context.Context, request *storagepb.GetViewReq, options ...client.Option) (*storagepb.GetViewRsp, error) {
	return invoke[storagepb.GetViewRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "GetView", request, options)
}

func (c *Client) CreateView(ctx context.Context, request *storagepb.CreateViewReq, options ...client.Option) (*storagepb.CreateViewRsp, error) {
	return invoke[storagepb.CreateViewRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "CreateView", request, options)
}

func (c *Client) UpdateView(ctx context.Context, request *storagepb.UpdateViewReq, options ...client.Option) (*storagepb.UpdateViewRsp, error) {
	return invoke[storagepb.UpdateViewRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "UpdateView", request, options)
}

func (c *Client) DeleteView(ctx context.Context, request *storagepb.DeleteViewReq, options ...client.Option) (*storagepb.DeleteViewRsp, error) {
	return invoke[storagepb.DeleteViewRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "DeleteView", request, options)
}

func (c *Client) ListViews(ctx context.Context, request *storagepb.ListViewsReq, options ...client.Option) (*storagepb.ListViewsRsp, error) {
	return invoke[storagepb.ListViewsRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "ListViews", request, options)
}

func (c *Client) UpsertDatasetColumn(ctx context.Context, request *storagepb.UpsertDatasetColumnReq, options ...client.Option) (*storagepb.UpsertDatasetColumnRsp, error) {
	return invoke[storagepb.UpsertDatasetColumnRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "UpsertDatasetColumn", request, options)
}

func (c *Client) UpsertViewColumn(ctx context.Context, request *storagepb.UpsertViewColumnReq, options ...client.Option) (*storagepb.UpsertViewColumnRsp, error) {
	return invoke[storagepb.UpsertViewColumnRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "UpsertViewColumn", request, options)
}

func (c *Client) CheckDatasetActivation(ctx context.Context, request *storagepb.CheckDatasetActivationReq, options ...client.Option) (*storagepb.CheckDatasetActivationRsp, error) {
	return invoke[storagepb.CheckDatasetActivationRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "CheckDatasetActivation", request, options)
}

func (c *Client) ActivateDataset(ctx context.Context, request *storagepb.ActivateDatasetReq, options ...client.Option) (*storagepb.ActivateDatasetRsp, error) {
	return invoke[storagepb.ActivateDatasetRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "ActivateDataset", request, options)
}

func (c *Client) ResolveSubjects(ctx context.Context, request *storagepb.ResolveSubjectsReq, options ...client.Option) (*storagepb.ResolveSubjectsRsp, error) {
	return invoke[storagepb.ResolveSubjectsRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "ResolveSubjects", request, options)
}

func (c *Client) ListDatasetColumns(ctx context.Context, request *storagepb.ListDatasetColumnsReq, options ...client.Option) (*storagepb.ListDatasetColumnsRsp, error) {
	return invoke[storagepb.ListDatasetColumnsRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "ListDatasetColumns", request, options)
}

func (c *Client) ListTags(ctx context.Context, request *storagepb.ListTagsReq, options ...client.Option) (*storagepb.ListTagsRsp, error) {
	return invoke[storagepb.ListTagsRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "ListTags", request, options)
}

func (c *Client) ApplyTagSnapshot(ctx context.Context, request *storagepb.ApplyTagSnapshotReq, options ...client.Option) (*storagepb.ApplyTagSnapshotRsp, error) {
	return invoke[storagepb.ApplyTagSnapshotRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "ApplyTagSnapshot", request, options)
}

func (c *Client) ReportTagRunFailure(ctx context.Context, request *storagepb.ReportTagRunFailureReq, options ...client.Option) (*storagepb.ReportTagRunFailureRsp, error) {
	return invoke[storagepb.ReportTagRunFailureRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "ReportTagRunFailure", request, options)
}

func (c *Client) UpdateSubjectAttributes(ctx context.Context, request *storagepb.UpdateSubjectAttributesReq, options ...client.Option) (*storagepb.UpdateSubjectAttributesRsp, error) {
	return invoke[storagepb.UpdateSubjectAttributesRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "UpdateSubjectAttributes", request, options)
}

func (c *Client) GetDataSource(ctx context.Context, request *storagepb.GetDataSourceReq, options ...client.Option) (*storagepb.GetDataSourceRsp, error) {
	return invoke[storagepb.GetDataSourceRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "GetDataSource", request, options)
}

func (c *Client) UpdateDataSource(ctx context.Context, request *storagepb.UpdateDataSourceReq, options ...client.Option) (*storagepb.UpdateDataSourceRsp, error) {
	return invoke[storagepb.UpdateDataSourceRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "UpdateDataSource", request, options)
}

func (c *Client) ListSubjects(ctx context.Context, request *storagepb.ListSubjectsReq, options ...client.Option) (*storagepb.ListSubjectsRsp, error) {
	return invoke[storagepb.ListSubjectsRsp](ctx, c.gateway, "trpc.moox.storage.Metadata", "ListSubjects", request, options)
}

func (c *Client) RegisterSeriesSnapshot(ctx context.Context, request *storagepb.PrimaryRegisterSeriesSnapshotReq, options ...client.Option) (*storagepb.PrimaryRegisterSeriesSnapshotRsp, error) {
	return invoke[storagepb.PrimaryRegisterSeriesSnapshotRsp](ctx, c.gateway, "trpc.moox.storage.PrimaryStore", "RegisterSeriesSnapshot", request, options)
}

// CountTimeSeriesRows 按标的统计时间范围内已存在的行数，只回传计数（完整性检查先比对根数用）。
func (c *Client) CountTimeSeriesRows(ctx context.Context, request *storagepb.CountTimeSeriesRowsReq, options ...client.Option) (*storagepb.CountTimeSeriesRowsRsp, error) {
	return invoke[storagepb.CountTimeSeriesRowsRsp](ctx, c.gateway, "trpc.moox.storage.PrimaryStore", "CountTimeSeriesRows", request, options)
}

func (c *Client) ReadTimeSeriesRows(ctx context.Context, request *storagepb.ReadTimeSeriesRowsReq, options ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	return invoke[storagepb.ReadTimeSeriesRowsRsp](ctx, c.gateway, "trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows", request, options)
}

func (c *Client) CommitTimeSeriesBatch(ctx context.Context, request *storagepb.PrimaryCommitTimeSeriesBatchReq, options ...client.Option) (*storagepb.PrimaryCommitTimeSeriesBatchRsp, error) {
	return invoke[storagepb.PrimaryCommitTimeSeriesBatchRsp](ctx, c.gateway, "trpc.moox.storage.PrimaryStore", "CommitTimeSeriesBatch", request, options)
}

func (c *Client) UpsertFields(ctx context.Context, request *storagepb.PrimaryUpsertFieldsReq, options ...client.Option) (*storagepb.PrimaryUpsertFieldsRsp, error) {
	return invoke[storagepb.PrimaryUpsertFieldsRsp](ctx, c.gateway, "trpc.moox.storage.PrimaryStore", "UpsertFields", request, options)
}

func (c *Client) ReportCollectorPeriodCompleted(ctx context.Context, request *storagepb.ReportCollectorPeriodCompletedReq, options ...client.Option) (*storagepb.ReportCollectorPeriodCompletedRsp, error) {
	return invoke[storagepb.ReportCollectorPeriodCompletedRsp](ctx, c.gateway, "trpc.moox.storage.PrimaryStore", "ReportCollectorPeriodCompleted", request, options)
}

func (c *Client) ReadRows(ctx context.Context, request *storagepb.PrimaryReadRowsReq, options ...client.Option) (*storagepb.PrimaryReadRowsRsp, error) {
	return invoke[storagepb.PrimaryReadRowsRsp](ctx, c.gateway, "trpc.moox.storage.PrimaryStore", "ReadRows", request, options)
}

func (c *Client) WaitViewSyncPoint(ctx context.Context, request *storagepb.WaitViewSyncPointReq, options ...client.Option) (*storagepb.WaitViewSyncPointRsp, error) {
	return invoke[storagepb.WaitViewSyncPointRsp](ctx, c.gateway, "trpc.moox.storage.PrimaryStore", "WaitViewSyncPoint", request, options)
}

func (c *Client) DeleteDatasetRows(ctx context.Context, request *storagepb.PrimaryDeleteDatasetRowsReq, options ...client.Option) (*storagepb.PrimaryDeleteDatasetRowsRsp, error) {
	return invoke[storagepb.PrimaryDeleteDatasetRowsRsp](ctx, c.gateway, "trpc.moox.storage.PrimaryStore", "DeleteDatasetRows", request, options)
}

func (c *Client) RestoreDatasetRows(ctx context.Context, request *storagepb.PrimaryRestoreDatasetRowsReq, options ...client.Option) (*storagepb.PrimaryRestoreDatasetRowsRsp, error) {
	return invoke[storagepb.PrimaryRestoreDatasetRowsRsp](ctx, c.gateway, "trpc.moox.storage.PrimaryStore", "RestoreDatasetRows", request, options)
}

var _ Metadata = (*Client)(nil)
var _ Primary = (*Client)(nil)
