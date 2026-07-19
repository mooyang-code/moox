//go:build legacy_viewindex

package viewindex

import (
	"context"
	"errors"
	"fmt"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/client"
)

type Client struct {
	engine string
	proxy  pb.ViewIndexClientProxy
}

func NewRemoteClient(serviceName, engine string, opts ...client.Option) *Client {
	if serviceName != "" {
		opts = append([]client.Option{client.WithServiceName(serviceName)}, opts...)
	}
	return &Client{engine: strings.ToLower(engine), proxy: pb.NewViewIndexClientProxy(opts...)}
}
func NewLocalClient(service pb.ViewIndexService, engine string) *Client {
	return &Client{engine: strings.ToLower(engine), proxy: &localProxy{service}}
}
func (c *Client) Engine() string { return c.engine }
func (c *Client) Prepare(ctx context.Context, id string, s ViewIndexSchema) error {
	rsp, e := c.proxy.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{IndexId: id, Engine: c.engine, Schema: &pb.ViewIndexSchema{SpaceId: s.SpaceID, ViewId: s.ViewID, ViewVersion: s.ViewVersion, Engine: s.Engine, Columns: s.Columns, ViewSchemaHash: s.SchemaHash}})
	return join(e, rsp.GetRetInfo())
}
func (c *Client) Apply(ctx context.Context, id string, b ViewIndexApplyBatch) error {
	rows := make([]*pb.ViewIndexRowWrite, 0, len(b.RowWrites))
	for _, w := range b.RowWrites {
		rows = append(rows, &pb.ViewIndexRowWrite{Operation: pb.ViewIndexRowWriteOperation_VIEW_INDEX_ROW_WRITE_OPERATION_UPSERT, Key: &pb.ViewIndexRowKey{RowKey: w.Key.Key}, Fields: w.Fields, Attributes: w.Attributes})
	}
	rsp, e := c.proxy.ApplyViewIndex(ctx, &pb.ApplyViewIndexReq{IndexId: id, Engine: c.engine, Batch: &pb.ViewIndexApplyBatch{RowWrites: rows, ViewRevision: b.ViewRevision, ViewSchemaHash: b.ViewSchemaHash, WriteMode: b.WriteMode.String()}})
	return join(e, rsp.GetRetInfo())
}
func (c *Client) Write(ctx context.Context, id string, b BatchWrite) error {
	e, ok := c.proxy.(interface {
		ApplyViewIndex(context.Context, *pb.ApplyViewIndexReq, ...client.Option) (*pb.ApplyViewIndexRsp, error)
	})
	_ = e
	_ = ok
	writes := make([]RowWrite, 0, len(b.TimeSeriesRows)+len(b.RecordRows))
	for _, r := range b.TimeSeriesRows {
		if r != nil {
			writes = append(writes, RowWrite{Key: RowKey{Key: queryTSKey(r.GetKey())}, Fields: r.GetFields()})
		}
	}
	for _, r := range b.RecordRows {
		if r != nil {
			writes = append(writes, RowWrite{Key: RowKey{Key: queryRecordKey(r.GetKey())}, Fields: r.GetFields()})
		}
	}
	return c.Apply(ctx, id, ViewIndexApplyBatch{RowWrites: writes, ViewRevision: b.ViewVersion, ViewSchemaHash: b.SchemaHash, WriteMode: LiveWrite})
}
func (c *Client) Stat(ctx context.Context, id string) (ViewIndexStats, error) {
	rsp, e := c.proxy.StatViewIndex(ctx, &pb.StatViewIndexReq{IndexId: id, Engine: c.engine})
	if e != nil {
		return ViewIndexStats{}, e
	}
	if err := retErr(rsp.GetRetInfo()); err != nil {
		return ViewIndexStats{}, err
	}
	s := rsp.GetStats()
	return ViewIndexStats{Exists: s.GetExists(), ViewVersion: s.GetViewVersion(), EntryCount: int64(s.GetEntryCount()), MinVersion: s.GetMinVersion(), MaxVersion: s.GetMaxVersion(), SchemaHash: s.GetViewSchemaHash(), PhysicalBytes: s.GetPhysicalBytes(), UpdatedAt: s.GetUpdatedAt(), FreeDiskBytes: s.GetFreeDiskBytes(), IndexedFrom: s.GetIndexedFrom(), IndexedTo: s.GetIndexedTo()}, nil
}
func (c *Client) Remove(ctx context.Context, id string) error {
	rsp, e := c.proxy.RemoveViewIndex(ctx, &pb.RemoveViewIndexReq{IndexId: id, Engine: c.engine})
	return join(e, rsp.GetRetInfo())
}
func (c *Client) List(ctx context.Context) ([]string, error) {
	rsp, e := c.proxy.ListViewIndexes(ctx, &pb.ListViewIndexesReq{Engine: c.engine})
	if e != nil {
		return nil, e
	}
	if err := retErr(rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rsp.GetIndexes()))
	for _, x := range rsp.GetIndexes() {
		out = append(out, x.GetIndexId())
	}
	return out, nil
}
func join(e error, ri *pb.RetInfo) error {
	if e != nil {
		return e
	}
	return retErr(ri)
}
func retErr(ri *pb.RetInfo) error {
	if ri == nil {
		return errors.New("view index owner returned no status")
	}
	if ri.GetCode() == pb.ErrorCode_SUCCESS {
		return nil
	}
	return fmt.Errorf("view index owner failed: %s", ri.GetMsg())
}

type localProxy struct{ svc pb.ViewIndexService }

func (p *localProxy) PrepareViewIndex(c context.Context, r *pb.PrepareViewIndexReq, _ ...client.Option) (*pb.PrepareViewIndexRsp, error) {
	return p.svc.PrepareViewIndex(c, r)
}
func (p *localProxy) ApplyViewIndex(c context.Context, r *pb.ApplyViewIndexReq, _ ...client.Option) (*pb.ApplyViewIndexRsp, error) {
	return p.svc.ApplyViewIndex(c, r)
}
func (p *localProxy) StatViewIndex(c context.Context, r *pb.StatViewIndexReq, _ ...client.Option) (*pb.StatViewIndexRsp, error) {
	return p.svc.StatViewIndex(c, r)
}
func (p *localProxy) RemoveViewIndex(c context.Context, r *pb.RemoveViewIndexReq, _ ...client.Option) (*pb.RemoveViewIndexRsp, error) {
	return p.svc.RemoveViewIndex(c, r)
}
func (p *localProxy) ListViewIndexes(c context.Context, r *pb.ListViewIndexesReq, _ ...client.Option) (*pb.ListViewIndexesRsp, error) {
	return p.svc.ListViewIndexes(c, r)
}
func (p *localProxy) QueryTimeSeriesIndex(c context.Context, r *pb.QueryTimeSeriesIndexReq, _ ...client.Option) (*pb.QueryTimeSeriesIndexRsp, error) {
	return p.svc.QueryTimeSeriesIndex(c, r)
}
func (p *localProxy) SearchRecordIndex(c context.Context, r *pb.SearchRecordIndexReq, _ ...client.Option) (*pb.SearchRecordIndexRsp, error) {
	return p.svc.SearchRecordIndex(c, r)
}
