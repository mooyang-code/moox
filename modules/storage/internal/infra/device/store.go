package device

import (
	"context"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

type FactStore interface {
	Close() error
	WriteRows(ctx context.Context, rows []*pb.DataRow, mode pb.WriteMode) error
	ReadRows(ctx context.Context, scope *pb.DataScope, mode pb.ReadMode, timeRange *pb.TimeRange, snapshotTime string, rowIDs []string, columnNames []string, page *pb.Page) ([]*pb.DataRow, *pb.PageResult, error)
}
