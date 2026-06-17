package view

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/metadata"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

type FactReader interface {
	ReadRows(ctx context.Context, scope *pb.DataScope, mode pb.ReadMode, timeRange *pb.TimeRange, snapshotTime string, rowIDs []string, columnNames []string, page *pb.Page) ([]*pb.DataRow, *pb.PageResult, error)
}

type ViewWriter interface {
	CreateResultTable(ctx context.Context, tableName string, columns []*pb.ViewColumn) error
	InsertRows(ctx context.Context, tableName string, rows []*pb.QueryViewRow) error
}

type Options struct {
	Metadata metadata.Store
	Facts    FactReader
	Views    ViewWriter
	Now      func() time.Time
}

type Builder struct {
	metadata     metadata.Store
	facts        FactReader
	views        ViewWriter
	now          func() time.Time
	buildMu      sync.Mutex
	activeBuilds map[string]struct{}
}

func NewBuilder(opts Options) *Builder {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Builder{
		metadata:     opts.Metadata,
		facts:        opts.Facts,
		views:        opts.Views,
		now:          now,
		activeBuilds: make(map[string]struct{}),
	}
}

func (b *Builder) Build(ctx context.Context, spaceID string, viewID string) (*pb.View, error) {
	if b == nil || b.metadata == nil || b.facts == nil || b.views == nil {
		return nil, errors.New("metadata, facts and views are required")
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
	primaryDatasetID := view.GetPrimaryDatasetId()
	if primaryDatasetID == "" && len(view.GetDatasetIds()) > 0 {
		primaryDatasetID = view.GetDatasetIds()[0]
	}
	if primaryDatasetID == "" {
		return nil, errors.New("view primary_dataset_id is required")
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

	resultName := resultTableName(spaceID, viewID, b.now())
	if err := b.views.CreateResultTable(ctx, resultName, columns); err != nil {
		return nil, err
	}
	rows, err := b.readViewRows(ctx, view, primaryDatasetID, columns)
	if err != nil {
		return nil, err
	}
	if err := b.views.InsertRows(ctx, resultName, rows); err != nil {
		return nil, err
	}

	updated := proto.Clone(view).(*pb.View)
	updated.ActiveResult = resultName
	updated.BuildStatus = "active"
	updated.Columns = nil
	if updated.Status == "" {
		updated.Status = "active"
	}
	return b.metadata.UpsertView(ctx, updated)
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

func (b *Builder) RebuildPendingViews(ctx context.Context, spaceID string) ([]*pb.View, error) {
	if b == nil || b.metadata == nil {
		return nil, errMetadataRequired()
	}
	views, err := b.listAllActiveViews(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	var built []*pb.View
	for _, view := range views {
		if view.GetBuildStatus() != "" && view.GetBuildStatus() != "pending" {
			continue
		}
		unlock, ok := b.tryLockView(view.GetSpaceId(), view.GetViewId())
		if !ok {
			continue
		}
		item, err := b.buildLocked(ctx, view.GetSpaceId(), view.GetViewId())
		unlock()
		if err != nil {
			return nil, err
		}
		built = append(built, item)
	}
	return built, nil
}

func (b *Builder) readViewRows(ctx context.Context, view *pb.View, primaryDatasetID string, columns []*pb.ViewColumn) ([]*pb.QueryViewRow, error) {
	spaceID := view.GetSpaceId()
	datasetIDs := viewDatasetIDs(primaryDatasetID, view.GetDatasetIds(), columns)
	sourceColumns := sourceColumnNamesByDataset(primaryDatasetID, columns)
	subjects, err := b.datasetSubjects(ctx, spaceID, primaryDatasetID)
	if err != nil {
		return nil, err
	}
	timeRange := buildTimeRange(b.now(), view.GetQueryWindow())
	var out []*pb.QueryViewRow
	for _, subjectID := range subjects {
		rowsByDataset := make(map[string][]*pb.DataRow, len(datasetIDs))
		indexByDataset := make(map[string]map[string]*pb.DataRow, len(datasetIDs))
		for _, datasetID := range datasetIDs {
			readRows, err := b.readAllRows(ctx, &pb.DataScope{
				SpaceId:   spaceID,
				DatasetId: datasetID,
				SubjectId: subjectID,
			}, timeRange, sourceColumns[datasetID])
			if err != nil {
				return nil, err
			}
			rowsByDataset[datasetID] = readRows
			indexByDataset[datasetID] = indexRowsByGrain(readRows, view.GetGrainKeys())
		}
		for _, primaryRow := range rowsByDataset[primaryDatasetID] {
			key := rowGrainKey(primaryRow, view.GetGrainKeys())
			rowSet := make(map[string]*pb.DataRow, len(datasetIDs))
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
			out = append(out, &pb.QueryViewRow{
				SubjectId: primaryRow.GetKey().GetScope().GetSubjectId(),
				DataTime:  primaryRow.GetKey().GetDataTime(),
				Values:    mapped,
			})
		}
	}
	return out, nil
}

func (b *Builder) readAllRows(ctx context.Context, scope *pb.DataScope, timeRange *pb.TimeRange, columnNames []string) ([]*pb.DataRow, error) {
	const pageSize = uint32(1000)
	var out []*pb.DataRow
	for pageNo := uint32(1); ; pageNo++ {
		rows, page, err := b.facts.ReadRows(ctx, scope, pb.ReadMode_READ_MODE_RANGE, timeRange, "", nil, columnNames, &pb.Page{Page: pageNo, Size: pageSize})
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
		if page == nil || !page.GetHasMore() {
			return out, nil
		}
	}
}

func (b *Builder) datasetSubjects(ctx context.Context, spaceID string, datasetID string) ([]string, error) {
	bindings, err := b.listAllDataSetSubjects(ctx, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	if len(bindings) == 0 {
		return []string{""}, nil
	}
	subjects := make([]string, 0, len(bindings))
	seen := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		if binding.GetStatus() != "" && binding.GetStatus() != "active" {
			continue
		}
		subjectID := binding.GetSubjectId()
		if subjectID == "" || seen[subjectID] {
			continue
		}
		seen[subjectID] = true
		subjects = append(subjects, subjectID)
	}
	if len(subjects) == 0 {
		return []string{""}, nil
	}
	return subjects, nil
}

func (b *Builder) listAllActiveViews(ctx context.Context, spaceID string) ([]*pb.View, error) {
	const pageSize = uint32(1000)
	var out []*pb.View
	for pageNo := uint32(1); ; pageNo++ {
		views, page, err := b.metadata.ListViews(ctx, spaceID, "", "active", &pb.Page{Page: pageNo, Size: pageSize})
		if err != nil {
			return nil, err
		}
		out = append(out, views...)
		if page == nil || !page.GetHasMore() {
			return out, nil
		}
	}
}

func (b *Builder) listAllDataSetSubjects(ctx context.Context, spaceID string, datasetID string) ([]*pb.DataSetSubject, error) {
	const pageSize = uint32(1000)
	var out []*pb.DataSetSubject
	for pageNo := uint32(1); ; pageNo++ {
		bindings, page, err := b.metadata.ListDataSetSubjectsPage(ctx, spaceID, datasetID, "", &pb.Page{Page: pageNo, Size: pageSize})
		if err != nil {
			return nil, err
		}
		out = append(out, bindings...)
		if page == nil || !page.GetHasMore() {
			return out, nil
		}
	}
}

func mapViewValues(rowsByDataset map[string]*pb.DataRow, primaryDatasetID string, columns []*pb.ViewColumn) []*pb.ColumnValue {
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

func indexRowsByGrain(rows []*pb.DataRow, grainKeys []string) map[string]*pb.DataRow {
	out := make(map[string]*pb.DataRow, len(rows))
	for _, row := range rows {
		out[rowGrainKey(row, grainKeys)] = row
	}
	return out
}

func rowGrainKey(row *pb.DataRow, grainKeys []string) string {
	if len(grainKeys) == 0 {
		grainKeys = []string{"subject_id", "data_time", "freq", "dimensions"}
	}
	parts := make([]string, 0, len(grainKeys))
	for _, key := range grainKeys {
		switch key {
		case "subject_id":
			parts = append(parts, grainPart("subject_id", row.GetKey().GetScope().GetSubjectId()))
		case "data_time":
			parts = append(parts, grainPart("data_time", row.GetKey().GetDataTime()))
		case "freq":
			parts = append(parts, grainPart("freq", row.GetKey().GetScope().GetFreq()))
		case "dimensions":
			parts = append(parts, grainPart("dimensions", factkey.DimensionsHash(row.GetKey().GetScope().GetDimensions())))
		case "row_id":
			parts = append(parts, grainPart("row_id", row.GetKey().GetRowId()))
		default:
			if strings.HasPrefix(key, "dimension.") {
				name := strings.TrimPrefix(key, "dimension.")
				parts = append(parts, grainPart(key, row.GetKey().GetScope().GetDimensions()[name]))
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
	return &pb.TimeRange{StartTime: start, StartInclusive: true}
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

func resultTableName(spaceID string, viewID string, now time.Time) string {
	raw := fmt.Sprintf("view_result_%s_%s_%d", spaceID, viewID, now.UnixNano())
	name := invalidTableChar.ReplaceAllString(raw, "_")
	name = strings.Trim(name, "_")
	if name == "" {
		return "view_result"
	}
	if first := name[0]; (first < 'A' || first > 'Z') && (first < 'a' || first > 'z') && first != '_' {
		name = "view_result_" + name
	}
	return name
}
