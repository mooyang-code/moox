package bleve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	blevelib "github.com/blevesearch/bleve/v2"
	blevequery "github.com/blevesearch/bleve/v2/search/query"
	"github.com/mooyang-code/moox/modules/storage/internal/services/device/factkey"
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

func Open(opts Options) (*Index, error) {
	if opts.Path == "" {
		return nil, errors.New("bleve path is required")
	}
	mapping := blevelib.NewIndexMapping()
	docMapping := blevelib.NewDocumentMapping()
	rowMapping := blevelib.NewTextFieldMapping()
	rowMapping.Store = true
	rowMapping.Index = false
	docMapping.AddFieldMappingsAt("_row_json", rowMapping)
	mapping.DefaultMapping = docMapping

	index, err := blevelib.Open(opts.Path)
	if err != nil {
		index, err = blevelib.New(opts.Path, mapping)
	}
	if err != nil {
		return nil, err
	}
	return &Index{index: index}, nil
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
				doc[column.GetColumnName()] = typedValueString(column.GetValue())
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
	var query blevequery.Query = blevelib.NewMatchAllQuery()
	if strings.TrimSpace(req.TextQuery) != "" {
		query = blevelib.NewMatchQuery(req.TextQuery)
	}
	searchReq := blevelib.NewSearchRequestOptions(query, 10000, 0, false)
	searchReq.Fields = []string{"_row_json"}
	result, err := i.index.Search(searchReq)
	if err != nil {
		return nil, nil, err
	}
	var rows []*pb.DataRow
	allowSubjects := stringSet(req.SubjectIDs)
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
		if req.SpaceID != "" && scope.GetSpaceId() != req.SpaceID {
			continue
		}
		if req.DatasetID != "" && scope.GetDatasetId() != req.DatasetID {
			continue
		}
		if len(allowSubjects) > 0 && !allowSubjects[scope.GetSubjectId()] {
			continue
		}
		if !timeInRange(row.GetKey().GetDataTime(), req.TimeRange) {
			continue
		}
		rows = append(rows, row)
	}
	return pageRows(rows, req.Page)
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
		return string(v.BytesValue)
	case *pb.TypedValue_ListValue:
		raw, _ := json.Marshal(v.ListValue)
		return string(raw)
	default:
		return ""
	}
}

func stringSet(values []string) map[string]bool {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func timeInRange(value string, timeRange *pb.TimeRange) bool {
	if timeRange == nil {
		return true
	}
	if start := strings.TrimSpace(timeRange.GetStartTime()); start != "" && value < start {
		return false
	}
	if end := strings.TrimSpace(timeRange.GetEndTime()); end != "" && value > end {
		return false
	}
	return true
}

func pageRows(rows []*pb.DataRow, page *pb.Page) ([]*pb.DataRow, *pb.PageResult, error) {
	pageNo := uint32(1)
	size := uint32(1000)
	if page != nil {
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
