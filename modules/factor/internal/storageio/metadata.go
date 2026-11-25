package storageio

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"google.golang.org/protobuf/proto"
)

const (
	DatasetRoleFactorResult = "factor_result"
	DataKindTimeSeries      = "time_series"
	DataKindRecord          = "record"
	DatasetStatusActive     = "active"
	ColumnStatusActive      = "active"
	ColumnTypeString        = "string"
	ColumnTypeInt           = "int"
	ColumnTypeDouble        = "double"
	ColumnTypeBool          = "bool"
	ColumnTypeTime          = "time"
	ColumnTypeJSON          = "json"
	ColumnTypeBytes         = "bytes"
)

type DatasetInfo struct {
	SpaceID      string
	DatasetID    string
	DataSourceID string
	Name         string
	Description  string
	DataKind     string
	Freq         string
	// Retention is the read-only effective retention from the Storage
	// policy: "<n>h" or "forever".
	Retention   string
	Status      string
	SubjectTags []string
	Attributes  map[string]string
	Revision    uint64
}

type ColumnInfo struct {
	ColumnName  string
	ValueType   string
	DisplayName string
	Group       string
	Required    bool
	Aliases     []string
	Status      string
	Attributes  map[string]string
}

type ResultDatasetSpec struct {
	SpaceID         string
	DatasetID       string
	SourceDatasetID string
	Name            string
	Description     string
	DataSourceID    string
	DataKind        string
	Frequency       string
	SubjectTags     []string
	Attributes      map[string]string
	Columns         []ColumnInfo
}

type Metadata interface {
	GetDataset(ctx context.Context, spaceID, datasetID string) (DatasetInfo, error)
	ListColumns(ctx context.Context, spaceID, datasetID string) ([]ColumnInfo, error)
	CreateResultDataset(ctx context.Context, spec ResultDatasetSpec) error
	UpsertColumns(ctx context.Context, spaceID, datasetID string, cols []ColumnInfo) error
	DeleteDataset(ctx context.Context, spaceID, datasetID string) error
	SetDatasetSubjectTags(ctx context.Context, spaceID, datasetID string, tags []string) error
}

func (c *Client) GetDataset(ctx context.Context, spaceID, datasetID string) (DatasetInfo, error) {
	if err := c.metadataReady("get dataset"); err != nil {
		return DatasetInfo{}, err
	}
	rsp, err := c.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{SpaceId: spaceID, DatasetId: datasetID})
	if err != nil {
		return DatasetInfo{}, rpcError("get dataset", err)
	}
	if rsp == nil {
		return DatasetInfo{}, fmt.Errorf("%w: get dataset returned an empty response", ErrInfra)
	}
	if err := responseError("get dataset", rsp.GetRetInfo()); err != nil {
		return DatasetInfo{}, err
	}
	if rsp.GetDataset() == nil {
		return DatasetInfo{}, fmt.Errorf("%w: get dataset returned no dataset", ErrInfra)
	}
	return datasetInfoFromProto(rsp.GetDataset()), nil
}

func (c *Client) DatasetColumns(ctx context.Context, spaceID, datasetID string) ([]string, error) {
	cols, err := c.ListColumns(ctx, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	reserved := map[string]struct{}{
		"subject_id": {}, "freq": {}, "data_time": {}, "series_tag": {},
	}
	out := make([]string, 0, len(cols))
	for _, col := range cols {
		if col.Status != "" && col.Status != ColumnStatusActive {
			continue
		}
		if _, ok := reserved[col.ColumnName]; ok {
			continue
		}
		out = append(out, col.ColumnName)
	}
	sort.Strings(out)
	return out, nil
}

func (c *Client) ListColumns(ctx context.Context, spaceID, datasetID string) ([]ColumnInfo, error) {
	if err := c.metadataReady("list dataset columns"); err != nil {
		return nil, err
	}
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(datasetID) == "" {
		return nil, errors.New("space_id and dataset_id are required")
	}
	var out []ColumnInfo
	for page := uint32(1); ; page++ {
		rsp, err := c.metadata.ListDatasetColumns(ctx, &storagepb.ListDatasetColumnsReq{
			SpaceId: spaceID, DatasetId: datasetID,
			Page: &commonpb.Page{Page: page, Size: 1000},
		})
		if err != nil {
			return nil, rpcError("list dataset columns", err)
		}
		if rsp == nil {
			return nil, fmt.Errorf("%w: list dataset columns returned an empty response", ErrInfra)
		}
		if err := responseError("list dataset columns", rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		for _, item := range rsp.GetColumns() {
			if item != nil {
				out = append(out, columnInfoFromProto(item))
			}
		}
		pageResult := rsp.GetPageResult()
		if pageResult == nil || !pageResult.GetHasMore() {
			break
		}
		if page > 10000 {
			return nil, fmt.Errorf("%w: metadata column pagination exceeded the page limit", ErrInfra)
		}
	}
	return out, nil
}

func (c *Client) CreateResultDataset(ctx context.Context, spec ResultDatasetSpec) error {
	if err := c.metadataReady("create result dataset"); err != nil {
		return err
	}
	if err := validateResultDatasetSpec(spec); err != nil {
		return err
	}
	expected := datasetFromResultSpec(spec)
	existing, getErr := c.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{SpaceId: spec.SpaceID, DatasetId: spec.DatasetID})
	if getErr != nil {
		return rpcError("check result dataset", getErr)
	}
	if existing == nil {
		return fmt.Errorf("%w: check result dataset returned an empty response", ErrInfra)
	}
	if existing.GetRetInfo() != nil && existing.GetRetInfo().GetCode() == commonpb.ErrorCode_SUCCESS {
		if err := validateExistingResultDataset(expected, existing.GetDataset()); err != nil {
			return err
		}
		return c.UpsertColumns(ctx, spec.SpaceID, spec.DatasetID, spec.Columns)
	}
	if existing.GetRetInfo() == nil || !isDatasetNotFound(existing.GetRetInfo().GetCode()) {
		return responseError("check result dataset", existing.GetRetInfo())
	}
	columns, err := datasetColumnsToProto(spec.SpaceID, spec.DatasetID, spec.Columns)
	if err != nil {
		return err
	}
	// 数据集与列在同一事务里建好，返回前 Storage 已发布元数据缓存，建好即可写入。
	created, err := c.metadata.CreateDataset(ctx, &storagepb.CreateDatasetReq{Dataset: expected, Columns: columns})
	if err != nil {
		return rpcError("create result dataset", err)
	}
	if created == nil {
		return fmt.Errorf("%w: create result dataset returned an empty response", ErrInfra)
	}
	if created.GetRetInfo() != nil && created.GetRetInfo().GetCode() == commonpb.ErrorCode_CONFLICT {
		current, readErr := c.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{SpaceId: spec.SpaceID, DatasetId: spec.DatasetID})
		if readErr != nil {
			return rpcError("confirm existing result dataset", readErr)
		}
		if current == nil {
			return fmt.Errorf("%w: confirm existing result dataset returned an empty response", ErrInfra)
		}
		if err := responseError("confirm existing result dataset", current.GetRetInfo()); err != nil {
			return err
		}
		if err := validateExistingResultDataset(expected, current.GetDataset()); err != nil {
			return err
		}
		return c.UpsertColumns(ctx, spec.SpaceID, spec.DatasetID, spec.Columns)
	}
	if err := responseError("create result dataset", created.GetRetInfo()); err != nil {
		return err
	}
	return validateExistingResultDataset(expected, created.GetDataset())
}

// SetDatasetSubjectTags replaces a dataset's subject tags and keeps every other
// field; the update carries the revision it read, so a concurrent edit fails
// instead of being overwritten.
func (c *Client) SetDatasetSubjectTags(ctx context.Context, spaceID, datasetID string, tags []string) error {
	if err := c.metadataReady("set dataset subject tags"); err != nil {
		return err
	}
	current, err := c.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{SpaceId: spaceID, DatasetId: datasetID})
	if err != nil {
		return rpcError("read dataset before setting subject tags", err)
	}
	if current == nil {
		return fmt.Errorf("%w: read dataset before setting subject tags returned an empty response", ErrInfra)
	}
	if err := responseError("read dataset before setting subject tags", current.GetRetInfo()); err != nil {
		return err
	}
	dataset := proto.Clone(current.GetDataset()).(*storagepb.Dataset)
	dataset.SubjectTags = append([]string(nil), tags...)
	updated, err := c.metadata.UpdateDataset(ctx, &storagepb.UpdateDatasetReq{Dataset: dataset})
	if err != nil {
		return rpcError("set dataset subject tags", err)
	}
	if updated == nil {
		return fmt.Errorf("%w: set dataset subject tags returned an empty response", ErrInfra)
	}
	return responseError("set dataset subject tags", updated.GetRetInfo())
}

func (c *Client) UpsertColumns(ctx context.Context, spaceID, datasetID string, columns []ColumnInfo) error {
	if err := c.metadataReady("upsert dataset columns"); err != nil {
		return err
	}
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(datasetID) == "" {
		return errors.New("space_id and dataset_id are required")
	}
	ordered, err := datasetColumnsToProto(spaceID, datasetID, columns)
	if err != nil {
		return err
	}
	for _, column := range ordered {
		rsp, err := c.metadata.UpsertDatasetColumn(ctx, &storagepb.UpsertDatasetColumnReq{Column: column})
		if err != nil {
			return rpcError("upsert dataset column", err)
		}
		if rsp == nil {
			return fmt.Errorf("%w: upsert dataset column returned an empty response", ErrInfra)
		}
		if err := responseError("upsert dataset column", rsp.GetRetInfo()); err != nil {
			return err
		}
	}
	return nil
}

// datasetColumnsToProto 把列定义按列名排序后转成 Storage 的列，拒绝空列名、重名和不支持的类型。
func datasetColumnsToProto(spaceID, datasetID string, columns []ColumnInfo) ([]*storagepb.DatasetColumn, error) {
	ordered := append([]ColumnInfo(nil), columns...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ColumnName < ordered[j].ColumnName })
	out := make([]*storagepb.DatasetColumn, 0, len(ordered))
	seen := make(map[string]struct{}, len(ordered))
	for _, item := range ordered {
		if strings.TrimSpace(item.ColumnName) == "" {
			return nil, errors.New("column_name is required")
		}
		if _, ok := seen[item.ColumnName]; ok {
			return nil, fmt.Errorf("duplicate dataset column %q", item.ColumnName)
		}
		seen[item.ColumnName] = struct{}{}
		valueType := columnTypeToProto(item.ValueType)
		if valueType == storagepb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED {
			return nil, fmt.Errorf("dataset column %q has unsupported value type", item.ColumnName)
		}
		out = append(out, &storagepb.DatasetColumn{
			SpaceId: spaceID, DatasetId: datasetID, ColumnName: item.ColumnName,
			ValueType: valueType, DisplayName: item.DisplayName, Group: item.Group, Required: item.Required,
			Aliases: append([]string(nil), item.Aliases...), Status: defaultString(item.Status, ColumnStatusActive),
			Attributes: cloneStringMap(item.Attributes),
		})
	}
	return out, nil
}

func (c *Client) DeleteDataset(ctx context.Context, spaceID, datasetID string) error {
	if err := c.metadataReady("delete dataset"); err != nil {
		return err
	}
	// Storage 在服务端一次删完：先删元数据，再删行；查询索引由 storage-view 下一轮维护摘掉。
	// 中途失败时重试同一个调用即可：元数据已经不在时 Storage 照样删行。
	rsp, err := c.metadata.DeleteDataset(ctx, &storagepb.DeleteDatasetReq{SpaceId: spaceID, DatasetId: datasetID})
	if err != nil {
		return rpcError("delete dataset", err)
	}
	if rsp == nil {
		return fmt.Errorf("%w: delete dataset returned an empty response", ErrInfra)
	}
	if rsp.GetRetInfo() != nil && isDatasetNotFound(rsp.GetRetInfo().GetCode()) {
		return nil
	}
	if err := responseError("delete dataset", rsp.GetRetInfo()); err != nil {
		return err
	}
	return nil
}

func isDatasetNotFound(code commonpb.ErrorCode) bool {
	return code == commonpb.ErrorCode_NOT_FOUND || code == commonpb.ErrorCode_DATASET_NOT_FOUND
}

func validateResultDatasetSpec(spec ResultDatasetSpec) error {
	if strings.TrimSpace(spec.SpaceID) == "" || strings.TrimSpace(spec.DatasetID) == "" ||
		strings.TrimSpace(spec.DataSourceID) == "" ||
		strings.TrimSpace(spec.SourceDatasetID) == "" || strings.TrimSpace(spec.Name) == "" ||
		strings.TrimSpace(spec.Frequency) == "" {
		return errors.New("space, result/source dataset, name, data source and frequency are required")
	}
	if spec.DataKind != DataKindTimeSeries {
		return fmt.Errorf("factor result dataset must be %q", DataKindTimeSeries)
	}
	return nil
}

// resultMaintenancePriority 告诉 Storage 查询索引的维护顺序：越小越先，因子结果排在采集结果（0）之后；Storage 不认识“因子”。
const resultMaintenancePriority = 10

func datasetFromResultSpec(spec ResultDatasetSpec) *storagepb.Dataset {
	attributes := cloneStringMap(spec.Attributes)
	if attributes == nil {
		attributes = make(map[string]string)
	}
	attributes["dataset_role"] = DatasetRoleFactorResult
	attributes["source_dataset_id"] = spec.SourceDatasetID
	return &storagepb.Dataset{
		SpaceId: spec.SpaceID, DatasetId: spec.DatasetID, DataSourceId: spec.DataSourceID,
		Name: spec.Name, Description: spec.Description, DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES,
		Freq: spec.Frequency, Status: DatasetStatusActive, Attributes: attributes,
		SubjectTags:         append([]string(nil), spec.SubjectTags...),
		MaintenancePriority: resultMaintenancePriority,
	}
}

func validateExistingResultDataset(expected, actual *storagepb.Dataset) error {
	if actual == nil {
		return fmt.Errorf("%w: result dataset response omitted the dataset", ErrInfra)
	}
	if actual.GetSpaceId() != expected.GetSpaceId() || actual.GetDatasetId() != expected.GetDatasetId() ||
		actual.GetDataSourceId() != expected.GetDataSourceId() || actual.GetDataKind() != expected.GetDataKind() ||
		actual.GetName() != expected.GetName() || actual.GetDescription() != expected.GetDescription() ||
		actual.GetAttributes()["dataset_role"] != DatasetRoleFactorResult ||
		actual.GetAttributes()["source_dataset_id"] != expected.GetAttributes()["source_dataset_id"] ||
		actual.GetFreq() != expected.GetFreq() ||
		!equalStrings(actual.GetSubjectTags(), expected.GetSubjectTags()) ||
		!containsAttributes(actual.GetAttributes(), expected.GetAttributes()) {
		return fmt.Errorf("result dataset %s conflicts with the requested factor result contract", expected.GetDatasetId())
	}
	return nil
}

func datasetInfoFromProto(item *storagepb.Dataset) DatasetInfo {
	return DatasetInfo{
		SpaceID: item.GetSpaceId(), DatasetID: item.GetDatasetId(), DataSourceID: item.GetDataSourceId(),
		Name: item.GetName(), Description: item.GetDescription(),
		DataKind: dataKindFromProto(item.GetDataKind()),
		Freq:     item.GetFreq(), Retention: item.GetRetention(),
		Status: item.GetStatus(), SubjectTags: append([]string(nil), item.GetSubjectTags()...),
		Attributes: cloneStringMap(item.GetAttributes()), Revision: item.GetRevision(),
	}
}

func columnInfoFromProto(item *storagepb.DatasetColumn) ColumnInfo {
	return ColumnInfo{
		ColumnName: item.GetColumnName(), ValueType: columnTypeFromProto(item.GetValueType()),
		DisplayName: item.GetDisplayName(), Group: item.GetGroup(),
		Required: item.GetRequired(), Aliases: append([]string(nil), item.GetAliases()...),
		Status: item.GetStatus(), Attributes: cloneStringMap(item.GetAttributes()),
	}
}

func dataKindFromProto(value storagepb.DataKind) string {
	switch value {
	case storagepb.DataKind_DATA_KIND_TIME_SERIES:
		return DataKindTimeSeries
	case storagepb.DataKind_DATA_KIND_RECORD:
		return DataKindRecord
	default:
		return ""
	}
}

func columnTypeFromProto(value storagepb.FieldValueType) string {
	switch value {
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING:
		return ColumnTypeString
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_INT:
		return ColumnTypeInt
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
		return ColumnTypeDouble
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_BOOL:
		return ColumnTypeBool
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME:
		return ColumnTypeTime
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_JSON:
		return ColumnTypeJSON
	case storagepb.FieldValueType_FIELD_VALUE_TYPE_BYTES:
		return ColumnTypeBytes
	default:
		return ""
	}
}

func columnTypeToProto(value string) storagepb.FieldValueType {
	switch value {
	case ColumnTypeString:
		return storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING
	case ColumnTypeInt:
		return storagepb.FieldValueType_FIELD_VALUE_TYPE_INT
	case ColumnTypeDouble:
		return storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE
	case ColumnTypeBool:
		return storagepb.FieldValueType_FIELD_VALUE_TYPE_BOOL
	case ColumnTypeTime:
		return storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME
	case ColumnTypeJSON:
		return storagepb.FieldValueType_FIELD_VALUE_TYPE_JSON
	case ColumnTypeBytes:
		return storagepb.FieldValueType_FIELD_VALUE_TYPE_BYTES
	default:
		return storagepb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED
	}
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func equalStrings(left, right []string) bool {
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	return reflect.DeepEqual(left, right)
}

func containsAttributes(actual, expected map[string]string) bool {
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}
	return true
}
