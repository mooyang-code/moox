package viewindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/encoding/protojson"
)

type Slot string

const (
	SlotA Slot = "a"
	SlotB Slot = "b"
)

type WriteMode uint8

const (
	LiveWrite WriteMode = iota + 1
	Backfill
)

func (m WriteMode) String() string {
	switch m {
	case Backfill:
		return "BACKFILL"
	default:
		return "LIVE_WRITE"
	}
}

type RowKey struct{ Key *pb.RowKey }

type RowWrite struct {
	Key        RowKey
	Fields     []*pb.FieldValue
	Attributes map[string]*pb.TypedValue
}

type ViewIndexWriteBatch struct {
	RowWrites      []RowWrite
	ViewRevision   uint64
	ViewSchemaHash string
	WriteMode      WriteMode
}

func (k RowKey) Validate() error {
	if k.Key == nil {
		return errors.New("row key is required")
	}
	if k.Key.GetSpaceId() == "" || k.Key.GetDatasetId() == "" {
		return errors.New("row key space_id and dataset_id are required")
	}
	if k.Key.GetTimeSeries() == nil && k.Key.GetRecord() == nil {
		return errors.New("row key kind is required")
	}
	return nil
}

func (w RowWrite) Validate() error {
	if err := w.Key.Validate(); err != nil {
		return err
	}
	for _, f := range w.Fields {
		if f == nil || f.GetFieldId() == "" {
			return errors.New("field upsert requires field_id")
		}
	}
	for name, value := range w.Attributes {
		if name == "" || value == nil {
			return errors.New("attribute upsert requires name and value")
		}
	}
	return nil
}

func (b ViewIndexWriteBatch) Validate() error {
	if len(b.RowWrites) == 0 {
		return errors.New("view index write batch is empty")
	}
	if b.ViewRevision == 0 || strings.TrimSpace(b.ViewSchemaHash) == "" {
		return errors.New("view_revision and view_schema_hash are required")
	}
	if b.WriteMode == 0 {
		return errors.New("write_mode is required")
	}
	for i, w := range b.RowWrites {
		if err := w.Validate(); err != nil {
			return fmt.Errorf("row write %d: %w", i, err)
		}
	}
	return nil
}

type ViewIndexSchema struct {
	SpaceID          string
	ViewID           string
	PrimaryDatasetID string
	ViewVersion      uint64
	Engine           string
	Columns          []*pb.ViewColumn
	SchemaHash       string
	// RowsPerSeries 是建立索引时使用的每序列保留行数，写入索引自身的元数据；0 表示未知。不参与 schema 哈希。
	RowsPerSeries uint64
}

type ViewIndexStats struct {
	Exists        bool
	ViewVersion   uint64
	EntryCount    int64
	PhysicalBytes uint64
	MinVersion    string
	MaxVersion    string
	SchemaHash    string
	UpdatedAt     string
	FreeDiskBytes uint64
	IndexedFrom   string
	IndexedTo     string
	// RowsPerSeries 是索引建立时记录的每序列保留行数；0 表示未知（旧索引）。
	RowsPerSeries uint64
}

type Filter struct {
	Column string
	Op     pb.FilterOp
	Values []*pb.TypedValue
}

type FilterGroup struct {
	Conds   []Filter
	Logical pb.FilterLogical
}

// TimeSeriesSelector is a range predicate, not an exact row identity.
// A nil SeriesTag matches all tags; a non-nil empty value matches only the
// default series.
type TimeSeriesSelector struct {
	SpaceID   string
	DatasetID string
	SubjectID string
	Freq      string
	SeriesTag *string
}

type QuerySpec struct {
	Keys         []*pb.RowKey
	Selectors    []TimeSeriesSelector
	TimeRange    *pb.TimeRange
	VersionRange *pb.VersionRange
	TextQuery    string
	Groups       []FilterGroup
	GroupLogical pb.FilterLogical
	Sorts        []*pb.SortSpec
	Order        pb.SortOrder
	Includes     []string
	AfterKey     *pb.RowKey
	Offset       int
	Limit        int
	TotalMode    pb.TotalMode
}

// Engine is the single physical View index contract. Index existence is owned
// by metadata catalog state; an engine only prepares, writes, queries, stats,
// and removes an explicitly named index.
type Engine interface {
	Engine() string
	Prepare(context.Context, string, ViewIndexSchema) error
	Write(context.Context, string, ViewIndexWriteBatch) error
	Query(context.Context, string, QuerySpec) ([]*pb.RowFieldValues, int64, error)
	Stat(context.Context, string) (ViewIndexStats, error)
	Remove(context.Context, string) error
}

// RowCounter 是可选能力：按标的统计谓词范围内不同 data_time 的行数，只回传计数。
// 完整性检查先比对根数，只有不够的标的才读行，读取量随缺口而不是随标的数增长。
type RowCounter interface {
	CountBySubject(context.Context, string, QuerySpec) (map[string]int64, error)
}

// SchemaExtender applies an append-only schema revision to an existing active
// physical index without replacing the index or rebuilding its rows.
type SchemaExtender interface {
	ExtendColumns(context.Context, string, ViewIndexSchema, ViewIndexSchema) error
}

// ErrSchemaExtensionConflict means the physical index cannot satisfy the requested append-only contract.
var ErrSchemaExtensionConflict = errors.New("view schema extension conflicts with physical index")

// IsAppendOnlyViewColumns reports whether desired preserves every physical
// column in active and appends at least one new column after its sort order.
func IsAppendOnlyViewColumns(active, desired []*pb.ViewColumn) bool {
	if len(active) == 0 || len(desired) <= len(active) {
		return false
	}
	activeByName := make(map[string]*pb.ViewColumn, len(active))
	usedOrder := make(map[uint32]struct{}, len(desired))
	var maxOrder uint32
	for index, column := range active {
		if column == nil || column.GetColumnName() == "" {
			return false
		}
		if _, exists := activeByName[column.GetColumnName()]; exists {
			return false
		}
		activeByName[column.GetColumnName()] = column
		if index == 0 || column.GetSortOrder() > maxOrder {
			maxOrder = column.GetSortOrder()
		}
	}
	seen := make(map[string]struct{}, len(desired))
	appended := 0
	for _, column := range desired {
		if column == nil || column.GetColumnName() == "" {
			return false
		}
		if _, exists := seen[column.GetColumnName()]; exists {
			return false
		}
		seen[column.GetColumnName()] = struct{}{}
		if _, exists := usedOrder[column.GetSortOrder()]; exists {
			return false
		}
		usedOrder[column.GetSortOrder()] = struct{}{}
		previous := activeByName[column.GetColumnName()]
		if previous == nil {
			if column.GetSortOrder() <= maxOrder {
				return false
			}
			appended++
			continue
		}
		if previous.GetOriginId() != column.GetOriginId() || previous.GetOriginType() != column.GetOriginType() ||
			previous.GetValueType() != column.GetValueType() || previous.GetSortOrder() != column.GetSortOrder() {
			return false
		}
	}
	return appended == len(desired)-len(active)
}

// ManagedIndexLister returns only official physical index IDs owned by this
// engine. Callers may remove returned IDs through Engine.Remove; filesystem
// paths never cross this boundary.
type ManagedIndexLister interface {
	ListManagedIndexes(context.Context) ([]string, error)
}

// ExistenceChecker is the lightweight physical-index probe used by the live
// event path. Stat may scan the whole index to calculate coverage and counts,
// which is far too expensive to run before every row write.
type ExistenceChecker interface {
	Exists(context.Context, string) (bool, error)
}

// MetadataStatReader returns the persisted schema contract without scanning
// all rows. Startup and restore only need to verify that an active index is
// present and matches Metadata; the full Stat operation may execute COUNT/MIN
// and MAX over a large DuckDB file and must remain on the periodic View Maintainer
// path.
type MetadataStatReader interface {
	StatMetadata(context.Context, string) (ViewIndexStats, error)
}

type SeriesCapacityResult struct {
	Exceeded  bool
	SubjectID string
	Freq      string
	SeriesTag string
	Rows      uint64
}

// SeriesCapacityReader reports the first sequence whose physical row count
// exceeds the configured limit. It is intentionally optional so record
// indexes do not need to invent a time-series capacity model.
type SeriesCapacityReader interface {
	SeriesCapacity(context.Context, string, uint64) (SeriesCapacityResult, error)
}

// OldestRowsCut 是一个序列本批要删除的最旧数据：data_time 早于 Before 的行，按计划共 Rows 行。
type OldestRowsCut struct {
	SubjectID string
	Freq      string
	SeriesTag string
	Before    time.Time
	Rows      uint64
}

// OldestRowsDeleter 删除每个序列超出保留根数的最旧数据，由 View 的定时任务调用；记录型索引不实现。
//
// PlanOldestRows 只读（不需要写锁）：列出最多 maxSeries 个行数超过 keep 的序列，合计最多 maxRows 行，
// 并按一致快照算出每个序列的删除分界。DeleteOldestRows 由调用方在持有该索引写锁时调用：在一个事务里删掉分界之前的行、
// 按实际删除行数减少序列计数，返回删除的总行数。RefreshCoverageFrom 同样在写锁内调用，把覆盖起点前移到剩余行的最早时间。
type OldestRowsDeleter interface {
	PlanOldestRows(ctx context.Context, indexID string, keep uint64, maxSeries, maxRows int) ([]OldestRowsCut, error)
	DeleteOldestRows(ctx context.Context, indexID string, cuts []OldestRowsCut) (uint64, error)
	RefreshCoverageFrom(ctx context.Context, indexID string) (string, error)
}

func RowKeyID(k *pb.RowKey) string {
	raw, _ := protojson.Marshal(k)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func HashViewIndexSchema(schema ViewIndexSchema) string {
	type col struct {
		Name, OriginID        string
		OriginType, ValueType int32
		SortOrder             uint32
	}
	shape := struct {
		SpaceID, ViewID, PrimaryDatasetID, Engine string
		Columns                                   []col
	}{SpaceID: schema.SpaceID, ViewID: schema.ViewID, PrimaryDatasetID: schema.PrimaryDatasetID, Engine: schema.Engine}
	for _, c := range schema.Columns {
		if c != nil {
			shape.Columns = append(shape.Columns, col{c.GetColumnName(), c.GetOriginId(), int32(c.GetOriginType()), int32(c.GetValueType()), c.GetSortOrder()})
		}
	}
	raw, _ := json.Marshal(shape)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
