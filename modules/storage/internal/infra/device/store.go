package device

import (
	"context"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// FactStore 定义主存设备必须提供的事实行读写接口。
type FactStore interface {
	Close() error
	WriteRows(ctx context.Context, rows []*pb.PrimaryStoreRow) error
	ReadRows(ctx context.Context, keys []*pb.PrimaryStoreKey, versionRange *pb.VersionRange, order pb.SortOrder, columnNames []string, page *pb.Page) ([]*pb.PrimaryStoreRow, *pb.PageResult, error)
	ScanRows(ctx context.Context, target *pb.PrimaryStoreTarget, dataKind pb.DataKind, versionRange *pb.VersionRange, order pb.SortOrder, columnNames []string, page *pb.Page) ([]*pb.PrimaryStoreRow, *pb.PageResult, error)
}
