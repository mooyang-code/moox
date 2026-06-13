package quantstore

import (
	"bufio"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	pb "github.com/mooyang-code/moox/modules/storage/proto/genv2"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	defaultRoot = "var/storage"
)

type Store struct {
	root string
	mu   sync.Mutex
}

type CSVImportOptions struct {
	WorkspaceID     string
	DatasetID       string
	InstrumentID    string
	ExchangeID      string
	Freq            string
	TimeColumn      string
	DimensionValues []*pb.DimensionValue
}

func New(root string) *Store {
	if root == "" {
		root = os.Getenv("MOOX_STORAGE_HOME")
	}
	if root == "" {
		root = defaultRoot
	}
	return &Store{root: root}
}

func (s *Store) Root() string {
	return s.root
}

func Success(msg string) *pb.RetInfo {
	if msg == "" {
		msg = "success"
	}
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: msg}
}

func Error(code pb.ErrorCode, err error) *pb.RetInfo {
	if err == nil {
		return &pb.RetInfo{Code: code}
	}
	return &pb.RetInfo{Code: code, Msg: err.Error()}
}

func StringValue(name, value string) *pb.FieldValue {
	return &pb.FieldValue{
		FieldName: name,
		ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		Value:     &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: value}},
	}
}

func DoubleValue(name string, value float64) *pb.FieldValue {
	return &pb.FieldValue{
		FieldName: name,
		ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Value:     &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}},
	}
}

func IntValue(name string, value int64) *pb.FieldValue {
	return &pb.FieldValue{
		FieldName: name,
		ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_INT,
		Value:     &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: value}},
	}
}

func (s *Store) SetTimeSeries(ctx context.Context, points []*pb.TimeSeriesPoint, mode pb.WriteMode) (uint64, error) {
	_ = ctx
	if len(points) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	grouped := make(map[string][]*pb.TimeSeriesPoint)
	for _, point := range points {
		if point == nil || point.GetDataRef() == nil {
			return 0, errors.New("time series point data_ref is required")
		}
		if err := validateDataRef(point.GetDataRef()); err != nil {
			return 0, err
		}
		grouped[s.timeSeriesPath(point.GetDataRef())] = append(grouped[s.timeSeriesPath(point.GetDataRef())], point)
	}

	var affected uint64
	for path, rows := range grouped {
		if mode == pb.WriteMode_WRITE_MODE_OVERWRITE || mode == pb.WriteMode_WRITE_MODE_DELETE {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return affected, err
			}
		}
		if mode == pb.WriteMode_WRITE_MODE_DELETE {
			continue
		}
		if err := appendMessages(path, rowsToMessages(rows)); err != nil {
			return affected, err
		}
		affected += uint64(len(rows))
	}
	return affected, nil
}

func (s *Store) ScanTimeSeries(ctx context.Context, ref *pb.DataRef, timeRange *pb.TimeRange, fieldNames []string, page *pb.Page) ([]*pb.TimeSeriesPoint, *pb.PageResult, error) {
	_ = ctx
	if ref == nil {
		return nil, nil, errors.New("data_ref is required")
	}
	if err := validateDataRef(ref); err != nil {
		return nil, nil, err
	}
	paths, err := s.timeSeriesPaths(ref)
	if err != nil {
		return nil, nil, err
	}

	var rows []*pb.TimeSeriesPoint
	for _, path := range paths {
		var fileRows []*pb.TimeSeriesPoint
		if err := readMessages(path, func() proto.Message { return &pb.TimeSeriesPoint{} }, func(msg proto.Message) {
			fileRows = append(fileRows, msg.(*pb.TimeSeriesPoint))
		}); err != nil {
			return nil, nil, err
		}
		for _, row := range fileRows {
			if !timeInRange(row.GetTime(), timeRange) {
				continue
			}
			rows = append(rows, filterPointFields(row, fieldNames))
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].GetDataRef().GetInstrumentId() == rows[j].GetDataRef().GetInstrumentId() {
			return rows[i].GetTime() < rows[j].GetTime()
		}
		return rows[i].GetDataRef().GetInstrumentId() < rows[j].GetDataRef().GetInstrumentId()
	})
	paged, result := pageTimeSeries(rows, page)
	return paged, result, nil
}

func (s *Store) SetFactorValues(ctx context.Context, points []*pb.FactorValuePoint, mode pb.WriteMode) (uint64, error) {
	_ = ctx
	if len(points) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	grouped := make(map[string][]*pb.FactorValuePoint)
	for _, point := range points {
		if point == nil || point.GetDataRef() == nil {
			return 0, errors.New("factor value point data_ref is required")
		}
		if err := validateDataRef(point.GetDataRef()); err != nil {
			return 0, err
		}
		if point.GetFactorInstanceId() == "" {
			return 0, errors.New("factor_instance_id is required")
		}
		path := s.factorPath(point.GetDataRef(), point.GetFactorInstanceId())
		grouped[path] = append(grouped[path], point)
	}

	var affected uint64
	for path, rows := range grouped {
		if mode == pb.WriteMode_WRITE_MODE_OVERWRITE || mode == pb.WriteMode_WRITE_MODE_DELETE {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return affected, err
			}
		}
		if mode == pb.WriteMode_WRITE_MODE_DELETE {
			continue
		}
		if err := appendMessages(path, factorRowsToMessages(rows)); err != nil {
			return affected, err
		}
		affected += uint64(len(rows))
	}
	return affected, nil
}

func (s *Store) ScanFactorValues(ctx context.Context, ref *pb.DataRef, factorIDs []string, timeRange *pb.TimeRange, page *pb.Page) ([]*pb.FactorValuePoint, *pb.PageResult, error) {
	_ = ctx
	if ref == nil {
		return nil, nil, errors.New("data_ref is required")
	}
	if err := validateDataRef(ref); err != nil {
		return nil, nil, err
	}
	paths, err := s.factorPaths(ref, factorIDs)
	if err != nil {
		return nil, nil, err
	}
	var rows []*pb.FactorValuePoint
	for _, path := range paths {
		if err := readMessages(path, func() proto.Message { return &pb.FactorValuePoint{} }, func(msg proto.Message) {
			row := msg.(*pb.FactorValuePoint)
			if timeInRange(row.GetTime(), timeRange) {
				rows = append(rows, row)
			}
		}); err != nil {
			return nil, nil, err
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].GetDataRef().GetInstrumentId() == rows[j].GetDataRef().GetInstrumentId() {
			return rows[i].GetTime() < rows[j].GetTime()
		}
		return rows[i].GetDataRef().GetInstrumentId() < rows[j].GetDataRef().GetInstrumentId()
	})
	paged, result := pageFactors(rows, page)
	return paged, result, nil
}

func (s *Store) UpsertRecords(ctx context.Context, records []*pb.Record, mode pb.WriteMode) (uint64, error) {
	_ = ctx
	if len(records) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	grouped := make(map[string][]*pb.Record)
	for _, record := range records {
		if record == nil || record.GetDataRef() == nil {
			return 0, errors.New("record data_ref is required")
		}
		if err := validateDataRef(record.GetDataRef()); err != nil {
			return 0, err
		}
		grouped[s.recordPath(record.GetDataRef())] = append(grouped[s.recordPath(record.GetDataRef())], record)
	}
	var affected uint64
	for path, rows := range grouped {
		if mode == pb.WriteMode_WRITE_MODE_OVERWRITE || mode == pb.WriteMode_WRITE_MODE_DELETE {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return affected, err
			}
		}
		if mode == pb.WriteMode_WRITE_MODE_DELETE {
			continue
		}
		if err := appendMessages(path, recordRowsToMessages(rows)); err != nil {
			return affected, err
		}
		affected += uint64(len(rows))
	}
	return affected, nil
}

func (s *Store) QueryRecords(ctx context.Context, ref *pb.DataRef, page *pb.Page) ([]*pb.Record, *pb.PageResult, error) {
	_ = ctx
	if ref == nil {
		return nil, nil, errors.New("data_ref is required")
	}
	if err := validateDataRef(ref); err != nil {
		return nil, nil, err
	}
	paths, err := s.recordPaths(ref)
	if err != nil {
		return nil, nil, err
	}
	var rows []*pb.Record
	for _, path := range paths {
		if err := readMessages(path, func() proto.Message { return &pb.Record{} }, func(msg proto.Message) {
			rows = append(rows, msg.(*pb.Record))
		}); err != nil {
			return nil, nil, err
		}
	}
	paged, result := pageRecords(rows, page)
	return paged, result, nil
}

func (s *Store) LatestSnapshot(ctx context.Context, refs []*pb.DataRef, fieldNames []string, snapshotTime string) ([]*pb.LatestSnapshotRow, error) {
	var result []*pb.LatestSnapshotRow
	for _, ref := range refs {
		rows, _, err := s.ScanTimeSeries(ctx, ref, &pb.TimeRange{EndTime: snapshotTime, EndInclusive: true}, fieldNames, nil)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			continue
		}
		last := rows[len(rows)-1]
		result = append(result, &pb.LatestSnapshotRow{
			DataRef:      last.GetDataRef(),
			SnapshotTime: last.GetTime(),
			Fields:       last.GetFields(),
		})
	}
	return result, nil
}

func (s *Store) ImportCSV(ctx context.Context, path string, opts CSVImportOptions) (uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	header, err := readCSVHeader(reader, opts.TimeColumn)
	if err != nil {
		return 0, err
	}
	timeIndex := findColumn(header, opts.TimeColumn)
	if timeIndex < 0 {
		timeIndex = findAnyColumn(header, []string{"time", "timestamp", "date", "datetime", "open_time"})
	}
	if timeIndex < 0 {
		return 0, fmt.Errorf("time column not found in %s", path)
	}

	ref := &pb.DataRef{
		WorkspaceId:     opts.WorkspaceID,
		DatasetId:       opts.DatasetID,
		InstrumentId:    opts.InstrumentID,
		ExchangeId:      opts.ExchangeID,
		Freq:            opts.Freq,
		DimensionValues: opts.DimensionValues,
	}
	var batch []*pb.TimeSeriesPoint
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, err
		}
		if timeIndex >= len(record) {
			continue
		}
		point := &pb.TimeSeriesPoint{DataRef: ref, Time: strings.TrimSpace(record[timeIndex])}
		for i, name := range header {
			if i >= len(record) || i == timeIndex {
				continue
			}
			point.Fields = append(point.Fields, inferFieldValue(strings.TrimSpace(name), strings.TrimSpace(record[i])))
		}
		batch = append(batch, point)
	}
	return s.SetTimeSeries(ctx, batch, pb.WriteMode_WRITE_MODE_APPEND)
}

func (s *Store) timeSeriesPath(ref *pb.DataRef) string {
	parts := append([]string{s.root, "timeseries"}, keyParts(ref)...)
	return filepath.Join(parts...) + ".jsonl"
}

func (s *Store) recordPath(ref *pb.DataRef) string {
	parts := append([]string{s.root, "records"}, keyParts(ref)...)
	return filepath.Join(parts...) + ".jsonl"
}

func (s *Store) factorPath(ref *pb.DataRef, factorID string) string {
	parts := keyParts(ref)
	parts = append(parts, safe(factorID))
	return filepath.Join(s.root, "factors", filepath.Join(parts...)) + ".jsonl"
}

func (s *Store) timeSeriesPaths(ref *pb.DataRef) ([]string, error) {
	if ref.GetFreq() != "" && ref.GetExchangeId() != "" {
		return []string{s.timeSeriesPath(ref)}, nil
	}
	patternParts := keyPartsPattern(ref)
	return filepath.Glob(filepath.Join(s.root, "timeseries", filepath.Join(patternParts...)) + ".jsonl")
}

func (s *Store) recordPaths(ref *pb.DataRef) ([]string, error) {
	if ref.GetFreq() != "" && ref.GetExchangeId() != "" {
		return []string{s.recordPath(ref)}, nil
	}
	patternParts := keyPartsPattern(ref)
	return filepath.Glob(filepath.Join(s.root, "records", filepath.Join(patternParts...)) + ".jsonl")
}

func (s *Store) factorPaths(ref *pb.DataRef, factorIDs []string) ([]string, error) {
	if len(factorIDs) > 0 {
		paths := make([]string, 0, len(factorIDs))
		for _, factorID := range factorIDs {
			paths = append(paths, s.factorPath(ref, factorID))
		}
		return paths, nil
	}
	patternParts := keyParts(ref)
	patternParts = append(patternParts, "*")
	return filepath.Glob(filepath.Join(s.root, "factors", filepath.Join(patternParts...)) + ".jsonl")
}

func keyParts(ref *pb.DataRef) []string {
	return []string{
		safe(ref.GetWorkspaceId()),
		safe(ref.GetDatasetId()),
		safe(ref.GetExchangeId()),
		safe(ref.GetInstrumentId()),
		safe(defaultString(ref.GetFreq(), "default")),
		dimensionKey(ref.GetDimensionValues()),
	}
}

func keyPartsPattern(ref *pb.DataRef) []string {
	exchangeID := ref.GetExchangeId()
	if exchangeID == "" {
		exchangeID = "*"
	}
	freq := ref.GetFreq()
	if freq == "" {
		freq = "*"
	}
	dimensions := dimensionKey(ref.GetDimensionValues())
	return []string{
		safe(ref.GetWorkspaceId()),
		safe(ref.GetDatasetId()),
		safe(exchangeID),
		safe(ref.GetInstrumentId()),
		safe(freq),
		dimensions,
	}
}

func validateDataRef(ref *pb.DataRef) error {
	if ref.GetWorkspaceId() == "" {
		return errors.New("workspace_id is required")
	}
	if ref.GetDatasetId() == "" {
		return errors.New("dataset_id is required")
	}
	if ref.GetInstrumentId() == "" {
		return errors.New("instrument_id is required")
	}
	return nil
}

func appendMessages(path string, rows []proto.Message) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	for _, row := range rows {
		data, err := protojson.Marshal(row)
		if err != nil {
			return err
		}
		if _, err := file.Write(append(data, '\n')); err != nil {
			return err
		}
	}
	return nil
}

func readMessages(path string, newMessage func() proto.Message, onRow func(proto.Message)) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		msg := newMessage()
		if err := protojson.Unmarshal([]byte(line), msg); err != nil {
			return err
		}
		onRow(msg)
	}
	return scanner.Err()
}

func rowsToMessages(rows []*pb.TimeSeriesPoint) []proto.Message {
	out := make([]proto.Message, 0, len(rows))
	for _, row := range rows {
		out = append(out, row)
	}
	return out
}

func factorRowsToMessages(rows []*pb.FactorValuePoint) []proto.Message {
	out := make([]proto.Message, 0, len(rows))
	for _, row := range rows {
		out = append(out, row)
	}
	return out
}

func recordRowsToMessages(rows []*pb.Record) []proto.Message {
	out := make([]proto.Message, 0, len(rows))
	for _, row := range rows {
		out = append(out, row)
	}
	return out
}

func filterPointFields(point *pb.TimeSeriesPoint, includes []string) *pb.TimeSeriesPoint {
	if len(includes) == 0 {
		return point
	}
	allow := make(map[string]bool, len(includes))
	for _, name := range includes {
		allow[name] = true
	}
	filtered := proto.Clone(point).(*pb.TimeSeriesPoint)
	filtered.Fields = filtered.Fields[:0]
	for _, field := range point.GetFields() {
		if allow[field.GetFieldName()] || allow[field.GetFieldId()] {
			filtered.Fields = append(filtered.Fields, field)
		}
	}
	return filtered
}

func timeInRange(value string, timeRange *pb.TimeRange) bool {
	if timeRange == nil {
		return true
	}
	if start := strings.TrimSpace(timeRange.GetStartTime()); start != "" {
		if value < start || (!timeRange.GetStartInclusive() && value == start) {
			return false
		}
	}
	if end := strings.TrimSpace(timeRange.GetEndTime()); end != "" {
		if value > end || (!timeRange.GetEndInclusive() && value == end) {
			return false
		}
	}
	return true
}

func pageTimeSeries(rows []*pb.TimeSeriesPoint, page *pb.Page) ([]*pb.TimeSeriesPoint, *pb.PageResult) {
	start, end, result := pageBounds(len(rows), page)
	return rows[start:end], result
}

func pageFactors(rows []*pb.FactorValuePoint, page *pb.Page) ([]*pb.FactorValuePoint, *pb.PageResult) {
	start, end, result := pageBounds(len(rows), page)
	return rows[start:end], result
}

func pageRecords(rows []*pb.Record, page *pb.Page) ([]*pb.Record, *pb.PageResult) {
	start, end, result := pageBounds(len(rows), page)
	return rows[start:end], result
}

func pageBounds(total int, page *pb.Page) (int, int, *pb.PageResult) {
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
	if start > total {
		start = total
	}
	end := start + int(size)
	if end > total {
		end = total
	}
	return start, end, &pb.PageResult{
		Page:    pageNo,
		Size:    size,
		Total:   uint64(total),
		HasMore: end < total,
	}
}

func inferFieldValue(name, raw string) *pb.FieldValue {
	if raw == "" {
		return StringValue(name, raw)
	}
	if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return IntValue(name, v)
	}
	if v, err := strconv.ParseFloat(raw, 64); err == nil {
		return DoubleValue(name, v)
	}
	return StringValue(name, raw)
}

func findColumn(header []string, name string) int {
	if name == "" {
		return -1
	}
	for i, col := range header {
		if strings.EqualFold(strings.TrimSpace(col), strings.TrimSpace(name)) {
			return i
		}
	}
	return -1
}

func findAnyColumn(header []string, names []string) int {
	for _, name := range names {
		if idx := findColumn(header, name); idx >= 0 {
			return idx
		}
	}
	return -1
}

func readCSVHeader(reader *csv.Reader, timeColumn string) ([]string, error) {
	for i := 0; i < 50; i++ {
		row, err := reader.Read()
		if err != nil {
			return nil, err
		}
		if findColumn(row, timeColumn) >= 0 || findAnyColumn(row, []string{"time", "timestamp", "date", "datetime", "open_time", "candle_begin_time"}) >= 0 {
			return row, nil
		}
	}
	return nil, errors.New("csv header not found")
}

func dimensionKey(values []*pb.DimensionValue) string {
	if len(values) == 0 {
		return "default"
	}
	items := make([]string, 0, len(values))
	for _, value := range values {
		items = append(items, safe(value.GetName())+"="+safe(value.GetValue()))
	}
	sort.Strings(items)
	return strings.Join(items, "__")
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func safe(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "_"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_", "..", "_")
	return replacer.Replace(value)
}
