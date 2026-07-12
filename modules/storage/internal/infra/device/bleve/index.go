package bleve

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"github.com/mooyang-code/moox/modules/storage/internal/core/factvalue"
	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	"github.com/mooyang-code/moox/modules/storage/internal/core/factkey"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/encoding/protojson"
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
	ViewVersion uint64 `json:"view_version"`
	EntryCount  int64  `json:"entry_count"`
	MinVersion  string `json:"min_version"`
	MaxVersion  string `json:"max_version"`
	SchemaHash  string `json:"schema_hash"`
	UpdatedAt   string `json:"updated_at"`
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

func (i *Index) SetSchema(ctx context.Context, viewVersion uint64, schemaHash string) error {
	_ = ctx
	i.writeMu.Lock()
	defer i.writeMu.Unlock()
	stats, err := i.readStats()
	if err != nil {
		return err
	}
	stats.ViewVersion = viewVersion
	stats.SchemaHash = schemaHash
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
	_ = ctx
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
		raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(row)
		if err != nil {
			return err
		}
		key := row.GetKey()
		version := factkey.NormalizeVersion(key.GetVersion())
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
			if text := factvalue.String(column.GetValue()); text != "" {
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
		docID := documentID(row)
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

func (i *Index) Stat(ctx context.Context) (viewindex.ViewIndexStats, error) {
	_ = ctx
	stats, err := i.readStats()
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	return viewindex.ViewIndexStats{
		Exists:      true,
		ViewVersion: stats.ViewVersion,
		EntryCount:  stats.EntryCount,
		MinVersion:  stats.MinVersion,
		MaxVersion:  stats.MaxVersion,
		SchemaHash:  stats.SchemaHash,
		UpdatedAt:   stats.UpdatedAt,
	}, nil
}

func (i *Index) SearchRecordRows(ctx context.Context, req SearchRequest) ([]*pb.RecordRow, *pb.PageResult, error) {
	_ = ctx
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
	return factkey.NormalizeVersion(value)
}

func buildRecordKeysQuery(keys []*pb.RecordKey) blevequery.Query {
	disjuncts := make([]blevequery.Query, 0, len(keys))
	for _, key := range keys {
		if key == nil || strings.TrimSpace(key.GetRecordId()) == "" {
			continue
		}
		musts := []blevequery.Query{scopeFieldQuery(strings.TrimSpace(key.GetRecordId()), "record_id")}
		if version := strings.TrimSpace(key.GetVersion()); version != "" {
			musts = append(musts, termQuery(factkey.NormalizeVersion(version), "version"))
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
		pattern := wildcardLiteral(factvalue.String(expected))
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
		return wildcardQuery("*"+wildcardLiteral(factvalue.String(expected))+"*", indexField), nil
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
	if number, ok := factvalue.Numeric(value); ok {
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
		if normalized, err := factkey.NormalizeTimeVersion(typed.TimeValue); err == nil {
			return normalized
		}
	}
	return factvalue.String(value)
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
