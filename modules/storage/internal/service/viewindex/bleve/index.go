package bleve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	blevelib "github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/standard"
	"github.com/blevesearch/bleve/v2/mapping"
	blevesearch "github.com/blevesearch/bleve/v2/search"
	blevequery "github.com/blevesearch/bleve/v2/search/query"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type Options struct{ Path string }

type Index struct {
	root   string
	mu     sync.Mutex
	openMu sync.Mutex
	open   map[string]*handle
}

type handle struct {
	mu      sync.Mutex
	index   blevelib.Index
	columns map[string]pb.FieldValueType
	meta    indexMeta
}

type indexMeta struct {
	SpaceID          string                       `json:"space_id"`
	ViewVersion      uint64                       `json:"view_version"`
	SchemaHash       string                       `json:"schema_hash"`
	PrimaryDatasetID string                       `json:"primary_dataset_id"`
	UpdatedAt        string                       `json:"updated_at"`
	Columns          map[string]pb.FieldValueType `json:"columns"`
}

func Open(opts Options) (*Index, error) {
	root := strings.TrimSpace(opts.Path)
	if root == "" {
		return nil, errors.New("bleve path is required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Index{root: root, open: make(map[string]*handle)}, nil
}

func (i *Index) Engine() string { return "bleve" }

func (i *Index) Prepare(_ context.Context, id string, schema viewindex.ViewIndexSchema) error {
	if err := i.close(id); err != nil {
		return err
	}
	path, err := i.path(id)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	meta := indexMeta{SpaceID: schema.SpaceID, ViewVersion: schema.ViewVersion, SchemaHash: schema.SchemaHash, PrimaryDatasetID: schema.PrimaryDatasetID, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Columns: schemaColumns(schema.Columns)}
	index, err := blevelib.New(path, buildMapping(meta.Columns))
	if err != nil {
		return err
	}
	if err := index.Close(); err != nil {
		return err
	}
	return i.writeMeta(id, meta)
}

func (i *Index) Write(ctx context.Context, id string, batch viewindex.ViewIndexWriteBatch) error {
	if err := batch.Validate(); err != nil {
		return err
	}
	h, err := i.get(id)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.meta.ViewVersion != batch.ViewRevision {
		return fmt.Errorf("view revision conflict: current=%d requested=%d", h.meta.ViewVersion, batch.ViewRevision)
	}
	if h.meta.SchemaHash != batch.ViewSchemaHash {
		return errors.New("view schema hash conflict")
	}
	writeBatch := h.index.NewBatch()
	pending := make(map[string]map[string]any, len(batch.RowWrites))
	for _, write := range batch.RowWrites {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Record documents are upserts too. Read-modify-write preserves fields
		// omitted by a partial LiveWrite, and Backfill never overwrites a value
		// that arrived through the live stream first.
		rowID := viewindex.RowKeyID(write.Key.Key)
		base, ok := pending[rowID]
		if !ok {
			base, err = existingDocument(ctx, h.index, rowID, h.meta)
			if err != nil {
				return err
			}
		}
		row, err := documentRowFromBase(ctx, write, h.meta, base, batch.WriteMode)
		if err != nil {
			return err
		}
		pending[rowID] = row
		if err := writeBatch.Index(rowID, row); err != nil {
			return err
		}
	}
	if err := h.index.Batch(writeBatch); err != nil {
		return err
	}
	h.meta.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return i.writeMeta(id, h.meta)
}

func (i *Index) Query(ctx context.Context, id string, spec viewindex.QuerySpec) ([]*pb.RowFieldValues, int64, error) {
	h, err := i.get(id)
	if err != nil {
		return nil, 0, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	query, err := buildQuery(spec, h.meta)
	if err != nil {
		return nil, 0, err
	}
	limit := spec.Limit
	if limit <= 0 {
		limit = 1000
	}
	offset := spec.Offset
	if offset < 0 {
		offset = 0
	}
	req := blevelib.NewSearchRequestOptions(query, limit, offset, false)
	fields := []string{"record_id", "version"}
	for name := range h.columns {
		if len(spec.Includes) == 0 || contains(spec.Includes, name) {
			fields = append(fields, name)
		}
	}
	if len(fields) > 0 {
		req.Fields = fields
	}
	if len(spec.Sorts) != 0 {
		sorts := make([]string, 0, len(spec.Sorts))
		for _, sortSpec := range spec.Sorts {
			if sortSpec == nil || !containsKey(h.columns, sortSpec.GetFieldName()) && sortSpec.GetFieldName() != "record_id" && sortSpec.GetFieldName() != "version" {
				return nil, 0, fmt.Errorf("unknown sort column %q", sortSpec.GetFieldName())
			}
			name := sortSpec.GetFieldName()
			if sortSpec.GetDesc() {
				name = "-" + name
			}
			sorts = append(sorts, name)
		}
		req.SortBy(sorts)
	}
	result, err := h.index.SearchInContext(ctx, req)
	if err != nil {
		return nil, 0, err
	}
	rows := make([]*pb.RowFieldValues, 0, len(result.Hits))
	for _, hit := range result.Hits {
		row, err := storedRow(hit, h.meta)
		if err != nil {
			return nil, 0, err
		}
		rows = append(rows, row)
	}
	total := int64(-1)
	if spec.TotalMode != pb.TotalMode_NONE {
		total = int64(result.Total)
	}
	return rows, total, nil
}

func (i *Index) Stat(ctx context.Context, id string) (viewindex.ViewIndexStats, error) {
	path, err := i.path(id)
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return viewindex.ViewIndexStats{}, nil
	}
	h, err := i.get(id)
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	count, err := h.index.DocCount()
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	stats := viewindex.ViewIndexStats{Exists: true, ViewVersion: h.meta.ViewVersion, SchemaHash: h.meta.SchemaHash, UpdatedAt: h.meta.UpdatedAt, EntryCount: int64(count)}
	return stats, nil
}

// StatMetadata avoids a full DocCount during startup restore. The count is
// still collected by Stat during periodic reconciliation.
func (i *Index) StatMetadata(ctx context.Context, id string) (viewindex.ViewIndexStats, error) {
	path, err := i.path(id)
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return viewindex.ViewIndexStats{}, nil
	} else if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	h, err := i.get(id)
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return viewindex.ViewIndexStats{Exists: true, ViewVersion: h.meta.ViewVersion, SchemaHash: h.meta.SchemaHash, UpdatedAt: h.meta.UpdatedAt}, nil
}

func (i *Index) Exists(_ context.Context, id string) (bool, error) {
	path, err := i.path(id)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func (i *Index) Remove(_ context.Context, id string) error {
	if err := i.close(id); err != nil {
		return err
	}
	path, err := i.path(id)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	return nil
}

func (i *Index) Close() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	var result error
	for id, h := range i.open {
		result = errors.Join(result, h.index.Close())
		delete(i.open, id)
	}
	return result
}

func (i *Index) close(id string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if h := i.open[id]; h != nil {
		if err := h.index.Close(); err != nil {
			return err
		}
		delete(i.open, id)
	}
	return nil
}

func (i *Index) get(id string) (*handle, error) {
	path, err := i.path(id)
	if err != nil {
		return nil, err
	}
	i.mu.Lock()
	if h := i.open[id]; h != nil {
		i.mu.Unlock()
		return h, nil
	}
	i.mu.Unlock()
	// Bleve's bbolt backend permits only one process-level opener for an index.
	// Serialize cold opens while keeping the fast cached path lock-free for the
	// expensive backend operation.
	i.openMu.Lock()
	defer i.openMu.Unlock()
	i.mu.Lock()
	if h := i.open[id]; h != nil {
		i.mu.Unlock()
		return h, nil
	}
	i.mu.Unlock()
	meta, err := i.readMeta(id)
	if err != nil {
		return nil, err
	}
	index, err := blevelib.Open(path)
	if err != nil {
		return nil, err
	}
	h := &handle{index: index, meta: meta, columns: meta.Columns}
	i.mu.Lock()
	if existing := i.open[id]; existing != nil {
		_ = index.Close()
		i.mu.Unlock()
		return existing, nil
	}
	i.open[id] = h
	i.mu.Unlock()
	return h, nil
}

func (i *Index) path(id string) (string, error) {
	if id == "" || filepath.Base(id) != id {
		return "", errors.New("invalid view index id")
	}
	return filepath.Join(i.root, id), nil
}

func (i *Index) ListManagedIndexes(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(i.root)
	if err != nil {
		return nil, fmt.Errorf("list Bleve view indexes: %w", err)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() || strings.Contains(entry.Name(), ".prepare-") {
			continue
		}
		if _, err := viewindex.ParseViewIndexID(entry.Name()); err != nil {
			continue
		}
		ids = append(ids, entry.Name())
	}
	sort.Strings(ids)
	return ids, nil
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
	tmp := filepath.Join(path, "meta.json.tmp")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(path, "meta.json"))
}

func buildMapping(columns map[string]pb.FieldValueType) mapping.IndexMapping {
	indexMapping := blevelib.NewIndexMapping()
	doc := blevelib.NewDocumentMapping()
	for name, valueType := range columns {
		switch valueType {
		case pb.FieldValueType_FIELD_VALUE_TYPE_INT, pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
			field := blevelib.NewNumericFieldMapping()
			field.Store = true
			field.Name = name
			doc.AddFieldMapping(field)
		case pb.FieldValueType_FIELD_VALUE_TYPE_BOOL:
			field := blevelib.NewBooleanFieldMapping()
			field.Store = true
			field.Name = name
			doc.AddFieldMapping(field)
		case pb.FieldValueType_FIELD_VALUE_TYPE_TIME:
			field := blevelib.NewDateTimeFieldMapping()
			field.Store = true
			field.Name = name
			doc.AddFieldMapping(field)
		case pb.FieldValueType_FIELD_VALUE_TYPE_STRING:
			// Keyword for exact/sort; text for analyzed match on the same column.
			keyword := blevelib.NewKeywordFieldMapping()
			keyword.Store = true
			keyword.Name = name
			doc.AddFieldMapping(keyword)
			text := blevelib.NewTextFieldMapping()
			text.Analyzer = standard.Name
			text.Store = false
			text.Name = name
			doc.AddFieldMapping(text)
		default:
			field := blevelib.NewKeywordFieldMapping()
			field.Store = true
			field.Name = name
			doc.AddFieldMapping(field)
		}
	}
	for _, name := range []string{"record_id", "version"} {
		field := blevelib.NewKeywordFieldMapping()
		field.Store = true
		field.Name = name
		doc.AddFieldMapping(field)
	}
	allText := blevelib.NewTextFieldMapping()
	allText.Analyzer = standard.Name
	allText.Name = "all_text"
	allText.Store = false
	doc.AddFieldMapping(allText)
	indexMapping.DefaultMapping = doc
	return indexMapping
}

func schemaColumns(columns []*pb.ViewColumn) map[string]pb.FieldValueType {
	out := make(map[string]pb.FieldValueType, len(columns))
	for _, column := range columns {
		if column != nil && column.GetColumnName() != "" {
			out[column.GetColumnName()] = column.GetValueType()
		}
	}
	return out
}

func documentRow(ctx context.Context, write viewindex.RowWrite, meta indexMeta, index blevelib.Index, mode viewindex.WriteMode) (map[string]any, error) {
	key := write.Key.Key.GetRecord()
	if key == nil {
		return nil, errors.New("bleve only accepts record row keys")
	}
	doc, err := existingDocument(ctx, index, viewindex.RowKeyID(write.Key.Key), meta)
	if err != nil {
		return nil, err
	}
	return documentRowFromBase(ctx, write, meta, doc, mode)
}

func documentRowFromBase(ctx context.Context, write viewindex.RowWrite, meta indexMeta, doc map[string]any, mode viewindex.WriteMode) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := write.Key.Key.GetRecord()
	if key == nil {
		return nil, errors.New("bleve only accepts record row keys")
	}
	if doc != nil {
		copyDoc := make(map[string]any, len(doc)+len(meta.Columns)*2)
		for name, value := range doc {
			copyDoc[name] = value
		}
		doc = copyDoc
	}
	if doc == nil {
		doc = map[string]any{"record_id": key.GetRecordId(), "version": key.GetVersion()}
	}
	for _, field := range write.Fields {
		if field == nil {
			continue
		}
		value, err := typedValueToNative(field.GetValue())
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", field.GetFieldId(), err)
		}
		if _, ok := meta.Columns[field.GetFieldId()]; !ok {
			return nil, fmt.Errorf("unknown view column %q", field.GetFieldId())
		}
		if mode == viewindex.Backfill {
			if _, exists := doc[field.GetFieldId()]; exists {
				continue
			}
		}
		if value == nil {
			delete(doc, field.GetFieldId())
			doc[nullMarker(field.GetFieldId())] = "true"
		} else {
			delete(doc, nullMarker(field.GetFieldId()))
			doc[field.GetFieldId()] = value
		}
	}
	for name, typed := range write.Attributes {
		if _, ok := meta.Columns[name]; !ok {
			return nil, fmt.Errorf("unknown view column %q", name)
		}
		if mode == viewindex.Backfill {
			if _, exists := doc[name]; exists {
				continue
			}
		}
		value, err := typedValueToNative(typed)
		if err != nil {
			return nil, err
		}
		if value == nil {
			delete(doc, name)
			doc[nullMarker(name)] = "true"
		} else {
			delete(doc, nullMarker(name))
			doc[name] = value
		}
	}
	for name := range meta.Columns {
		if _, ok := doc[name]; !ok {
			doc[nullMarker(name)] = "true"
		}
	}
	text := make([]string, 0, len(meta.Columns)+2)
	text = append(text, key.GetRecordId(), key.GetVersion())
	for name := range meta.Columns {
		if value, ok := doc[name]; ok {
			text = append(text, fmt.Sprint(value))
		}
	}
	doc["all_text"] = strings.Join(text, " ")
	return doc, nil
}

func existingDocument(ctx context.Context, index blevelib.Index, id string, meta indexMeta) (map[string]any, error) {
	request := blevelib.NewSearchRequestOptions(blevequery.NewDocIDQuery([]string{id}), 1, 0, false)
	fields := make([]string, 0, len(meta.Columns)+2)
	fields = append(fields, "record_id", "version")
	for name := range meta.Columns {
		fields = append(fields, name)
	}
	sort.Strings(fields)
	request.Fields = fields
	result, err := index.SearchInContext(ctx, request)
	if err != nil {
		return nil, err
	}
	if len(result.Hits) == 0 {
		return nil, nil
	}
	doc := make(map[string]any, len(result.Hits[0].Fields))
	for name, value := range result.Hits[0].Fields {
		doc[name] = value
	}
	return doc, nil
}

func storedRow(hit *blevesearch.DocumentMatch, meta indexMeta) (*pb.RowFieldValues, error) {
	recordID := storedString(hit.Fields["record_id"])
	version := storedString(hit.Fields["version"])
	row := &pb.RowFieldValues{Key: &pb.RowKey{SpaceId: meta.SpaceID, DatasetId: meta.PrimaryDatasetID, Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: recordID, Version: version}}}}
	for name, valueType := range meta.Columns {
		value, ok := hit.Fields[name]
		if !ok || value == nil {
			continue
		}
		typed, err := nativeToTypedValue(value, valueType)
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", name, err)
		}
		row.Fields = append(row.Fields, &pb.FieldValue{FieldId: name, Value: typed})
	}
	sort.Slice(row.Fields, func(a, b int) bool { return row.Fields[a].GetFieldId() < row.Fields[b].GetFieldId() })
	return row, nil
}

func buildQuery(spec viewindex.QuerySpec, meta indexMeta) (blevequery.Query, error) {
	parts := make([]blevequery.Query, 0)
	if len(spec.Keys) != 0 {
		keyQueries := make([]blevequery.Query, 0, len(spec.Keys))
		for _, key := range spec.Keys {
			if key == nil || key.GetRecord() == nil {
				return nil, errors.New("bleve query key must be record")
			}
			record := key.GetRecord()
			if record.GetVersion() == "" {
				q := blevequery.NewTermQuery(record.GetRecordId())
				q.SetField("record_id")
				keyQueries = append(keyQueries, q)
				continue
			}
			keyQueries = append(keyQueries, blevequery.NewDocIDQuery([]string{viewindex.RowKeyID(key)}))
		}
		if len(keyQueries) == 1 {
			parts = append(parts, keyQueries[0])
		} else {
			parts = append(parts, blevequery.NewDisjunctionQuery(keyQueries))
		}
	}
	if strings.TrimSpace(spec.TextQuery) != "" {
		q := blevelib.NewMatchQuery(spec.TextQuery)
		q.SetField("all_text")
		parts = append(parts, q)
	}
	if spec.VersionRange != nil && (spec.VersionRange.GetStartVersion() != "" || spec.VersionRange.GetEndVersion() != "") {
		q := blevequery.NewTermRangeInclusiveQuery(nilIfEmpty(spec.VersionRange.GetStartVersion()), nilIfEmpty(spec.VersionRange.GetEndVersion()), boolPtr(true), boolPtr(true))
		q.SetField("version")
		parts = append(parts, q)
	}
	if after := spec.AfterKey; after != nil {
		key := after.GetRecord()
		if key == nil {
			return nil, errors.New("bleve cursor key must be record")
		}
		idAfter := blevequery.NewTermRangeInclusiveQuery(key.GetRecordId(), "", boolPtr(false), boolPtr(true))
		idAfter.SetField("record_id")
		sameID := blevequery.NewTermQuery(key.GetRecordId())
		sameID.SetField("record_id")
		versionAfter := blevequery.NewTermRangeInclusiveQuery(key.GetVersion(), "", boolPtr(false), boolPtr(true))
		versionAfter.SetField("version")
		parts = append(parts, blevequery.NewDisjunctionQuery([]blevequery.Query{idAfter, blevequery.NewConjunctionQuery([]blevequery.Query{sameID, versionAfter})}))
	}
	filter, err := filterQuery(spec.Groups, spec.GroupLogical, meta.Columns)
	if err != nil {
		return nil, err
	}
	if filter != nil {
		parts = append(parts, filter)
	}
	if len(parts) == 0 {
		return blevelib.NewMatchAllQuery(), nil
	}
	return blevequery.NewConjunctionQuery(parts), nil
}

func filterQuery(groups []viewindex.FilterGroup, groupLogical pb.FilterLogical, columns map[string]pb.FieldValueType) (blevequery.Query, error) {
	if len(groups) == 0 {
		return nil, nil
	}
	groupsQuery := make([]blevequery.Query, 0, len(groups))
	for _, group := range groups {
		conds := make([]blevequery.Query, 0, len(group.Conds))
		for _, cond := range group.Conds {
			if _, ok := columns[cond.Column]; !ok && cond.Column != "version" {
				return nil, fmt.Errorf("unknown filter column %q", cond.Column)
			}
			q, err := conditionQuery(cond, columns[cond.Column])
			if err != nil {
				return nil, err
			}
			conds = append(conds, q)
		}
		if len(conds) == 0 {
			continue
		}
		if group.Logical == pb.FilterLogical_FILTER_LOGICAL_OR {
			groupsQuery = append(groupsQuery, blevequery.NewDisjunctionQuery(conds))
		} else {
			groupsQuery = append(groupsQuery, blevequery.NewConjunctionQuery(conds))
		}
	}
	if len(groupsQuery) == 0 {
		return nil, nil
	}
	if groupLogical == pb.FilterLogical_FILTER_LOGICAL_OR {
		return blevequery.NewDisjunctionQuery(groupsQuery), nil
	}
	return blevequery.NewConjunctionQuery(groupsQuery), nil
}

func conditionQuery(cond viewindex.Filter, valueType pb.FieldValueType) (blevequery.Query, error) {
	if len(cond.Values) == 0 {
		return nil, errors.New("filter values are required")
	}
	value, err := typedValueToNative(cond.Values[0])
	if err != nil {
		return nil, err
	}
	field := cond.Column
	eq := func(v any) blevequery.Query {
		if v == nil {
			q := blevequery.NewTermQuery("true")
			q.SetField(nullMarker(field))
			return q
		}
		switch valueType {
		case pb.FieldValueType_FIELD_VALUE_TYPE_INT, pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
			number := nativeNumber(v)
			q := blevequery.NewNumericRangeInclusiveQuery(&number, &number, boolPtr(true), boolPtr(true))
			q.SetField(field)
			return q
		case pb.FieldValueType_FIELD_VALUE_TYPE_BOOL:
			boolValue, _ := v.(bool)
			q := blevequery.NewBoolFieldQuery(boolValue)
			q.SetField(field)
			return q
		default:
			q := blevequery.NewTermQuery(fmt.Sprint(v))
			q.SetField(field)
			return q
		}
	}
	switch cond.Op {
	case pb.FilterOp_FILTER_OP_EQ:
		return eq(value), nil
	case pb.FilterOp_FILTER_OP_NE:
		return negateQuery(eq(value)), nil
	case pb.FilterOp_FILTER_OP_LIKE:
		q := blevequery.NewWildcardQuery(bleveSubstringPattern(fmt.Sprint(value)))
		q.SetField(field)
		return q, nil
	case pb.FilterOp_FILTER_OP_NOT_LIKE:
		q := blevequery.NewWildcardQuery(bleveSubstringPattern(fmt.Sprint(value)))
		q.SetField(field)
		null := blevequery.NewTermQuery("true")
		null.SetField(nullMarker(field))
		return blevequery.NewBooleanQuery([]blevequery.Query{blevequery.NewMatchAllQuery()}, nil, []blevequery.Query{q, null}), nil
	case pb.FilterOp_FILTER_OP_IN, pb.FilterOp_FILTER_OP_NOT_IN:
		queries := make([]blevequery.Query, 0, len(cond.Values))
		for _, item := range cond.Values {
			v, err := typedValueToNative(item)
			if err != nil {
				return nil, err
			}
			queries = append(queries, eq(v))
		}
		q := blevequery.NewDisjunctionQuery(queries)
		if cond.Op == pb.FilterOp_FILTER_OP_NOT_IN {
			return negateQuery(q), nil
		}
		return q, nil
	case pb.FilterOp_FILTER_OP_GT, pb.FilterOp_FILTER_OP_GTE, pb.FilterOp_FILTER_OP_LT, pb.FilterOp_FILTER_OP_LTE, pb.FilterOp_FILTER_OP_BETWEEN:
		if valueType != pb.FieldValueType_FIELD_VALUE_TYPE_INT && valueType != pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE {
			return nil, errors.New("range filter requires numeric column")
		}
		min, max := (*float64)(nil), (*float64)(nil)
		if cond.Op == pb.FilterOp_FILTER_OP_GT || cond.Op == pb.FilterOp_FILTER_OP_GTE || cond.Op == pb.FilterOp_FILTER_OP_BETWEEN {
			n := nativeNumber(value)
			min = &n
		}
		if cond.Op == pb.FilterOp_FILTER_OP_LT || cond.Op == pb.FilterOp_FILTER_OP_LTE || cond.Op == pb.FilterOp_FILTER_OP_BETWEEN {
			last, err := typedValueToNative(cond.Values[len(cond.Values)-1])
			if err != nil {
				return nil, err
			}
			n := nativeNumber(last)
			max = &n
		}
		minInclusive, maxInclusive := boolPtr(cond.Op != pb.FilterOp_FILTER_OP_GT), boolPtr(cond.Op != pb.FilterOp_FILTER_OP_LT)
		q := blevequery.NewNumericRangeInclusiveQuery(min, max, minInclusive, maxInclusive)
		q.SetField(field)
		return q, nil
	default:
		return nil, fmt.Errorf("unsupported filter operator %s", cond.Op)
	}
}

// bleveSubstringPattern wraps a literal as *value* for FILTER_OP_LIKE substring match.
func bleveSubstringPattern(value string) string {
	var b strings.Builder
	b.WriteByte('*')
	for _, r := range value {
		switch r {
		case '*', '?', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('*')
	return b.String()
}

func nullMarker(field string) string { return "__moox_null__" + field }

func negateQuery(q blevequery.Query) blevequery.Query {
	return blevequery.NewBooleanQuery([]blevequery.Query{blevequery.NewMatchAllQuery()}, nil, []blevequery.Query{q})
}

func typedValueToNative(value *pb.TypedValue) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch v := value.GetValue().(type) {
	case *pb.TypedValue_StringValue:
		return v.StringValue, nil
	case *pb.TypedValue_IntValue:
		return float64(v.IntValue), nil
	case *pb.TypedValue_DoubleValue:
		return v.DoubleValue, nil
	case *pb.TypedValue_BoolValue:
		return v.BoolValue, nil
	case *pb.TypedValue_TimeValue:
		return time.Parse(time.RFC3339Nano, v.TimeValue)
	case *pb.TypedValue_JsonValue:
		return v.JsonValue, nil
	case *pb.TypedValue_BytesValue:
		return string(v.BytesValue), nil
	case *pb.TypedValue_NullValue:
		return nil, nil
	default:
		return nil, errors.New("unsupported typed value")
	}
}

func nativeToTypedValue(value any, valueType pb.FieldValueType) (*pb.TypedValue, error) {
	switch valueType {
	case pb.FieldValueType_FIELD_VALUE_TYPE_INT:
		return &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: int64(nativeNumber(value))}}, nil
	case pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
		return &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: nativeNumber(value)}}, nil
	case pb.FieldValueType_FIELD_VALUE_TYPE_BOOL:
		v, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("unexpected bool type %T", value)
		}
		return &pb.TypedValue{Value: &pb.TypedValue_BoolValue{BoolValue: v}}, nil
	case pb.FieldValueType_FIELD_VALUE_TYPE_TIME:
		if v, ok := value.(time.Time); ok {
			return &pb.TypedValue{Value: &pb.TypedValue_TimeValue{TimeValue: v.UTC().Format(time.RFC3339Nano)}}, nil
		}
	}
	return &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: fmt.Sprint(value)}}, nil
}

func nativeNumber(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int64:
		return float64(v)
	case int:
		return float64(v)
	case string:
		n, _ := strconv.ParseFloat(v, 64)
		return n
	default:
		return 0
	}
}

func nilIfEmpty(value string) string {
	if value == "" {
		return ""
	}
	return value
}

func boolPtr(value bool) *bool { return &value }

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsKey(values map[string]pb.FieldValueType, key string) bool {
	_, ok := values[key]
	return ok
}

func storedString(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case []string:
		if len(value) != 0 {
			return value[0]
		}
	case []interface{}:
		if len(value) != 0 {
			return fmt.Sprint(value[0])
		}
	}
	return fmt.Sprint(value)
}
