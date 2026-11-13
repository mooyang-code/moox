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
	// Cursor 非空时，与这批行在同一次提交里记下跟随变更日志已扫描到的位置。
	Cursor *ChangeCursor
}

// ChangeCursor 是变更日志里的一个位置：序号只在同一个 StoreID 内可比较。
type ChangeCursor struct {
	StoreID  string
	Sequence uint64
}

// FollowState 是 View 索引跟随变更日志的进度，与索引一起落盘。
type FollowState struct {
	// Cursor 是已扫描到的位置；StoreID 为空表示索引没有记录过进度。
	Cursor ChangeCursor
	// BuiltSequence 是索引建成那一刻的日志头部序号（H_done）：不大于它的完成标记只推进游标、不发布就绪事件。
	BuiltSequence uint64
}

// FollowStateWriter 单独落盘跟随进度：建成时记下 H_start 与 H_done，定时任务落盘只扫描、没有写入的前进。
// 同一个 StoreID 下序号只增不减，较旧的进度不会盖掉较新的。
type FollowStateWriter interface {
	SaveFollowState(ctx context.Context, indexID string, state FollowState) error
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

// BuiltColumn 是索引文件里记下的一列已建列，只含列契约（schema 哈希）用到的字段。
// View 的已建状态只以索引文件为准，重启后由它还原写入契约。
type BuiltColumn struct {
	Name       string `json:"name"`
	OriginType int32  `json:"origin_type"`
	OriginID   string `json:"origin_id"`
	ValueType  int32  `json:"value_type"`
	SortOrder  uint32 `json:"sort_order"`
}

// EncodeBuiltColumns 取出要记进索引文件的列契约。
func EncodeBuiltColumns(columns []*pb.ViewColumn) []BuiltColumn {
	built := make([]BuiltColumn, 0, len(columns))
	for _, column := range columns {
		if column != nil {
			built = append(built, BuiltColumn{Name: column.GetColumnName(), OriginType: int32(column.GetOriginType()), OriginID: column.GetOriginId(), ValueType: int32(column.GetValueType()), SortOrder: column.GetSortOrder()})
		}
	}
	return built
}

// DecodeBuiltColumns 把索引文件里的列契约还原成 View 列。
func DecodeBuiltColumns(built []BuiltColumn) []*pb.ViewColumn {
	columns := make([]*pb.ViewColumn, 0, len(built))
	for _, column := range built {
		columns = append(columns, &pb.ViewColumn{ColumnName: column.Name, OriginType: pb.ColumnOriginType(column.OriginType), OriginId: column.OriginID, ValueType: pb.FieldValueType(column.ValueType), SortOrder: column.SortOrder})
	}
	return columns
}

type ViewIndexStats struct {
	Exists bool
	// DatasetID 与 Columns 是索引建成时记下的数据集与已建列（StatMetadata 与 Stat 都返回）。
	DatasetID     string
	Columns       []*pb.ViewColumn
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
	// RowsPerSeries 是索引建立时记录的每序列保留行数；0 表示未知（记录型 View 不按根数保留）。
	RowsPerSeries uint64
	// Follow 是落盘的跟随进度（StatMetadata 与 Stat 都返回）。
	Follow FollowState
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

// Engine is the single physical View index contract. The index file is the only
// record of a View's built state; an engine prepares, writes, queries, stats,
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

// SchemaExtender 把只追加列的新修订应用到已有的活动索引上：原地加列，已有行的新列为空。
type SchemaExtender interface {
	ExtendColumns(context.Context, string, ViewIndexSchema, ViewIndexSchema) error
}

// ErrSchemaExtensionConflict means the physical index cannot satisfy the requested append-only contract.
var ErrSchemaExtensionConflict = errors.New("view schema extension conflicts with physical index")

// ErrColumnNotProjected 表示查询请求的列不在 View 的已建列里；DataView 据此返回 VIEW_COLUMN_NOT_FOUND。
var ErrColumnNotProjected = errors.New("view column is not projected")

// ColumnNotProjectedError 带上缺的列名，错误文字保持“View column "x" is not projected”。
type ColumnNotProjectedError struct{ Column string }

func (e ColumnNotProjectedError) Error() string {
	return fmt.Sprintf("View column %q is not projected", e.Column)
}
func (e ColumnNotProjectedError) Is(target error) bool { return target == ErrColumnNotProjected }

// IsAppendOnlyViewColumns reports whether desired preserves every physical
// column in active and appends at least one new column after its sort order.
func IsAppendOnlyViewColumns(active, desired []*pb.ViewColumn) bool {
	if len(desired) <= len(active) {
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
			if len(active) > 0 && column.GetSortOrder() <= maxOrder {
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
// all rows. Startup and restore only need the built contract recorded in the
// index; the full Stat operation may execute COUNT/MIN and MAX over a large
// DuckDB file and must remain on the periodic View Maintainer path.
type MetadataStatReader interface {
	StatMetadata(context.Context, string) (ViewIndexStats, error)
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
// 按实际删除行数减少序列计数并更新索引的修改时间（查询按它判断数据是否在读取期间变化），返回删除的总行数。
// FinishOldestRowsDelete 同样在写锁内调用，一轮删完后执行：把覆盖起点前移到剩余行的最早时间，并把记录的保留根数改为 keep
// （没有记录或记录得更大时），让读取方知道每个序列最多只有这么多行。
type OldestRowsDeleter interface {
	PlanOldestRows(ctx context.Context, indexID string, keep uint64, maxSeries, maxRows int) ([]OldestRowsCut, error)
	DeleteOldestRows(ctx context.Context, indexID string, cuts []OldestRowsCut) (uint64, error)
	FinishOldestRowsDelete(ctx context.Context, indexID string, keep uint64) (string, error)
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
