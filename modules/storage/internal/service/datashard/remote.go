package datashard

import (
	"context"
	"fmt"
	"strings"
	"sync"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/client"
)

// RemoteClient 通过 tRPC 调用远端 PrimaryStore 服务。
type RemoteClient struct {
	serviceName string
	proxies     sync.Map
}

func NewRemoteClient(serviceName string) *RemoteClient {
	return &RemoteClient{serviceName: serviceName}
}

func (c *RemoteClient) WriteRows(ctx context.Context, target *pb.ShardTarget, rows []*pb.ShardRow) error {
	rsp, err := c.proxyFor(target).MergeRows(ctx, &pb.MergeRowsReq{
		Target: target,
		Rows:   rows,
	})
	if err != nil {
		return err
	}
	return retInfoError(rsp.GetRetInfo())
}

func (c *RemoteClient) ReadRows(ctx context.Context, target *pb.ShardTarget, req *pb.ReadRowsReq) ([]*pb.ShardRow, *pb.PageResult, error) {
	if req == nil {
		req = &pb.ReadRowsReq{}
	}
	rsp, err := c.proxyFor(target).ReadRows(ctx, &pb.ReadRowsReq{
		AuthInfo:     req.GetAuthInfo(),
		Target:       target,
		Keys:         req.GetKeys(),
		VersionRange: req.GetVersionRange(),
		Order:        req.GetOrder(),
		ColumnNames:  req.GetColumnNames(),
		Page:         req.GetPage(),
	})
	if err != nil {
		return nil, nil, err
	}
	if err := retInfoError(rsp.GetRetInfo()); err != nil {
		return nil, nil, err
	}
	return rsp.GetRows(), rsp.GetPageResult(), nil
}

func (c *RemoteClient) ScanRows(ctx context.Context, target *pb.ShardTarget, req *pb.ScanRowsReq) ([]*pb.ShardRow, *pb.PageResult, error) {
	if req == nil {
		req = &pb.ScanRowsReq{}
	}
	rsp, err := c.proxyFor(target).ScanRows(ctx, &pb.ScanRowsReq{
		AuthInfo:     req.GetAuthInfo(),
		Target:       target,
		DataKind:     req.GetDataKind(),
		VersionRange: req.GetVersionRange(),
		Order:        req.GetOrder(),
		ColumnNames:  req.GetColumnNames(),
		KeyPrefix:    req.GetKeyPrefix(),
		Page:         req.GetPage(),
	})
	if err != nil {
		return nil, nil, err
	}
	if err := retInfoError(rsp.GetRetInfo()); err != nil {
		return nil, nil, err
	}
	return rsp.GetRows(), rsp.GetPageResult(), nil
}

func (c *RemoteClient) DeleteRows(ctx context.Context, target *pb.ShardTarget, keys []*pb.ShardKey) error {
	rsp, err := c.proxyFor(target).DeleteRows(ctx, &pb.DeleteRowsReq{Target: target, Keys: keys})
	if err != nil {
		return err
	}
	return retInfoError(rsp.GetRetInfo())
}

func (c *RemoteClient) proxyFor(target *pb.ShardTarget) pb.DataShardClientProxy {
	endpoint := ""
	if target != nil {
		endpoint = strings.TrimSpace(target.GetEndpoint())
	}
	key := c.serviceName + "|" + endpoint
	if value, ok := c.proxies.Load(key); ok {
		return value.(pb.DataShardClientProxy)
	}
	proxy := pb.NewDataShardClientProxy(remoteClientOptions(c.serviceName, endpoint)...)
	actual, _ := c.proxies.LoadOrStore(key, proxy)
	return actual.(pb.DataShardClientProxy)
}

func remoteClientOptions(serviceName string, endpoint string) []client.Option {
	opts := make([]client.Option, 0, 2)
	if strings.TrimSpace(serviceName) != "" {
		opts = append(opts, client.WithServiceName(strings.TrimSpace(serviceName)))
	}
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" || endpoint == "local" {
		return opts
	}
	if strings.Contains(endpoint, "://") {
		return append(opts, client.WithTarget(endpoint))
	}
	if strings.Contains(endpoint, ":") {
		return append(opts, client.WithTarget("ip://"+endpoint))
	}
	return append(opts, client.WithServiceName(endpoint))
}

func retInfoError(ret *pb.RetInfo) error {
	if ret == nil || ret.GetCode() == pb.ErrorCode_SUCCESS {
		return nil
	}
	return fmt.Errorf("primary store returns %s: %s", ret.GetCode().String(), ret.GetMsg())
}
