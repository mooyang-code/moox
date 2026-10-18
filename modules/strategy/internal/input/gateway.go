package input

import (
	"context"
	"errors"
	"time"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"trpc.group/trpc-go/trpc-go/client"
)

// MetadataClient 是策略读取 Storage Metadata 用到的最小方法集合，便于测试替换。
type MetadataClient interface {
	GetView(context.Context, *storagepb.GetViewReq, ...client.Option) (*storagepb.GetViewRsp, error)
	ListViewColumns(context.Context, *storagepb.ListViewColumnsReq, ...client.Option) (*storagepb.ListViewColumnsRsp, error)
	GetDataset(context.Context, *storagepb.GetDatasetReq, ...client.Option) (*storagepb.GetDatasetRsp, error)
	GetTag(context.Context, *storagepb.GetTagReq, ...client.Option) (*storagepb.GetTagRsp, error)
	ListDatasetSubjects(context.Context, *storagepb.ListDatasetSubjectsReq, ...client.Option) (*storagepb.ListDatasetSubjectsRsp, error)
	ListSubjects(context.Context, *storagepb.ListSubjectsReq, ...client.Option) (*storagepb.ListSubjectsRsp, error)
	ListTagMembers(context.Context, *storagepb.ListTagMembersReq, ...client.Option) (*storagepb.ListTagMembersRsp, error)
}

// DataViewClient 是策略读取 Storage DataView 用到的最小方法集合。
type DataViewClient interface {
	QueryTimeSeriesRows(context.Context, *storagepb.QueryTimeSeriesRowsReq, ...client.Option) (*storagepb.QueryTimeSeriesRowsRsp, error)
}

// FactorClient 是策略读取 FactorMgr 用到的最小方法集合。
type FactorClient interface {
	GetFactor(context.Context, *factorpb.GetFactorReq, ...client.Option) (*factorpb.GetFactorRsp, error)
}

const (
	storageMetadataService = "trpc.moox.storage.Metadata"
	storageDataViewService = "trpc.moox.storage.DataView"
	factorMgrService       = "trpc.moox.factor.FactorMgr"
)

// gatewayAdapter 经共享的网关客户端（目录发现、签名与重试都在其中）调用 Storage 与 Factor。
type gatewayAdapter struct {
	gateway gatewayclient.Invoker
	// timeouts 分别是 Storage 与 Factor 调用的单次超时；零表示沿用调用方 ctx 的截止时间。
	storageTimeout time.Duration
	factorTimeout  time.Duration
}

var (
	_ MetadataClient = (*gatewayAdapter)(nil)
	_ DataViewClient = (*gatewayAdapter)(nil)
	_ FactorClient   = (*gatewayAdapter)(nil)
)

// NewGatewayClient 构造经网关访问 Storage（Metadata、DataView）与 Factor 的客户端。
func NewGatewayClient(gateway gatewayclient.Invoker, storageTimeout, factorTimeout time.Duration) *RPCClient {
	adapter := &gatewayAdapter{gateway: gateway, storageTimeout: storageTimeout, factorTimeout: factorTimeout}
	return &RPCClient{Metadata: adapter, DataView: adapter, Factor: adapter}
}

func invokeGateway[T any](ctx context.Context, gateway gatewayclient.Invoker, timeout time.Duration, service, method string, request any, options []client.Option) (*T, error) {
	if gateway == nil {
		return nil, errors.New("strategy 网关客户端未配置")
	}
	if len(options) != 0 {
		return nil, errors.New("strategy 的网关调用由 ctx 与服务目录决定参数，不接受 tRPC 客户端选项")
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	response := new(T)
	if err := gateway.Invoke(ctx, service, method, request, response); err != nil {
		return nil, err
	}
	return response, nil
}

func (a *gatewayAdapter) GetView(ctx context.Context, request *storagepb.GetViewReq, options ...client.Option) (*storagepb.GetViewRsp, error) {
	return invokeGateway[storagepb.GetViewRsp](ctx, a.gateway, a.storageTimeout, storageMetadataService, "GetView", request, options)
}

func (a *gatewayAdapter) ListViewColumns(ctx context.Context, request *storagepb.ListViewColumnsReq, options ...client.Option) (*storagepb.ListViewColumnsRsp, error) {
	return invokeGateway[storagepb.ListViewColumnsRsp](ctx, a.gateway, a.storageTimeout, storageMetadataService, "ListViewColumns", request, options)
}

func (a *gatewayAdapter) GetDataset(ctx context.Context, request *storagepb.GetDatasetReq, options ...client.Option) (*storagepb.GetDatasetRsp, error) {
	return invokeGateway[storagepb.GetDatasetRsp](ctx, a.gateway, a.storageTimeout, storageMetadataService, "GetDataset", request, options)
}

func (a *gatewayAdapter) GetTag(ctx context.Context, request *storagepb.GetTagReq, options ...client.Option) (*storagepb.GetTagRsp, error) {
	return invokeGateway[storagepb.GetTagRsp](ctx, a.gateway, a.storageTimeout, storageMetadataService, "GetTag", request, options)
}

func (a *gatewayAdapter) ListDatasetSubjects(ctx context.Context, request *storagepb.ListDatasetSubjectsReq, options ...client.Option) (*storagepb.ListDatasetSubjectsRsp, error) {
	return invokeGateway[storagepb.ListDatasetSubjectsRsp](ctx, a.gateway, a.storageTimeout, storageMetadataService, "ListDatasetSubjects", request, options)
}

func (a *gatewayAdapter) ListSubjects(ctx context.Context, request *storagepb.ListSubjectsReq, options ...client.Option) (*storagepb.ListSubjectsRsp, error) {
	return invokeGateway[storagepb.ListSubjectsRsp](ctx, a.gateway, a.storageTimeout, storageMetadataService, "ListSubjects", request, options)
}

func (a *gatewayAdapter) ListTagMembers(ctx context.Context, request *storagepb.ListTagMembersReq, options ...client.Option) (*storagepb.ListTagMembersRsp, error) {
	return invokeGateway[storagepb.ListTagMembersRsp](ctx, a.gateway, a.storageTimeout, storageMetadataService, "ListTagMembers", request, options)
}

func (a *gatewayAdapter) QueryTimeSeriesRows(ctx context.Context, request *storagepb.QueryTimeSeriesRowsReq, options ...client.Option) (*storagepb.QueryTimeSeriesRowsRsp, error) {
	return invokeGateway[storagepb.QueryTimeSeriesRowsRsp](ctx, a.gateway, a.storageTimeout, storageDataViewService, "QueryTimeSeriesRows", request, options)
}

func (a *gatewayAdapter) GetFactor(ctx context.Context, request *factorpb.GetFactorReq, options ...client.Option) (*factorpb.GetFactorRsp, error) {
	return invokeGateway[factorpb.GetFactorRsp](ctx, a.gateway, a.factorTimeout, factorMgrService, "GetFactor", request, options)
}
