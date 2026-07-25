package viewindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
	seen := make(map[string]struct{}, len(b.RowWrites))
	for i, w := range b.RowWrites {
		if err := w.Validate(); err != nil {
			return fmt.Errorf("row write %d: %w", i, err)
		}
		id := RowKeyID(w.Key.Key)
		if _, ok := seen[id]; ok {
			return fmt.Errorf("row write %d duplicates row key", i)
		}
		seen[id] = struct{}{}
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
}

type ViewIndexStats struct {
	Exists        bool
	ViewVersion   uint64
	EntryCount    int64
	MinVersion    string
	MaxVersion    string
	SchemaHash    string
	UpdatedAt     string
	FreeDiskBytes uint64
	IndexedFrom   string
	IndexedTo     string
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

type QuerySpec struct {
	Keys         []*pb.RowKey
	TimeRange    *pb.TimeRange
	VersionRange *pb.VersionRange
	TextQuery    string
	Groups       []FilterGroup
	GroupLogical pb.FilterLogical
	Sorts        []*pb.SortSpec
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
