package bleve

import (
	"context"
	"errors"
	"strings"

	blevelib "github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/mapping"
	blevequery "github.com/blevesearch/bleve/v2/search/query"
	"github.com/mooyang-code/moox/modules/storage/internal/core/factvalue"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/encoding/protojson"
)

type Options struct {
	Path string
}

type Index struct {
	index blevelib.Index
}

type SearchRequest struct {
	SpaceID    string
	DatasetID  string
	SubjectIDs []string
	TextQuery  string
	TimeRange  *pb.TimeRange
	Page       *pb.Page
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

// buildIndexMapping 构造索引映射：
//   - _row_json 仅存储不索引（用于回放完整行）；
//   - space_id / dataset_id / subject_id / freq / data_time 建为不分词 keyword 字段，
//     支持精确 TermQuery，避免查询时 MatchAll 全表扫描后内存过滤。
func buildIndexMapping() mapping.IndexMapping {
	mapping := blevelib.NewIndexMapping()
	docMapping := blevelib.NewDocumentMapping()

	rowMapping := blevelib.NewTextFieldMapping()
	rowMapping.Store = true
	rowMapping.Index = false
	docMapping.AddFieldMappingsAt("_row_json", rowMapping)

	for _, field := range []string{"space_id", "dataset_id", "subject_id", "freq", "data_time"} {
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

func (i *Index) IndexRows(ctx context.Context, rows []*pb.DataRow, textIndexedColumns map[string]bool) error {
	_ = ctx
	batch := i.index.NewBatch()
	for _, row := range rows {
		raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(row)
		if err != nil {
			return err
		}
		doc := map[string]any{
			"space_id":   row.GetKey().GetScope().GetSpaceId(),
			"dataset_id": row.GetKey().GetScope().GetDatasetId(),
			"subject_id": row.GetKey().GetScope().GetSubjectId(),
			"freq":       row.GetKey().GetScope().GetFreq(),
			"data_time":  row.GetKey().GetDataTime(),
			"row_id":     row.GetKey().GetRowId(),
			"_row_json":  string(raw),
		}
		for _, column := range row.GetColumns() {
			if textIndexedColumns[column.GetColumnName()] {
				doc[column.GetColumnName()] = factvalue.String(column.GetValue())
			}
		}
		if err := batch.Index(documentID(row), doc); err != nil {
			return err
		}
	}
	return i.index.Batch(batch)
}

func (i *Index) SearchRows(ctx context.Context, req SearchRequest) ([]*pb.DataRow, *pb.PageResult, error) {
	_ = ctx
	query := buildBooleanQuery(req)
	var rows []*pb.DataRow
	allowSubjects := factvalue.StringSet(req.SubjectIDs)
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
			row := &pb.DataRow{}
			if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(raw), row); err != nil {
				return nil, nil, err
			}
			scope := row.GetKey().GetScope()
			// 兜底校验：复合查询已在索引层过滤 space/dataset/subject，这里再确认一次，
			// 同时处理无法下推到索引的 data_time 范围（跨时区按 RFC3339 解析）。
			if req.SpaceID != "" && scope.GetSpaceId() != req.SpaceID {
				continue
			}
			if req.DatasetID != "" && scope.GetDatasetId() != req.DatasetID {
				continue
			}
			if len(allowSubjects) > 0 && !allowSubjects[scope.GetSubjectId()] {
				continue
			}
			if !factvalue.TimeInRangeClosed(row.GetKey().GetDataTime(), req.TimeRange) {
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

// buildBooleanQuery 把可下推到 Bleve 索引的条件组合成 BooleanQuery：
// space_id / dataset_id / subject_id 作为精确 TermQuery（Must），
// 文本查询作为 MatchQuery（Must）；无任何条件时退化为 MatchAll。
func buildBooleanQuery(req SearchRequest) blevequery.Query {
	var musts []blevequery.Query
	if space := strings.TrimSpace(req.SpaceID); space != "" {
		musts = append(musts, scopeFieldQuery(space, "space_id"))
	}
	if dataset := strings.TrimSpace(req.DatasetID); dataset != "" {
		musts = append(musts, scopeFieldQuery(dataset, "dataset_id"))
	}
	if len(req.SubjectIDs) == 1 {
		if subject := strings.TrimSpace(req.SubjectIDs[0]); subject != "" {
			musts = append(musts, scopeFieldQuery(subject, "subject_id"))
		}
	} else if len(req.SubjectIDs) > 1 {
		// 多 subject 用 OR 子句下推，整体作为一个 Must 条件。
		disjuncts := make([]blevequery.Query, 0, len(req.SubjectIDs))
		for _, subject := range req.SubjectIDs {
			if s := strings.TrimSpace(subject); s != "" {
				disjuncts = append(disjuncts, scopeFieldQuery(s, "subject_id"))
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

func documentID(row *pb.DataRow) string {
	key := row.GetKey()
	scope := key.GetScope()
	return strings.Join([]string{
		scope.GetSpaceId(),
		scope.GetDatasetId(),
		scope.GetSubjectId(),
		scope.GetFreq(),
		factkey.DimensionsHash(scope.GetDimensions()),
		key.GetDataTime(),
		key.GetRowId(),
	}, "/")
}

func typedValueString(value *pb.TypedValue) string {
	return factvalue.String(value)
}

func pageRows(rows []*pb.DataRow, page *pb.Page) ([]*pb.DataRow, *pb.PageResult, error) {
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
	return rows[start:end], &pb.PageResult{Page: pageNo, Size: size, Total: uint64(len(rows)), HasMore: end < len(rows)}, nil
}
