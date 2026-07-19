package datashard

import (
	"context"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// Client 定义访问 PrimaryStore 服务的客户端接口。
type Client interface {
	WriteRows(ctx context.Context, device *pb.ShardTarget, rows []*pb.ShardRow) error
	ReadRows(ctx context.Context, device *pb.ShardTarget, req *pb.ReadRowsReq) ([]*pb.ShardRow, *pb.PageResult, error)
	ScanRows(ctx context.Context, device *pb.ShardTarget, req *pb.ScanRowsReq) ([]*pb.ShardRow, *pb.PageResult, error)
}

type HeadReader interface {
	HeadSequence(ctx context.Context, device *pb.ShardTarget) (uint64, error)
}

type Deleter interface {
	DeleteRows(context.Context, *pb.ShardTarget, []*pb.ShardKey) error
}
