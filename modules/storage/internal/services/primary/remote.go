package primary

import (
	"context"
	"fmt"
	"strings"
	"sync"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
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

func (c *RemoteClient) WriteRows(ctx context.Context, target *pb.PrimaryStoreTarget, rows []*pb.PrimaryStoreRow) error {
	rsp, err := c.proxyFor(target).WritePrimaryRows(ctx, &pb.WritePrimaryRowsReq{
		Target: target,
		Rows:   rows,
	})
	if err != nil {
		return err
	}
	return retInfoError(rsp.GetRetInfo())
}

func (c *RemoteClient) ReadRows(ctx context.Context, target *pb.PrimaryStoreTarget, req *pb.ReadPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	if req == nil {
		req = &pb.ReadPrimaryRowsReq{}
	}
	rsp, err := c.proxyFor(target).ReadPrimaryRows(ctx, &pb.ReadPrimaryRowsReq{
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

func (c *RemoteClient) ScanRows(ctx context.Context, target *pb.PrimaryStoreTarget, req *pb.ScanPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	if req == nil {
		req = &pb.ScanPrimaryRowsReq{}
	}
	rsp, err := c.proxyFor(target).ScanPrimaryRows(ctx, &pb.ScanPrimaryRowsReq{
		AuthInfo:     req.GetAuthInfo(),
		Target:       target,
		DataKind:     req.GetDataKind(),
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

func (c *RemoteClient) proxyFor(target *pb.PrimaryStoreTarget) pb.PrimaryStoreClientProxy {
	endpoint := ""
	if target != nil {
		endpoint = strings.TrimSpace(target.GetEndpoint())
	}
	key := c.serviceName + "|" + endpoint
	if value, ok := c.proxies.Load(key); ok {
		return value.(pb.PrimaryStoreClientProxy)
	}
	proxy := pb.NewPrimaryStoreClientProxy(remoteClientOptions(c.serviceName, endpoint)...)
	actual, _ := c.proxies.LoadOrStore(key, proxy)
	return actual.(pb.PrimaryStoreClientProxy)
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
