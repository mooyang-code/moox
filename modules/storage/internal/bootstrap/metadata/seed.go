package metadata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/sqlite"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
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
	Spaces          int
	DataSources     int
	Subjects        int
	SubjectSymbols  int
	Datasets        int
	DatasetSubjects int
	FieldGroups     int
	Fields          int
	Factors         int
	DatasetColumns  int
	Views           int
	ViewColumns     int
	Devices         int
}

// ImportSeed 读取显式指定的 metadata seed 文件，并按依赖顺序通过元数据控制面写入。
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
func importEntities(ctx context.Context, store metadata.Store, seed seedFile) (ImportResult, error) {
	var result ImportResult
	var err error
	if err := validateSeedDatasets(ctx, store, seed.Datasets); err != nil {
		return result, err
	}
	seed, err = normalizeSeedFieldGroups(seed)
	if err != nil {
		return result, err
	}

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
			Freqs: item.Freqs, Status: "disabled", Attributes: item.Attributes,
			DataNodeId: item.DataNodeID, KeepDuration: item.KeepDuration,
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

	for _, item := range seed.FieldGroups {
		if _, err := store.UpsertFieldGroup(ctx, &pb.FieldGroup{
			SpaceId: item.SpaceID, GroupId: item.GroupID, Name: item.Name, Description: item.Description,
			ParentGroupId: item.ParentGroupID, SortOrder: item.SortOrder, Status: item.Status,
		}); err != nil {
			return result, seedErr("field_group", item.GroupID, err)
		}
		result.FieldGroups++
	}

	for _, item := range seed.Fields {
		if _, err := store.UpsertField(ctx, &pb.Field{
			SpaceId: item.SpaceID, GroupId: item.GroupID, FieldId: item.FieldID, Name: item.Name, Description: item.Description,
			ValueType: parseValueType(item.ValueType), Unit: item.Unit,
			ValidationRuleJson: item.ValidationRuleJSON, WriteExample: item.WriteExample, SortOrder: item.SortOrder, Status: item.Status,
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
			ValueType: parseValueType(item.ValueType), Required: item.Required,
			Aliases: item.Aliases, Attributes: item.Attributes, Status: item.Status,
		}); err != nil {
			return result, seedErr("dataset_column", item.DatasetID+"."+item.ColumnName, err)
		}
		result.DatasetColumns++
	}

	for _, item := range seed.Views {
		if _, err := store.UpsertView(ctx, &pb.View{
			SpaceId: item.SpaceID, ViewId: item.ViewID, Name: item.Name, Description: item.Description,
			PrimaryDatasetId: item.PrimaryDatasetID, DatasetIds: item.DatasetIDs, GrainKeys: item.GrainKeys,
			FilterJson: item.FilterJSON, Engine: item.Engine, KeepDuration: item.KeepDuration,
			Status: item.Status,
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
			Attributes: seedViewColumnAttributes(item),
		}); err != nil {
			return result, seedErr("view_column", item.ViewID+"."+item.ColumnName, err)
		}
		result.ViewColumns++
	}

	for _, item := range seed.Devices {
		if _, err := store.UpsertDevice(ctx, &pb.Device{
			DeviceId: item.DeviceID, Name: item.Name, Engine: item.Engine,
			Endpoint: item.Endpoint, ConfigJson: item.ConfigJSON, Status: item.Status, Attributes: item.Attributes,
		}); err != nil {
			return result, seedErr("device", item.DeviceID, err)
		}
		result.Devices++
	}

	return result, nil
}

func seedViewColumnAttributes(item seedViewColumn) map[string]string {
	attributes := make(map[string]string, len(item.Attributes)+1)
	for key, value := range item.Attributes {
		attributes[key] = value
	}
	if item.SpaceID == "moox_system" && strings.TrimSpace(attributes["display_name"]) == "" {
		attributes["display_name"] = item.ColumnName
	}
	return attributes
}

// validateSeedDatasets performs all Dataset checks before the first write.
// DataNodes are deployment-owned, so a seed import may reference an already
// registered active node but must never create one implicitly.
func validateSeedDatasets(ctx context.Context, store metadata.Reader, datasets []seedDataset) error {
	for _, item := range datasets {
		dataNodeID := strings.TrimSpace(item.DataNodeID)
		if dataNodeID == "" {
			return fmt.Errorf("dataset %q data_node_id is required", item.DatasetID)
		}
		if strings.TrimSpace(item.KeepDuration) == "" {
			return fmt.Errorf("dataset %q keep_duration is required", item.DatasetID)
		}
		node, err := store.GetDataNode(ctx, dataNodeID)
		if err != nil {
			return seedErr("data_node", dataNodeID, err)
		}
		if node.GetStatus() != "active" {
			return fmt.Errorf("data node %q must be active before metadata seed import", dataNodeID)
		}
	}
	return nil
}

func normalizeSeedFieldGroups(seed seedFile) (seedFile, error) {
	existing := make(map[string]seedFieldGroup, len(seed.FieldGroups))
	for _, group := range seed.FieldGroups {
		key := group.SpaceID + "\x00" + group.GroupID
		if group.SpaceID == "" || group.GroupID == "" || group.Name == "" {
			return seedFile{}, errors.New("field_group space_id, group_id and name are required")
		}
		if _, duplicate := existing[key]; duplicate {
			return seedFile{}, fmt.Errorf("duplicate field_group %s/%s", group.SpaceID, group.GroupID)
		}
		existing[key] = group
	}
	for _, group := range seed.FieldGroups {
		if group.ParentGroupID == "" {
			continue
		}
		parent, ok := existing[group.SpaceID+"\x00"+group.ParentGroupID]
		if !ok {
			return seedFile{}, fmt.Errorf("field_group %s/%s references undefined parent %q", group.SpaceID, group.GroupID, group.ParentGroupID)
		}
		if parent.ParentGroupID != "" {
			return seedFile{}, fmt.Errorf("field_group %s/%s exceeds the two-level hierarchy", group.SpaceID, group.GroupID)
		}
	}
	for i := range seed.Fields {
		if seed.Fields[i].GroupID == "" {
			seed.Fields[i].GroupID = "general"
			key := seed.Fields[i].SpaceID + "\x00general"
			if _, ok := existing[key]; !ok {
				seed.FieldGroups = append(seed.FieldGroups, seedFieldGroup{SpaceID: seed.Fields[i].SpaceID, GroupID: "general", Name: "通用字段"})
				existing[key] = seed.FieldGroups[len(seed.FieldGroups)-1]
			}
			continue
		}
		key := seed.Fields[i].SpaceID + "\x00" + seed.Fields[i].GroupID
		if _, ok := existing[key]; !ok {
			return seedFile{}, fmt.Errorf("field %s/%s references undefined field_group %q", seed.Fields[i].SpaceID, seed.Fields[i].FieldID, seed.Fields[i].GroupID)
		}
	}
	return seed, nil
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

// ---- seed 文件结构（领域型，与显式传入的 metadata YAML 对应）----

// seedFile 对应 metadata YAML 的顶层配置。
type seedFile struct {
	Spaces          []seedSpace          `yaml:"spaces"`
	DataSources     []seedDataSource     `yaml:"data_sources"`
	Subjects        []seedSubject        `yaml:"subjects"`
	SubjectSymbols  []seedSubjectSymbol  `yaml:"subject_symbols"`
	Datasets        []seedDataset        `yaml:"datasets"`
	DatasetSubjects []seedDatasetSubject `yaml:"dataset_subjects"`
	FieldGroups     []seedFieldGroup     `yaml:"field_groups"`
	Fields          []seedField          `yaml:"fields"`
	Factors         []seedFactor         `yaml:"factors"`
	DatasetColumns  []seedDatasetColumn  `yaml:"dataset_columns"`
	Views           []seedView           `yaml:"views"`
	ViewColumns     []seedViewColumn     `yaml:"view_columns"`
	Devices         []seedDevice         `yaml:"devices"`
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
	SpaceID      string            `yaml:"space_id"`
	DatasetID    string            `yaml:"dataset_id"`
	DataSourceID string            `yaml:"data_source_id"`
	DataNodeID   string            `yaml:"data_node_id"`
	Name         string            `yaml:"name"`
	Description  string            `yaml:"description"`
	DataKind     string            `yaml:"data_kind"`
	Freqs        []string          `yaml:"freqs"`
	KeepDuration string            `yaml:"keep_duration"`
	Status       string            `yaml:"status"`
	Attributes   map[string]string `yaml:"attributes"`
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
	GroupID            string `yaml:"group_id"`
	FieldID            string `yaml:"field_id"`
	Name               string `yaml:"name"`
	Description        string `yaml:"description"`
	ValueType          string `yaml:"value_type"`
	Unit               string `yaml:"unit"`
	ValidationRuleJSON string `yaml:"validation_rule_json"`
	WriteExample       string `yaml:"write_example"`
	SortOrder          uint32 `yaml:"sort_order"`
	Status             string `yaml:"status"`
}

type seedFieldGroup struct {
	SpaceID       string `yaml:"space_id"`
	GroupID       string `yaml:"group_id"`
	Name          string `yaml:"name"`
	Description   string `yaml:"description"`
	ParentGroupID string `yaml:"parent_group_id"`
	SortOrder     uint32 `yaml:"sort_order"`
	Status        string `yaml:"status"`
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
	SpaceID    string            `yaml:"space_id"`
	DatasetID  string            `yaml:"dataset_id"`
	ColumnName string            `yaml:"column_name"`
	OriginType string            `yaml:"origin_type"`
	OriginID   string            `yaml:"origin_id"`
	ValueType  string            `yaml:"value_type"`
	Required   bool              `yaml:"required"`
	Aliases    []string          `yaml:"aliases"`
	Attributes map[string]string `yaml:"attributes"`
	Status     string            `yaml:"status"`
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
	KeepDuration     string   `yaml:"keep_duration"`
	Status           string   `yaml:"status"`
}

// seedViewColumn 描述 View 中对外可查询的结果列。
type seedViewColumn struct {
	SpaceID    string            `yaml:"space_id"`
	ViewID     string            `yaml:"view_id"`
	ColumnName string            `yaml:"column_name"`
	OriginType string            `yaml:"origin_type"`
	OriginID   string            `yaml:"origin_id"`
	ValueType  string            `yaml:"value_type"`
	OnlineTime string            `yaml:"online_time"`
	SortOrder  uint32            `yaml:"sort_order"`
	Attributes map[string]string `yaml:"attributes"`
}

// seedDevice 描述待初始化的物理存储设备。
type seedDevice struct {
	DeviceID   string            `yaml:"device_id"`
	Name       string            `yaml:"name"`
	Engine     string            `yaml:"engine"`
	Endpoint   string            `yaml:"endpoint"`
	ConfigJSON string            `yaml:"config_json"`
	Status     string            `yaml:"status"`
	Attributes map[string]string `yaml:"attributes"`
}
