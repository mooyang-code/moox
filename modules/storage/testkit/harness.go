// Package testkit exposes a small in-process Storage Access harness backed by
// the real Pebble primary store. It is intended for cross-module integration
// tests that cannot import Storage's internal packages directly.
package testkit

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
	storagepebble "github.com/mooyang-code/moox/modules/storage/internal/infra/device/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
)

type DatasetSchema struct {
	SpaceID   string
	DatasetID string
	Columns   map[string]pb.FieldValueType
}

type Harness struct {
	store   *storagepebble.Store
	schemas map[string]DatasetSchema
}

func Open(path string, schemas []DatasetSchema) (*Harness, error) {
	store, err := storagepebble.Open(storagepebble.Options{Path: path, DisableSyncWrites: true})
	if err != nil {
		return nil, err
	}
	h := &Harness{store: store, schemas: make(map[string]DatasetSchema, len(schemas))}
	for _, schema := range schemas {
		key := datasetKey(schema.SpaceID, schema.DatasetID)
		if strings.TrimSpace(schema.SpaceID) == "" || strings.TrimSpace(schema.DatasetID) == "" {
			_ = store.Close()
			return nil, fmt.Errorf("space_id and dataset_id are required")
		}
		if _, exists := h.schemas[key]; exists {
			_ = store.Close()
			return nil, fmt.Errorf("duplicate dataset schema %s", key)
		}
		h.schemas[key] = schema
	}
	return h, nil
}

func (h *Harness) Close() error { return h.store.Close() }

func (h *Harness) WriteTimeSeriesRows(ctx context.Context, req *pb.WriteTimeSeriesRowsReq, _ ...client.Option) (*pb.WriteTimeSeriesRowsRsp, error) {
	if req == nil || len(req.GetRows()) == 0 {
		return &pb.WriteTimeSeriesRowsRsp{RetInfo: failure("rows are required")}, nil
	}
	rows := make([]*pb.PrimaryStoreRow, 0, len(req.GetRows()))
	for _, row := range req.GetRows() {
		if err := h.validateTimeSeriesRow(row); err != nil {
			return &pb.WriteTimeSeriesRowsRsp{RetInfo: failure(err.Error())}, nil
		}
		key := row.GetKey()
		version, err := factkey.NormalizeTimeVersion(key.GetDataTime())
		if err != nil {
			return &pb.WriteTimeSeriesRowsRsp{RetInfo: failure("data_time must be RFC3339")}, nil
		}
		rows = append(rows, &pb.PrimaryStoreRow{
			Key:     &pb.PrimaryStoreKey{SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(), DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Key: factkey.BuildTimeSeriesDataKey(key.GetSubjectId(), key.GetFreq(), key.GetDimensions()), Version: version},
			Columns: cloneColumns(row.GetColumns()), Attributes: cloneMap(row.GetAttributes()), WriteMode: req.GetWriteMode(),
		})
	}
	if err := h.store.WriteRows(ctx, rows); err != nil {
		return nil, err
	}
	return &pb.WriteTimeSeriesRowsRsp{RetInfo: success()}, nil
}

func (h *Harness) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq, _ ...client.Option) (*pb.ReadTimeSeriesRowsRsp, error) {
	if req == nil || len(req.GetKeys()) == 0 {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: failure("keys are required")}, nil
	}
	out := make([]*pb.TimeSeriesRow, 0, len(req.GetKeys()))
	for _, key := range req.GetKeys() {
		if _, ok := h.schemas[datasetKey(key.GetSpaceId(), key.GetDatasetId())]; !ok {
			return &pb.ReadTimeSeriesRowsRsp{RetInfo: failure("unknown dataset")}, nil
		}
		version := ""
		var versionRange *pb.VersionRange
		if key.GetDataTime() != "" {
			normalized, err := factkey.NormalizeTimeVersion(key.GetDataTime())
			if err != nil {
				return &pb.ReadTimeSeriesRowsRsp{RetInfo: failure("invalid data_time")}, nil
			}
			version = normalized
		} else if req.GetTimeRange() != nil {
			start, err := factkey.NormalizeTimeVersion(req.GetTimeRange().GetStartTime())
			if err != nil {
				return &pb.ReadTimeSeriesRowsRsp{RetInfo: failure("invalid start_time")}, nil
			}
			end, err := factkey.NormalizeTimeVersion(req.GetTimeRange().GetEndTime())
			if err != nil {
				return &pb.ReadTimeSeriesRowsRsp{RetInfo: failure("invalid end_time")}, nil
			}
			versionRange = &pb.VersionRange{StartVersion: start, EndVersion: end}
		} else {
			return &pb.ReadTimeSeriesRowsRsp{RetInfo: failure("data_time or time_range is required")}, nil
		}
		primaryKey := &pb.PrimaryStoreKey{SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(), DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Key: factkey.BuildTimeSeriesDataKey(key.GetSubjectId(), key.GetFreq(), key.GetDimensions()), Version: version}
		page := req.GetPage()
		if page == nil {
			page = &pb.Page{Page: 1, Size: 1000}
		}
		rows, _, err := h.store.ReadRows(ctx, []*pb.PrimaryStoreKey{primaryKey}, versionRange, req.GetOrder(), req.GetColumnNames(), page)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			copiedKey := proto.Clone(key).(*pb.TimeSeriesKey)
			copiedKey.DataTime = row.GetKey().GetVersion()
			out = append(out, &pb.TimeSeriesRow{Key: copiedKey, Columns: cloneColumns(row.GetColumns()), Attributes: cloneMap(row.GetAttributes())})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetKey().GetDataTime() < out[j].GetKey().GetDataTime() })
	if req.GetOrder() == pb.SortOrder_SORT_ORDER_DESC {
		for left, right := 0, len(out)-1; left < right; left, right = left+1, right-1 {
			out[left], out[right] = out[right], out[left]
		}
	}
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: success(), Rows: out, PageResult: &pb.PageResult{Page: 1, Size: uint32(len(out))}}, nil
}

func (h *Harness) writeRecordRows(ctx context.Context, req *pb.WriteRecordRowsReq) (*pb.WriteRecordRowsRsp, error) {
	if req == nil || len(req.GetRows()) == 0 {
		return &pb.WriteRecordRowsRsp{RetInfo: failure("rows are required")}, nil
	}
	rows := make([]*pb.PrimaryStoreRow, 0, len(req.GetRows()))
	keys := make([]*pb.RecordKey, 0, len(req.GetRows()))
	for _, row := range req.GetRows() {
		if row == nil || row.GetKey() == nil {
			return &pb.WriteRecordRowsRsp{RetInfo: failure("row key is required")}, nil
		}
		key := row.GetKey()
		schema, ok := h.schemas[datasetKey(key.GetSpaceId(), key.GetDatasetId())]
		if !ok {
			return &pb.WriteRecordRowsRsp{RetInfo: failure("unknown dataset")}, nil
		}
		if strings.TrimSpace(key.GetRecordId()) == "" || strings.TrimSpace(key.GetVersion()) == "" {
			return &pb.WriteRecordRowsRsp{RetInfo: failure("record_id and version are required")}, nil
		}
		if err := validateColumns(schema, row.GetColumns()); err != nil {
			return &pb.WriteRecordRowsRsp{RetInfo: failure(err.Error())}, nil
		}
		dataKey, err := factkey.BuildRecordDataKey(key.GetRecordId())
		if err != nil {
			return &pb.WriteRecordRowsRsp{RetInfo: failure(err.Error())}, nil
		}
		rows = append(rows, &pb.PrimaryStoreRow{Key: &pb.PrimaryStoreKey{SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(), DataKind: pb.DataKind_DATA_KIND_RECORD, Key: dataKey, Version: factkey.NormalizeVersion(key.GetVersion())}, Columns: cloneColumns(row.GetColumns()), Attributes: cloneMap(row.GetAttributes()), WriteMode: req.GetWriteMode()})
		keys = append(keys, proto.Clone(key).(*pb.RecordKey))
	}
	if err := h.store.WriteRows(ctx, rows); err != nil {
		return nil, err
	}
	return &pb.WriteRecordRowsRsp{RetInfo: success(), Keys: keys}, nil
}

func (h *Harness) WriteRecordRows(ctx context.Context, req *pb.WriteRecordRowsReq, _ ...client.Option) (*pb.WriteRecordRowsRsp, error) {
	return h.writeRecordRows(ctx, req)
}

func (h *Harness) ReadRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq, _ ...client.Option) (*pb.ReadRecordRowsRsp, error) {
	if req == nil || len(req.GetKeys()) == 0 {
		return &pb.ReadRecordRowsRsp{RetInfo: failure("keys are required")}, nil
	}
	out := make([]*pb.RecordRow, 0, len(req.GetKeys()))
	for _, key := range req.GetKeys() {
		if _, ok := h.schemas[datasetKey(key.GetSpaceId(), key.GetDatasetId())]; !ok {
			return &pb.ReadRecordRowsRsp{RetInfo: failure("unknown dataset")}, nil
		}
		dataKey, err := factkey.BuildRecordDataKey(key.GetRecordId())
		if err != nil {
			return &pb.ReadRecordRowsRsp{RetInfo: failure(err.Error())}, nil
		}
		primaryKey := &pb.PrimaryStoreKey{SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(), DataKind: pb.DataKind_DATA_KIND_RECORD, Key: dataKey, Version: factkey.NormalizeVersion(key.GetVersion())}
		rows, _, err := h.store.ReadRows(ctx, []*pb.PrimaryStoreKey{primaryKey}, nil, req.GetOrder(), req.GetColumnNames(), &pb.Page{Page: 1, Size: 1})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			copied := proto.Clone(key).(*pb.RecordKey)
			copied.Version = row.GetKey().GetVersion()
			out = append(out, &pb.RecordRow{Key: copied, Columns: cloneColumns(row.GetColumns()), Attributes: cloneMap(row.GetAttributes())})
		}
	}
	return &pb.ReadRecordRowsRsp{RetInfo: success(), Rows: out, PageResult: &pb.PageResult{Page: 1, Size: uint32(len(out))}}, nil
}

func (h *Harness) validateTimeSeriesRow(row *pb.TimeSeriesRow) error {
	if row == nil || row.GetKey() == nil {
		return fmt.Errorf("row key is required")
	}
	key := row.GetKey()
	schema, ok := h.schemas[datasetKey(key.GetSpaceId(), key.GetDatasetId())]
	if !ok {
		return fmt.Errorf("unknown dataset %s/%s", key.GetSpaceId(), key.GetDatasetId())
	}
	if strings.TrimSpace(key.GetSubjectId()) == "" || strings.TrimSpace(key.GetFreq()) == "" {
		return fmt.Errorf("subject_id and freq are required")
	}
	return validateColumns(schema, row.GetColumns())
}

func validateColumns(schema DatasetSchema, columns []*pb.ColumnValue) error {
	seen := make(map[string]bool, len(columns))
	for _, column := range columns {
		expected, exists := schema.Columns[column.GetColumnName()]
		if !exists {
			return fmt.Errorf("column %q is not registered", column.GetColumnName())
		}
		if seen[column.GetColumnName()] {
			return fmt.Errorf("duplicate column %q", column.GetColumnName())
		}
		seen[column.GetColumnName()] = true
		if column.GetValueType() != expected {
			return fmt.Errorf("column %q has type %s, want %s", column.GetColumnName(), column.GetValueType(), expected)
		}
	}
	return nil
}

func datasetKey(spaceID, datasetID string) string { return spaceID + "\x00" + datasetID }
func success() *pb.RetInfo                        { return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"} }
func failure(message string) *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_INVALID_PARAM, Msg: message}
}
func cloneColumns(in []*pb.ColumnValue) []*pb.ColumnValue {
	out := make([]*pb.ColumnValue, 0, len(in))
	for _, value := range in {
		out = append(out, proto.Clone(value).(*pb.ColumnValue))
	}
	return out
}
func cloneMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
