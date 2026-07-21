package viewindex

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
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
	Replace
)

func (m WriteMode) String() string {
	if m == Backfill {
		return "BACKFILL"
	}
	if m == Replace {
		return "REPLACE"
	}
	return "LIVE_WRITE"
}

type RowKey struct{ Key *pb.RowKey }

type RowWrite struct {
	Key        RowKey
	Fields     []*pb.FieldValue
	Attributes map[string]*pb.TypedValue
}

type ViewIndexApplyBatch struct {
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
	return nil
}

func (b ViewIndexApplyBatch) Validate() error {
	if len(b.RowWrites) == 0 {
		return errors.New("view index apply batch is empty")
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

type BatchWrite struct {
	TimeSeriesRows []*pb.TimeSeriesRow
	RecordRows     []*pb.RecordRow
	Columns        []*pb.ViewColumn
	ViewVersion    uint64
	SchemaHash     string
}

type ViewIndexStats struct {
	Exists        bool
	ViewVersion   uint64
	EntryCount    int64
	MinVersion    string
	MaxVersion    string
	SchemaHash    string
	PhysicalBytes uint64
	UpdatedAt     string
	FreeDiskBytes uint64
	IndexedFrom   string
	IndexedTo     string
}

type ViewIndexEngine interface {
	Engine() string
	Prepare(context.Context, string, ViewIndexSchema) error
	Stat(context.Context, string) (ViewIndexStats, error)
	Remove(context.Context, string) error
}

type ViewIndexApplier interface {
	Apply(context.Context, string, ViewIndexApplyBatch) error
}

type ManagedEngine interface {
	ViewIndexEngine
	ViewIndexApplier
	List(context.Context) ([]string, error)
}

type QueryEngine interface {
	ManagedEngine
	Query(context.Context, string, []*pb.RowKey, []string) ([]*pb.RowFieldValues, error)
	Scan(context.Context, string, int, int) ([]*pb.RowFieldValues, error)
	Delete(context.Context, string, []*pb.RowKey) error
}

// MemoryEngine is a small engine core shared by DuckDB and Bleve owners. The
// owner packages provide the physical path; this core enforces A/B and merge
// semantics without progress tables or source checkpoints.
type MemoryEngine struct {
	name    string
	root    string
	mu      sync.RWMutex
	indexes map[string]*memoryIndex
}

type memoryIndex struct {
	schema ViewIndexSchema
	rows   map[string]*pb.RowFieldValues
}

type persistedIndex struct {
	Schema ViewIndexSchema   `json:"schema"`
	Rows   map[string]string `json:"rows"`
}

func NewMemoryEngine(name, root string) *MemoryEngine {
	e := &MemoryEngine{name: name, root: root, indexes: make(map[string]*memoryIndex)}
	e.load()
	return e
}
func (e *MemoryEngine) Engine() string { return e.name }
func (e *MemoryEngine) Prepare(_ context.Context, id string, schema ViewIndexSchema) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("index_id is required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.indexes[id] = &memoryIndex{schema: schema, rows: make(map[string]*pb.RowFieldValues)}
	if e.root != "" {
		path := filepath.Join(e.root, id)
		if e.name == "duckdb" {
			path += ".duckdb"
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644); err != nil {
				return err
			} else {
				_ = file.Close()
			}
		} else if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
	}
	return e.persistLocked(id)
}
func (e *MemoryEngine) Apply(_ context.Context, id string, batch ViewIndexApplyBatch) error {
	if err := batch.Validate(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	idx := e.indexes[id]
	if idx == nil {
		return fmt.Errorf("index %q is not prepared", id)
	}
	if idx.schema.ViewVersion != 0 && idx.schema.ViewVersion != batch.ViewRevision {
		return fmt.Errorf("view revision conflict: current=%d requested=%d", idx.schema.ViewVersion, batch.ViewRevision)
	}
	if idx.schema.SchemaHash != "" && idx.schema.SchemaHash != batch.ViewSchemaHash {
		return fmt.Errorf("view schema hash conflict")
	}
	for _, w := range batch.RowWrites {
		key := RowKeyID(w.Key.Key)
		row := idx.rows[key]
		if row == nil {
			row = &pb.RowFieldValues{Key: w.Key.Key}
			idx.rows[key] = row
		}
		row = ApplyRowWriteWithMode(row, w, batch.WriteMode == Backfill, batch.WriteMode == Replace)
		idx.rows[key] = row
	}
	idx.schema.ViewVersion = batch.ViewRevision
	idx.schema.SchemaHash = batch.ViewSchemaHash
	return e.persistLocked(id)
}
func (e *MemoryEngine) Stat(_ context.Context, id string) (ViewIndexStats, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	idx := e.indexes[id]
	if idx == nil {
		return ViewIndexStats{}, nil
	}
	return ViewIndexStats{Exists: true, ViewVersion: idx.schema.ViewVersion, EntryCount: int64(len(idx.rows)), SchemaHash: idx.schema.SchemaHash}, nil
}
func (e *MemoryEngine) Remove(_ context.Context, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.indexes, id)
	if e.root != "" {
		path := filepath.Join(e.root, id)
		if e.name == "duckdb" {
			path += ".duckdb"
			_ = os.Remove(path)
		} else {
			_ = os.RemoveAll(path)
		}
		_ = os.Remove(e.statePath(id))
	}
	return nil
}

func (e *MemoryEngine) statePath(id string) string {
	return filepath.Join(e.root, id+".state.json")
}

// persistLocked stores a compact protobuf snapshot beside the physical index.
// The engine remains deliberately small, but a View restart must not turn a
// successful build into an empty index. The snapshot is replaced atomically;
// DuckDB/Bleve owners can later swap this file-backed core for their native
// storage without changing the RPC contract.
func (e *MemoryEngine) persistLocked(id string) error {
	if e.root == "" {
		return nil
	}
	idx := e.indexes[id]
	if idx == nil {
		return nil
	}
	rows := make(map[string]string, len(idx.rows))
	for key, row := range idx.rows {
		data, err := proto.Marshal(row)
		if err != nil {
			return err
		}
		rows[key] = base64.RawStdEncoding.EncodeToString(data)
	}
	data, err := json.Marshal(persistedIndex{Schema: idx.schema, Rows: rows})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(e.root, 0o755); err != nil {
		return err
	}
	tmp := e.statePath(id) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, e.statePath(id))
}

func (e *MemoryEngine) load() {
	if e.root == "" {
		return
	}
	entries, err := os.ReadDir(e.root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".state.json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(e.root, entry.Name()))
		if err != nil {
			continue
		}
		var saved persistedIndex
		if json.Unmarshal(data, &saved) != nil {
			continue
		}
		rows := make(map[string]*pb.RowFieldValues, len(saved.Rows))
		for key, encoded := range saved.Rows {
			rowData, err := base64.RawStdEncoding.DecodeString(encoded)
			if err != nil {
				continue
			}
			row := &pb.RowFieldValues{}
			if proto.Unmarshal(rowData, row) == nil {
				rows[key] = row
			}
		}
		id := strings.TrimSuffix(entry.Name(), ".state.json")
		e.indexes[id] = &memoryIndex{schema: saved.Schema, rows: rows}
	}
}
func (e *MemoryEngine) List(_ context.Context) ([]string, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]string, 0, len(e.indexes))
	for id := range e.indexes {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}
func (e *MemoryEngine) Query(_ context.Context, id string, keys []*pb.RowKey, fields []string) ([]*pb.RowFieldValues, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	idx := e.indexes[id]
	if idx == nil {
		return nil, fmt.Errorf("index %q is not prepared", id)
	}
	out := make([]*pb.RowFieldValues, 0, len(keys))
	for _, k := range keys {
		if row := idx.rows[RowKeyID(k)]; row != nil {
			out = append(out, projectRow(row, fields))
		}
	}
	return out, nil
}

func (e *MemoryEngine) Delete(_ context.Context, id string, keys []*pb.RowKey) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	idx, ok := e.indexes[id]
	if !ok {
		return os.ErrNotExist
	}
	for _, key := range keys {
		if key != nil {
			delete(idx.rows, RowKeyID(key))
		}
	}
	return e.persistLocked(id)
}

func (e *MemoryEngine) Scan(_ context.Context, id string, offset, limit int) ([]*pb.RowFieldValues, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	idx := e.indexes[id]
	if idx == nil {
		return nil, fmt.Errorf("index %q is not prepared", id)
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	ids := make([]string, 0, len(idx.rows))
	for rowID := range idx.rows {
		ids = append(ids, rowID)
	}
	sort.Strings(ids)
	if offset >= len(ids) {
		return nil, nil
	}
	end := offset + limit
	if end > len(ids) {
		end = len(ids)
	}
	rows := make([]*pb.RowFieldValues, 0, end-offset)
	for _, rowID := range ids[offset:end] {
		rows = append(rows, proto.Clone(idx.rows[rowID]).(*pb.RowFieldValues))
	}
	return rows, nil
}
func (e *MemoryEngine) Write(ctx context.Context, id string, batch BatchWrite) error {
	writes := make([]RowWrite, 0, len(batch.TimeSeriesRows)+len(batch.RecordRows))
	for _, r := range batch.TimeSeriesRows {
		if r != nil {
			writes = append(writes, RowWrite{Key: RowKey{Key: queryTSKey(r.GetKey())}, Fields: r.GetFields()})
		}
	}
	for _, r := range batch.RecordRows {
		if r != nil {
			writes = append(writes, RowWrite{Key: RowKey{Key: queryRecordKey(r.GetKey())}, Fields: r.GetFields()})
		}
	}
	return e.Apply(ctx, id, ViewIndexApplyBatch{RowWrites: writes, ViewRevision: batch.ViewVersion, ViewSchemaHash: batch.SchemaHash, WriteMode: LiveWrite})
}

func queryTSKey(k *pb.TimeSeriesKey) *pb.RowKey {
	if k == nil {
		return nil
	}
	return &pb.RowKey{SpaceId: k.GetSpaceId(), DatasetId: k.GetDatasetId(), Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: k.GetSubjectId(), Freq: k.GetFreq(), DataTime: k.GetDataTime()}}}
}
func queryRecordKey(k *pb.RecordKey) *pb.RowKey {
	if k == nil {
		return nil
	}
	return &pb.RowKey{SpaceId: k.GetSpaceId(), DatasetId: k.GetDatasetId(), Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: k.GetRecordId(), Version: k.GetVersion()}}}
}
func RowKeyID(k *pb.RowKey) string {
	raw, _ := protojson.Marshal(k)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func ApplyRowWrite(row *pb.RowFieldValues, write RowWrite, onlyMissing bool) *pb.RowFieldValues {
	return ApplyRowWriteWithMode(row, write, onlyMissing, false)
}

func ApplyRowWriteWithMode(row *pb.RowFieldValues, write RowWrite, onlyMissing, replace bool) *pb.RowFieldValues {
	if replace {
		fields := make([]*pb.FieldValue, 0, len(write.Fields))
		for _, field := range write.Fields {
			if field != nil {
				fields = append(fields, proto.Clone(field).(*pb.FieldValue))
			}
		}
		return &pb.RowFieldValues{Key: proto.Clone(write.Key.Key).(*pb.RowKey), Fields: fields, Attributes: cloneAttributes(write.Attributes)}
	}
	if row == nil {
		row = &pb.RowFieldValues{Key: proto.Clone(write.Key.Key).(*pb.RowKey)}
	} else {
		row = proto.Clone(row).(*pb.RowFieldValues)
	}
	mergeFields(row, write.Fields, write.Attributes, onlyMissing)
	return row
}

func cloneAttributes(attrs map[string]*pb.TypedValue) map[string]*pb.TypedValue {
	if len(attrs) == 0 {
		return nil
	}
	out := make(map[string]*pb.TypedValue, len(attrs))
	for key, value := range attrs {
		out[key] = proto.Clone(value).(*pb.TypedValue)
	}
	return out
}
func mergeFields(row *pb.RowFieldValues, fields []*pb.FieldValue, attrs map[string]*pb.TypedValue, onlyMissing bool) {
	pos := map[string]int{}
	for i, f := range row.Fields {
		if f != nil {
			pos[f.GetFieldId()] = i
		}
	}
	for _, f := range fields {
		if f == nil {
			continue
		}
		if i, ok := pos[f.GetFieldId()]; ok {
			if !onlyMissing {
				row.Fields[i] = f
			}
		} else {
			row.Fields = append(row.Fields, f)
			pos[f.GetFieldId()] = len(row.Fields) - 1
		}
	}
	if row.Attributes == nil {
		row.Attributes = map[string]*pb.TypedValue{}
	}
	for k, v := range attrs {
		if _, ok := row.Attributes[k]; !ok || !onlyMissing {
			row.Attributes[k] = v
		}
	}
}
func projectRow(row *pb.RowFieldValues, fields []string) *pb.RowFieldValues {
	if len(fields) == 0 {
		return proto.Clone(row).(*pb.RowFieldValues)
	}
	want := map[string]struct{}{}
	for _, f := range fields {
		want[f] = struct{}{}
	}
	out := &pb.RowFieldValues{Key: proto.Clone(row.GetKey()).(*pb.RowKey), Attributes: map[string]*pb.TypedValue{}}
	for _, f := range row.Fields {
		if f != nil {
			if _, ok := want[f.GetFieldId()]; ok {
				out.Fields = append(out.Fields, proto.Clone(f).(*pb.FieldValue))
			}
		}
	}
	for k, v := range row.Attributes {
		if _, ok := want[k]; ok {
			out.Attributes[k] = proto.Clone(v).(*pb.TypedValue)
		}
	}
	return out
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
