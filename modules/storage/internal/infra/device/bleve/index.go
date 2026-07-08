package bleve

import (
	"context"
	"errors"
	"strings"
	"sync"

	blevelib "github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/mapping"
	blevequery "github.com/blevesearch/bleve/v2/search/query"
	bleveindex "github.com/blevesearch/bleve_index_api"
	"github.com/mooyang-code/moox/modules/storage/internal/core/factvalue"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Options 保存 Bleve 索引打开与初始化配置。
type Options struct {
	Path string
}

// Index 封装 Record 视图的 Bleve 索引读写能力。
type Index struct {
	index blevelib.Index
	mu    sync.Mutex
}

// SearchRequest 描述一次 Bleve 复合检索请求。
type SearchRequest struct {
	SpaceID      string
	DatasetID    string
	RecordIDs    []string
	TextQuery    string
	VersionRange *pb.VersionRange
	Page         *pb.Page
}

const searchBatchSize = 10000

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
	return &Index{index: index}, nil
}

func OpenExisting(opts Options) (*Index, error) {
	if opts.Path == "" {
		return nil, errors.New("bleve path is required")
	}
	index, err := blevelib.Open(opts.Path)
	if err != nil {
		return nil, err
	}
	return &Index{index: index}, nil
}

func buildIndexMapping() mapping.IndexMapping {
	mapping := blevelib.NewIndexMapping()
	docMapping := blevelib.NewDocumentMapping()

	rowMapping := blevelib.NewTextFieldMapping()
	rowMapping.Store = true
	rowMapping.Index = false
	docMapping.AddFieldMappingsAt("_row_json", rowMapping)

	for _, field := range []string{"space_id", "dataset_id", "record_id", "version"} {
		kw := blevelib.NewKeywordFieldMapping()
		kw.Store = false
		kw.Index = true
		docMapping.AddFieldMappingsAt(field, kw)
	}

	mapping.DefaultMapping = docMapping
	return mapping
}

func (i *Index) Close() error {
	if i == nil || i.index == nil {
		return nil
	}
	return i.index.Close()
}

func (i *Index) IndexRows(ctx context.Context, rows []*pb.RecordRow, textIndexedColumns map[string]bool) error {
	_ = ctx
	i.mu.Lock()
	defer i.mu.Unlock()
	batch := i.index.NewBatch()
	for _, row := range rows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		merged, err := i.mergeExistingRecordPatch(row)
		if err != nil {
			return err
		}
		raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(merged)
		if err != nil {
			return err
		}
		key := merged.GetKey()
		doc := map[string]any{
			"space_id":   key.GetSpaceId(),
			"dataset_id": key.GetDatasetId(),
			"record_id":  key.GetRecordId(),
			"version":    key.GetVersion(),
			"_row_json":  string(raw),
		}
		for _, column := range merged.GetColumns() {
			if textIndexedColumns[column.GetColumnName()] {
				doc[column.GetColumnName()] = factvalue.String(column.GetValue())
			}
		}
		if err := batch.Index(documentID(merged), doc); err != nil {
			return err
		}
	}
	return i.index.Batch(batch)
}

func (i *Index) mergeExistingRecordPatch(patch *pb.RecordRow) (*pb.RecordRow, error) {
	if patch == nil {
		return nil, nil
	}
	existing, err := i.loadStoredRecordRow(documentID(patch))
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return proto.Clone(patch).(*pb.RecordRow), nil
	}
	merged := proto.Clone(existing).(*pb.RecordRow)
	merged.Key = proto.Clone(patch.GetKey()).(*pb.RecordKey)
	merged.Attributes = mergeRecordAttributes(merged.GetAttributes(), patch.GetAttributes())
	merged.Columns = mergeRecordColumns(merged.GetColumns(), patch.GetColumns())
	return merged, nil
}

func (i *Index) loadStoredRecordRow(id string) (*pb.RecordRow, error) {
	doc, err := i.index.Document(id)
	if err != nil || doc == nil {
		return nil, err
	}
	var raw []byte
	doc.VisitFields(func(field bleveindex.Field) {
		if field.Name() == "_row_json" {
			raw = append([]byte(nil), field.Value()...)
		}
	})
	if len(raw) == 0 {
		return nil, nil
	}
	row := &pb.RecordRow{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, row); err != nil {
		return nil, err
	}
	return row, nil
}

func mergeRecordAttributes(base map[string]string, patch map[string]string) map[string]string {
	if len(base) == 0 && len(patch) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(patch))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range patch {
		out[key] = value
	}
	return out
}

func mergeRecordColumns(base []*pb.ColumnValue, patch []*pb.ColumnValue) []*pb.ColumnValue {
	out := make([]*pb.ColumnValue, 0, len(base)+len(patch))
	positions := make(map[string]int, len(base)+len(patch))
	for _, column := range base {
		if column == nil {
			continue
		}
		positions[column.GetColumnName()] = len(out)
		out = append(out, proto.Clone(column).(*pb.ColumnValue))
	}
	for _, column := range patch {
		if column == nil {
			continue
		}
		copied := proto.Clone(column).(*pb.ColumnValue)
		if idx, ok := positions[column.GetColumnName()]; ok {
			out[idx] = copied
			continue
		}
		positions[column.GetColumnName()] = len(out)
		out = append(out, copied)
	}
	return out
}

func (i *Index) SearchRecordRows(ctx context.Context, req SearchRequest) ([]*pb.RecordRow, *pb.PageResult, error) {
	_ = ctx
	query := buildBooleanQuery(req)
	var rows []*pb.RecordRow
	allowRecords := factvalue.StringSet(req.RecordIDs)
	for from := 0; ; from += searchBatchSize {
		searchReq := blevelib.NewSearchRequestOptions(query, searchBatchSize, from, false)
		searchReq.Fields = []string{"_row_json"}
		result, err := i.index.Search(searchReq)
		if err != nil {
			return nil, nil, err
		}
		for _, hit := range result.Hits {
			raw, ok := hit.Fields["_row_json"].(string)
			if !ok {
				continue
			}
			row := &pb.RecordRow{}
			if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(raw), row); err != nil {
				return nil, nil, err
			}
			key := row.GetKey()
			if req.SpaceID != "" && key.GetSpaceId() != req.SpaceID {
				continue
			}
			if req.DatasetID != "" && key.GetDatasetId() != req.DatasetID {
				continue
			}
			if len(allowRecords) > 0 && !allowRecords[key.GetRecordId()] {
				continue
			}
			if !versionInRange(key.GetVersion(), req.VersionRange) {
				continue
			}
			rows = append(rows, row)
		}
		if len(result.Hits) < searchBatchSize || uint64(from+len(result.Hits)) >= result.Total {
			break
		}
	}
	return pageRows(rows, req.Page)
}

func buildBooleanQuery(req SearchRequest) blevequery.Query {
	var musts []blevequery.Query
	if space := strings.TrimSpace(req.SpaceID); space != "" {
		musts = append(musts, scopeFieldQuery(space, "space_id"))
	}
	if dataset := strings.TrimSpace(req.DatasetID); dataset != "" {
		musts = append(musts, scopeFieldQuery(dataset, "dataset_id"))
	}
	if len(req.RecordIDs) == 1 {
		if recordID := strings.TrimSpace(req.RecordIDs[0]); recordID != "" {
			musts = append(musts, scopeFieldQuery(recordID, "record_id"))
		}
	} else if len(req.RecordIDs) > 1 {
		disjuncts := make([]blevequery.Query, 0, len(req.RecordIDs))
		for _, recordID := range req.RecordIDs {
			if id := strings.TrimSpace(recordID); id != "" {
				disjuncts = append(disjuncts, scopeFieldQuery(id, "record_id"))
			}
		}
		if len(disjuncts) > 0 {
			musts = append(musts, blevelib.NewDisjunctionQuery(disjuncts...))
		}
	}
	if text := strings.TrimSpace(req.TextQuery); text != "" {
		musts = append(musts, blevelib.NewMatchQuery(text))
	}
	if len(musts) == 0 {
		return blevelib.NewMatchAllQuery()
	}
	return blevelib.NewConjunctionQuery(musts...)
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

func versionInRange(version string, versionRange *pb.VersionRange) bool {
	if versionRange == nil {
		return true
	}
	version = factkey.NormalizeVersion(version)
	if start := factkey.NormalizeVersion(versionRange.GetStartVersion()); versionRange.GetStartVersion() != "" && version < start {
		return false
	}
	if end := factkey.NormalizeVersion(versionRange.GetEndVersion()); versionRange.GetEndVersion() != "" && version > end {
		return false
	}
	return true
}

func pageRows(rows []*pb.RecordRow, page *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error) {
	pageNo := uint32(1)
	size := uint32(len(rows))
	if page != nil {
		size = 1000
		if page.GetPage() > 0 {
			pageNo = page.GetPage()
		}
		if page.GetSize() > 0 {
			size = page.GetSize()
		}
	}
	start := int((pageNo - 1) * size)
	if start > len(rows) {
		start = len(rows)
	}
	end := start + int(size)
	if end > len(rows) {
		end = len(rows)
	}
	return rows[start:end], &pb.PageResult{Page: pageNo, Size: size, Total: uint32(len(rows)), HasMore: end < len(rows)}, nil
}
