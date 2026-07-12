package device

import (
	"context"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// OutboxEntry is the immutable message persisted beside fact rows.
type OutboxEntry struct {
	Sequence  uint64
	MessageID string
	Topic     string
	Data      []byte
	CreatedAt time.Time
}

// FactStore 定义主存设备必须提供的事实行读写接口。
type FactStore interface {
	Close() error
	WriteRows(ctx context.Context, rows []*pb.PrimaryStoreRow) error
	WriteRowsWithOutbox(ctx context.Context, rows []*pb.PrimaryStoreRow, entry *OutboxEntry) error
	ListOutbox(ctx context.Context, after uint64, maxItems int, maxBytes int) ([]*OutboxEntry, error)
	DeleteOutbox(ctx context.Context, sequences []uint64) error
	ReadRows(ctx context.Context, keys []*pb.PrimaryStoreKey, versionRange *pb.VersionRange, order pb.SortOrder, columnNames []string, page *pb.Page) ([]*pb.PrimaryStoreRow, *pb.PageResult, error)
	ScanRows(ctx context.Context, target *pb.PrimaryStoreTarget, dataKind pb.DataKind, versionRange *pb.VersionRange, order pb.SortOrder, columnNames []string, page *pb.Page) ([]*pb.PrimaryStoreRow, *pb.PageResult, error)
}

// FactDeleter is optional so read/write-only test stores remain valid. The
// Pebble implementation provides it for Storage retention maintenance.
type FactDeleter interface {
	DeleteRows(context.Context, []*pb.PrimaryStoreKey) error
}

// FactPrefixScanner optionally narrows a time-series scan to one subject/freq
// data-key prefix, avoiding a full dataset walk on large host datasets.
type FactPrefixScanner interface {
	ScanRowsWithPrefix(context.Context, *pb.PrimaryStoreTarget, pb.DataKind, *pb.VersionRange, pb.SortOrder, []string, *pb.Page, string) ([]*pb.PrimaryStoreRow, *pb.PageResult, error)
}
