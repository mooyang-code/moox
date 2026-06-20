package cmd

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/spf13/cobra"
)

const (
	defaultStorageImportFormat    = "auto"
	defaultStorageImportBatchSize = 1000
	maxStorageImportFileSize      = 512 << 20
)

var storageImportFlags storageImportOptions
var (
	storageImportRetryWindow = 20 * time.Second
	storageImportRetryDelay  = time.Second
)

var storageImportCmd = &cobra.Command{
	Use:   "import",
	Short: "导入历史事实数据",
	Long: `导入本地历史事实数据到 moox-storage。

示例:
  moox-cli storage import --format csv --file ~/Downloads/ARB-USDT.csv \
    --access-url http://127.0.0.1:19104 --metadata-url http://127.0.0.1:19101 \
    --space crypto --dataset binance_spot_kline --subject ARB-USDT --freq 1m \
    --time-column candle_begin_time

  moox-cli storage import --file ~/Downloads/ARB-USDT.csv --dry-run \
    --metadata-url http://127.0.0.1:19101 --space crypto --dataset binance_spot_kline \
    --subject ARB-USDT --freq 1m --time-column candle_begin_time

  moox-cli storage import --format csv --file ~/Downloads/ARB-USDT.csv \
    --view spot_kline_close_view --dataset binance_spot_kline --subject ARB-USDT`,
	RunE: func(cmd *cobra.Command, args []string) error {
		opts := storageImportFlags
		opts.Format = defaultFlag(opts.Format, defaultStorageImportFormat)
		opts.MetadataURL = defaultMetadataImportURL(opts.MetadataURL)
		meta := httpStorageImportMetadataClient{URL: opts.MetadataURL}
		writer := httpStorageDataWriter{URL: opts.AccessURL}
		summary, err := runStorageImport(cmd.Context(), opts, meta, writer)
		if err != nil {
			return err
		}
		return writeStorageImportSummary(summary)
	},
}

type storageImportOptions struct {
	Format       string
	File         string
	AccessURL    string
	MetadataURL  string
	SpaceID      string
	ViewID       string
	DatasetID    string
	SubjectID    string
	DataSourceID string
	Freq         string
	TimeColumn   string
	Dimensions   []string
	WriteMode    string
	BatchSize    int
	DryRun       bool
}

type storageImportContext struct {
	Options storageImportOptions
	Columns map[string]*pb.DataSetColumn
}

type storageImportParseResult struct {
	Rows  []*pb.TimeSeriesRow
	Stats storageImportStats
}

type storageImportStats struct {
	ValidatedRows int `json:"validated_rows"`
	SkippedRows   int `json:"skipped_rows,omitempty"`
}

type storageImportSummary struct {
	Status           string `json:"status"`
	File             string `json:"file"`
	Format           string `json:"format"`
	AccessURL        string `json:"access_url,omitempty"`
	MetadataURL      string `json:"metadata_url"`
	SpaceID          string `json:"space"`
	ViewID           string `json:"view,omitempty"`
	DatasetID        string `json:"dataset"`
	SubjectID        string `json:"subject"`
	Freq             string `json:"freq,omitempty"`
	ValidatedRows    int    `json:"validated_rows"`
	WrittenRows      int    `json:"written_rows,omitempty"`
	WouldWriteRows   int    `json:"would_write_rows,omitempty"`
	Batches          int    `json:"batches,omitempty"`
	BoundSubject     bool   `json:"bound_subject,omitempty"`
	WouldBindSubject bool   `json:"would_bind_subject,omitempty"`
}

type storageImportMetadataClient interface {
	GetDataSet(context.Context, string, string) (*pb.DataSet, error)
	GetView(context.Context, string, string) (*pb.View, error)
	GetSubject(context.Context, string, string) (*pb.Subject, error)
	ListDataSetColumns(context.Context, string, string) ([]*pb.DataSetColumn, error)
	ListDataSetSubjects(context.Context, string, string, string) ([]*pb.DataSetSubject, error)
	BindDataSetSubject(context.Context, *pb.DataSetSubject) error
}

type storageDataWriter interface {
	WriteTimeSeriesRows(context.Context, *pb.WriteTimeSeriesRowsReq) error
}

type storageFileImporter interface {
	Format() string
	ReadRows(string, storageImportContext) (storageImportParseResult, error)
}

type csvStorageFileImporter struct{}

type httpStorageImportMetadataClient struct {
	URL string
}

type httpStorageDataWriter struct {
	URL string
}

func runStorageImport(ctx context.Context, opts storageImportOptions, meta storageImportMetadataClient, writer storageDataWriter) (storageImportSummary, error) {
	format, err := inferStorageImportFormat(opts.Format, opts.File)
	if err != nil {
		return storageImportSummary{}, err
	}
	opts.Format = format
	opts.BatchSize = normalizedStorageImportBatchSize(opts.BatchSize)
	opts.WriteMode = defaultFlag(opts.WriteMode, "upsert")
	if err := validateStorageImportOptions(opts); err != nil {
		return storageImportSummary{}, err
	}
	if err := validateStorageImportFile(opts.File); err != nil {
		return storageImportSummary{}, err
	}

	dataset, err := meta.GetDataSet(ctx, opts.SpaceID, opts.DatasetID)
	if err != nil {
		return storageImportSummary{}, err
	}
	if dataset == nil {
		return storageImportSummary{}, fmt.Errorf("dataset %s/%s not found", opts.SpaceID, opts.DatasetID)
	}
	if opts.DataSourceID != "" && dataset.GetDataSourceId() != "" && dataset.GetDataSourceId() != opts.DataSourceID {
		return storageImportSummary{}, fmt.Errorf("dataset %s data_source_id=%s, want %s", opts.DatasetID, dataset.GetDataSourceId(), opts.DataSourceID)
	}
	if err := validateStorageImportFreq(opts, dataset); err != nil {
		return storageImportSummary{}, err
	}
	if opts.ViewID != "" {
		view, err := meta.GetView(ctx, opts.SpaceID, opts.ViewID)
		if err != nil {
			return storageImportSummary{}, err
		}
		if view == nil || !stringSliceContains(view.GetDatasetIds(), opts.DatasetID) {
			return storageImportSummary{}, fmt.Errorf("view %s does not include dataset %s", opts.ViewID, opts.DatasetID)
		}
	}
	subject, err := meta.GetSubject(ctx, opts.SpaceID, opts.SubjectID)
	if err != nil {
		return storageImportSummary{}, err
	}
	if subject == nil || subject.GetStatus() == "deleted" {
		return storageImportSummary{}, fmt.Errorf("subject %s/%s not found", opts.SpaceID, opts.SubjectID)
	}

	columns, err := meta.ListDataSetColumns(ctx, opts.SpaceID, opts.DatasetID)
	if err != nil {
		return storageImportSummary{}, err
	}
	columnByName, err := storageImportColumnMap(columns)
	if err != nil {
		return storageImportSummary{}, err
	}
	importer, err := storageImporterForFormat(format)
	if err != nil {
		return storageImportSummary{}, err
	}
	result, err := importer.ReadRows(opts.File, storageImportContext{Options: opts, Columns: columnByName})
	if err != nil {
		return storageImportSummary{}, err
	}

	subjects, err := meta.ListDataSetSubjects(ctx, opts.SpaceID, opts.DatasetID, opts.SubjectID)
	if err != nil {
		return storageImportSummary{}, err
	}
	needsBind := !storageImportSubjectBound(subjects, opts.SubjectID)
	summary := storageImportSummary{
		Status:        "imported",
		File:          opts.File,
		Format:        format,
		AccessURL:     opts.AccessURL,
		MetadataURL:   opts.MetadataURL,
		SpaceID:       opts.SpaceID,
		ViewID:        opts.ViewID,
		DatasetID:     opts.DatasetID,
		SubjectID:     opts.SubjectID,
		Freq:          opts.Freq,
		ValidatedRows: result.Stats.ValidatedRows,
	}
	if opts.DryRun {
		summary.Status = "dry_run"
		summary.WouldWriteRows = len(result.Rows)
		summary.WouldBindSubject = needsBind
		return summary, nil
	}
	if needsBind {
		if err := meta.BindDataSetSubject(ctx, &pb.DataSetSubject{
			SpaceId:     opts.SpaceID,
			DatasetId:   opts.DatasetID,
			SubjectId:   opts.SubjectID,
			SubjectRole: "normal",
			Status:      "active",
		}); err != nil {
			return storageImportSummary{}, err
		}
		summary.BoundSubject = true
	}
	writeMode, err := parseStorageImportWriteMode(opts.WriteMode)
	if err != nil {
		return storageImportSummary{}, err
	}
	for start := 0; start < len(result.Rows); start += opts.BatchSize {
		end := start + opts.BatchSize
		if end > len(result.Rows) {
			end = len(result.Rows)
		}
		if err := writeStorageImportRows(ctx, writer, &pb.WriteTimeSeriesRowsReq{WriteMode: writeMode, Rows: result.Rows[start:end]}, true); err != nil {
			return storageImportSummary{}, err
		}
		summary.Batches++
		summary.WrittenRows += end - start
	}
	return summary, nil
}

func writeStorageImportRows(ctx context.Context, writer storageDataWriter, req *pb.WriteTimeSeriesRowsReq, allowMetadataRetry bool) error {
	err := writer.WriteTimeSeriesRows(ctx, req)
	if err == nil || !allowMetadataRetry || !retryableStorageImportWriteError(err) {
		return err
	}
	deadline := time.Now().Add(storageImportRetryWindow)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(storageImportRetryDelay):
		}
		err = writer.WriteTimeSeriesRows(ctx, req)
		if err == nil {
			return nil
		}
		if !retryableStorageImportWriteError(err) {
			return err
		}
	}
	return err
}

func retryableStorageImportWriteError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, pattern := range []string{
		"not bound",
		"not registered",
		"route not found",
		"subject",
		"dataset",
		"路由",
		"绑定",
		"未注册",
	} {
		if strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}

func inferStorageImportFormat(format string, path string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(format))
	if value == "" || value == "auto" {
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".csv":
			return "csv", nil
		default:
			return "", fmt.Errorf("cannot infer import format from %q; pass --format csv", path)
		}
	}
	switch value {
	case "csv":
		return value, nil
	default:
		return "", fmt.Errorf("unsupported import format %q", format)
	}
}

func storageImporterForFormat(format string) (storageFileImporter, error) {
	switch format {
	case "csv":
		return csvStorageFileImporter{}, nil
	default:
		return nil, fmt.Errorf("unsupported import format %q", format)
	}
}

func validateStorageImportOptions(opts storageImportOptions) error {
	required := map[string]string{
		"file":         opts.File,
		"metadata-url": opts.MetadataURL,
		"space":        opts.SpaceID,
		"dataset":      opts.DatasetID,
		"subject":      opts.SubjectID,
		"time-column":  opts.TimeColumn,
	}
	if !opts.DryRun {
		required["access-url"] = opts.AccessURL
	}
	if opts.Freq == "" {
		required["freq"] = opts.Freq
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("必须指定 --%s", name)
		}
	}
	return nil
}

func validateStorageImportFile(path string) error {
	cleaned := filepath.Clean(path)
	for _, forbidden := range []string{"/etc", "/proc", "/sys"} {
		if cleaned == forbidden || strings.HasPrefix(cleaned, forbidden+string(filepath.Separator)) {
			return fmt.Errorf("refuse to import sensitive path %s", path)
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		for _, rel := range []string{".ssh", ".aws", ".kube"} {
			forbidden := filepath.Join(home, rel)
			if cleaned == forbidden || strings.HasPrefix(cleaned, forbidden+string(filepath.Separator)) {
				return fmt.Errorf("refuse to import sensitive path %s", path)
			}
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("import file %s is not a regular file", path)
	}
	if info.Size() > maxStorageImportFileSize {
		return fmt.Errorf("import file %s is too large: %d bytes", path, info.Size())
	}
	return nil
}

func validateStorageImportFreq(opts storageImportOptions, dataset *pb.DataSet) error {
	if opts.Freq == "" || len(dataset.GetFreqs()) == 0 {
		return nil
	}
	if !stringSliceContains(dataset.GetFreqs(), opts.Freq) {
		return fmt.Errorf("dataset %s does not support freq %s", opts.DatasetID, opts.Freq)
	}
	return nil
}

func storageImportColumnMap(columns []*pb.DataSetColumn) (map[string]*pb.DataSetColumn, error) {
	if len(columns) == 0 {
		return nil, fmt.Errorf("dataset columns are empty")
	}
	values := make(map[string]*pb.DataSetColumn, len(columns))
	for _, column := range columns {
		if column.GetColumnName() == "" {
			continue
		}
		if column.GetStatus() == "deleted" {
			continue
		}
		values[column.GetColumnName()] = column
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("dataset columns are empty")
	}
	return values, nil
}

func storageImportSubjectBound(subjects []*pb.DataSetSubject, subjectID string) bool {
	for _, item := range subjects {
		if item.GetSubjectId() == subjectID && item.GetStatus() != "deleted" {
			return true
		}
	}
	return false
}

func parseStorageImportWriteMode(value string) (pb.WriteMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "upsert":
		return pb.WriteMode_WRITE_MODE_UPSERT, nil
	case "append":
		return pb.WriteMode_WRITE_MODE_APPEND, nil
	case "overwrite":
		return pb.WriteMode_WRITE_MODE_OVERWRITE, nil
	default:
		return pb.WriteMode_WRITE_MODE_UNSPECIFIED, fmt.Errorf("unsupported write-mode %q", value)
	}
}

func normalizedStorageImportBatchSize(size int) int {
	if size <= 0 {
		return defaultStorageImportBatchSize
	}
	return size
}

func (csvStorageFileImporter) Format() string { return "csv" }

func (csvStorageFileImporter) ReadRows(path string, ctx storageImportContext) (storageImportParseResult, error) {
	file, err := os.Open(path)
	if err != nil {
		return storageImportParseResult{}, err
	}
	defer file.Close()
	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	header, err := readStorageCSVHeader(reader, ctx.Options.TimeColumn)
	if err != nil {
		return storageImportParseResult{}, err
	}
	normalizedHeader, timeIndex, err := validateStorageCSVHeader(header, ctx)
	if err != nil {
		return storageImportParseResult{}, err
	}
	dimensions, err := parseStorageImportDimensions(ctx.Options.Dimensions)
	if err != nil {
		return storageImportParseResult{}, err
	}
	var result storageImportParseResult
	line := 1
	for {
		record, err := reader.Read()
		if err != nil {
			if err == io.EOF {
				break
			}
			return storageImportParseResult{}, err
		}
		line++
		if emptyCSVRecord(record) {
			result.Stats.SkippedRows++
			continue
		}
		if timeIndex >= len(record) || strings.TrimSpace(record[timeIndex]) == "" {
			return storageImportParseResult{}, fmt.Errorf("row %d column %s is required", line, ctx.Options.TimeColumn)
		}
		dataTime, err := normalizeStorageImportTime(strings.TrimSpace(record[timeIndex]))
		if err != nil {
			return storageImportParseResult{}, fmt.Errorf("row %d column %s invalid time %q: %w", line, ctx.Options.TimeColumn, strings.TrimSpace(record[timeIndex]), err)
		}
		row := &pb.TimeSeriesRow{
			Key: &pb.TimeSeriesKey{
				SpaceId:    ctx.Options.SpaceID,
				DatasetId:  ctx.Options.DatasetID,
				SubjectId:  ctx.Options.SubjectID,
				Freq:       ctx.Options.Freq,
				Dimensions: dimensions,
				DataTime:   dataTime,
			},
		}
		for index, name := range normalizedHeader {
			if name == "" || name == ctx.Options.TimeColumn {
				continue
			}
			column := ctx.Columns[name]
			value := ""
			if index < len(record) {
				value = strings.TrimSpace(record[index])
			}
			if value == "" {
				if column.GetRequired() {
					return storageImportParseResult{}, fmt.Errorf("row %d column %s is required", line, name)
				}
				continue
			}
			typed, err := storageImportTypedValue(value, column.GetValueType())
			if err != nil {
				return storageImportParseResult{}, fmt.Errorf("row %d column %s invalid %s value %q: %w", line, name, storageImportValueTypeName(column.GetValueType()), value, err)
			}
			row.Columns = append(row.Columns, &pb.ColumnValue{
				ColumnName: name,
				ValueType:  column.GetValueType(),
				Value:      typed,
			})
		}
		result.Rows = append(result.Rows, row)
		result.Stats.ValidatedRows++
	}
	if result.Stats.ValidatedRows == 0 {
		return storageImportParseResult{}, fmt.Errorf("CSV %s has no data rows", path)
	}
	return result, nil
}

func parseStorageImportDimensions(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	dimensions := make(map[string]string, len(values))
	for _, raw := range values {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("dimension %q must use name=value format", raw)
		}
		name := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if name == "" || value == "" {
			return nil, fmt.Errorf("dimension %q must use non-empty name=value format", raw)
		}
		if _, ok := dimensions[name]; ok {
			return nil, fmt.Errorf("duplicate dimension %s", name)
		}
		dimensions[name] = value
	}
	if len(dimensions) == 0 {
		return nil, nil
	}
	return dimensions, nil
}

func readStorageCSVHeader(reader *csv.Reader, timeColumn string) ([]string, error) {
	for {
		record, err := reader.Read()
		if err != nil {
			if err == io.EOF {
				return nil, fmt.Errorf("CSV header with time column %q not found", timeColumn)
			}
			return nil, err
		}
		for _, name := range record {
			if strings.TrimSpace(name) == timeColumn {
				return record, nil
			}
		}
	}
}

func validateStorageCSVHeader(header []string, ctx storageImportContext) ([]string, int, error) {
	normalized := make([]string, len(header))
	seen := make(map[string]struct{}, len(header))
	timeIndex := -1
	for index, raw := range header {
		name := strings.TrimSpace(raw)
		normalized[index] = name
		if name == "" {
			return nil, -1, fmt.Errorf("CSV header has empty column at index %d", index+1)
		}
		if _, ok := seen[name]; ok {
			return nil, -1, fmt.Errorf("CSV header has duplicate column %s", name)
		}
		seen[name] = struct{}{}
		if name == ctx.Options.TimeColumn {
			timeIndex = index
			continue
		}
		if _, ok := ctx.Columns[name]; !ok {
			return nil, -1, fmt.Errorf("CSV column %s is not registered in dataset %s", name, ctx.Options.DatasetID)
		}
	}
	if timeIndex < 0 {
		return nil, -1, fmt.Errorf("CSV header with time column %q not found", ctx.Options.TimeColumn)
	}
	for name, column := range ctx.Columns {
		if column.GetRequired() {
			if _, ok := seen[name]; !ok {
				return nil, -1, fmt.Errorf("required dataset column %s is missing from CSV header", name)
			}
		}
	}
	return normalized, timeIndex, nil
}

func emptyCSVRecord(record []string) bool {
	for _, item := range record {
		if strings.TrimSpace(item) != "" {
			return false
		}
	}
	return true
}

func normalizeStorageImportTime(value string) (string, error) {
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC().Format(time.RFC3339Nano), nil
	}
	layouts := []string{
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	var lastErr error
	for _, layout := range layouts {
		parsed, err := time.ParseInLocation(layout, value, time.UTC)
		if err == nil {
			return parsed.UTC().Format(time.RFC3339Nano), nil
		}
		lastErr = err
	}
	return "", lastErr
}

func storageImportTypedValue(value string, valueType pb.FieldValueType) (*pb.TypedValue, error) {
	switch valueType {
	case pb.FieldValueType_FIELD_VALUE_TYPE_STRING, pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED:
		return &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: value}}, nil
	case pb.FieldValueType_FIELD_VALUE_TYPE_INT:
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, err
		}
		return &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: parsed}}, nil
	case pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, err
		}
		return &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: parsed}}, nil
	case pb.FieldValueType_FIELD_VALUE_TYPE_BOOL:
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return nil, err
		}
		return &pb.TypedValue{Value: &pb.TypedValue_BoolValue{BoolValue: parsed}}, nil
	case pb.FieldValueType_FIELD_VALUE_TYPE_TIME:
		parsed, err := normalizeStorageImportTime(value)
		if err != nil {
			return nil, err
		}
		return &pb.TypedValue{Value: &pb.TypedValue_TimeValue{TimeValue: parsed}}, nil
	case pb.FieldValueType_FIELD_VALUE_TYPE_JSON:
		if !json.Valid([]byte(value)) {
			return nil, fmt.Errorf("invalid json")
		}
		return &pb.TypedValue{Value: &pb.TypedValue_JsonValue{JsonValue: value}}, nil
	case pb.FieldValueType_FIELD_VALUE_TYPE_BYTES:
		return &pb.TypedValue{Value: &pb.TypedValue_BytesValue{BytesValue: []byte(value)}}, nil
	default:
		return nil, fmt.Errorf("unsupported value type %s", valueType.String())
	}
}

func storageImportValueTypeName(valueType pb.FieldValueType) string {
	name := valueType.String()
	name = strings.TrimPrefix(name, "FIELD_VALUE_TYPE_")
	return strings.ToLower(name)
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func writeStorageImportSummary(summary storageImportSummary) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(summary)
}

func (c httpStorageImportMetadataClient) GetDataSet(ctx context.Context, spaceID string, datasetID string) (*pb.DataSet, error) {
	rsp := &pb.GetDataSetRsp{}
	if err := postStorage(ctx, c.URL, metadataServiceName, "GetDataSet", &pb.GetDataSetReq{SpaceId: spaceID, DatasetId: datasetID}, rsp); err != nil {
		return nil, err
	}
	return rsp.GetDataset(), nil
}

func (c httpStorageImportMetadataClient) GetView(ctx context.Context, spaceID string, viewID string) (*pb.View, error) {
	rsp := &pb.GetViewRsp{}
	if err := postStorage(ctx, c.URL, metadataServiceName, "GetView", &pb.GetViewReq{SpaceId: spaceID, ViewId: viewID}, rsp); err != nil {
		return nil, err
	}
	return rsp.GetView(), nil
}

func (c httpStorageImportMetadataClient) GetSubject(ctx context.Context, spaceID string, subjectID string) (*pb.Subject, error) {
	rsp := &pb.GetSubjectRsp{}
	if err := postStorage(ctx, c.URL, metadataServiceName, "GetSubject", &pb.GetSubjectReq{SpaceId: spaceID, SubjectId: subjectID}, rsp); err != nil {
		return nil, err
	}
	return rsp.GetSubject(), nil
}

func (c httpStorageImportMetadataClient) ListDataSetColumns(ctx context.Context, spaceID string, datasetID string) ([]*pb.DataSetColumn, error) {
	rsp := &pb.ListDataSetColumnsRsp{}
	if err := postStorage(ctx, c.URL, metadataServiceName, "ListDataSetColumns", &pb.ListDataSetColumnsReq{
		SpaceId:   spaceID,
		DatasetId: datasetID,
		Page:      &pb.Page{Page: 1, Size: 10000},
	}, rsp); err != nil {
		return nil, err
	}
	return rsp.GetColumns(), nil
}

func (c httpStorageImportMetadataClient) ListDataSetSubjects(ctx context.Context, spaceID string, datasetID string, subjectID string) ([]*pb.DataSetSubject, error) {
	rsp := &pb.ListDataSetSubjectsRsp{}
	if err := postStorage(ctx, c.URL, metadataServiceName, "ListDataSetSubjects", &pb.ListDataSetSubjectsReq{
		SpaceId:   spaceID,
		DatasetId: datasetID,
		SubjectId: subjectID,
		Page:      &pb.Page{Page: 1, Size: 10000},
	}, rsp); err != nil {
		return nil, err
	}
	return rsp.GetDatasetSubjects(), nil
}

func (c httpStorageImportMetadataClient) BindDataSetSubject(ctx context.Context, item *pb.DataSetSubject) error {
	return postStorage(ctx, c.URL, metadataServiceName, "BindDataSetSubject", &pb.BindDataSetSubjectReq{DatasetSubject: item}, &pb.BindDataSetSubjectRsp{})
}

func (w httpStorageDataWriter) WriteTimeSeriesRows(ctx context.Context, req *pb.WriteTimeSeriesRowsReq) error {
	return postStorage(ctx, w.URL, "trpc.storage.data.DataService", "WriteTimeSeriesRows", req, &pb.WriteTimeSeriesRowsRsp{})
}

func init() {
	storageCmd.AddCommand(storageImportCmd)
	storageImportCmd.Flags().StringVar(&storageImportFlags.Format, "format", defaultStorageImportFormat, "导入文件格式：auto/csv")
	storageImportCmd.Flags().StringVar(&storageImportFlags.File, "file", "", "本地数据文件路径")
	storageImportCmd.Flags().StringVar(&storageImportFlags.AccessURL, "access-url", "", "moox-storage DataService HTTP 地址，例如 http://127.0.0.1:19104")
	storageImportCmd.Flags().StringVar(&storageImportFlags.MetadataURL, "metadata-url", "", "moox-storage MetadataService HTTP 地址，例如 http://127.0.0.1:19101")
	storageImportCmd.Flags().StringVar(&storageImportFlags.SpaceID, "space", "", "Space ID")
	storageImportCmd.Flags().StringVar(&storageImportFlags.ViewID, "view", "", "可选 View ID；传入时校验 dataset 属于该 view")
	storageImportCmd.Flags().StringVar(&storageImportFlags.DatasetID, "dataset", "", "DataSet ID")
	storageImportCmd.Flags().StringVar(&storageImportFlags.SubjectID, "subject", "", "Subject ID")
	storageImportCmd.Flags().StringVar(&storageImportFlags.DataSourceID, "data-source", "", "可选 DataSource ID；传入时校验 dataset 归属")
	storageImportCmd.Flags().StringVar(&storageImportFlags.Freq, "freq", "", "时序频率，例如 1m/1h/1d")
	storageImportCmd.Flags().StringVar(&storageImportFlags.TimeColumn, "time-column", "candle_begin_time", "CSV 时间列名")
	storageImportCmd.Flags().StringArrayVar(&storageImportFlags.Dimensions, "dimension", nil, "自定义维度，格式 name=value，可重复")
	storageImportCmd.Flags().StringVar(&storageImportFlags.WriteMode, "write-mode", "upsert", "写入模式：upsert/append/overwrite")
	storageImportCmd.Flags().IntVar(&storageImportFlags.BatchSize, "batch-size", defaultStorageImportBatchSize, "每批写入行数")
	storageImportCmd.Flags().BoolVar(&storageImportFlags.DryRun, "dry-run", false, "只校验并输出导入计划，不绑定 subject，不写入数据")
}
