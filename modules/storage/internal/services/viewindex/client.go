package viewindex

import (
	"context"
	"errors"
	"fmt"
	"strings"

	coreviewindex "github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/client"
)

type Client struct {
	engine string
	proxy  pb.ViewIndexClientProxy
}

var _ ManagedEngine = (*Client)(nil)
var _ TimeSeriesQuerier = (*Client)(nil)
var _ RecordQuerier = (*Client)(nil)

func NewRemoteClient(serviceName string, engine string, opts ...client.Option) *Client {
	if serviceName != "" {
		opts = append([]client.Option{client.WithServiceName(serviceName)}, opts...)
	}
	return newClientWithProxy(engine, pb.NewViewIndexClientProxy(opts...))
}

// NewLocalClient uses the same owner RPC boundary as a remote client without a
// network hop. Bundled deployments therefore retain all identity and write fences.
func NewLocalClient(service pb.ViewIndexService, engine string) *Client {
	return newClientWithProxy(engine, &localViewIndexProxy{service: service})
}

func newClientWithProxy(engine string, proxy pb.ViewIndexClientProxy) *Client {
	return &Client{engine: strings.ToLower(strings.TrimSpace(engine)), proxy: proxy}
}

func (c *Client) Engine() string {
	return c.engine
}

func (c *Client) Prepare(ctx context.Context, indexID string, schema coreviewindex.ViewIndexSchema) error {
	rsp, err := c.proxy.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{
		IndexId: indexID,
		Engine:  c.engine,
		Schema: &pb.ViewIndexSchema{
			SpaceId: schema.SpaceID, ViewId: schema.ViewID, ViewVersion: schema.ViewVersion,
			Engine: c.engine, Columns: schema.Columns, SchemaHash: schema.SchemaHash,
		},
	})
	if err != nil {
		return err
	}
	return ownerRetInfoError(rsp.GetRetInfo())
}

func (c *Client) Write(ctx context.Context, indexID string, batch coreviewindex.ViewIndexBatch) error {
	rsp, err := c.proxy.WriteViewIndex(ctx, &pb.WriteViewIndexReq{
		IndexId: indexID,
		Engine:  c.engine,
		Batch: &pb.ViewIndexBatch{
			TimeSeriesRows: batch.TimeSeriesRows, RecordRows: batch.RecordRows, Columns: batch.Columns,
			ViewVersion: batch.ViewVersion, SchemaHash: batch.SchemaHash,
		},
	})
	if err != nil {
		return err
	}
	return ownerRetInfoError(rsp.GetRetInfo())
}

func (c *Client) Stat(ctx context.Context, indexID string) (coreviewindex.ViewIndexStats, error) {
	rsp, err := c.proxy.StatViewIndex(ctx, &pb.StatViewIndexReq{IndexId: indexID, Engine: c.engine})
	if err != nil {
		return coreviewindex.ViewIndexStats{}, err
	}
	if err := ownerRetInfoError(rsp.GetRetInfo()); err != nil {
		return coreviewindex.ViewIndexStats{}, err
	}
	return statsFromProto(rsp.GetStats()), nil
}

func (c *Client) Remove(ctx context.Context, indexID string) error {
	rsp, err := c.proxy.RemoveViewIndex(ctx, &pb.RemoveViewIndexReq{IndexId: indexID, Engine: c.engine})
	if err != nil {
		return err
	}
	return ownerRetInfoError(rsp.GetRetInfo())
}

func (c *Client) List(ctx context.Context) ([]string, error) {
	rsp, err := c.proxy.ListViewIndexes(ctx, &pb.ListViewIndexesReq{Engine: c.engine})
	if err != nil {
		return nil, err
	}
	if err := ownerRetInfoError(rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rsp.GetIndexes()))
	for _, item := range rsp.GetIndexes() {
		out = append(out, item.GetIndexId())
	}
	return out, nil
}

func (c *Client) QueryTimeSeriesRows(ctx context.Context, indexID string, req *pb.QueryTimeSeriesRowsReq) ([]*pb.ResultColumn, []*pb.TimeSeriesRow, *pb.PageResult, error) {
	rsp, err := c.proxy.QueryTimeSeriesIndex(ctx, &pb.QueryTimeSeriesIndexReq{IndexId: indexID, Query: req})
	if err != nil {
		return nil, nil, nil, err
	}
	if err := ownerRetInfoError(rsp.GetRetInfo()); err != nil {
		return nil, nil, nil, err
	}
	return rsp.GetColumns(), rsp.GetRows(), rsp.GetPageResult(), nil
}

func (c *Client) QueryRecordRows(ctx context.Context, indexID string, datasetID string, req *pb.SearchRecordRowsReq) ([]*pb.ResultColumn, []*pb.RecordRow, *pb.PageResult, error) {
	rsp, err := c.proxy.SearchRecordIndex(ctx, &pb.SearchRecordIndexReq{IndexId: indexID, DatasetId: datasetID, Query: req})
	if err != nil {
		return nil, nil, nil, err
	}
	if err := ownerRetInfoError(rsp.GetRetInfo()); err != nil {
		return nil, nil, nil, err
	}
	return rsp.GetColumns(), rsp.GetRows(), rsp.GetPageResult(), nil
}

func ownerRetInfoError(ret *pb.RetInfo) error {
	if ret == nil {
		return errors.New("view index owner returned no status")
	}
	if ret.GetCode() == pb.ErrorCode_SUCCESS {
		return nil
	}
	return fmt.Errorf("view index owner failed: code=%d msg=%s", ret.GetCode(), ret.GetMsg())
}

type localViewIndexProxy struct {
	service pb.ViewIndexService
}

func (p *localViewIndexProxy) PrepareViewIndex(ctx context.Context, req *pb.PrepareViewIndexReq, _ ...client.Option) (*pb.PrepareViewIndexRsp, error) {
	return p.service.PrepareViewIndex(ctx, req)
}

func (p *localViewIndexProxy) WriteViewIndex(ctx context.Context, req *pb.WriteViewIndexReq, _ ...client.Option) (*pb.WriteViewIndexRsp, error) {
	return p.service.WriteViewIndex(ctx, req)
}

func (p *localViewIndexProxy) StatViewIndex(ctx context.Context, req *pb.StatViewIndexReq, _ ...client.Option) (*pb.StatViewIndexRsp, error) {
	return p.service.StatViewIndex(ctx, req)
}

func (p *localViewIndexProxy) RemoveViewIndex(ctx context.Context, req *pb.RemoveViewIndexReq, _ ...client.Option) (*pb.RemoveViewIndexRsp, error) {
	return p.service.RemoveViewIndex(ctx, req)
}

func (p *localViewIndexProxy) ListViewIndexes(ctx context.Context, req *pb.ListViewIndexesReq, _ ...client.Option) (*pb.ListViewIndexesRsp, error) {
	return p.service.ListViewIndexes(ctx, req)
}

func (p *localViewIndexProxy) QueryTimeSeriesIndex(ctx context.Context, req *pb.QueryTimeSeriesIndexReq, _ ...client.Option) (*pb.QueryTimeSeriesIndexRsp, error) {
	return p.service.QueryTimeSeriesIndex(ctx, req)
}

func (p *localViewIndexProxy) SearchRecordIndex(ctx context.Context, req *pb.SearchRecordIndexReq, _ ...client.Option) (*pb.SearchRecordIndexRsp, error) {
	return p.service.SearchRecordIndex(ctx, req)
}
