package adapter

import (
	"context"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

type Client interface {
	WriteRows(ctx context.Context, device *pb.DeviceRef, rows []*pb.DataRow, mode pb.WriteMode) error
	ReadRows(ctx context.Context, device *pb.DeviceRef, req *pb.ReadRowsReq) ([]*pb.DataRow, *pb.PageResult, error)
}
