package adapter

import (
	"context"

	"github.com/mooyang-code/moox/modules/storage/pkg/quantstore"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

type LocalClient struct {
	store *quantstore.Store
}

func NewLocalClient(store *quantstore.Store) *LocalClient {
	return &LocalClient{store: store}
}

func (c *LocalClient) WriteRows(ctx context.Context, device *pb.DeviceRef, rows []*pb.DataRow, mode pb.WriteMode) error {
	_ = device
	return c.store.WriteRows(ctx, rows, mode)
}

func (c *LocalClient) ReadRows(ctx context.Context, device *pb.DeviceRef, req *pb.ReadRowsReq) ([]*pb.DataRow, *pb.PageResult, error) {
	_ = device
	return c.store.ReadRows(ctx, req.GetScope(), req.GetReadMode(), req.GetTimeRange(), req.GetSnapshotTime(), req.GetRowIds(), req.GetColumnNames(), req.GetPage())
}
