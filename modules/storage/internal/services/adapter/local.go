package adapter

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/storage/internal/services/device"
	"github.com/mooyang-code/moox/modules/storage/pkg/quantstore"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

type LocalOptions struct {
	Pebble device.FactStore
}

type Local struct {
	pebble device.FactStore
}

func NewLocal(opts LocalOptions) *Local {
	return &Local{pebble: opts.Pebble}
}

func (l *Local) WriteRows(ctx context.Context, ref *pb.DeviceRef, rows []*pb.DataRow, mode pb.WriteMode) error {
	switch ref.GetEngine() {
	case "", "pebble":
		return l.pebble.WriteRows(ctx, rows, mode)
	default:
		return fmt.Errorf("unsupported write engine %s", ref.GetEngine())
	}
}

func (l *Local) ReadRows(ctx context.Context, ref *pb.DeviceRef, req *pb.ReadRowsReq) ([]*pb.DataRow, *pb.PageResult, error) {
	switch ref.GetEngine() {
	case "", "pebble":
		return l.pebble.ReadRows(ctx, req.GetScope(), req.GetReadMode(), req.GetTimeRange(), req.GetSnapshotTime(), req.GetRowIds(), req.GetColumnNames(), req.GetPage())
	default:
		return nil, nil, fmt.Errorf("unsupported read engine %s", ref.GetEngine())
	}
}

type LocalClient struct {
	store *quantstore.Store
}

func NewLocalClient(store *quantstore.Store) *LocalClient {
	return &LocalClient{store: store}
}

func (c *LocalClient) WriteRows(ctx context.Context, device *pb.DeviceRef, rows []*pb.DataRow, mode pb.WriteMode) error {
	switch device.GetEngine() {
	case "", "pebble":
		return c.store.WriteRows(ctx, rows, mode)
	default:
		return fmt.Errorf("unsupported write engine %s", device.GetEngine())
	}
}

func (c *LocalClient) ReadRows(ctx context.Context, device *pb.DeviceRef, req *pb.ReadRowsReq) ([]*pb.DataRow, *pb.PageResult, error) {
	switch device.GetEngine() {
	case "", "pebble":
		return c.store.ReadRows(ctx, req.GetScope(), req.GetReadMode(), req.GetTimeRange(), req.GetSnapshotTime(), req.GetRowIds(), req.GetColumnNames(), req.GetPage())
	default:
		return nil, nil, fmt.Errorf("unsupported read engine %s", device.GetEngine())
	}
}
