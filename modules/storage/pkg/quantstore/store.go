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

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const defaultRoot = "var/storage"

type Store struct {
	root string
	mu   sync.Mutex
}

type CSVImportOptions struct {
	DatasetID  string
	SubjectID  string
	Freq       string
	TimeColumn string
	Dimensions map[string]string
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

func StringValue(name, value string) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: value}},
	}
}

func DoubleValue(name string, value float64) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}},
	}
}

func IntValue(name string, value int64) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_INT,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: value}},
	}
}

func (s *Store) WriteRows(ctx context.Context, rows []*pb.DataRow, mode pb.WriteMode) error {
	_ = ctx
	if len(rows) == 0 {
		return nil
	}
	if mode == pb.WriteMode_WRITE_MODE_UNSPECIFIED {
		mode = pb.WriteMode_WRITE_MODE_UPSERT
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	grouped := make(map[string][]*pb.DataRow)
	for _, row := range rows {
		if err := validateRow(row); err != nil {
			return err
		}
		path := s.factPath(row.GetSlice())
		grouped[path] = append(grouped[path], row)
	}

	for path, pathRows := range grouped {
		switch mode {
		case pb.WriteMode_WRITE_MODE_APPEND:
			if err := appendMessages(path, dataRowsToMessages(pathRows)); err != nil {
				return err
			}
		case pb.WriteMode_WRITE_MODE_OVERWRITE:
			if err := rewriteRows(path, pathRows); err != nil {
				return err
			}
		default:
			if err := upsertRows(path, pathRows); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) ReadRows(ctx context.Context, slice *pb.DataSlice, readMode pb.ReadMode, timeRange *pb.TimeRange, snapshotTime string, rowIDs []string, columnNames []string, page *pb.Page) ([]*pb.DataRow, *pb.PageResult, error) {
	_ = ctx
	if err := validateSlice(slice); err != nil {
		return nil, nil, err
	}
	if readMode == pb.ReadMode_READ_MODE_UNSPECIFIED {
		readMode = pb.ReadMode_READ_MODE_RANGE
	}
	if readMode == pb.ReadMode_READ_MODE_POINT && len(rowIDs) == 0 {
		return nil, nil, errors.New("row_ids is required for point read")
	}

	paths, err := s.factPaths(slice)
	if err != nil {
		return nil, nil, err
	}
	allowRows := makeSet(rowIDs)

	var rows []*pb.DataRow
	for _, path := range paths {
		if err := readMessages(path, func() proto.Message { return &pb.DataRow{} }, func(msg proto.Message) {
			row := msg.(*pb.DataRow)
			if readMode == pb.ReadMode_READ_MODE_POINT && !allowRows[row.GetRowId()] {
				return
			}
			if readMode != pb.ReadMode_READ_MODE_POINT && !timeInRange(row.GetDataTime(), timeRange) {
				return
			}
			if readMode == pb.ReadMode_READ_MODE_LATEST_BEFORE && snapshotTime != "" && row.GetDataTime() > snapshotTime {
				return
			}
			rows = append(rows, filterRowColumns(row, columnNames))
		}); err != nil {
			return nil, nil, err
		}
	}

	if readMode == pb.ReadMode_READ_MODE_LATEST_BEFORE {
		rows = latestRows(rows)
	}
	sortRows(rows)
	paged, result := pageRows(rows, page)
	return paged, result, nil
}

func (s *Store) ImportCSV(ctx context.Context, path string, opts CSVImportOptions) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	header, err := readCSVHeader(reader, opts.TimeColumn)
	if err != nil {
		return err
	}
	timeIndex := findColumn(header, opts.TimeColumn)
	if timeIndex < 0 {
		timeIndex = findAnyColumn(header, []string{"time", "timestamp", "date", "datetime", "open_time"})
	}
	if timeIndex < 0 {
		return fmt.Errorf("time column not found in %s", path)
	}

	slice := &pb.DataSlice{
		DatasetId:  opts.DatasetID,
		SubjectId:  opts.SubjectID,
		Freq:       opts.Freq,
		Dimensions: opts.Dimensions,
	}
	var batch []*pb.DataRow
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if timeIndex >= len(record) {
			continue
		}
		row := &pb.DataRow{Slice: slice, DataTime: strings.TrimSpace(record[timeIndex])}
		for i, name := range header {
			if i >= len(record) || i == timeIndex {
				continue
			}
			row.Columns = append(row.Columns, inferColumnValue(strings.TrimSpace(name), strings.TrimSpace(record[i])))
		}
		batch = append(batch, row)
	}
	return s.WriteRows(ctx, batch, pb.WriteMode_WRITE_MODE_APPEND)
}

func (s *Store) factPath(slice *pb.DataSlice) string {
	parts := []string{
		s.root,
		"facts",
		safe(slice.GetDatasetId()),
		safe(defaultString(slice.GetSubjectId(), "default")),
		safe(defaultString(slice.GetFreq(), "default")),
		dimensionKey(slice.GetDimensions()),
	}
	return filepath.Join(parts...) + ".jsonl"
}

func (s *Store) factPaths(slice *pb.DataSlice) ([]string, error) {
	parts := []string{
		s.root,
		"facts",
		safe(slice.GetDatasetId()),
		patternPart(slice.GetSubjectId()),
		patternPart(slice.GetFreq()),
		dimensionPattern(slice.GetDimensions()),
	}
	return filepath.Glob(filepath.Join(parts...) + ".jsonl")
}

func validateRow(row *pb.DataRow) error {
	if row == nil {
		return errors.New("row is required")
	}
	return validateSlice(row.GetSlice())
}

func validateSlice(slice *pb.DataSlice) error {
	if slice == nil {
		return errors.New("slice is required")
	}
	if slice.GetDatasetId() == "" {
		return errors.New("dataset_id is required")
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

func rewriteRows(path string, rows []*pb.DataRow) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return appendMessages(path, dataRowsToMessages(rows))
}

func upsertRows(path string, rows []*pb.DataRow) error {
	var existing []*pb.DataRow
	if err := readMessages(path, func() proto.Message { return &pb.DataRow{} }, func(msg proto.Message) {
		existing = append(existing, msg.(*pb.DataRow))
	}); err != nil {
		return err
	}
	index := make(map[string]int, len(existing))
	for i, row := range existing {
		index[rowKey(row, i)] = i
	}
	for i, row := range rows {
		key := rowKey(row, len(existing)+i)
		if pos, ok := index[key]; ok {
			existing[pos] = row
			continue
		}
		index[key] = len(existing)
		existing = append(existing, row)
	}
	return rewriteRows(path, existing)
}

func dataRowsToMessages(rows []*pb.DataRow) []proto.Message {
	out := make([]proto.Message, 0, len(rows))
	for _, row := range rows {
		out = append(out, row)
	}
	return out
}

func filterRowColumns(row *pb.DataRow, includes []string) *pb.DataRow {
	if len(includes) == 0 {
		return row
	}
	allow := makeSet(includes)
	filtered := proto.Clone(row).(*pb.DataRow)
	filtered.Columns = filtered.Columns[:0]
	for _, column := range row.GetColumns() {
		if allow[column.GetColumnName()] {
			filtered.Columns = append(filtered.Columns, column)
		}
	}
	return filtered
}

func latestRows(rows []*pb.DataRow) []*pb.DataRow {
	latest := make(map[string]*pb.DataRow)
	for _, row := range rows {
		key := row.GetSlice().GetSubjectId()
		if key == "" {
			key = row.GetSlice().GetDatasetId()
		}
		if prev := latest[key]; prev == nil || row.GetDataTime() > prev.GetDataTime() {
			latest[key] = row
		}
	}
	out := make([]*pb.DataRow, 0, len(latest))
	for _, row := range latest {
		out = append(out, row)
	}
	return out
}

func sortRows(rows []*pb.DataRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		left := rows[i]
		right := rows[j]
		if left.GetSlice().GetSubjectId() != right.GetSlice().GetSubjectId() {
			return left.GetSlice().GetSubjectId() < right.GetSlice().GetSubjectId()
		}
		if left.GetDataTime() != right.GetDataTime() {
			return left.GetDataTime() < right.GetDataTime()
		}
		return left.GetRowId() < right.GetRowId()
	})
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

func pageRows(rows []*pb.DataRow, page *pb.Page) ([]*pb.DataRow, *pb.PageResult) {
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

func inferColumnValue(name, raw string) *pb.ColumnValue {
	if raw == "" {
		return StringValue(name, raw)
	}
	if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return IntValue(name, v)
	}
	if v, err := strconv.ParseFloat(raw, 64); err == nil {
		return DoubleValue(name, v)
	}
	if v, err := strconv.ParseBool(raw); err == nil {
		return &pb.ColumnValue{
			ColumnName: name,
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_BOOL,
			Value:      &pb.TypedValue{Value: &pb.TypedValue_BoolValue{BoolValue: v}},
		}
	}
	return StringValue(name, raw)
}

func rowKey(row *pb.DataRow, fallback int) string {
	if row.GetRowId() != "" {
		return "id:" + row.GetRowId()
	}
	if row.GetDataTime() != "" {
		return "time:" + row.GetDataTime()
	}
	return fmt.Sprintf("append:%d", fallback)
}

func makeSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
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

func dimensionKey(values map[string]string) string {
	if len(values) == 0 {
		return "default"
	}
	items := make([]string, 0, len(values))
	for key, value := range values {
		items = append(items, safe(key)+"="+safe(value))
	}
	sort.Strings(items)
	return strings.Join(items, "__")
}

func dimensionPattern(values map[string]string) string {
	if len(values) == 0 {
		return "*"
	}
	return dimensionKey(values)
}

func patternPart(value string) string {
	if value == "" {
		return "*"
	}
	return safe(value)
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
