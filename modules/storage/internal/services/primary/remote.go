package primary

import (
	"context"
	"fmt"
	"strings"
	"sync"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/client"
)

type RemoteClient struct {
	serviceName string
	proxies     sync.Map
}

func NewRemoteClient(serviceName string) *RemoteClient {
	return &RemoteClient{serviceName: serviceName}
}

func (c *RemoteClient) WriteRows(ctx context.Context, target *pb.PrimaryTarget, rows []*pb.DataRow, mode pb.WriteMode) error {
	rsp, err := c.proxyFor(target).WritePrimaryRows(ctx, &pb.WritePrimaryRowsReq{
		Target:    target,
		WriteMode: mode,
		Rows:      rows,
	})
	if err != nil {
		return err
	}
	return retInfoError(rsp.GetRetInfo())
}

func (c *RemoteClient) ReadRows(ctx context.Context, target *pb.PrimaryTarget, req *pb.ReadRowsReq) ([]*pb.DataRow, *pb.PageResult, error) {
	if req == nil {
		req = &pb.ReadRowsReq{}
	}
	rsp, err := c.proxyFor(target).ReadPrimaryRows(ctx, &pb.ReadPrimaryRowsReq{
		AuthInfo:     req.GetAuthInfo(),
		Target:       target,
		ReadMode:     req.GetReadMode(),
		Scope:        req.GetScope(),
		TimeRange:    req.GetTimeRange(),
		SnapshotTime: req.GetSnapshotTime(),
		RowIds:       req.GetRowIds(),
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

func (c *RemoteClient) proxyFor(target *pb.PrimaryTarget) pb.PrimaryStoreServiceClientProxy {
	endpoint := ""
	if target != nil {
		endpoint = strings.TrimSpace(target.GetEndpoint())
	}
	key := c.serviceName + "|" + endpoint
	if value, ok := c.proxies.Load(key); ok {
		return value.(pb.PrimaryStoreServiceClientProxy)
	}
	proxy := pb.NewPrimaryStoreServiceClientProxy(remoteClientOptions(c.serviceName, endpoint)...)
	actual, _ := c.proxies.LoadOrStore(key, proxy)
	return actual.(pb.PrimaryStoreServiceClientProxy)
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
