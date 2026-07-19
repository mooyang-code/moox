package bleve

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	blevelib "github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/keyword"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/standard"
	"github.com/blevesearch/bleve/v2/mapping"
	blevequery "github.com/blevesearch/bleve/v2/search/query"
	"github.com/blevesearch/bleve_index_api"
	"github.com/mooyang-code/moox/modules/storage/internal/rowkey"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	"github.com/mooyang-code/moox/modules/storage/internal/typedvalue"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Options 保存 Bleve 索引打开与初始化配置。
type Options struct {
	Path string
}

// Index 封装 Record 视图的 Bleve 索引读写能力。
type Index struct {
	index        blevelib.Index
	unmarshalRow func([]byte, *pb.RecordRow) error
	writeMu      sync.Mutex
}

// SearchRequest 描述一次 Bleve 复合检索请求。
type SearchRequest struct {
	SpaceID      string
	DatasetID    string
	Keys         []*pb.RecordKey
	TextQuery    string
	VersionRange *pb.VersionRange
	Filters      []*pb.FilterExpr
	Page         *pb.Page
	Sorts        []*pb.SortSpec
}

const (
	statsDocumentID = "__moox_view_index_stats"
	allTextField    = "__all_text"
)

type indexStatsDocument struct {
	ViewVersion         uint64            `json:"view_version"`
	EntryCount          int64             `json:"entry_count"`
	MinVersion          string            `json:"min_version"`
	MaxVersion          string            `json:"max_version"`
	SchemaHash          string            `json:"schema_hash"`
	UpdatedAt           string            `json:"updated_at"`
	IndexedFrom         string            `json:"indexed_from,omitempty"`
	IndexedTo           string            `json:"indexed_to,omitempty"`
	Checkpoints         map[string]uint64 `json:"checkpoints,omitempty"`
	RequiredColumnNames []string          `json:"required_column_names,omitempty"`
}

func Open(opts Options) (*Index, error) {
	if opts.Path == "" {
		return nil, errors.New("bleve path is required")
	}
	index, err := blevelib.Open(opts.Path)
	if err != nil {
		index, err = blevelib.New(opts.Path, buildIndexMapping())
	}
	if err != nil {
		return nil, err
	}
	return newIndex(index), nil
}

func OpenExisting(opts Options) (*Index, error) {
	if opts.Path == "" {
		return nil, errors.New("bleve path is required")
	}
	index, err := blevelib.Open(opts.Path)
	if err != nil {
		return nil, err
	}
	return newIndex(index), nil
}

func newIndex(index blevelib.Index) *Index {
	return &Index{
		index: index,
		unmarshalRow: func(raw []byte, row *pb.RecordRow) error {
			return (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, row)
		},
	}
}

func buildIndexMapping() mapping.IndexMapping {
	mapping := blevelib.NewIndexMapping()
	mapping.DefaultAnalyzer = keyword.Name
	docMapping := blevelib.NewDocumentMapping()
	docMapping.DefaultAnalyzer = keyword.Name

	rowMapping := blevelib.NewTextFieldMapping()
	rowMapping.Store = true
	rowMapping.Index = false
	docMapping.AddFieldMappingsAt("_row_json", rowMapping)

	statsMapping := blevelib.NewTextFieldMapping()
	statsMapping.Store = true
	statsMapping.Index = false
	docMapping.AddFieldMappingsAt("_stats_json", statsMapping)

	allTextMapping := blevelib.NewTextFieldMapping()
	allTextMapping.Analyzer = standard.Name
	allTextMapping.Store = false
	docMapping.AddFieldMappingsAt(allTextField, allTextMapping)

	for _, field := range []string{"_doc_type", "space_id", "dataset_id", "record_id", "version"} {
		kw := blevelib.NewKeywordFieldMapping()
		kw.Store = false
		kw.Index = true
		docMapping.AddFieldMappingsAt(field, kw)
	}

	mapping.DefaultMapping = docMapping
	return mapping
}

func (i *Index) SetSchema(ctx context.Context, viewVersion uint64, schemaHash string, columns []*pb.ViewColumn) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	i.writeMu.Lock()
	defer i.writeMu.Unlock()
	stats, err := i.readStats()
	if err != nil {
		return err
	}
	stats.ViewVersion = viewVersion
	stats.SchemaHash = schemaHash
	stats.RequiredColumnNames = viewColumnNames(columns)
	stats.IndexedFrom, stats.IndexedTo = "", ""
	stats.Checkpoints = make(map[string]uint64)
	stats.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return i.index.Index(statsDocumentID, stats.toDocument())
}

func (i *Index) Close() error {
	if i == nil || i.index == nil {
		return nil
	}
	return i.index.Close()
}

func (i *Index) IndexRows(ctx context.Context, rows []*pb.RecordRow, textIndexedColumns map[string]bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	i.writeMu.Lock()
	defer i.writeMu.Unlock()
	stats, err := i.readStats()
	if err != nil {
		return err
	}
	batch := i.index.NewBatch()
	seenDocs := make(map[string]bool, len(rows))
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		if row == nil || row.GetKey() == nil {
			continue
		}
		docID := documentID(row)
		existing, err := i.existingRecordRow(docID)
		if err != nil {
			return err
		}
		if existing == nil && len(textIndexedColumns) > 1 && len(row.GetColumns()) < len(textIndexedColumns) {
			continue
		}
		if existing != nil {
			row = mergeRecordRow(existing, row)
		}
		raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(row)
		if err != nil {
			return err
		}
		key := row.GetKey()
		version := rowkey.NormalizeVersion(key.GetVersion())
		doc := map[string]any{
			"_doc_type":  "row",
			"space_id":   key.GetSpaceId(),
			"dataset_id": key.GetDatasetId(),
			"record_id":  key.GetRecordId(),
			"version":    version,
			"_row_json":  string(raw),
		}
		for _, column := range row.GetColumns() {
			name := strings.TrimSpace(column.GetColumnName())
			if !textIndexedColumns[name] || column.GetValue() == nil {
				continue
			}
			doc[columnIndexField(name)] = bleveFieldValue(column.GetValue())
			doc[columnExistsField(name)] = "1"
			if text := typedvalue.String(column.GetValue()); text != "" {
				doc[columnNonEmptyField(name)] = "1"
				doc[allTextField] = strings.TrimSpace(strings.TrimSpace(anyString(doc[allTextField])) + " " + text)
			}
		}
		for _, field := range []string{"record_id", "version"} {
			value := doc[field].(string)
			doc[columnExistsField(field)] = "1"
			if value != "" {
				doc[columnNonEmptyField(field)] = "1"
			}
		}
		if !seenDocs[docID] {
			exists, err := i.documentExists(docID)
			if err != nil {
				return err
			}
			if !exists {
				stats.EntryCount++
			}
			seenDocs[docID] = true
		}
		updateStatsVersion(&stats, version)
		if err := batch.Index(docID, doc); err != nil {
			return err
		}
	}
	stats.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := batch.Index(statsDocumentID, stats.toDocument()); err != nil {
		return err
	}
	return i.index.Batch(batch)
}

func (i *Index) DeleteRows(ctx context.Context, rows []*pb.RecordRow) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	i.writeMu.Lock()
	defer i.writeMu.Unlock()
	stats, err := i.readStats()
	if err != nil {
		return err
	}
	batch := i.index.NewBatch()
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		if row == nil || row.GetKey() == nil {
			continue
		}
		docID := documentID(row)
		if _, ok := seen[docID]; ok {
			continue
		}
		seen[docID] = struct{}{}
		exists, err := i.documentExists(docID)
		if err != nil {
			return err
		}
		if exists && stats.EntryCount > 0 {
			stats.EntryCount--
		}
		batch.Delete(docID)
	}
	stats.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := batch.Index(statsDocumentID, stats.toDocument()); err != nil {
		return err
	}
	return i.index.Batch(batch)
}

// ApplyRows applies MERGE, REPLACE and DELETE operations in one Bleve batch.
func (i *Index) ApplyRows(ctx context.Context, apply viewindex.ViewIndexApplyBatch) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := apply.Validate(); err != nil {
		return err
	}
	i.writeMu.Lock()
	defer i.writeMu.Unlock()
	stats, err := i.readStats()
	if err != nil {
		return err
	}
	if stats.Checkpoints == nil {
		stats.Checkpoints = make(map[string]uint64)
	}
	if stats.ViewVersion != 0 && apply.ViewVersion == 0 {
		return errors.New("view schema version is required")
	}
	if apply.ViewVersion != 0 && stats.ViewVersion != 0 && apply.ViewVersion != stats.ViewVersion {
		return fmt.Errorf("view schema version conflict: current=%d requested=%d", stats.ViewVersion, apply.ViewVersion)
	}
	if stats.SchemaHash != "" && apply.ViewSchemaHash == "" {
		return errors.New("view schema hash is required")
	}
	if apply.ViewSchemaHash != "" && stats.SchemaHash != "" && apply.ViewSchemaHash != stats.SchemaHash {
		return fmt.Errorf("view schema hash conflict: current=%s requested=%s", stats.SchemaHash, apply.ViewSchemaHash)
	}
	if err := viewindex.ValidateIndexRangeProgress(stats.IndexedFrom, stats.IndexedTo, apply.IndexRangeUpdate); err != nil {
		return err
	}
	covered, err := validateBleveProgress(stats.Checkpoints, apply.CheckpointUpdates)
	if err != nil {
		return err
	}
	if covered {
		apply.RowWrites = nil
		apply.CheckpointUpdates = nil
	}
	textIndexedColumns := make(map[string]bool)
	var missing []*pb.RecordKey
	for _, write := range apply.RowWrites {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, column := range write.Columns {
			if column != nil && column.GetColumnName() != "" {
				textIndexedColumns[column.GetColumnName()] = true
			}
		}
	}
	for _, write := range apply.RowWrites {
		if err := ctx.Err(); err != nil {
			return err
		}
		if write.Key.RecordKey == nil {
			return errors.New("bleve apply requires record row keys")
		}
		if write.Operation == viewindex.RowWriteOperationReplace {
			if err := validateCompleteReplace(write.Columns, write.RemovedColumnNames, stats.RequiredColumnNames); err != nil {
				return err
			}
		}
		if write.Operation == viewindex.RowWriteOperationMerge {
			existing, err := i.existingRecordRow(documentID(&pb.RecordRow{Key: write.Key.RecordKey}))
			if err != nil {
				return err
			}
			if existing == nil {
				missing = append(missing, write.Key.RecordKey)
				continue
			}
			for _, column := range existing.GetColumns() {
				if column != nil && column.GetColumnName() != "" {
					textIndexedColumns[column.GetColumnName()] = true
				}
			}
		}
	}
	if len(missing) > 0 {
		return &viewindex.MissingRowsError{RecordKeys: missing}
	}
	batch := i.index.NewBatch()
	seen := make(map[string]struct{}, len(apply.RowWrites))
	for _, write := range apply.RowWrites {
		if err := ctx.Err(); err != nil {
			return err
		}
		row := &pb.RecordRow{Key: write.Key.RecordKey, Columns: write.Columns, Attributes: write.Attributes, AttributesToDelete: write.AttributesToDelete, RemovedColumnNames: write.RemovedColumnNames, RemovedColumns: write.RemovedColumns, SourceShardId: write.SourceShardID, SourceSequence: write.SourceSequence}
		docID := documentID(row)
		if _, ok := seen[docID]; ok {
			continue
		}
		seen[docID] = struct{}{}
		switch write.Operation {
		case viewindex.RowWriteOperationDelete:
			existing, err := i.existingRecordRow(docID)
			if err != nil {
				return err
			}
			if existing != nil && viewindex.IsStaleSource(existing.GetSourceShardId(), existing.GetSourceSequence(), write.SourceShardID, write.SourceSequence) {
				continue
			}
			if existing != nil && stats.EntryCount > 0 {
				stats.EntryCount--
			}
			batch.Delete(docID)
		case viewindex.RowWriteOperationMerge, viewindex.RowWriteOperationReplace:
			existing, err := i.existingRecordRow(docID)
			if err != nil {
				return err
			}
			if existing != nil && viewindex.IsStaleSource(existing.GetSourceShardId(), existing.GetSourceSequence(), write.SourceShardID, write.SourceSequence) {
				continue
			}
			if write.Operation == viewindex.RowWriteOperationMerge {
				row = mergeRecordRow(existing, row)
			}
			row.AttributesToDelete = nil
			doc, err := recordDocument(row, textIndexedColumns)
			if err != nil {
				return err
			}
			if existing == nil {
				stats.EntryCount++
			}
			updateStatsVersion(&stats, rowkey.NormalizeVersion(row.GetKey().GetVersion()))
			if err := batch.Index(docID, doc); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported row operation %d", write.Operation)
		}
	}
	for _, update := range apply.CheckpointUpdates {
		stats.Checkpoints[update.ShardID] = update.LastAppliedSequence
	}
	if apply.IndexRangeUpdate != nil {
		if apply.IndexRangeUpdate.IndexedFrom != nil {
			stats.IndexedFrom = *apply.IndexRangeUpdate.IndexedFrom
		}
		if apply.IndexRangeUpdate.IndexedTo != nil {
			stats.IndexedTo = *apply.IndexRangeUpdate.IndexedTo
		}
	}
	stats.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := batch.Index(statsDocumentID, stats.toDocument()); err != nil {
		return err
	}
	return i.index.Batch(batch)
}

func viewColumnNames(columns []*pb.ViewColumn) []string {
	names := make([]string, 0, len(columns))
	for _, column := range columns {
		if column != nil && strings.TrimSpace(column.GetColumnName()) != "" {
			names = append(names, column.GetColumnName())
		}
	}
	return names
}

func validateCompleteReplace(columns []*pb.ColumnValue, removed []string, required []string) error {
	present := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		if column != nil && strings.TrimSpace(column.GetColumnName()) != "" {
			present[column.GetColumnName()] = struct{}{}
		}
	}
	removedSet := make(map[string]struct{}, len(removed))
	for _, name := range removed {
		removedSet[name] = struct{}{}
	}
	for _, name := range required {
		if _, ok := present[name]; !ok {
			if _, removed := removedSet[name]; removed {
				continue
			}
			return fmt.Errorf("REPLACE row is missing view column %q", name)
		}
	}
	return nil
}

func validateBleveProgress(current map[string]uint64, updates []viewindex.ShardCheckpointUpdate) (bool, error) {
	if len(updates) == 0 {
		return false, nil
	}
	seen := make(map[string]struct{}, len(updates))
	covered := true
	sawCovered := false
	sawPending := false
	for _, update := range updates {
		if _, ok := seen[update.ShardID]; ok {
			return false, fmt.Errorf("duplicate checkpoint shard %q", update.ShardID)
		}
		seen[update.ShardID] = struct{}{}
		value := current[update.ShardID]
		if value == update.ExpectedLastAppliedSequence {
			covered = false
			sawPending = true
			continue
		}
		if value >= update.LastAppliedSequence {
			sawCovered = true
			continue
		}
		return false, fmt.Errorf("checkpoint conflict for shard %q", update.ShardID)
	}
	if sawCovered && sawPending {
		return false, errors.New("checkpoint apply mixes covered and pending shards")
	}
	return covered, nil
}

func recordDocument(row *pb.RecordRow, textIndexedColumns map[string]bool) (map[string]any, error) {
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(row)
	if err != nil {
		return nil, err
	}
	key := row.GetKey()
	doc := map[string]any{"_doc_type": "row", "space_id": key.GetSpaceId(), "dataset_id": key.GetDatasetId(), "record_id": key.GetRecordId(), "version": rowkey.NormalizeVersion(key.GetVersion()), "_row_json": string(raw)}
	for _, column := range row.GetColumns() {
		name := strings.TrimSpace(column.GetColumnName())
		if !textIndexedColumns[name] || column.GetValue() == nil {
			continue
		}
		doc[columnIndexField(name)] = bleveFieldValue(column.GetValue())
		doc[columnExistsField(name)] = "1"
		if text := typedvalue.String(column.GetValue()); text != "" {
			doc[columnNonEmptyField(name)] = "1"
			doc[allTextField] = strings.TrimSpace(strings.TrimSpace(anyString(doc[allTextField])) + " " + text)
		}
	}
	for _, field := range []string{"record_id", "version"} {
		value := doc[field].(string)
		doc[columnExistsField(field)] = "1"
		if value != "" {
			doc[columnNonEmptyField(field)] = "1"
		}
	}
	return doc, nil
}

func (i *Index) existingRecordRow(docID string) (*pb.RecordRow, error) {
	doc, err := i.index.Document(docID)
	if err != nil || doc == nil {
		return nil, err
	}
	var raw string
	doc.VisitFields(func(field index.Field) {
		if field.Name() == "_row_json" {
			raw = string(field.Value())
		}
	})
	if raw == "" {
		return nil, nil
	}
	row := &pb.RecordRow{}
	if err := i.unmarshalRow([]byte(raw), row); err != nil {
		return nil, err
	}
	return row, nil
}

func mergeRecordRow(base, patch *pb.RecordRow) *pb.RecordRow {
	if base == nil {
		merged := proto.Clone(patch).(*pb.RecordRow)
		for _, name := range patch.GetRemovedColumnNames() {
			merged.RemovedColumns = appendRecordColumnTombstone(merged.RemovedColumns, &pb.ColumnRemoval{ColumnName: name, SourceShardId: patch.GetSourceShardId(), SourceSequence: patch.GetSourceSequence()})
		}
		merged.RemovedColumnNames = nil
		return merged
	}
	merged := proto.Clone(base).(*pb.RecordRow)
	merged.Key = proto.Clone(patch.GetKey()).(*pb.RecordKey)
	positions := make(map[string]int, len(merged.GetColumns()))
	for idx, column := range merged.GetColumns() {
		positions[column.GetColumnName()] = idx
	}
	for _, column := range patch.GetColumns() {
		if column == nil {
			continue
		}
		if idx, ok := positions[column.GetColumnName()]; ok && isStaleRecordColumn(merged.Columns[idx], column) {
			continue
		}
		if isStaleRecordColumnTombstone(merged.RemovedColumns, column) {
			continue
		}
		merged.RemovedColumns = removeRecordColumnTombstone(merged.RemovedColumns, column.GetColumnName())
		copied := proto.Clone(column).(*pb.ColumnValue)
		if idx, ok := positions[column.GetColumnName()]; ok {
			merged.Columns[idx] = copied
			continue
		}
		positions[column.GetColumnName()] = len(merged.Columns)
		merged.Columns = append(merged.Columns, copied)
	}
	for _, name := range patch.GetRemovedColumnNames() {
		removal := &pb.ColumnRemoval{ColumnName: name, SourceShardId: patch.GetSourceShardId(), SourceSequence: patch.GetSourceSequence()}
		if isStaleRecordColumnRemovalTombstone(merged.RemovedColumns, removal) {
			continue
		}
		if idx, ok := positions[name]; ok {
			if isStaleRecordColumnRemoval(merged.Columns[idx], removal) {
				continue
			}
			merged.Columns = append(merged.Columns[:idx], merged.Columns[idx+1:]...)
			positions = make(map[string]int, len(merged.Columns))
			for pos, value := range merged.Columns {
				positions[value.GetColumnName()] = pos
			}
		}
		merged.RemovedColumns = appendRecordColumnTombstone(merged.RemovedColumns, removal)
	}
	for _, removal := range patch.GetRemovedColumns() {
		if removal == nil {
			continue
		}
		if isStaleRecordColumnRemovalTombstone(merged.RemovedColumns, removal) {
			continue
		}
		if idx, ok := positions[removal.GetColumnName()]; ok {
			if isStaleRecordColumnRemoval(merged.Columns[idx], removal) {
				continue
			}
			merged.Columns = append(merged.Columns[:idx], merged.Columns[idx+1:]...)
			positions = make(map[string]int, len(merged.Columns))
			for pos, value := range merged.Columns {
				positions[value.GetColumnName()] = pos
			}
		}
		merged.RemovedColumns = appendRecordColumnTombstone(merged.RemovedColumns, removal)
	}
	if len(patch.GetAttributes()) > 0 {
		if merged.Attributes == nil {
			merged.Attributes = make(map[string]string, len(patch.GetAttributes()))
		}
		for key, value := range patch.GetAttributes() {
			merged.Attributes[key] = value
		}
	}
	for _, key := range patch.GetAttributesToDelete() {
		delete(merged.Attributes, key)
	}
	merged.SourceShardId = patch.GetSourceShardId()
	merged.SourceSequence = patch.GetSourceSequence()
	merged.RemovedColumnNames = nil
	return merged
}

func isStaleRecordColumn(existing, incoming *pb.ColumnValue) bool {
	return existing != nil && incoming != nil && incoming.GetSourceShardId() != "" && incoming.GetSourceSequence() != 0 &&
		existing.GetSourceShardId() == incoming.GetSourceShardId() && existing.GetSourceSequence() >= incoming.GetSourceSequence()
}

func isStaleRecordColumnRemoval(existing *pb.ColumnValue, incoming *pb.ColumnRemoval) bool {
	return existing != nil && incoming != nil && incoming.GetSourceShardId() != "" && incoming.GetSourceSequence() != 0 && existing.GetSourceShardId() == incoming.GetSourceShardId() && existing.GetSourceSequence() >= incoming.GetSourceSequence()
}

func isStaleRecordColumnTombstone(existing []*pb.ColumnRemoval, incoming *pb.ColumnValue) bool {
	if incoming == nil || incoming.GetSourceShardId() == "" || incoming.GetSourceSequence() == 0 {
		return false
	}
	for _, removal := range existing {
		if removal != nil && removal.GetColumnName() == incoming.GetColumnName() && removal.GetSourceShardId() == incoming.GetSourceShardId() && removal.GetSourceSequence() >= incoming.GetSourceSequence() {
			return true
		}
	}
	return false
}

func isStaleRecordColumnRemovalTombstone(existing []*pb.ColumnRemoval, incoming *pb.ColumnRemoval) bool {
	if incoming == nil || incoming.GetSourceShardId() == "" || incoming.GetSourceSequence() == 0 {
		return false
	}
	for _, removal := range existing {
		if removal != nil && removal.GetColumnName() == incoming.GetColumnName() && removal.GetSourceShardId() == incoming.GetSourceShardId() && removal.GetSourceSequence() >= incoming.GetSourceSequence() {
			return true
		}
	}
	return false
}

func appendRecordColumnTombstone(values []*pb.ColumnRemoval, incoming *pb.ColumnRemoval) []*pb.ColumnRemoval {
	if incoming == nil || incoming.GetColumnName() == "" {
		return values
	}
	filtered := values[:0]
	for _, value := range values {
		if value != nil && value.GetColumnName() != incoming.GetColumnName() {
			filtered = append(filtered, value)
		}
	}
	return append(filtered, proto.Clone(incoming).(*pb.ColumnRemoval))
}

func removeRecordColumnTombstone(values []*pb.ColumnRemoval, name string) []*pb.ColumnRemoval {
	filtered := values[:0]
	for _, value := range values {
		if value != nil && value.GetColumnName() != name {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func (i *Index) Stat(ctx context.Context) (viewindex.ViewIndexStats, error) {
	if err := ctx.Err(); err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	stats, err := i.readStats()
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	return viewindex.ViewIndexStats{
		Exists:           true,
		ViewVersion:      stats.ViewVersion,
		EntryCount:       stats.EntryCount,
		MinVersion:       stats.MinVersion,
		MaxVersion:       stats.MaxVersion,
		SchemaHash:       stats.SchemaHash,
		UpdatedAt:        stats.UpdatedAt,
		IndexedFrom:      stats.IndexedFrom,
		IndexedTo:        stats.IndexedTo,
		ShardCheckpoints: stats.Checkpoints,
	}, nil
}

func (i *Index) SearchRecordRows(ctx context.Context, req SearchRequest) ([]*pb.RecordRow, *pb.PageResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	query, err := buildBooleanQuery(req)
	if err != nil {
		return nil, nil, err
	}
	pageNo, size := normalizeSearchPage(req.Page)
	from := int((pageNo - 1) * size)
	searchReq := blevelib.NewSearchRequestOptions(query, int(size)+1, from, false)
	searchReq.Fields = []string{"_row_json"}
	searchReq.SortBy(bleveSortOrder(req.Sorts))
	result, err := i.index.Search(searchReq)
	if err != nil {
		return nil, nil, err
	}
	rows := make([]*pb.RecordRow, 0, len(result.Hits))
	for _, hit := range result.Hits {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		raw, ok := hit.Fields["_row_json"].(string)
		if !ok {
			continue
		}
		row := &pb.RecordRow{}
		if err := i.unmarshalRow([]byte(raw), row); err != nil {
			return nil, nil, err
		}
		rows = append(rows, row)
	}
	hasMore := uint32(len(rows)) > size || uint64(from+len(rows)) < result.Total
	if uint32(len(rows)) > size {
		rows = rows[:size]
	}
	total := result.Total
	if total > math.MaxUint32 {
		total = math.MaxUint32
	}
	return rows, &pb.PageResult{Page: pageNo, Size: size, Total: uint32(total), HasMore: hasMore}, nil
}

func buildBooleanQuery(req SearchRequest) (blevequery.Query, error) {
	musts := []blevequery.Query{termQuery("row", "_doc_type")}
	if space := strings.TrimSpace(req.SpaceID); space != "" {
		musts = append(musts, scopeFieldQuery(space, "space_id"))
	}
	if dataset := strings.TrimSpace(req.DatasetID); dataset != "" {
		musts = append(musts, scopeFieldQuery(dataset, "dataset_id"))
	}
	if keyQuery := buildRecordKeysQuery(req.Keys); keyQuery != nil {
		musts = append(musts, keyQuery)
	}
	if text := strings.TrimSpace(req.TextQuery); text != "" {
		textQuery := blevelib.NewMatchQuery(text)
		textQuery.SetField(allTextField)
		musts = append(musts, textQuery)
	}
	if versionRange := req.VersionRange; versionRange != nil {
		minVersion := normalizedVersionBound(versionRange.GetStartVersion())
		maxVersion := normalizedVersionBound(versionRange.GetEndVersion())
		if minVersion != "" || maxVersion != "" {
			inclusive := true
			rangeQuery := blevequery.NewTermRangeInclusiveQuery(minVersion, maxVersion, &inclusive, &inclusive)
			rangeQuery.SetField("version")
			musts = append(musts, rangeQuery)
		}
	}
	for _, filter := range req.Filters {
		query, err := buildFilterQuery(filter)
		if err != nil {
			return nil, err
		}
		if query != nil {
			musts = append(musts, query)
		}
	}
	return blevelib.NewConjunctionQuery(musts...), nil
}

func normalizedVersionBound(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return rowkey.NormalizeVersion(value)
}

func buildRecordKeysQuery(keys []*pb.RecordKey) blevequery.Query {
	disjuncts := make([]blevequery.Query, 0, len(keys))
	for _, key := range keys {
		if key == nil || strings.TrimSpace(key.GetRecordId()) == "" {
			continue
		}
		musts := []blevequery.Query{scopeFieldQuery(strings.TrimSpace(key.GetRecordId()), "record_id")}
		if version := strings.TrimSpace(key.GetVersion()); version != "" {
			musts = append(musts, termQuery(rowkey.NormalizeVersion(version), "version"))
		}
		disjuncts = append(disjuncts, blevelib.NewConjunctionQuery(musts...))
	}
	if len(disjuncts) == 0 {
		return nil
	}
	if len(disjuncts) == 1 {
		return disjuncts[0]
	}
	return blevelib.NewDisjunctionQuery(disjuncts...)
}

func normalizeSearchPage(page *pb.Page) (uint32, uint32) {
	pageNo := uint32(1)
	size := uint32(25)
	if page != nil {
		if page.GetPage() > 0 {
			pageNo = page.GetPage()
		}
		if page.GetSize() > 0 {
			size = page.GetSize()
		}
	}
	if size > 1000 {
		size = 1000
	}
	return pageNo, size
}

func bleveSortOrder(sorts []*pb.SortSpec) []string {
	if len(sorts) == 0 {
		return []string{"-version", "_id"}
	}
	out := make([]string, 0, len(sorts)+1)
	for _, sortSpec := range sorts {
		field := strings.TrimSpace(sortSpec.GetFieldName())
		if field == "" {
			continue
		}
		field = queryIndexField(field)
		if sortSpec.GetDesc() {
			field = "-" + field
		}
		out = append(out, field)
	}
	out = append(out, "_id")
	return out
}

func buildFilterQuery(filter *pb.FilterExpr) (blevequery.Query, error) {
	if filter == nil || strings.TrimSpace(filter.GetExpr()) == "" {
		return nil, nil
	}
	if fn, field, token, ok := parseFilterFunction(filter.GetExpr()); ok {
		field = strings.TrimSpace(field)
		if field == "" {
			return nil, errors.New("filter field is required")
		}
		switch fn {
		case "is_empty":
			return withoutQuery(termQuery("1", columnNonEmptyField(field))), nil
		case "is_not_empty":
			return termQuery("1", columnNonEmptyField(field)), nil
		}
		expected := filterTypedValue(token, filter.GetArgs())
		if expected == nil {
			return nil, errors.New("unsupported filter value " + token)
		}
		pattern := wildcardLiteral(typedvalue.String(expected))
		switch fn {
		case "starts_with":
			return wildcardQuery(pattern+"*", queryIndexField(field)), nil
		case "ends_with":
			return wildcardQuery("*"+pattern, queryIndexField(field)), nil
		case "not_contains":
			return withoutQuery(wildcardQuery("*"+pattern+"*", queryIndexField(field))), nil
		default:
			return nil, errors.New("unsupported filter expression " + filter.GetExpr())
		}
	}
	field, op, token, ok := parseSimpleFilter(filter.GetExpr())
	if !ok {
		return nil, errors.New("unsupported filter expression " + filter.GetExpr())
	}
	expected := filterTypedValue(token, filter.GetArgs())
	if expected == nil {
		return nil, errors.New("unsupported filter value " + token)
	}
	indexField := queryIndexField(field)
	if op == "contains" {
		return wildcardQuery("*"+wildcardLiteral(typedvalue.String(expected))+"*", indexField), nil
	}
	comparison, err := comparisonQuery(indexField, expected, op)
	if err != nil {
		return nil, err
	}
	if op == "!=" {
		return blevequery.NewBooleanQuery(
			[]blevequery.Query{termQuery("1", columnExistsField(field))}, nil, []blevequery.Query{comparison},
		), nil
	}
	return comparison, nil
}

func comparisonQuery(field string, value *pb.TypedValue, op string) (blevequery.Query, error) {
	if number, ok := typedvalue.Numeric(value); ok {
		inclusive := true
		switch op {
		case "=", "==", "!=":
			query := blevequery.NewNumericRangeInclusiveQuery(&number, &number, &inclusive, &inclusive)
			query.SetField(field)
			return query, nil
		case ">", ">=":
			query := blevequery.NewNumericRangeInclusiveQuery(&number, nil, boolPointer(op == ">="), nil)
			query.SetField(field)
			return query, nil
		case "<", "<=":
			query := blevequery.NewNumericRangeInclusiveQuery(nil, &number, nil, boolPointer(op == "<="))
			query.SetField(field)
			return query, nil
		default:
			return nil, errors.New("unsupported numeric filter operator " + op)
		}
	}
	text := indexedStringValue(value)
	switch op {
	case "=", "==", "!=":
		return termQuery(text, field), nil
	case ">", ">=", "<", "<=":
		inclusive := op == ">=" || op == "<="
		min, max := "", ""
		var minInclusive, maxInclusive *bool
		if op == ">" || op == ">=" {
			min = text
			minInclusive = &inclusive
		} else {
			max = text
			maxInclusive = &inclusive
		}
		query := blevequery.NewTermRangeInclusiveQuery(min, max, minInclusive, maxInclusive)
		query.SetField(field)
		return query, nil
	default:
		return nil, errors.New("unsupported filter operator " + op)
	}
}

func parseSimpleFilter(expr string) (left, op, right string, ok bool) {
	expr = strings.TrimSpace(expr)
	for _, candidate := range []string{" contains ", "==", "!=", ">=", "<=", "=", ">", "<"} {
		if idx := strings.Index(expr, candidate); idx >= 0 {
			left = strings.TrimSpace(expr[:idx])
			right = strings.TrimSpace(expr[idx+len(candidate):])
			op = strings.TrimSpace(candidate)
			return left, op, right, left != "" && right != ""
		}
	}
	return "", "", "", false
}

func parseFilterFunction(expr string) (name, field, token string, ok bool) {
	expr = strings.TrimSpace(expr)
	open := strings.Index(expr, "(")
	if open <= 0 || !strings.HasSuffix(expr, ")") {
		return "", "", "", false
	}
	name = strings.TrimSpace(expr[:open])
	body := strings.TrimSpace(strings.TrimSuffix(expr[open+1:], ")"))
	switch name {
	case "is_empty", "is_not_empty":
		if body == "" || strings.Contains(body, ",") {
			return "", "", "", false
		}
		return name, body, "", true
	case "starts_with", "ends_with", "not_contains":
		field, token, ok = strings.Cut(body, ",")
		field, token = strings.TrimSpace(field), strings.TrimSpace(token)
		return name, field, token, ok && field != "" && token != ""
	default:
		return "", "", "", false
	}
}

func filterTypedValue(token string, args map[string]*pb.TypedValue) *pb.TypedValue {
	token = strings.TrimSpace(token)
	if strings.HasPrefix(token, "$") {
		return args[strings.TrimPrefix(token, "$")]
	}
	if len(token) >= 2 && ((strings.HasPrefix(token, "'") && strings.HasSuffix(token, "'")) ||
		(strings.HasPrefix(token, `"`) && strings.HasSuffix(token, `"`))) {
		return &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: token[1 : len(token)-1]}}
	}
	return nil
}

func bleveFieldValue(value *pb.TypedValue) any {
	switch typed := value.GetValue().(type) {
	case *pb.TypedValue_IntValue:
		return typed.IntValue
	case *pb.TypedValue_DoubleValue:
		return typed.DoubleValue
	case *pb.TypedValue_BoolValue:
		return strconv.FormatBool(typed.BoolValue)
	default:
		return indexedStringValue(value)
	}
}

func indexedStringValue(value *pb.TypedValue) string {
	if typed, ok := value.GetValue().(*pb.TypedValue_TimeValue); ok {
		if normalized, err := rowkey.NormalizeTimeVersion(typed.TimeValue); err == nil {
			return normalized
		}
	}
	return typedvalue.String(value)
}

func queryIndexField(name string) string {
	switch name {
	case "space_id", "dataset_id", "record_id", "version":
		return name
	default:
		return columnIndexField(name)
	}
}

func columnIndexField(name string) string {
	return "__column_" + hex.EncodeToString([]byte(strings.TrimSpace(name)))
}

func columnExistsField(name string) string {
	return "__exists_" + hex.EncodeToString([]byte(strings.TrimSpace(name)))
}

func columnNonEmptyField(name string) string {
	return "__nonempty_" + hex.EncodeToString([]byte(strings.TrimSpace(name)))
}

func wildcardQuery(pattern string, field string) blevequery.Query {
	query := blevequery.NewWildcardQuery(pattern)
	query.SetField(field)
	return query
}

func withoutQuery(query blevequery.Query) blevequery.Query {
	return blevequery.NewBooleanQuery(nil, nil, []blevequery.Query{query})
}

func wildcardLiteral(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `*`, `\*`)
	return strings.ReplaceAll(value, `?`, `\?`)
}

func boolPointer(value bool) *bool {
	return &value
}

func anyString(value any) string {
	text, _ := value.(string)
	return text
}

func scopeFieldQuery(value string, field string) blevequery.Query {
	match := blevelib.NewMatchQuery(value)
	match.SetField(field)
	return blevelib.NewDisjunctionQuery(termQuery(value, field), match)
}

func termQuery(value string, field string) blevequery.Query {
	q := blevelib.NewTermQuery(value)
	q.SetField(field)
	return q
}

func documentID(row *pb.RecordRow) string {
	key := row.GetKey()
	return strings.Join([]string{
		key.GetSpaceId(),
		key.GetDatasetId(),
		key.GetRecordId(),
		key.GetVersion(),
	}, "/")
}

func (i *Index) readStats() (indexStatsDocument, error) {
	doc, err := i.index.Document(statsDocumentID)
	if err != nil || doc == nil {
		return indexStatsDocument{}, err
	}
	var raw string
	doc.VisitFields(func(field index.Field) {
		if field.Name() == "_stats_json" {
			raw = string(field.Value())
		}
	})
	if raw == "" {
		return indexStatsDocument{}, nil
	}
	var stats indexStatsDocument
	if err := json.Unmarshal([]byte(raw), &stats); err != nil {
		return indexStatsDocument{}, err
	}
	return stats, nil
}

func (i *Index) documentExists(docID string) (bool, error) {
	doc, err := i.index.Document(docID)
	if err != nil {
		return false, err
	}
	return doc != nil, nil
}

func (s indexStatsDocument) toDocument() map[string]any {
	raw, _ := json.Marshal(s)
	return map[string]any{"_stats_json": string(raw)}
}

func updateStatsVersion(stats *indexStatsDocument, version string) {
	if version == "" {
		return
	}
	if stats.MinVersion == "" || version < stats.MinVersion {
		stats.MinVersion = version
	}
	if stats.MaxVersion == "" || version > stats.MaxVersion {
		stats.MaxVersion = version
	}
}
