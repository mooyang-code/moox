package materializer

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata"
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
	metadata metadata.Store
	facts    FactReader
	views    ViewWriter
	now      func() time.Time
}

func NewBuilder(opts Options) *Builder {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Builder{
		metadata: opts.Metadata,
		facts:    opts.Facts,
		views:    opts.Views,
		now:      now,
	}
}

func (b *Builder) Build(ctx context.Context, spaceID string, viewID string) (*pb.View, error) {
	if b == nil || b.metadata == nil || b.facts == nil || b.views == nil {
		return nil, errors.New("metadata, facts and views are required")
	}
	if spaceID == "" || viewID == "" {
		return nil, errors.New("space_id and view_id are required")
	}
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
	rows, err := b.readPrimaryRows(ctx, spaceID, primaryDatasetID, view.GetQueryWindow(), columns)
	if err != nil {
		return nil, err
	}
	if err := b.views.InsertRows(ctx, resultName, rows); err != nil {
		return nil, err
	}

	updated := proto.Clone(view).(*pb.View)
	updated.ActiveResult = resultName
	updated.BuildStatus = "active"
	if updated.Status == "" {
		updated.Status = "active"
	}
	return b.metadata.UpsertView(ctx, updated)
}

func (b *Builder) BuildView(ctx context.Context, spaceID string, viewID string) (*pb.View, error) {
	return b.Build(ctx, spaceID, viewID)
}

func (b *Builder) RebuildPendingViews(ctx context.Context, spaceID string) ([]*pb.View, error) {
	if b == nil || b.metadata == nil {
		return nil, errors.New("metadata is required")
	}
	views, _, err := b.metadata.ListViews(ctx, spaceID, "", "active", nil)
	if err != nil {
		return nil, err
	}
	var built []*pb.View
	for _, view := range views {
		if view.GetBuildStatus() != "" && view.GetBuildStatus() != "pending" {
			continue
		}
		item, err := b.Build(ctx, view.GetSpaceId(), view.GetViewId())
		if err != nil {
			return nil, err
		}
		built = append(built, item)
	}
	return built, nil
}

func (b *Builder) readPrimaryRows(ctx context.Context, spaceID string, datasetID string, queryWindow string, columns []*pb.ViewColumn) ([]*pb.QueryViewRow, error) {
	subjects, err := b.datasetSubjects(ctx, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	sourceColumns := sourceColumnNames(datasetID, columns)
	timeRange := buildTimeRange(b.now(), queryWindow)
	var out []*pb.QueryViewRow
	for _, subjectID := range subjects {
		readRows, _, err := b.facts.ReadRows(ctx, &pb.DataScope{
			SpaceId:   spaceID,
			DatasetId: datasetID,
			SubjectId: subjectID,
		}, pb.ReadMode_READ_MODE_RANGE, timeRange, "", nil, sourceColumns, nil)
		if err != nil {
			return nil, err
		}
		for _, row := range readRows {
			mapped := mapViewValues(row, datasetID, columns)
			out = append(out, &pb.QueryViewRow{
				SubjectId: row.GetKey().GetScope().GetSubjectId(),
				DataTime:  row.GetKey().GetDataTime(),
				Values:    mapped,
			})
		}
	}
	return out, nil
}

func (b *Builder) datasetSubjects(ctx context.Context, spaceID string, datasetID string) ([]string, error) {
	bindings, err := b.metadata.ListDataSetSubjects(ctx, spaceID, datasetID)
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

func mapViewValues(row *pb.DataRow, datasetID string, columns []*pb.ViewColumn) []*pb.ColumnValue {
	values := make(map[string]*pb.ColumnValue, len(row.GetColumns()))
	for _, column := range row.GetColumns() {
		values[column.GetColumnName()] = column
	}
	out := make([]*pb.ColumnValue, 0, len(columns))
	for _, viewColumn := range columns {
		sourceName := sourceColumnName(datasetID, viewColumn)
		source, ok := values[sourceName]
		if !ok {
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

func sourceColumnNames(datasetID string, columns []*pb.ViewColumn) []string {
	seen := make(map[string]bool, len(columns))
	names := make([]string, 0, len(columns))
	for _, column := range columns {
		name := sourceColumnName(datasetID, column)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
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
