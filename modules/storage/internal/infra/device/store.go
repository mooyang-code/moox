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
	ApplyRecordMutations(ctx context.Context, requestID string, mutations []*pb.RecordMutation) (*pb.RecordRowsCommittedEvent, error)
	OpenRecordSnapshot(ctx context.Context, mode pb.RecordReadMode, updatedTimeRange *pb.TimeRange) (snapshotID string, commitSeq uint64, err error)
	ReadRecordSnapshot(ctx context.Context, snapshotID string, target *pb.PrimaryStoreTarget, recordIDs []string) ([]*pb.RecordRow, error)
	ScanRecordSnapshot(ctx context.Context, snapshotID string, target *pb.PrimaryStoreTarget, page *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error)
	RenewRecordSnapshot(ctx context.Context, snapshotID string) error
	CloseRecordSnapshot(ctx context.Context, snapshotID string) error
	RecordWatermark(ctx context.Context) (sourceID string, commitSeq uint64, err error)
	ScanRecordJournal(ctx context.Context, after uint64, through uint64, page *pb.Page) (events []*pb.RecordRowsCommittedEvent, scannedThrough uint64, result *pb.PageResult, err error)
}
