package metadata

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	"github.com/mooyang-code/moox/modules/storage/internal/core/metadata"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/infra/metadata/sqlite"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"gopkg.in/yaml.v2"
)

// SeedOptions 描述元数据导入所需的配置。
type SeedOptions struct {
	Storage    storageconfig.StorageConfig
	SchemaPath string
	SeedPath   string
}

// ImportResult 汇总各类元数据的导入数量，便于日志展示。
type ImportResult struct {
	Spaces             int
	DataSources        int
	Subjects           int
	SubjectSymbols     int
	Datasets           int
	DatasetSubjects    int
	Fields             int
	Factors            int
	DatasetColumns     int
	Views              int
	ViewColumns        int
	PrimaryStoreNodes  int
	Devices            int
	PrimaryStoreRoutes int
}

// ImportSeed 读取领域型 metadata seed 文件，并按依赖顺序通过元数据控制面写入。
// 它会先确保 schema 存在（幂等），再逐类 Upsert，全部使用 Upsert 语义，可重复执行。
func ImportSeed(ctx context.Context, opts SeedOptions) (ImportResult, error) {
	var result ImportResult
	if opts.SeedPath == "" {
		return result, fmt.Errorf("metadata seed path is required")
	}
	raw, err := os.ReadFile(opts.SeedPath)
	if err != nil {
		return result, fmt.Errorf("read seed file: %w", err)
	}
	var seed seedFile
	if err := yaml.UnmarshalStrict(raw, &seed); err != nil {
		return result, fmt.Errorf("parse seed file %s: %w", opts.SeedPath, err)
	}

	root := opts.Storage.Root
	if root == "" {
		root = "var/storage"
	}
	metadataPath := opts.Storage.Metadata.Path
	if metadataPath == "" {
		metadataPath = filepath.Join(root, "metadata", "storage_metadata.db")
	}
	if err := os.MkdirAll(filepath.Dir(metadataPath), 0o755); err != nil {
		return result, err
	}
	store, err := metasqlite.Open(ctx, metasqlite.Options{Path: metadataPath, SchemaPath: opts.SchemaPath})
	if err != nil {
		return result, err
	}
	defer store.Close()
	if opts.SchemaPath != "" {
		if err := store.InitSchema(ctx); err != nil {
			return result, fmt.Errorf("ensure schema: %w", err)
		}
	}

	return importEntities(ctx, store, seed)
}

// importEntities 按依赖顺序写入各类元数据：父实体先于子实体。
func importEntities(ctx context.Context, store metadata.Writer, seed seedFile) (ImportResult, error) {
	var result ImportResult

	for _, item := range seed.Spaces {
		if _, err := store.UpsertSpace(ctx, &pb.Space{
			SpaceId: item.SpaceID, Name: item.Name, Description: item.Description,
			Owner: item.Owner, Status: item.Status,
		}); err != nil {
			return result, seedErr("space", item.SpaceID, err)
		}
		result.Spaces++
	}

	for _, item := range seed.DataSources {
		if _, err := store.UpsertDataSource(ctx, &pb.DataSource{
			SpaceId: item.SpaceID, DataSourceId: item.DataSourceID, Name: item.Name,
			Kind: item.Kind, Market: item.Market, Timezone: item.Timezone,
			ConfigJson: item.ConfigJSON, Status: item.Status,
		}); err != nil {
			return result, seedErr("data_source", item.DataSourceID, err)
		}
		result.DataSources++
	}

	for _, item := range seed.Subjects {
		if _, err := store.UpsertSubject(ctx, &pb.Subject{
			SpaceId: item.SpaceID, SubjectId: item.SubjectID, SubjectType: item.SubjectType,
			Name: item.Name, Market: item.Market, Currency: item.Currency,
			Timezone: item.Timezone, Status: item.Status,
		}); err != nil {
			return result, seedErr("subject", item.SubjectID, err)
		}
		result.Subjects++
	}

	for _, item := range seed.SubjectSymbols {
		if _, err := store.UpsertSubjectSymbol(ctx, &pb.SubjectSymbol{
			SpaceId: item.SpaceID, SubjectId: item.SubjectID, DataSourceId: item.DataSourceID,
			ExternalSymbol: item.ExternalSymbol, Status: item.Status,
		}); err != nil {
			return result, seedErr("subject_symbol", item.SubjectID, err)
		}
		result.SubjectSymbols++
	}

	for _, item := range seed.Datasets {
		if _, err := store.UpsertDataset(ctx, &pb.Dataset{
			SpaceId: item.SpaceID, DatasetId: item.DatasetID, DataSourceId: item.DataSourceID,
			Name: item.Name, Description: item.Description, DataKind: parseDataKind(item.DataKind),
			Freqs: item.Freqs, Status: item.Status,
		}); err != nil {
			return result, seedErr("dataset", item.DatasetID, err)
		}
		result.Datasets++
	}

	for _, item := range seed.DatasetSubjects {
		if _, err := store.BindDatasetSubject(ctx, &pb.DatasetSubject{
			SpaceId: item.SpaceID, DatasetId: item.DatasetID, SubjectId: item.SubjectID,
			SubjectRole: item.SubjectRole, EffectiveStartTime: item.EffectiveStartTime,
			EffectiveEndTime: item.EffectiveEndTime, Status: item.Status,
		}); err != nil {
			return result, seedErr("dataset_subject", item.DatasetID+"/"+item.SubjectID, err)
		}
		result.DatasetSubjects++
	}

	for _, item := range seed.Fields {
		if _, err := store.UpsertField(ctx, &pb.Field{
			SpaceId: item.SpaceID, FieldId: item.FieldID, Name: item.Name, Description: item.Description,
			ValueType: parseValueType(item.ValueType), Unit: item.Unit,
			ValidationRuleJson: item.ValidationRuleJSON, WriteExample: item.WriteExample, Status: item.Status,
		}); err != nil {
			return result, seedErr("field", item.FieldID, err)
		}
		result.Fields++
	}

	for _, item := range seed.Factors {
		if _, err := store.UpsertFactor(ctx, &pb.Factor{
			SpaceId: item.SpaceID, FactorId: item.FactorID, Name: item.Name, Description: item.Description,
			Algorithm: item.Algorithm, ParamsJson: item.ParamsJSON,
			ValueType: parseValueType(item.ValueType), Status: item.Status,
		}); err != nil {
			return result, seedErr("factor", item.FactorID, err)
		}
		result.Factors++
	}

	for _, item := range seed.DatasetColumns {
		if _, err := store.UpsertDatasetColumn(ctx, &pb.DatasetColumn{
			SpaceId: item.SpaceID, DatasetId: item.DatasetID, ColumnName: item.ColumnName,
			OriginType: parseDatasetColumnOriginType(item.OriginType), OriginId: item.OriginID,
			ValueType: parseValueType(item.ValueType), Required: item.Required, IsUnique: item.IsUnique,
			Aliases: item.Aliases, Status: item.Status,
		}); err != nil {
			return result, seedErr("dataset_column", item.DatasetID+"."+item.ColumnName, err)
		}
		result.DatasetColumns++
	}

	for _, item := range seed.Views {
		if _, err := store.UpsertView(ctx, &pb.View{
			SpaceId: item.SpaceID, ViewId: item.ViewID, Name: item.Name, Description: item.Description,
			PrimaryDatasetId: item.PrimaryDatasetID, DatasetIds: item.DatasetIDs, GrainKeys: item.GrainKeys,
			FilterJson: item.FilterJSON, Engine: item.Engine, QueryWindow: item.QueryWindow,
			BuildStatus: item.BuildStatus, Status: item.Status,
		}); err != nil {
			return result, seedErr("view", item.ViewID, err)
		}
		result.Views++
	}

	for _, item := range seed.ViewColumns {
		if _, err := store.UpsertViewColumn(ctx, &pb.ViewColumn{
			SpaceId: item.SpaceID, ViewId: item.ViewID, ColumnName: item.ColumnName,
			OriginType: parseColumnOriginType(item.OriginType), OriginId: item.OriginID,
			ValueType: parseValueType(item.ValueType), OnlineTime: item.OnlineTime, SortOrder: item.SortOrder,
		}); err != nil {
			return result, seedErr("view_column", item.ViewID+"."+item.ColumnName, err)
		}
		result.ViewColumns++
	}

	for _, item := range seed.PrimaryStoreNodes {
		if _, err := store.UpsertPrimaryStoreNode(ctx, &pb.PrimaryStoreNode{
			NodeId: item.NodeID, Name: item.Name, Endpoint: item.Endpoint, Weight: item.Weight,
			Status: item.Status, ConfigJson: item.ConfigJSON,
		}); err != nil {
			return result, seedErr("storage_node", item.NodeID, err)
		}
		result.PrimaryStoreNodes++
	}

	for _, item := range seed.Devices {
		if _, err := store.UpsertDevice(ctx, &pb.Device{
			DeviceId: item.DeviceID, NodeId: item.NodeID, Name: item.Name, Engine: item.Engine,
			Endpoint: item.Endpoint, ConfigJson: item.ConfigJSON, Status: item.Status,
		}); err != nil {
			return result, seedErr("device", item.DeviceID, err)
		}
		result.Devices++
	}

	for _, item := range seed.PrimaryStoreRoutes {
		if _, err := store.UpsertPrimaryStoreRoute(ctx, &pb.PrimaryStoreRoute{
			SpaceId: item.SpaceID, RouteId: item.RouteID, DatasetId: item.DatasetID,
			SubjectId: item.SubjectID, SubjectPattern: item.SubjectPattern, HashRule: item.HashRule,
			NodeId: item.NodeID, Priority: item.Priority, Status: item.Status,
		}); err != nil {
			return result, seedErr("storage_route", item.RouteID, err)
		}
		result.PrimaryStoreRoutes++
	}

	return result, nil
}

func seedErr(kind string, id string, err error) error {
	return fmt.Errorf("import %s %q: %w", kind, id, err)
}

func parseValueType(value string) pb.FieldValueType {
	switch value {
	case "string":
		return pb.FieldValueType_FIELD_VALUE_TYPE_STRING
	case "int":
		return pb.FieldValueType_FIELD_VALUE_TYPE_INT
	case "double":
		return pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE
	case "bool":
		return pb.FieldValueType_FIELD_VALUE_TYPE_BOOL
	case "time":
		return pb.FieldValueType_FIELD_VALUE_TYPE_TIME
	case "json":
		return pb.FieldValueType_FIELD_VALUE_TYPE_JSON
	case "bytes":
		return pb.FieldValueType_FIELD_VALUE_TYPE_BYTES
	default:
		return pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED
	}
}

func parseDataKind(value string) pb.DataKind {
	switch value {
	case "record":
		return pb.DataKind_DATA_KIND_RECORD
	case "time_series":
		return pb.DataKind_DATA_KIND_TIME_SERIES
	case "snapshot":
		return pb.DataKind_DATA_KIND_SNAPSHOT
	case "event":
		return pb.DataKind_DATA_KIND_EVENT
	case "document":
		return pb.DataKind_DATA_KIND_DOCUMENT
	case "table":
		return pb.DataKind_DATA_KIND_TABLE
	default:
		return pb.DataKind_DATA_KIND_UNSPECIFIED
	}
}

func parseDatasetColumnOriginType(value string) pb.DatasetColumnOriginType {
	switch value {
	case "field":
		return pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD
	case "factor":
		return pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FACTOR
	case "system":
		return pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_SYSTEM
	default:
		return pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_UNSPECIFIED
	}
}

func parseColumnOriginType(value string) pb.ColumnOriginType {
	switch value {
	case "dataset_column":
		return pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN
	case "expression":
		return pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_EXPRESSION
	case "system":
		return pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_SYSTEM
	default:
		return pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_UNSPECIFIED
	}
}

// ---- seed 文件结构（领域型，与 config/metadata.seed.yaml 对应）----

// seedFile 对应 metadata.seed.yaml 的顶层配置。
type seedFile struct {
	Spaces             []seedSpace             `yaml:"spaces"`
	DataSources        []seedDataSource        `yaml:"data_sources"`
	Subjects           []seedSubject           `yaml:"subjects"`
	SubjectSymbols     []seedSubjectSymbol     `yaml:"subject_symbols"`
	Datasets           []seedDataset           `yaml:"datasets"`
	DatasetSubjects    []seedDatasetSubject    `yaml:"dataset_subjects"`
	Fields             []seedField             `yaml:"fields"`
	Factors            []seedFactor            `yaml:"factors"`
	DatasetColumns     []seedDatasetColumn     `yaml:"dataset_columns"`
	Views              []seedView              `yaml:"views"`
	ViewColumns        []seedViewColumn        `yaml:"view_columns"`
	PrimaryStoreNodes  []seedPrimaryStoreNode  `yaml:"primary_store_nodes"`
	Devices            []seedDevice            `yaml:"devices"`
	PrimaryStoreRoutes []seedPrimaryStoreRoute `yaml:"primary_store_routes"`
}

// seedSpace 描述待初始化的 Space 元数据。
type seedSpace struct {
	SpaceID     string `yaml:"space_id"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Owner       string `yaml:"owner"`
	Status      string `yaml:"status"`
}

// seedDataSource 描述待初始化的数据源元数据。
type seedDataSource struct {
	SpaceID      string `yaml:"space_id"`
	DataSourceID string `yaml:"data_source_id"`
	Name         string `yaml:"name"`
	Kind         string `yaml:"kind"`
	Market       string `yaml:"market"`
	Timezone     string `yaml:"timezone"`
	ConfigJSON   string `yaml:"config_json"`
	Status       string `yaml:"status"`
}

// seedSubject 描述待初始化的 Subject 元数据。
type seedSubject struct {
	SpaceID     string `yaml:"space_id"`
	SubjectID   string `yaml:"subject_id"`
	SubjectType string `yaml:"subject_type"`
	Name        string `yaml:"name"`
	Market      string `yaml:"market"`
	Currency    string `yaml:"currency"`
	Timezone    string `yaml:"timezone"`
	Status      string `yaml:"status"`
}

// seedSubjectSymbol 描述 Subject 与外部数据源符号的映射。
type seedSubjectSymbol struct {
	SpaceID        string `yaml:"space_id"`
	SubjectID      string `yaml:"subject_id"`
	DataSourceID   string `yaml:"data_source_id"`
	ExternalSymbol string `yaml:"external_symbol"`
	Status         string `yaml:"status"`
}

// seedDataset 描述待初始化的 Dataset 元数据。
type seedDataset struct {
	SpaceID      string   `yaml:"space_id"`
	DatasetID    string   `yaml:"dataset_id"`
	DataSourceID string   `yaml:"data_source_id"`
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	DataKind     string   `yaml:"data_kind"`
	Freqs        []string `yaml:"freqs"`
	Status       string   `yaml:"status"`
}

// seedDatasetSubject 描述 Dataset 与 Subject 的绑定关系。
type seedDatasetSubject struct {
	SpaceID            string `yaml:"space_id"`
	DatasetID          string `yaml:"dataset_id"`
	SubjectID          string `yaml:"subject_id"`
	SubjectRole        string `yaml:"subject_role"`
	EffectiveStartTime string `yaml:"effective_start_time"`
	EffectiveEndTime   string `yaml:"effective_end_time"`
	Status             string `yaml:"status"`
}

// seedField 描述待初始化的字段定义。
type seedField struct {
	SpaceID            string `yaml:"space_id"`
	FieldID            string `yaml:"field_id"`
	Name               string `yaml:"name"`
	Description        string `yaml:"description"`
	ValueType          string `yaml:"value_type"`
	Unit               string `yaml:"unit"`
	ValidationRuleJSON string `yaml:"validation_rule_json"`
	WriteExample       string `yaml:"write_example"`
	Status             string `yaml:"status"`
}

// seedFactor 描述待初始化的因子定义。
type seedFactor struct {
	SpaceID     string `yaml:"space_id"`
	FactorID    string `yaml:"factor_id"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Algorithm   string `yaml:"algorithm"`
	ParamsJSON  string `yaml:"params_json"`
	ValueType   string `yaml:"value_type"`
	Status      string `yaml:"status"`
}

// seedDatasetColumn 描述 Dataset 中可写入的列定义。
type seedDatasetColumn struct {
	SpaceID    string   `yaml:"space_id"`
	DatasetID  string   `yaml:"dataset_id"`
	ColumnName string   `yaml:"column_name"`
	OriginType string   `yaml:"origin_type"`
	OriginID   string   `yaml:"origin_id"`
	ValueType  string   `yaml:"value_type"`
	Required   bool     `yaml:"required"`
	IsUnique   bool     `yaml:"is_unique"`
	Aliases    []string `yaml:"aliases"`
	Status     string   `yaml:"status"`
}

// seedView 描述待初始化的 View 定义。
type seedView struct {
	SpaceID          string   `yaml:"space_id"`
	ViewID           string   `yaml:"view_id"`
	Name             string   `yaml:"name"`
	Description      string   `yaml:"description"`
	PrimaryDatasetID string   `yaml:"primary_dataset_id"`
	DatasetIDs       []string `yaml:"dataset_ids"`
	GrainKeys        []string `yaml:"grain_keys"`
	FilterJSON       string   `yaml:"filter_json"`
	Engine           string   `yaml:"engine"`
	QueryWindow      string   `yaml:"query_window"`
	BuildStatus      string   `yaml:"build_status"`
	Status           string   `yaml:"status"`
}

// seedViewColumn 描述 View 中对外可查询的结果列。
type seedViewColumn struct {
	SpaceID    string `yaml:"space_id"`
	ViewID     string `yaml:"view_id"`
	ColumnName string `yaml:"column_name"`
	OriginType string `yaml:"origin_type"`
	OriginID   string `yaml:"origin_id"`
	ValueType  string `yaml:"value_type"`
	OnlineTime string `yaml:"online_time"`
	SortOrder  uint32 `yaml:"sort_order"`
}

// seedPrimaryStoreNode 描述待初始化的主存节点。
type seedPrimaryStoreNode struct {
	NodeID     string `yaml:"node_id"`
	Name       string `yaml:"name"`
	Endpoint   string `yaml:"endpoint"`
	Weight     uint32 `yaml:"weight"`
	ConfigJSON string `yaml:"config_json"`
	Status     string `yaml:"status"`
}

// seedDevice 描述待初始化的物理存储设备。
type seedDevice struct {
	DeviceID   string `yaml:"device_id"`
	NodeID     string `yaml:"node_id"`
	Name       string `yaml:"name"`
	Engine     string `yaml:"engine"`
	Endpoint   string `yaml:"endpoint"`
	ConfigJSON string `yaml:"config_json"`
	Status     string `yaml:"status"`
}

// seedPrimaryStoreRoute 描述待初始化的主存路由。
type seedPrimaryStoreRoute struct {
	SpaceID        string `yaml:"space_id"`
	RouteID        string `yaml:"route_id"`
	DatasetID      string `yaml:"dataset_id"`
	SubjectID      string `yaml:"subject_id"`
	SubjectPattern string `yaml:"subject_pattern"`
	HashRule       string `yaml:"hash_rule"`
	NodeID         string `yaml:"node_id"`
	Priority       uint32 `yaml:"priority"`
	Status         string `yaml:"status"`
}
