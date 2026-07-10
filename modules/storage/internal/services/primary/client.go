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
	ApplyRecordMutations(ctx context.Context, device *pb.PrimaryStoreTarget, requestID string, mutations []*pb.RecordMutation) (*pb.RecordRowsCommittedEvent, error)
	OpenRecordSnapshot(ctx context.Context, req *pb.OpenRecordSnapshotReq) (*pb.OpenRecordSnapshotRsp, error)
	ReadRecordSnapshot(ctx context.Context, req *pb.ReadRecordSnapshotReq) (*pb.ReadRecordSnapshotRsp, error)
	ScanRecordSnapshot(ctx context.Context, req *pb.ScanRecordSnapshotReq) (*pb.ScanRecordSnapshotRsp, error)
	RenewRecordSnapshot(ctx context.Context, req *pb.RenewRecordSnapshotReq) error
	CloseRecordSnapshot(ctx context.Context, req *pb.CloseRecordSnapshotReq) error
	GetRecordWatermark(ctx context.Context, device *pb.PrimaryStoreTarget) (sourceID string, commitSeq uint64, err error)
	ScanRecordJournal(ctx context.Context, req *pb.ScanRecordJournalReq) (*pb.ScanRecordJournalRsp, error)
}
