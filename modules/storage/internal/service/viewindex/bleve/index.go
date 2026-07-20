package bleve

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	blevelib "github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/standard"
	"github.com/blevesearch/bleve/v2/mapping"
	blevequery "github.com/blevesearch/bleve/v2/search/query"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

type Options struct{ Path string }

type Index struct {
	root string
	mu   sync.Mutex
}

type indexMeta struct {
	ViewVersion uint64 `json:"view_version"`
	SchemaHash  string `json:"schema_hash"`
	UpdatedAt   string `json:"updated_at"`
}

func Open(opts Options) (*Index, error) {
	root := strings.TrimSpace(opts.Path)
	if root == "" {
		return nil, errors.New("bleve path is required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Index{root: root}, nil
}

func OpenExisting(opts Options) (*Index, error) { return Open(opts) }
func (i *Index) Engine() string                 { return "bleve" }

func (i *Index) Prepare(_ context.Context, id string, schema viewindex.ViewIndexSchema) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	path, err := i.path(id)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	index, err := blevelib.New(path, buildMapping())
	if err != nil {
		return err
	}
	if err := index.Close(); err != nil {
		return err
	}
	return i.writeMeta(id, indexMeta{ViewVersion: schema.ViewVersion, SchemaHash: schema.SchemaHash, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)})
}

func (i *Index) Apply(ctx context.Context, id string, batch viewindex.ViewIndexApplyBatch) error {
	if err := batch.Validate(); err != nil {
		return err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	meta, err := i.readMeta(id)
	if err != nil {
		return err
	}
	if meta.ViewVersion != batch.ViewRevision {
		return fmt.Errorf("view revision conflict: current=%d requested=%d", meta.ViewVersion, batch.ViewRevision)
	}
	if meta.SchemaHash != batch.ViewSchemaHash {
		return errors.New("view schema hash conflict")
	}
	index, err := i.openIndex(id)
	if err != nil {
		return err
	}
	defer index.Close()
	for _, write := range batch.RowWrites {
		if err := ctx.Err(); err != nil {
			return err
		}
		docID := viewindex.RowKeyID(write.Key.Key)
		row, err := readRow(index, docID)
		if err != nil {
			return err
		}
		row = viewindex.ApplyRowWrite(row, write, batch.WriteMode == viewindex.Backfill)
		if err := index.Index(docID, rowDocument(row)); err != nil {
			return err
		}
	}
	meta.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return i.writeMeta(id, meta)
}

func (i *Index) Query(ctx context.Context, id string, keys []*pb.RowKey, fields []string) ([]*pb.RowFieldValues, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	index, err := i.openIndex(id)
	if err != nil {
		return nil, err
	}
	defer index.Close()
	out := make([]*pb.RowFieldValues, 0, len(keys))
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row, err := readRow(index, viewindex.RowKeyID(key))
		if err != nil {
			return nil, err
		}
		if row != nil {
			out = append(out, project(row, fields))
		}
	}
	return out, nil
}

func (i *Index) Scan(ctx context.Context, id string, offset, limit int) ([]*pb.RowFieldValues, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	index, err := i.openIndex(id)
	if err != nil {
		return nil, err
	}
	defer index.Close()
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	req := blevelib.NewSearchRequestOptions(blevelib.NewMatchAllQuery(), limit, offset, false)
	req.Fields = []string{"row_proto"}
	req.SortBy([]string{"_id"})
	result, err := index.SearchInContext(ctx, req)
	if err != nil {
		return nil, err
	}
	out := make([]*pb.RowFieldValues, 0, len(result.Hits))
	for _, hit := range result.Hits {
		encoded, _ := hit.Fields["row_proto"].(string)
		row, err := decodeRow(encoded)
		if err != nil {
			return nil, err
		}
		if row != nil {
			out = append(out, row)
		}
	}
	return out, nil
}

func (i *Index) Search(ctx context.Context, id, text string, fields []string, size int) ([]*pb.RowFieldValues, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	index, err := i.openIndex(id)
	if err != nil {
		return nil, err
	}
	defer index.Close()
	var query blevequery.Query
	if strings.TrimSpace(text) == "" {
		query = blevelib.NewMatchAllQuery()
	} else {
		match := blevelib.NewMatchQuery(text)
		match.SetField("all_text")
		query = match
	}
	if size <= 0 {
		size = 100
	}
	req := blevelib.NewSearchRequestOptions(query, size, 0, false)
	req.Fields = []string{"row_proto"}
	result, err := index.SearchInContext(ctx, req)
	if err != nil {
		return nil, err
	}
	out := make([]*pb.RowFieldValues, 0, len(result.Hits))
	for _, hit := range result.Hits {
		encoded, _ := hit.Fields["row_proto"].(string)
		row, err := decodeRow(encoded)
		if err != nil {
			return nil, err
		}
		if row != nil {
			out = append(out, project(row, fields))
		}
	}
	return out, nil
}

func (i *Index) Write(ctx context.Context, id string, batch viewindex.BatchWrite) error {
	writes := make([]viewindex.RowWrite, 0, len(batch.RecordRows))
	for _, row := range batch.RecordRows {
		if row != nil {
			writes = append(writes, viewindex.RowWrite{Key: viewindex.RowKey{Key: recordKey(row.GetKey())}, Fields: row.GetFields()})
		}
	}
	if len(batch.TimeSeriesRows) != 0 {
		return errors.New("bleve view index rejects time-series rows")
	}
	return i.Apply(ctx, id, viewindex.ViewIndexApplyBatch{RowWrites: writes, ViewRevision: batch.ViewVersion, ViewSchemaHash: batch.SchemaHash, WriteMode: viewindex.LiveWrite})
}

func (i *Index) Stat(_ context.Context, id string) (viewindex.ViewIndexStats, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	meta, err := i.readMeta(id)
	if errors.Is(err, os.ErrNotExist) {
		return viewindex.ViewIndexStats{}, nil
	}
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	index, err := i.openIndex(id)
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	count, err := index.DocCount()
	closeErr := index.Close()
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	if closeErr != nil {
		return viewindex.ViewIndexStats{}, closeErr
	}
	stats := viewindex.ViewIndexStats{Exists: true, ViewVersion: meta.ViewVersion, SchemaHash: meta.SchemaHash, UpdatedAt: meta.UpdatedAt, EntryCount: int64(count)}
	index, err = i.openIndex(id)
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	req := blevelib.NewSearchRequestOptions(blevelib.NewMatchAllQuery(), int(count), 0, false)
	req.Fields = []string{"row_proto"}
	result, err := index.Search(req)
	closeErr = index.Close()
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	if closeErr != nil {
		return viewindex.ViewIndexStats{}, closeErr
	}
	for _, hit := range result.Hits {
		encoded, _ := hit.Fields["row_proto"].(string)
		row, err := decodeRow(encoded)
		if err != nil {
			return viewindex.ViewIndexStats{}, err
		}
		updateRange(&stats, row.GetKey())
	}
	return stats, nil
}

func (i *Index) Remove(_ context.Context, id string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	path, err := i.path(id)
	if err != nil {
		return err
	}
	return os.RemoveAll(path)
}

func (i *Index) List(context.Context) ([]string, error) {
	entries, err := os.ReadDir(i.root)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, entry := range entries {
		if entry.IsDir() {
			if _, err := os.Stat(filepath.Join(i.root, entry.Name(), "meta.json")); err == nil {
				ids = append(ids, entry.Name())
			}
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (i *Index) Close() error { return nil }

func (i *Index) path(id string) (string, error) {
	if id == "" || filepath.Base(id) != id {
		return "", errors.New("invalid view index id")
	}
	return filepath.Join(i.root, id), nil
}

func (i *Index) openIndex(id string) (blevelib.Index, error) {
	path, err := i.path(id)
	if err != nil {
		return nil, err
	}
	return blevelib.Open(path)
}

func (i *Index) readMeta(id string) (indexMeta, error) {
	path, err := i.path(id)
	if err != nil {
		return indexMeta{}, err
	}
	raw, err := os.ReadFile(filepath.Join(path, "meta.json"))
	if err != nil {
		return indexMeta{}, err
	}
	var meta indexMeta
	err = json.Unmarshal(raw, &meta)
	return meta, err
}

func (i *Index) writeMeta(id string, meta indexMeta) error {
	path, err := i.path(id)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(path, "meta.json"), raw, 0o644)
}

func buildMapping() mapping.IndexMapping {
	indexMapping := blevelib.NewIndexMapping()
	doc := blevelib.NewDocumentMapping()
	rowProto := blevelib.NewTextFieldMapping()
	rowProto.Store = true
	rowProto.Index = false
	doc.AddFieldMappingsAt("row_proto", rowProto)
	allText := blevelib.NewTextFieldMapping()
	allText.Analyzer = standard.Name
	allText.Store = false
	doc.AddFieldMappingsAt("all_text", allText)
	indexMapping.DefaultMapping = doc
	return indexMapping
}

func rowDocument(row *pb.RowFieldValues) map[string]any {
	raw, _ := proto.Marshal(row)
	parts := make([]string, 0, len(row.GetFields())+len(row.GetAttributes()))
	doc := map[string]any{"row_proto": base64.RawStdEncoding.EncodeToString(raw)}
	for _, field := range row.GetFields() {
		if field == nil || field.GetValue() == nil {
			continue
		}
		text := typedValueText(field.GetValue())
		doc[field.GetFieldId()] = text
		parts = append(parts, text)
	}
	for key, value := range row.GetAttributes() {
		text := typedValueText(value)
		doc[key] = text
		parts = append(parts, text)
	}
	doc["all_text"] = strings.Join(parts, " ")
	return doc
}

func readRow(index blevelib.Index, docID string) (*pb.RowFieldValues, error) {
	query := blevelib.NewDocIDQuery([]string{docID})
	req := blevelib.NewSearchRequestOptions(query, 1, 0, false)
	req.Fields = []string{"row_proto"}
	result, err := index.Search(req)
	if err != nil || len(result.Hits) == 0 {
		return nil, err
	}
	encoded, _ := result.Hits[0].Fields["row_proto"].(string)
	return decodeRow(encoded)
}

func decodeRow(encoded string) (*pb.RowFieldValues, error) {
	if encoded == "" {
		return nil, nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	row := &pb.RowFieldValues{}
	if err := proto.Unmarshal(raw, row); err != nil {
		return nil, err
	}
	return row, nil
}

func project(row *pb.RowFieldValues, fields []string) *pb.RowFieldValues {
	if len(fields) == 0 {
		return proto.Clone(row).(*pb.RowFieldValues)
	}
	want := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		want[field] = struct{}{}
	}
	out := &pb.RowFieldValues{Key: proto.Clone(row.GetKey()).(*pb.RowKey)}
	for _, field := range row.GetFields() {
		if _, ok := want[field.GetFieldId()]; ok {
			out.Fields = append(out.Fields, proto.Clone(field).(*pb.FieldValue))
		}
	}
	for key, value := range row.GetAttributes() {
		if _, ok := want[key]; ok {
			if out.Attributes == nil {
				out.Attributes = make(map[string]*pb.TypedValue)
			}
			out.Attributes[key] = proto.Clone(value).(*pb.TypedValue)
		}
	}
	return out
}

func typedValueText(value *pb.TypedValue) string {
	if value == nil {
		return ""
	}
	switch v := value.GetValue().(type) {
	case *pb.TypedValue_StringValue:
		return v.StringValue
	case *pb.TypedValue_IntValue:
		return fmt.Sprintf("%d", v.IntValue)
	case *pb.TypedValue_DoubleValue:
		return fmt.Sprintf("%g", v.DoubleValue)
	case *pb.TypedValue_BoolValue:
		return fmt.Sprintf("%t", v.BoolValue)
	case *pb.TypedValue_TimeValue:
		return v.TimeValue
	case *pb.TypedValue_JsonValue:
		return v.JsonValue
	case *pb.TypedValue_BytesValue:
		return base64.RawStdEncoding.EncodeToString(v.BytesValue)
	default:
		return ""
	}
}

func recordKey(key *pb.RecordKey) *pb.RowKey {
	if key == nil {
		return nil
	}
	return &pb.RowKey{SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(), Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: key.GetRecordId(), Version: key.GetVersion()}}}
}

func updateRange(stats *viewindex.ViewIndexStats, key *pb.RowKey) {
	if stats == nil || key == nil {
		return
	}
	value := ""
	if row := key.GetTimeSeries(); row != nil {
		value = row.GetDataTime()
	} else if row := key.GetRecord(); row != nil {
		value = row.GetVersion()
	}
	if value == "" {
		return
	}
	if stats.IndexedFrom == "" || value < stats.IndexedFrom {
		stats.IndexedFrom = value
	}
	if stats.IndexedTo == "" || value > stats.IndexedTo {
		stats.IndexedTo = value
	}
}

var _ viewindex.QueryEngine = (*Index)(nil)
