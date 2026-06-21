package primary

import (
	"context"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// Client 定义访问 PrimaryStore 服务的客户端接口。
type Client interface {
	WriteRows(ctx context.Context, device *pb.PrimaryStoreTarget, rows []*pb.PrimaryStoreRow) error
	ReadRows(ctx context.Context, device *pb.PrimaryStoreTarget, req *pb.ReadPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error)
	ScanRows(ctx context.Context, device *pb.PrimaryStoreTarget, req *pb.ScanPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error)
}
