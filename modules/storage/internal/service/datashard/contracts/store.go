//go:build legacy_storage

package contracts

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
	WriteRows(ctx context.Context, rows []*pb.ShardRow) error
	WriteRowsWithOutbox(ctx context.Context, rows []*pb.ShardRow, entry *OutboxEntry) error
	ListOutbox(ctx context.Context, after uint64, maxItems int, maxBytes int) ([]*OutboxEntry, error)
	DeleteOutbox(ctx context.Context, sequences []uint64) error
	ReadRows(ctx context.Context, keys []*pb.ShardKey, versionRange *pb.VersionRange, order pb.SortOrder, columnNames []string, page *pb.Page) ([]*pb.ShardRow, *pb.PageResult, error)
	ScanRows(ctx context.Context, target *pb.ShardTarget, dataKind pb.DataKind, versionRange *pb.VersionRange, order pb.SortOrder, columnNames []string, page *pb.Page) ([]*pb.ShardRow, *pb.PageResult, error)
}

// CommittedWriter is implemented by a DataShard that owns event construction.
// Callers provide only fact patches; the shard creates the RowsCommitted
// payload after its atomic merge.
type CommittedWriter interface {
	WriteRowsWithCommittedMessage(context.Context, []*pb.ShardRow) error
}

// CommittedDeleter is the delete counterpart. A DataShard emits a DELETE
// RowsCommitted entry in the same Pebble batch as the fact removal.
type CommittedDeleter interface {
	DeleteRowsWithCommittedMessage(context.Context, []*pb.ShardKey) error
}

type ShardIdentity interface {
	ShardID() string
}

type ShardHeadReader interface {
	HeadSequence(context.Context) (uint64, error)
}

// FactDeleter is optional so read/write-only test stores remain valid. The
// Pebble implementation provides it for Storage retention maintenance.
type FactDeleter interface {
	DeleteRows(context.Context, []*pb.ShardKey) error
}

// FactPrefixScanner optionally narrows a time-series scan to one subject/freq
// data-key prefix, avoiding a full dataset walk on large host datasets.
type FactPrefixScanner interface {
	ScanRowsWithPrefix(context.Context, *pb.ShardTarget, pb.DataKind, *pb.VersionRange, pb.SortOrder, []string, *pb.Page, string) ([]*pb.ShardRow, *pb.PageResult, error)
}
