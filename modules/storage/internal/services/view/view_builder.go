package view

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
	searchsvc "github.com/mooyang-code/moox/modules/storage/internal/services/view/search"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/log"
)

// FactReader 定义 View 构建器读取主存 TimeSeries 行所需的接口。
type FactReader interface {
	ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error)
	ScanTimeSeriesRows(ctx context.Context, spaceID string, datasetID string, timeRange *pb.TimeRange, columnNames []string, page *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult, error)
}

// RecordFactReader 定义 View 构建器读取主存 Record 行所需的接口。
type RecordFactReader interface {
	ReadRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error)
	ScanRecordRows(ctx context.Context, spaceID string, datasetID string, versionRange *pb.VersionRange, columnNames []string, page *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error)
}

// ViewWriter 定义 View 构建器写入物化结果所需的接口。
type ViewWriter interface {
	CreateResultTable(ctx context.Context, tableName string, columns []*pb.ViewColumn) error
	InsertRows(ctx context.Context, tableName string, rows []*pb.TimeSeriesRow) error
}

// RecordIndexer 定义 View 构建器写入 Record 搜索索引所需的接口。
type RecordIndexer interface {
	IndexRecordViewRows(ctx context.Context, resultName string, columns []*pb.ViewColumn, rows []*pb.RecordRow) error
}

// Options 保存 View 构建器创建时的依赖与并发配置。
type Options struct {
	Metadata Metadata
	Facts    FactReader
	Records  RecordFactReader
	Views    ViewWriter
	Search   RecordIndexer
	Now      func() time.Time

	OnBuildStarted  func(ctx context.Context, item *pb.View, targetVersion uint64, resultName string)
	BeforeComplete  func(ctx context.Context, item *pb.View, targetVersion uint64, resultName string) error
	OnBuildFinished func(ctx context.Context, item *pb.View, targetVersion uint64, resultName string)
}

// Builder 负责从主存事实行构建版本化 View 结果。
//
// RotateViewIndexes (rotation.go) is the sole scheduled entry for the View
// index lifecycle; Builder.Build/BuildView remain available for direct,
// single-View on-demand use but are no longer wired to any RPC or schedule.
type Builder struct {
	metadata     Metadata
	facts        FactReader
	records      RecordFactReader
	views        ViewWriter
	search       RecordIndexer
	now          func() time.Time
	onStarted    func(ctx context.Context, item *pb.View, targetVersion uint64, resultName string)
	beforeDone   func(ctx context.Context, item *pb.View, targetVersion uint64, resultName string) error
	onFinished   func(ctx context.Context, item *pb.View, targetVersion uint64, resultName string)
	buildMu      sync.Mutex
	activeBuilds map[string]struct{}
}

func NewBuilder(opts Options) *Builder {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	records := opts.Records
	if records == nil {
		if reader, ok := opts.Facts.(RecordFactReader); ok {
			records = reader
		}
	}
	return &Builder{
		metadata:     opts.Metadata,
		facts:        opts.Facts,
		records:      records,
		views:        opts.Views,
		search:       opts.Search,
		now:          now,
		onStarted:    opts.OnBuildStarted,
		beforeDone:   opts.BeforeComplete,
		onFinished:   opts.OnBuildFinished,
		activeBuilds: make(map[string]struct{}),
	}
}

func (b *Builder) Build(ctx context.Context, spaceID string, viewID string) (*pb.View, error) {
	if b == nil || b.metadata == nil {
		return nil, errors.New("metadata is required")
	}
	if spaceID == "" || viewID == "" {
		return nil, errors.New("space_id and view_id are required")
	}
	unlock, ok := b.tryLockView(spaceID, viewID)
	if !ok {
		return nil, errors.New("view build is already running")
	}
	defer unlock()
	return b.buildLocked(ctx, spaceID, viewID)
}

func (b *Builder) buildLocked(ctx context.Context, spaceID string, viewID string) (*pb.View, error) {
	view, err := b.metadata.GetView(ctx, spaceID, viewID)
	if err != nil {
		return nil, err
	}
	if isBleveView(view) {
		return b.buildRecordLocked(ctx, view)
	}
	return b.buildTimeSeriesLocked(ctx, view)
}

func (b *Builder) buildTimeSeriesLocked(ctx context.Context, view *pb.View) (*pb.View, error) {
	spaceID := view.GetSpaceId()
	viewID := view.GetViewId()
	if b.facts == nil || b.views == nil {
		return nil, errors.New("facts and views are required")
	}
	if !isDuckDBView(view) {
		return nil, fmt.Errorf("view %s/%s engine %q is not supported by time series builder", spaceID, viewID, view.GetEngine())
	}
	primaryDatasetID := view.GetPrimaryDatasetId()
	if primaryDatasetID == "" && len(view.GetDatasetIds()) > 0 {
		primaryDatasetID = view.GetDatasetIds()[0]
	}
	if primaryDatasetID == "" {
		return nil, errors.New("view primary_dataset_id is required")
	}
	dataset, err := b.metadata.GetDataset(ctx, spaceID, primaryDatasetID)
	if err != nil {
		return nil, err
	}
	if dataset.GetDataKind() != pb.DataKind_DATA_KIND_TIME_SERIES {
		return nil, fmt.Errorf("view %s/%s primary dataset %s is not time series", spaceID, viewID, primaryDatasetID)
	}
	columns, _, err := b.metadata.ListViewColumns(ctx, spaceID, viewID, nil)
	if err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		columns = view.GetColumns()
	}
	if len(columns) == 0 {
		return nil, errors.New("view columns are required")
	}
	targetVersion := view.GetViewVersion()
	if targetVersion == 0 {
		targetVersion = 1
	}
	resultName := resultTableName(spaceID, viewID, targetVersion, b.now())
	if _, err := b.metadata.BeginViewBuild(ctx, spaceID, viewID, targetVersion, resultName); err != nil {
		return nil, err
	}
	if b.onStarted != nil {
		b.onStarted(ctx, view, targetVersion, resultName)
	}
	if b.onFinished != nil {
		defer b.onFinished(ctx, view, targetVersion, resultName)
	}
	if err := b.views.CreateResultTable(ctx, resultName, columns); err != nil {
		_ = b.metadata.FailViewBuild(ctx, spaceID, viewID, targetVersion, resultName, err)
		return nil, err
	}
	rows, err := b.readViewRows(ctx, view, primaryDatasetID, columns)
	if err != nil {
		_ = b.metadata.FailViewBuild(ctx, spaceID, viewID, targetVersion, resultName, err)
		return nil, err
	}
	if err := b.views.InsertRows(ctx, resultName, rows); err != nil {
		_ = b.metadata.FailViewBuild(ctx, spaceID, viewID, targetVersion, resultName, err)
		return nil, err
	}
	if b.beforeDone != nil {
		if err := b.beforeDone(ctx, view, targetVersion, resultName); err != nil {
			_ = b.metadata.FailViewBuild(ctx, spaceID, viewID, targetVersion, resultName, err)
			return nil, err
		}
	}
	if err := b.metadata.CompleteViewBuild(ctx, spaceID, viewID, targetVersion, resultName); err != nil {
		return nil, err
	}
	return b.metadata.GetView(ctx, spaceID, viewID)
}

func (b *Builder) buildRecordLocked(ctx context.Context, view *pb.View) (*pb.View, error) {
	spaceID := view.GetSpaceId()
	viewID := view.GetViewId()
	if b.records == nil || b.search == nil {
		return nil, errors.New("records and search are required")
	}
	if !isBleveView(view) {
		return nil, fmt.Errorf("view %s/%s engine %q is not supported by record builder", spaceID, viewID, view.GetEngine())
	}
	primaryDatasetID := view.GetPrimaryDatasetId()
	if primaryDatasetID == "" && len(view.GetDatasetIds()) > 0 {
		primaryDatasetID = view.GetDatasetIds()[0]
	}
	if primaryDatasetID == "" {
		return nil, errors.New("view primary_dataset_id is required")
	}
	dataset, err := b.metadata.GetDataset(ctx, spaceID, primaryDatasetID)
	if err != nil {
		return nil, err
	}
	if dataset.GetDataKind() != pb.DataKind_DATA_KIND_RECORD {
		return nil, fmt.Errorf("view %s/%s primary dataset %s is not record", spaceID, viewID, primaryDatasetID)
	}
	columns, _, err := b.metadata.ListViewColumns(ctx, spaceID, viewID, nil)
	if err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		columns = view.GetColumns()
	}
	if len(columns) == 0 {
		return nil, errors.New("view columns are required")
	}
	if !IsProjectableRecordView(view, columns) {
		return nil, fmt.Errorf("record view %s/%s contains unsupported columns for bleve projection", spaceID, viewID)
	}
	targetVersion := view.GetViewVersion()
	if targetVersion == 0 {
		targetVersion = 1
	}
	resultName := searchsvc.RecordIndexName(spaceID, viewID, targetVersion, b.now().UTC())
	if _, err := b.metadata.BeginViewBuild(ctx, spaceID, viewID, targetVersion, resultName); err != nil {
		return nil, err
	}
	if err := b.search.IndexRecordViewRows(ctx, resultName, columns, nil); err != nil {
		_ = b.metadata.FailViewBuild(ctx, spaceID, viewID, targetVersion, resultName, err)
		return nil, err
	}
	rows, err := b.readRecordViewRows(ctx, view, primaryDatasetID, columns)
	if err != nil {
		_ = b.metadata.FailViewBuild(ctx, spaceID, viewID, targetVersion, resultName, err)
		return nil, err
	}
	if err := b.search.IndexRecordViewRows(ctx, resultName, columns, rows); err != nil {
		_ = b.metadata.FailViewBuild(ctx, spaceID, viewID, targetVersion, resultName, err)
		return nil, err
	}
	if err := b.metadata.CompleteViewBuild(ctx, spaceID, viewID, targetVersion, resultName); err != nil {
		return nil, err
	}
	return b.metadata.GetView(ctx, spaceID, viewID)
}

func (b *Builder) BuildView(ctx context.Context, spaceID string, viewID string) (*pb.View, error) {
	return b.Build(ctx, spaceID, viewID)
}

func (b *Builder) tryLockView(spaceID string, viewID string) (func(), bool) {
	key := spaceID + "|" + viewID
	b.buildMu.Lock()
	if b.activeBuilds == nil {
		b.activeBuilds = make(map[string]struct{})
	}
	if _, ok := b.activeBuilds[key]; ok {
		b.buildMu.Unlock()
		return nil, false
	}
	b.activeBuilds[key] = struct{}{}
	b.buildMu.Unlock()

	return func() {
		b.buildMu.Lock()
		delete(b.activeBuilds, key)
		b.buildMu.Unlock()
	}, true
}

func isDuckDBView(view *pb.View) bool {
	return view != nil && (strings.TrimSpace(view.GetEngine()) == "" || strings.EqualFold(view.GetEngine(), "duckdb"))
}

func isBleveView(view *pb.View) bool {
	return view != nil && strings.EqualFold(strings.TrimSpace(view.GetEngine()), "bleve")
}

func (b *Builder) readRecordViewRows(ctx context.Context, view *pb.View, primaryDatasetID string, columns []*pb.ViewColumn) ([]*pb.RecordRow, error) {
	sourceColumns := sourceColumnNamesByDataset(primaryDatasetID, columns)
	var out []*pb.RecordRow
	cursor := ""
	for {
		rows, page, err := b.records.ScanRecordRows(ctx, view.GetSpaceId(), primaryDatasetID, nil, sourceColumns[primaryDatasetID], &pb.Page{Size: rebuildViewPageSize, Cursor: cursor})
		if err != nil {
			return nil, err
		}
		if len(rows) > 0 {
			projected, ok, err := RecordRowsForView(ctx, view, columns, rows, b.readRecordProjectionRow)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, fmt.Errorf("record view %s/%s contains unsupported columns for bleve projection", view.GetSpaceId(), view.GetViewId())
			}
			out = append(out, projected...)
		}
		if page == nil || !page.GetHasMore() || page.GetNextCursor() == "" {
			break
		}
		cursor = page.GetNextCursor()
	}
	return out, nil
}

func (b *Builder) readRecordProjectionRow(ctx context.Context, base *pb.RecordKey, datasetID string) (*pb.RecordRow, error) {
	if base == nil {
		return nil, nil
	}
	key := proto.Clone(base).(*pb.RecordKey)
	key.DatasetId = datasetID
	rsp, err := b.records.ReadRecordRows(ctx, &pb.ReadRecordRowsReq{Keys: []*pb.RecordKey{key}})
	if err != nil {
		return nil, err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return nil, errors.New(rsp.GetRetInfo().GetMsg())
	}
	if len(rsp.GetRows()) == 0 {
		return nil, nil
	}
	return rsp.GetRows()[0], nil
}

func (b *Builder) readViewRows(ctx context.Context, view *pb.View, primaryDatasetID string, columns []*pb.ViewColumn) ([]*pb.TimeSeriesRow, error) {
	spaceID := view.GetSpaceId()
	datasetIDs := viewDatasetIDs(primaryDatasetID, view.GetDatasetIds(), columns)
	sourceColumns := sourceColumnNamesByDataset(primaryDatasetID, columns)
	timeRange := buildTimeRange(b.now(), view.GetQueryWindow())

	rowsByDataset := make(map[string][]*pb.TimeSeriesRow, len(datasetIDs))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(viewBuildConcurrency)
	var mu sync.Mutex
	for _, datasetID := range datasetIDs {
		datasetID := datasetID
		group.Go(func() error {
			rows, err := b.readAllRows(groupCtx, spaceID, datasetID, timeRange, sourceColumns[datasetID])
			if err != nil {
				return err
			}
			rows, err = filterRowsByViewJSON(view, rows)
			if err != nil {
				return err
			}
			mu.Lock()
			rowsByDataset[datasetID] = rows
			mu.Unlock()
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	indexByDataset := make(map[string]map[string]*pb.TimeSeriesRow, len(datasetIDs))
	for _, datasetID := range datasetIDs {
		indexByDataset[datasetID] = indexRowsByGrain(rowsByDataset[datasetID], view.GetGrainKeys())
	}
	var out []*pb.TimeSeriesRow
	for _, primaryRow := range rowsByDataset[primaryDatasetID] {
		key := rowGrainKey(primaryRow, view.GetGrainKeys())
		rowSet := make(map[string]*pb.TimeSeriesRow, len(datasetIDs))
		rowSet[primaryDatasetID] = primaryRow
		for _, datasetID := range datasetIDs {
			if datasetID == primaryDatasetID {
				continue
			}
			if row := indexByDataset[datasetID][key]; row != nil {
				rowSet[datasetID] = row
			}
		}
		mapped := mapViewValues(rowSet, primaryDatasetID, columns)
		out = append(out, &pb.TimeSeriesRow{
			Key:        proto.Clone(primaryRow.GetKey()).(*pb.TimeSeriesKey),
			Columns:    mapped,
			Attributes: cloneStringMap(primaryRow.GetAttributes()),
		})
	}
	return out, nil
}

// viewBuildConcurrency 限制 View 构建时并行扫描 Dataset 数，避免压垮 PrimaryStore。
const viewBuildConcurrency = 8
const rebuildViewPageSize = uint32(1000)

func (b *Builder) readAllRows(ctx context.Context, spaceID string, datasetID string, timeRange *pb.TimeRange, columnNames []string) ([]*pb.TimeSeriesRow, error) {
	var out []*pb.TimeSeriesRow
	cursor := ""
	for {
		rows, page, err := b.facts.ScanTimeSeriesRows(ctx, spaceID, datasetID, timeRange, columnNames, &pb.Page{Size: rebuildViewPageSize, Cursor: cursor})
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
		if page == nil || !page.GetHasMore() || page.GetNextCursor() == "" {
			break
		}
		cursor = page.GetNextCursor()
	}
	return out, nil
}

type fixedViewFilter struct {
	SpaceID    string            `json:"space_id"`
	SubjectID  string            `json:"subject_id"`
	Freq       string            `json:"freq"`
	Dimensions map[string]string `json:"dimensions"`
}

func parseFixedViewFilter(raw string) (*fixedViewFilter, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return nil, nil
	}
	var filter fixedViewFilter
	if err := json.Unmarshal([]byte(raw), &filter); err != nil {
		return nil, fmt.Errorf("invalid view filter_json: %w", err)
	}
	if filter.SpaceID == "" && filter.SubjectID == "" && filter.Freq == "" && len(filter.Dimensions) == 0 {
		return nil, nil
	}
	return &filter, nil
}

func filterRowsByViewJSON(view *pb.View, rows []*pb.TimeSeriesRow) ([]*pb.TimeSeriesRow, error) {
	filter, err := parseFixedViewFilter(view.GetFilterJson())
	if err != nil {
		return nil, err
	}
	if filter == nil {
		return rows, nil
	}
	out := make([]*pb.TimeSeriesRow, 0, len(rows))
	for _, row := range rows {
		if fixedViewFilterMatchesRow(filter, row) {
			out = append(out, row)
		}
	}
	return out, nil
}

func fixedViewFilterMatchesRow(filter *fixedViewFilter, row *pb.TimeSeriesRow) bool {
	if row == nil || row.GetKey() == nil {
		return false
	}
	key := row.GetKey()
	if filter.SpaceID != "" && filter.SpaceID != key.GetSpaceId() {
		return false
	}
	if filter.SubjectID != "" && filter.SubjectID != key.GetSubjectId() {
		return false
	}
	if filter.Freq != "" && filter.Freq != key.GetFreq() {
		return false
	}
	for name, value := range filter.Dimensions {
		if key.GetDimensions()[name] != value {
			return false
		}
	}
	return true
}

func mapViewValues(rowsByDataset map[string]*pb.TimeSeriesRow, primaryDatasetID string, columns []*pb.ViewColumn) []*pb.ColumnValue {
	valuesByDataset := make(map[string]map[string]*pb.ColumnValue, len(rowsByDataset))
	for datasetID, row := range rowsByDataset {
		values := make(map[string]*pb.ColumnValue, len(row.GetColumns()))
		for _, column := range row.GetColumns() {
			values[column.GetColumnName()] = column
		}
		valuesByDataset[datasetID] = values
	}
	out := make([]*pb.ColumnValue, 0, len(columns))
	for _, viewColumn := range columns {
		datasetID := originDatasetID(primaryDatasetID, viewColumn)
		sourceName := sourceColumnName(datasetID, viewColumn)
		source, ok := valuesByDataset[datasetID][sourceName]
		if !ok {
			out = append(out, &pb.ColumnValue{ColumnName: viewColumn.GetColumnName(), ValueType: viewColumn.GetValueType()})
			continue
		}
		copied := proto.Clone(source).(*pb.ColumnValue)
		copied.ColumnName = viewColumn.GetColumnName()
		if copied.ValueType == pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED {
			copied.ValueType = viewColumn.GetValueType()
		}
		out = append(out, copied)
	}
	return out
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func sourceColumnNamesByDataset(primaryDatasetID string, columns []*pb.ViewColumn) map[string][]string {
	seen := make(map[string]map[string]bool)
	out := make(map[string][]string)
	for _, column := range columns {
		datasetID := originDatasetID(primaryDatasetID, column)
		name := sourceColumnName(datasetID, column)
		if name == "" {
			continue
		}
		if seen[datasetID] == nil {
			seen[datasetID] = make(map[string]bool)
		}
		if seen[datasetID][name] {
			continue
		}
		seen[datasetID][name] = true
		out[datasetID] = append(out[datasetID], name)
	}
	return out
}

func sourceColumnName(datasetID string, column *pb.ViewColumn) string {
	if column.GetOriginType() == pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN {
		originID := column.GetOriginId()
		prefix := datasetID + "."
		if strings.HasPrefix(originID, prefix) {
			return strings.TrimPrefix(originID, prefix)
		}
		if idx := strings.LastIndex(originID, "."); idx >= 0 {
			return originID[idx+1:]
		}
		if originID != "" {
			return originID
		}
	}
	return column.GetColumnName()
}

func originDatasetID(primaryDatasetID string, column *pb.ViewColumn) string {
	originID := column.GetOriginId()
	if column.GetOriginType() == pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN {
		if idx := strings.LastIndex(originID, "."); idx > 0 {
			return originID[:idx]
		}
	}
	return primaryDatasetID
}

func viewDatasetIDs(primaryDatasetID string, datasetIDs []string, columns []*pb.ViewColumn) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(datasetID string) {
		if datasetID == "" || seen[datasetID] {
			return
		}
		seen[datasetID] = true
		out = append(out, datasetID)
	}
	add(primaryDatasetID)
	for _, datasetID := range datasetIDs {
		add(datasetID)
	}
	for _, column := range columns {
		add(originDatasetID(primaryDatasetID, column))
	}
	return out
}

func indexRowsByGrain(rows []*pb.TimeSeriesRow, grainKeys []string) map[string]*pb.TimeSeriesRow {
	out := make(map[string]*pb.TimeSeriesRow, len(rows))
	for _, row := range rows {
		out[rowGrainKey(row, grainKeys)] = row
	}
	return out
}

func rowGrainKey(row *pb.TimeSeriesRow, grainKeys []string) string {
	if len(grainKeys) == 0 {
		grainKeys = []string{"subject_id", "data_time", "freq", "dimensions"}
	}
	parts := make([]string, 0, len(grainKeys))
	for _, key := range grainKeys {
		switch key {
		case "subject_id":
			parts = append(parts, grainPart("subject_id", row.GetKey().GetSubjectId()))
		case "data_time":
			parts = append(parts, grainPart("data_time", row.GetKey().GetDataTime()))
		case "freq":
			parts = append(parts, grainPart("freq", row.GetKey().GetFreq()))
		case "dimensions":
			parts = append(parts, grainPart("dimensions", factkey.DimensionsHash(row.GetKey().GetDimensions())))
		default:
			if strings.HasPrefix(key, "dimension.") {
				name := strings.TrimPrefix(key, "dimension.")
				parts = append(parts, grainPart(key, row.GetKey().GetDimensions()[name]))
			}
		}
	}
	return strings.Join(parts, "|")
}

func grainPart(name string, value string) string {
	return fmt.Sprintf("%s:%d:%s", name, len(value), value)
}

func buildTimeRange(now time.Time, queryWindow string) *pb.TimeRange {
	duration, ok := parseWindow(queryWindow)
	if !ok {
		return nil
	}
	start := now.Add(-duration).UTC().Format(time.RFC3339)
	return &pb.TimeRange{StartTime: start}
}

func parseWindow(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	unit := value[len(value)-1:]
	number := strings.TrimSpace(value[:len(value)-1])
	var count int
	if _, err := fmt.Sscanf(number, "%d", &count); err != nil || count <= 0 {
		return 0, false
	}
	switch unit {
	case "d", "D":
		return time.Duration(count) * 24 * time.Hour, true
	case "h", "H":
		return time.Duration(count) * time.Hour, true
	case "m", "M":
		return time.Duration(count) * time.Minute, true
	default:
		return 0, false
	}
}

var invalidTableChar = regexp.MustCompile(`[^A-Za-z0-9_]+`)

func resultTableName(spaceID string, viewID string, viewVersion uint64, now time.Time) string {
	if viewVersion == 0 {
		viewVersion = 1
	}
	viewPart := sanitizeResultTableName(viewID)
	if viewPart == "" {
		viewPart = "view"
	}
	spacePart := encodeResultTablePart(spaceID)
	raw := fmt.Sprintf("view_%s_s%s_v%d_%s_%d", viewPart, spacePart, viewVersion, "r", now.UnixNano())
	name := sanitizeResultTableName(raw)
	if name == "" {
		return "view_result"
	}
	return name
}

func encodeResultTablePart(value string) string {
	encoded := hex.EncodeToString([]byte(value))
	if encoded == "" {
		return "00"
	}
	return encoded
}

func sanitizeResultTableName(raw string) string {
	name := invalidTableChar.ReplaceAllString(raw, "_")
	name = strings.Trim(name, "_")
	if name == "" {
		return ""
	}
	if first := name[0]; (first < 'A' || first > 'Z') && (first < 'a' || first > 'z') && first != '_' {
		name = "view_result_" + name
	}
	return name
}

// ---------------------------------------------------------------------------
// Task 7: PrimaryStore backfill for warming View indexes.
//
// NewPrimaryStoreBackfill implements the BackfillFunc seam declared in
// rotation.go with a real PrimaryStore-backed backfill: it scans bounded
// windows via ScanTimeSeriesRows/ScanRecordRows, projects each page through
// the same TimeSeriesRowsForView/RecordRowsForView helpers used by the
// incremental dual-write path (builder/time_series.go, builder/record.go),
// and writes pages straight into the warming ViewIndexEngine. The full
// backfill window is never accumulated in memory.
// ---------------------------------------------------------------------------

// backfillPageSize bounds the PrimaryStore scan page size used per
// engine.Write call during PrimaryStore backfill.
const backfillPageSize = uint32(1000)

// PrimaryStoreBackfillOptions configures NewPrimaryStoreBackfill.
type PrimaryStoreBackfillOptions struct {
	// Metadata re-checks building validity before each page write and
	// resolves View columns and Dataset freqs.
	Metadata Metadata
	// Facts backfills TimeSeries Views from PrimaryStore.
	Facts FactReader
	// Records backfills Record Views from PrimaryStore.
	Records RecordFactReader
	// Config controls backfill window and safety-cap thresholds.
	Config RotationConfig
	// Now returns the current time; defaults to time.Now.
	Now func() time.Time
}

// NewPrimaryStoreBackfill returns a BackfillFunc that backfills a warming
// View index from PrimaryStore. It is the Task 7 production replacement
// for rotation.go's stubBackfill.
func NewPrimaryStoreBackfill(opts PrimaryStoreBackfillOptions) BackfillFunc {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	b := &primaryStoreBackfill{
		metadata: opts.Metadata,
		facts:    opts.Facts,
		records:  opts.Records,
		cfg:      opts.Config,
		now:      now,
	}
	return b.backfill
}

// primaryStoreBackfill implements the Task 7 PrimaryStore backfill for both
// TimeSeries (DuckDB) and Record (Bleve) warming View indexes.
type primaryStoreBackfill struct {
	metadata Metadata
	facts    FactReader
	records  RecordFactReader
	cfg      RotationConfig
	now      func() time.Time
}

func (b *primaryStoreBackfill) backfill(ctx context.Context, engine viewindex.ViewIndexEngine, item *pb.View, indexID string) (bool, error) {
	if item == nil || indexID == "" {
		return false, nil
	}
	if isBleveView(item) {
		return b.backfillRecordView(ctx, engine, item, indexID)
	}
	return b.backfillTimeSeriesView(ctx, engine, item, indexID)
}

// backfillTimeSeriesView scans the effective TimeSeries backfill window
// from PrimaryStore page by page and writes each projected page directly
// into the warming DuckDB index, without ever holding the full window in
// memory. Before each page write it re-checks the View's building pointer
// so a concurrent schema bump, capacity restart, or stale-claim cleanup
// stops the backfill without ever completing.
func (b *primaryStoreBackfill) backfillTimeSeriesView(ctx context.Context, engine viewindex.ViewIndexEngine, item *pb.View, indexID string) (bool, error) {
	if b.facts == nil {
		return false, errors.New("primary store backfill requires a FactReader for time series views")
	}
	spaceID, viewID := item.GetSpaceId(), item.GetViewId()
	targetVersion := item.GetBuildingViewVersion()
	primaryDatasetID := primaryDatasetIDOf(item)
	if primaryDatasetID == "" {
		return false, errors.New("view primary_dataset_id is required for backfill")
	}
	columns, err := b.viewColumns(ctx, item)
	if err != nil {
		return false, err
	}
	window := b.timeSeriesBackfillWindow(ctx, item, primaryDatasetID)
	timeRange := &pb.TimeRange{StartTime: b.now().Add(-window).UTC().Format(time.RFC3339)}
	sourceColumns := sourceColumnNamesByDataset(primaryDatasetID, columns)[primaryDatasetID]

	cursor := ""
	for {
		if !b.buildingStillValid(ctx, spaceID, viewID, targetVersion, indexID) {
			return false, nil
		}
		rows, page, err := b.facts.ScanTimeSeriesRows(ctx, spaceID, primaryDatasetID, timeRange, sourceColumns, &pb.Page{Size: backfillPageSize, Cursor: cursor})
		if err != nil {
			return false, err
		}
		if len(rows) > 0 {
			projected, ok, err := TimeSeriesRowsForView(ctx, item, columns, rows, b.readTimeSeriesProjectionRow)
			if err != nil {
				return false, err
			}
			if ok && len(projected) > 0 {
				if err := engine.Write(ctx, indexID, viewindex.ViewIndexBatch{TimeSeriesRows: projected, Columns: columns}); err != nil {
					return false, err
				}
			}
		}
		if page == nil || !page.GetHasMore() || page.GetNextCursor() == "" {
			return true, nil
		}
		cursor = page.GetNextCursor()
	}
}

func (b *primaryStoreBackfill) readTimeSeriesProjectionRow(ctx context.Context, base *pb.TimeSeriesKey, datasetID string) (*pb.TimeSeriesRow, error) {
	if base == nil {
		return nil, nil
	}
	key := proto.Clone(base).(*pb.TimeSeriesKey)
	key.DatasetId = datasetID
	rsp, err := b.facts.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{Keys: []*pb.TimeSeriesKey{key}})
	if err != nil {
		return nil, err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return nil, errors.New(rsp.GetRetInfo().GetMsg())
	}
	if len(rsp.GetRows()) == 0 {
		return nil, nil
	}
	return rsp.GetRows()[0], nil
}

// timeSeriesBackfillWindow computes the effective TimeSeries backfill
// window: max(view.query_window, rotation.default_backfill_window,
// rotation.time_series.freq_backfill_window[freq] for each Dataset freq,
// rotation.overlap_window).
func (b *primaryStoreBackfill) timeSeriesBackfillWindow(ctx context.Context, item *pb.View, primaryDatasetID string) time.Duration {
	window := maxDuration(parseWindowOrZero(item.GetQueryWindow()), b.cfg.DefaultBackfillWindow)
	for _, freq := range b.datasetFreqs(ctx, item.GetSpaceId(), primaryDatasetID) {
		if d, ok := b.cfg.TimeSeriesFreqBackfillWindow[freq]; ok {
			window = maxDuration(window, d)
		}
	}
	return maxDuration(window, b.cfg.OverlapWindow)
}

func (b *primaryStoreBackfill) datasetFreqs(ctx context.Context, spaceID string, datasetID string) []string {
	if b.metadata == nil {
		return nil
	}
	dataset, err := b.metadata.GetDataset(ctx, spaceID, datasetID)
	if err != nil || dataset == nil {
		return nil
	}
	return dataset.GetFreqs()
}

// backfillRecordView scans the effective Record backfill version window
// from PrimaryStore page by page and writes each projected page directly
// into the warming Bleve index. Non-timestamp Record versions cannot be
// windowed reliably by a version range filter, so the scan is additionally
// capped at record.max_backfill_entries with a warning log instead of
// trying to infer full history from Bleve.
func (b *primaryStoreBackfill) backfillRecordView(ctx context.Context, engine viewindex.ViewIndexEngine, item *pb.View, indexID string) (bool, error) {
	if b.records == nil {
		return false, errors.New("primary store backfill requires a RecordFactReader for record views")
	}
	spaceID, viewID := item.GetSpaceId(), item.GetViewId()
	targetVersion := item.GetBuildingViewVersion()
	primaryDatasetID := primaryDatasetIDOf(item)
	if primaryDatasetID == "" {
		return false, errors.New("view primary_dataset_id is required for backfill")
	}
	columns, err := b.viewColumns(ctx, item)
	if err != nil {
		return false, err
	}
	window := b.recordBackfillWindow(item)
	now := b.now().UTC()
	versionRange := &pb.VersionRange{
		StartVersion: now.Add(-window).Format(time.RFC3339),
		EndVersion:   now.Format(time.RFC3339),
	}
	sourceColumns := sourceColumnNamesByDataset(primaryDatasetID, columns)[primaryDatasetID]

	maxEntries := b.cfg.RecordMaxBackfillEntries
	var scanned int64
	cursor := ""
	for {
		if !b.buildingStillValid(ctx, spaceID, viewID, targetVersion, indexID) {
			return false, nil
		}
		rows, page, err := b.records.ScanRecordRows(ctx, spaceID, primaryDatasetID, versionRange, sourceColumns, &pb.Page{Size: backfillPageSize, Cursor: cursor})
		if err != nil {
			return false, err
		}
		scanned += int64(len(rows))
		if len(rows) > 0 {
			projected, ok, err := RecordRowsForView(ctx, item, columns, rows, b.readRecordProjectionRow)
			if err != nil {
				return false, err
			}
			if ok && len(projected) > 0 {
				if err := engine.Write(ctx, indexID, viewindex.ViewIndexBatch{RecordRows: projected, Columns: columns}); err != nil {
					return false, err
				}
			}
		}
		if maxEntries > 0 && scanned >= maxEntries {
			log.WarnContextf(ctx, "[ViewBuilder] record view %s/%s backfill hit record.max_backfill_entries=%d before the scan completed; treating as done and assuming non-timestamp record versions instead of inferring full history from Bleve", spaceID, viewID, maxEntries)
			return true, nil
		}
		if page == nil || !page.GetHasMore() || page.GetNextCursor() == "" {
			return true, nil
		}
		cursor = page.GetNextCursor()
	}
}

func (b *primaryStoreBackfill) readRecordProjectionRow(ctx context.Context, base *pb.RecordKey, datasetID string) (*pb.RecordRow, error) {
	if base == nil {
		return nil, nil
	}
	key := proto.Clone(base).(*pb.RecordKey)
	key.DatasetId = datasetID
	rsp, err := b.records.ReadRecordRows(ctx, &pb.ReadRecordRowsReq{Keys: []*pb.RecordKey{key}})
	if err != nil {
		return nil, err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return nil, errors.New(rsp.GetRetInfo().GetMsg())
	}
	if len(rsp.GetRows()) == 0 {
		return nil, nil
	}
	return rsp.GetRows()[0], nil
}

// recordBackfillWindow computes the effective Record backfill window:
// max(view.query_window, rotation.record.default_version_window,
// rotation.overlap_window).
func (b *primaryStoreBackfill) recordBackfillWindow(item *pb.View) time.Duration {
	window := maxDuration(parseWindowOrZero(item.GetQueryWindow()), b.cfg.RecordDefaultVersionWindow)
	return maxDuration(window, b.cfg.OverlapWindow)
}

// buildingStillValid re-checks the View's building pointer immediately
// before each page write so a concurrent schema bump, capacity restart, or
// stale-claim cleanup stops this backfill claim without ever calling
// CompleteViewBuild on a pointer that no longer matches.
func (b *primaryStoreBackfill) buildingStillValid(ctx context.Context, spaceID string, viewID string, targetVersion uint64, indexID string) bool {
	if b.metadata == nil {
		return true
	}
	current, err := b.metadata.GetView(ctx, spaceID, viewID)
	if err != nil || current == nil {
		return false
	}
	return current.GetBuildingResult() == indexID &&
		current.GetBuildingViewVersion() == targetVersion &&
		current.GetViewVersion() == targetVersion &&
		current.GetBuildStatus() == "building"
}

func (b *primaryStoreBackfill) viewColumns(ctx context.Context, item *pb.View) ([]*pb.ViewColumn, error) {
	if b.metadata == nil {
		return item.GetColumns(), nil
	}
	columns, _, err := b.metadata.ListViewColumns(ctx, item.GetSpaceId(), item.GetViewId(), &pb.Page{Size: 10000})
	if err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		columns = item.GetColumns()
	}
	return columns, nil
}

// primaryDatasetIDOf resolves a View's primary Dataset ID, falling back to
// the first entry of dataset_ids when primary_dataset_id is unset.
func primaryDatasetIDOf(item *pb.View) string {
	if item.GetPrimaryDatasetId() != "" {
		return item.GetPrimaryDatasetId()
	}
	if len(item.GetDatasetIds()) > 0 {
		return item.GetDatasetIds()[0]
	}
	return ""
}

func parseWindowOrZero(value string) time.Duration {
	d, ok := parseWindow(value)
	if !ok {
		return 0
	}
	return d
}

func maxDuration(a time.Duration, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
