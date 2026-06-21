package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
)

const (
	accessServiceName   = "trpc.storage.access.AccessService"
	metadataServiceName = "trpc.storage.metadata.MetadataService"
)

var (
	metadataImportFile        string
	metadataImportURL         string
	metadataImportDryRun      bool
	metadataImportIfNotExists bool
)

var metadataCmd = &cobra.Command{
	Use:   "metadata",
	Short: "存储元数据管理工具",
}

var metadataImportCmd = &cobra.Command{
	Use:   "import",
	Short: "导入存储元数据 seed",
	Long: `通过 moox-storage MetadataService 导入存储元数据 seed。

示例:
  moox-cli metadata import --file ../storage/config/metadata.seed.yaml --metadata-url http://127.0.0.1:19101
  moox-cli metadata import --file seed.yaml --dry-run
  moox-cli metadata import --file seed.yaml --if-not-exists`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(metadataImportFile) == "" {
			return fmt.Errorf("必须指定 --file")
		}
		seed, err := loadMetadataSeed(metadataImportFile)
		if err != nil {
			return err
		}
		calls, err := buildMetadataImportCalls(seed)
		if err != nil {
			return err
		}
		url := defaultMetadataImportURL(metadataImportURL)
		if metadataImportDryRun {
			return writeMetadataImportSummary(metadataImportSummary{
				Status:      "dry_run",
				DryRun:      true,
				MetadataURL: url,
				Planned:     len(calls),
				Resources:   countMetadataCalls(calls),
			})
		}
		summary, err := runMetadataImport(cmd.Context(), url, calls, metadataImportIfNotExists)
		if err != nil {
			return err
		}
		return writeMetadataImportSummary(summary)
	},
}

// metadataSeed 对应 CLI 元数据导入文件的顶层配置。
type metadataSeed struct {
	Spaces             []seedSpace             `yaml:"spaces"`
	DataSources        []seedDataSource        `yaml:"data_sources"`
	Subjects           []seedSubject           `yaml:"subjects"`
	SubjectSymbols     []seedSubjectSymbol     `yaml:"subject_symbols"`
	Datasets           []seedDataset           `yaml:"datasets"`
	DatasetSubjects    []seedDatasetSubject    `yaml:"dataset_subjects"`
	Fields             []seedField             `yaml:"fields"`
	DatasetColumns     []seedDatasetColumn     `yaml:"dataset_columns"`
	Views              []seedView              `yaml:"views"`
	ViewColumns        []seedViewColumn        `yaml:"view_columns"`
	PrimaryStoreNodes  []seedPrimaryStoreNode  `yaml:"primary_store_nodes"`
	Devices            []seedDevice            `yaml:"devices"`
	PrimaryStoreRoutes []seedPrimaryStoreRoute `yaml:"primary_store_routes"`
}

// seedCommon 保存元数据种子条目的通用字段。
type seedCommon struct {
	Status     string            `yaml:"status"`
	CreatedAt  string            `yaml:"created_at"`
	UpdatedAt  string            `yaml:"updated_at"`
	Attributes map[string]string `yaml:"attributes"`
}

// seedSpace 描述 CLI 可导入的 Space 元数据。
type seedSpace struct {
	SpaceID     string `yaml:"space_id"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Owner       string `yaml:"owner"`
	seedCommon  `yaml:",inline"`
}

// seedDataSource 描述 CLI 可导入的数据源元数据。
type seedDataSource struct {
	SpaceID      string `yaml:"space_id"`
	DataSourceID string `yaml:"data_source_id"`
	Name         string `yaml:"name"`
	Kind         string `yaml:"kind"`
	Market       string `yaml:"market"`
	Timezone     string `yaml:"timezone"`
	ConfigJSON   string `yaml:"config_json"`
	seedCommon   `yaml:",inline"`
}

// seedSubject 描述 CLI 可导入的 Subject 元数据。
type seedSubject struct {
	SpaceID     string `yaml:"space_id"`
	SubjectID   string `yaml:"subject_id"`
	SubjectType string `yaml:"subject_type"`
	Name        string `yaml:"name"`
	Market      string `yaml:"market"`
	Currency    string `yaml:"currency"`
	Timezone    string `yaml:"timezone"`
	seedCommon  `yaml:",inline"`
}

// seedSubjectSymbol 描述 Subject 与外部符号的映射元数据。
type seedSubjectSymbol struct {
	SpaceID        string `yaml:"space_id"`
	SubjectID      string `yaml:"subject_id"`
	DataSourceID   string `yaml:"data_source_id"`
	ExternalSymbol string `yaml:"external_symbol"`
	seedCommon     `yaml:",inline"`
}

// seedDataset 描述 CLI 可导入的 Dataset 元数据。
type seedDataset struct {
	SpaceID      string   `yaml:"space_id"`
	DatasetID    string   `yaml:"dataset_id"`
	DataSourceID string   `yaml:"data_source_id"`
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	DataKind     string   `yaml:"data_kind"`
	Freqs        []string `yaml:"freqs"`
	seedCommon   `yaml:",inline"`
}

// seedDatasetSubject 描述 Dataset 与 Subject 的绑定元数据。
type seedDatasetSubject struct {
	SpaceID            string `yaml:"space_id"`
	DatasetID          string `yaml:"dataset_id"`
	SubjectID          string `yaml:"subject_id"`
	SubjectRole        string `yaml:"subject_role"`
	EffectiveStartTime string `yaml:"effective_start_time"`
	EffectiveEndTime   string `yaml:"effective_end_time"`
	seedCommon         `yaml:",inline"`
}

// seedField 描述 CLI 可导入的字段元数据。
type seedField struct {
	SpaceID            string `yaml:"space_id"`
	FieldID            string `yaml:"field_id"`
	Name               string `yaml:"name"`
	Description        string `yaml:"description"`
	ValueType          string `yaml:"value_type"`
	Unit               string `yaml:"unit"`
	ValidationRuleJSON string `yaml:"validation_rule_json"`
	WriteExample       string `yaml:"write_example"`
	seedCommon         `yaml:",inline"`
}

// seedDatasetColumn 描述 CLI 可导入的 Dataset 列元数据。
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
	seedCommon `yaml:",inline"`
}

// seedView 描述 CLI 可导入的 View 元数据。
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
	ActiveResult     string   `yaml:"active_result"`
	BuildStatus      string   `yaml:"build_status"`
	seedCommon       `yaml:",inline"`
}

// seedViewColumn 描述 CLI 可导入的 View 结果列元数据。
type seedViewColumn struct {
	SpaceID    string `yaml:"space_id"`
	ViewID     string `yaml:"view_id"`
	ColumnName string `yaml:"column_name"`
	OriginType string `yaml:"origin_type"`
	OriginID   string `yaml:"origin_id"`
	ValueType  string `yaml:"value_type"`
	OnlineTime string `yaml:"online_time"`
	SortOrder  uint32 `yaml:"sort_order"`
	seedCommon `yaml:",inline"`
}

// seedPrimaryStoreNode 描述 CLI 可导入的主存节点元数据。
type seedPrimaryStoreNode struct {
	NodeID     string `yaml:"node_id"`
	Name       string `yaml:"name"`
	Endpoint   string `yaml:"endpoint"`
	Weight     uint32 `yaml:"weight"`
	ConfigJSON string `yaml:"config_json"`
	seedCommon `yaml:",inline"`
}

// seedDevice 描述 CLI 可导入的存储设备元数据。
type seedDevice struct {
	DeviceID   string `yaml:"device_id"`
	NodeID     string `yaml:"node_id"`
	Name       string `yaml:"name"`
	Engine     string `yaml:"engine"`
	Endpoint   string `yaml:"endpoint"`
	ConfigJSON string `yaml:"config_json"`
	seedCommon `yaml:",inline"`
}

// seedPrimaryStoreRoute 描述 CLI 可导入的主存路由元数据。
type seedPrimaryStoreRoute struct {
	SpaceID        string `yaml:"space_id"`
	RouteID        string `yaml:"route_id"`
	DatasetID      string `yaml:"dataset_id"`
	SubjectID      string `yaml:"subject_id"`
	SubjectPattern string `yaml:"subject_pattern"`
	HashRule       string `yaml:"hash_rule"`
	NodeID         string `yaml:"node_id"`
	Priority       uint32 `yaml:"priority"`
	seedCommon     `yaml:",inline"`
}

// metadataImportCall 封装一次元数据导入接口调用。
type metadataImportCall struct {
	Resource string
	Method   string
	Request  proto.Message
	Response proto.Message
	Exists   *metadataExistsProbe
}

// metadataExistsProbe 封装一次元数据是否存在的探测调用。
type metadataExistsProbe struct {
	Method   string
	Request  proto.Message
	Response proto.Message
}

// metadataImportSummary 汇总 CLI 元数据导入结果。
type metadataImportSummary struct {
	Status      string         `json:"status"`
	DryRun      bool           `json:"dry_run,omitempty"`
	MetadataURL string         `json:"metadata_url,omitempty"`
	Planned     int            `json:"planned"`
	Applied     int            `json:"applied"`
	Skipped     int            `json:"skipped"`
	Resources   map[string]int `json:"resources"`
}

func loadMetadataSeed(path string) (metadataSeed, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return metadataSeed{}, fmt.Errorf("读取 metadata seed 失败 %s: %w", path, err)
	}
	var seed metadataSeed
	if err := yaml.Unmarshal(raw, &seed); err != nil {
		return metadataSeed{}, fmt.Errorf("解析 metadata seed 失败 %s: %w", path, err)
	}
	return seed, nil
}

func buildMetadataImportCalls(seed metadataSeed) ([]metadataImportCall, error) {
	var calls []metadataImportCall
	for _, item := range seed.Spaces {
		space := item.toPB()
		calls = append(calls, metadataImportCall{
			Resource: "spaces",
			Method:   "CreateSpace",
			Request:  &pb.CreateSpaceReq{Space: space},
			Response: &pb.CreateSpaceRsp{},
			Exists: &metadataExistsProbe{
				Method:   "GetSpace",
				Request:  &pb.GetSpaceReq{SpaceId: space.GetSpaceId()},
				Response: &pb.GetSpaceRsp{},
			},
		})
	}
	for _, item := range seed.DataSources {
		source := item.toPB()
		calls = append(calls, metadataImportCall{
			Resource: "data_sources",
			Method:   "CreateDataSource",
			Request:  &pb.CreateDataSourceReq{DataSource: source},
			Response: &pb.CreateDataSourceRsp{},
			Exists: &metadataExistsProbe{
				Method:   "GetDataSource",
				Request:  &pb.GetDataSourceReq{SpaceId: source.GetSpaceId(), DataSourceId: source.GetDataSourceId()},
				Response: &pb.GetDataSourceRsp{},
			},
		})
	}
	for _, item := range seed.Subjects {
		calls = append(calls, metadataImportCall{Resource: "subjects", Method: "UpsertSubject", Request: &pb.UpsertSubjectReq{Subject: item.toPB()}, Response: &pb.UpsertSubjectRsp{}})
	}
	for _, item := range seed.SubjectSymbols {
		calls = append(calls, metadataImportCall{Resource: "subject_symbols", Method: "UpsertSubjectSymbol", Request: &pb.UpsertSubjectSymbolReq{SubjectSymbol: item.toPB()}, Response: &pb.UpsertSubjectSymbolRsp{}})
	}
	for _, item := range seed.Datasets {
		dataset, err := item.toPB()
		if err != nil {
			return nil, err
		}
		calls = append(calls, metadataImportCall{
			Resource: "datasets",
			Method:   "CreateDataset",
			Request:  &pb.CreateDatasetReq{Dataset: dataset},
			Response: &pb.CreateDatasetRsp{},
			Exists: &metadataExistsProbe{
				Method:   "GetDataset",
				Request:  &pb.GetDatasetReq{SpaceId: dataset.GetSpaceId(), DatasetId: dataset.GetDatasetId()},
				Response: &pb.GetDatasetRsp{},
			},
		})
	}
	for _, item := range seed.DatasetSubjects {
		calls = append(calls, metadataImportCall{Resource: "dataset_subjects", Method: "BindDatasetSubject", Request: &pb.BindDatasetSubjectReq{DatasetSubject: item.toPB()}, Response: &pb.BindDatasetSubjectRsp{}})
	}
	for _, item := range seed.Fields {
		field, err := item.toPB()
		if err != nil {
			return nil, err
		}
		calls = append(calls, metadataImportCall{
			Resource: "fields",
			Method:   "CreateField",
			Request:  &pb.CreateFieldReq{Field: field},
			Response: &pb.CreateFieldRsp{},
			Exists: &metadataExistsProbe{
				Method:   "GetField",
				Request:  &pb.GetFieldReq{SpaceId: field.GetSpaceId(), FieldId: field.GetFieldId()},
				Response: &pb.GetFieldRsp{},
			},
		})
	}
	for _, item := range seed.DatasetColumns {
		column, err := item.toPB()
		if err != nil {
			return nil, err
		}
		calls = append(calls, metadataImportCall{Resource: "dataset_columns", Method: "UpsertDatasetColumn", Request: &pb.UpsertDatasetColumnReq{Column: column}, Response: &pb.UpsertDatasetColumnRsp{}})
	}
	for _, item := range seed.PrimaryStoreNodes {
		node := item.toPB()
		calls = append(calls, metadataImportCall{
			Resource: "primary_store_nodes",
			Method:   "CreatePrimaryStoreNode",
			Request:  &pb.CreatePrimaryStoreNodeReq{Node: node},
			Response: &pb.CreatePrimaryStoreNodeRsp{},
			Exists: &metadataExistsProbe{
				Method:   "GetPrimaryStoreNode",
				Request:  &pb.GetPrimaryStoreNodeReq{NodeId: node.GetNodeId()},
				Response: &pb.GetPrimaryStoreNodeRsp{},
			},
		})
	}
	for _, item := range seed.Devices {
		device := item.toPB()
		calls = append(calls, metadataImportCall{
			Resource: "devices",
			Method:   "CreateDevice",
			Request:  &pb.CreateDeviceReq{Device: device},
			Response: &pb.CreateDeviceRsp{},
			Exists: &metadataExistsProbe{
				Method:   "GetDevice",
				Request:  &pb.GetDeviceReq{DeviceId: device.GetDeviceId()},
				Response: &pb.GetDeviceRsp{},
			},
		})
	}
	for _, item := range seed.PrimaryStoreRoutes {
		route := item.toPB()
		calls = append(calls, metadataImportCall{
			Resource: "primary_store_routes",
			Method:   "CreatePrimaryStoreRoute",
			Request:  &pb.CreatePrimaryStoreRouteReq{PrimaryStoreRoute: route},
			Response: &pb.CreatePrimaryStoreRouteRsp{},
			Exists: &metadataExistsProbe{
				Method:   "GetPrimaryStoreRoute",
				Request:  &pb.GetPrimaryStoreRouteReq{SpaceId: route.GetSpaceId(), RouteId: route.GetRouteId()},
				Response: &pb.GetPrimaryStoreRouteRsp{},
			},
		})
	}
	for _, item := range seed.Views {
		view := item.toPB()
		calls = append(calls, metadataImportCall{
			Resource: "views",
			Method:   "CreateView",
			Request:  &pb.CreateViewReq{View: view},
			Response: &pb.CreateViewRsp{},
			Exists: &metadataExistsProbe{
				Method:   "GetView",
				Request:  &pb.GetViewReq{SpaceId: view.GetSpaceId(), ViewId: view.GetViewId()},
				Response: &pb.GetViewRsp{},
			},
		})
	}
	for _, item := range seed.ViewColumns {
		column, err := item.toPB()
		if err != nil {
			return nil, err
		}
		calls = append(calls, metadataImportCall{Resource: "view_columns", Method: "UpsertViewColumn", Request: &pb.UpsertViewColumnReq{Column: column}, Response: &pb.UpsertViewColumnRsp{}})
	}
	return calls, nil
}

func runMetadataImport(ctx context.Context, metadataURL string, calls []metadataImportCall, ifNotExists bool) (metadataImportSummary, error) {
	summary := metadataImportSummary{
		Status:      "ok",
		MetadataURL: metadataURL,
		Planned:     len(calls),
		Resources:   countMetadataCalls(calls),
	}
	for _, call := range calls {
		if ifNotExists && call.Exists != nil {
			exists, err := metadataResourceExists(ctx, metadataURL, call.Exists)
			if err != nil {
				return summary, err
			}
			if exists {
				summary.Skipped++
				continue
			}
		}
		if err := postStorage(ctx, metadataURL, metadataServiceName, call.Method, call.Request, call.Response); err != nil {
			return summary, err
		}
		summary.Applied++
	}
	return summary, nil
}

func metadataResourceExists(ctx context.Context, metadataURL string, probe *metadataExistsProbe) (bool, error) {
	if err := postStorageRaw(ctx, metadataURL, metadataServiceName, probe.Method, probe.Request, probe.Response); err != nil {
		return false, err
	}
	retInfo, ok := responseRetInfo(probe.Response)
	if !ok || retInfo == nil {
		return false, fmt.Errorf("%s/%s failed: missing ret_info", metadataServiceName, probe.Method)
	}
	if retInfo.GetCode() == pb.ErrorCode_SUCCESS {
		return true, nil
	}
	if metadataNotFound(retInfo) {
		return false, nil
	}
	return false, fmt.Errorf("%s/%s failed: %s", metadataServiceName, probe.Method, retInfo.GetMsg())
}

func countMetadataCalls(calls []metadataImportCall) map[string]int {
	counts := make(map[string]int)
	for _, call := range calls {
		counts[call.Resource]++
	}
	return counts
}

func writeMetadataImportSummary(summary metadataImportSummary) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(summary)
}

func defaultMetadataImportURL(flagValue string) string {
	value := strings.TrimSpace(flagValue)
	if value == "" {
		value = strings.TrimSpace(os.Getenv("MOOX_METADATA_URL"))
	}
	if value == "" {
		value = "http://127.0.0.1:19101"
	}
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	return value
}

func metadataNotFound(retInfo *pb.RetInfo) bool {
	switch retInfo.GetCode() {
	case pb.ErrorCode_SPACE_NOT_FOUND,
		pb.ErrorCode_DATASET_NOT_FOUND,
		pb.ErrorCode_SUBJECT_NOT_FOUND,
		pb.ErrorCode_FIELD_NOT_FOUND,
		pb.ErrorCode_VIEW_NOT_FOUND,
		pb.ErrorCode_VIEW_COLUMN_NOT_FOUND,
		pb.ErrorCode_ROUTE_NOT_FOUND:
		return true
	default:
		msg := strings.ToLower(retInfo.GetMsg())
		return retInfo.GetCode() == pb.ErrorCode_INVALID_PARAM &&
			(strings.Contains(msg, "not found") || strings.Contains(msg, "不存在"))
	}
}

func (s seedCommon) status() string {
	if strings.TrimSpace(s.Status) == "" {
		return "active"
	}
	return s.Status
}

func (s seedSpace) toPB() *pb.Space {
	return &pb.Space{SpaceId: s.SpaceID, Name: s.Name, Description: s.Description, Owner: s.Owner, Status: s.status(), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Attributes: s.Attributes}
}

func (s seedDataSource) toPB() *pb.DataSource {
	return &pb.DataSource{SpaceId: s.SpaceID, DataSourceId: s.DataSourceID, Name: s.Name, Kind: s.Kind, Market: s.Market, Timezone: s.Timezone, ConfigJson: s.ConfigJSON, Status: s.status(), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Attributes: s.Attributes}
}

func (s seedSubject) toPB() *pb.Subject {
	return &pb.Subject{SpaceId: s.SpaceID, SubjectId: s.SubjectID, SubjectType: s.SubjectType, Name: s.Name, Market: s.Market, Currency: s.Currency, Timezone: s.Timezone, Status: s.status(), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Attributes: s.Attributes}
}

func (s seedSubjectSymbol) toPB() *pb.SubjectSymbol {
	return &pb.SubjectSymbol{SpaceId: s.SpaceID, SubjectId: s.SubjectID, DataSourceId: s.DataSourceID, ExternalSymbol: s.ExternalSymbol, Status: s.status(), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Attributes: s.Attributes}
}

func (s seedDataset) toPB() (*pb.Dataset, error) {
	dataKind, err := parseDataKind(s.DataKind)
	if err != nil {
		return nil, err
	}
	return &pb.Dataset{SpaceId: s.SpaceID, DatasetId: s.DatasetID, DataSourceId: s.DataSourceID, Name: s.Name, Description: s.Description, DataKind: dataKind, Freqs: s.Freqs, Status: s.status(), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Attributes: s.Attributes}, nil
}

func (s seedDatasetSubject) toPB() *pb.DatasetSubject {
	return &pb.DatasetSubject{SpaceId: s.SpaceID, DatasetId: s.DatasetID, SubjectId: s.SubjectID, SubjectRole: s.SubjectRole, EffectiveStartTime: s.EffectiveStartTime, EffectiveEndTime: s.EffectiveEndTime, Status: s.status(), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Attributes: s.Attributes}
}

func (s seedField) toPB() (*pb.Field, error) {
	valueType, err := parseFieldValueType(s.ValueType)
	if err != nil {
		return nil, err
	}
	return &pb.Field{SpaceId: s.SpaceID, FieldId: s.FieldID, Name: s.Name, Description: s.Description, ValueType: valueType, Unit: s.Unit, ValidationRuleJson: s.ValidationRuleJSON, WriteExample: s.WriteExample, Status: s.status(), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Attributes: s.Attributes}, nil
}

func (s seedDatasetColumn) toPB() (*pb.DatasetColumn, error) {
	originType, valueType, err := parseDatasetColumnAndValueTypes(s.OriginType, s.ValueType)
	if err != nil {
		return nil, err
	}
	return &pb.DatasetColumn{SpaceId: s.SpaceID, DatasetId: s.DatasetID, ColumnName: s.ColumnName, OriginType: originType, OriginId: s.OriginID, ValueType: valueType, Required: s.Required, IsUnique: s.IsUnique, Aliases: s.Aliases, Status: s.status(), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Attributes: s.Attributes}, nil
}

func (s seedView) toPB() *pb.View {
	return &pb.View{SpaceId: s.SpaceID, ViewId: s.ViewID, Name: s.Name, Description: s.Description, PrimaryDatasetId: s.PrimaryDatasetID, DatasetIds: s.DatasetIDs, GrainKeys: s.GrainKeys, FilterJson: s.FilterJSON, Engine: s.Engine, QueryWindow: s.QueryWindow, ActiveResult: s.ActiveResult, BuildStatus: s.BuildStatus, Status: s.status(), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Attributes: s.Attributes}
}

func (s seedViewColumn) toPB() (*pb.ViewColumn, error) {
	originType, valueType, err := parseColumnAndValueTypes(s.OriginType, s.ValueType)
	if err != nil {
		return nil, err
	}
	return &pb.ViewColumn{SpaceId: s.SpaceID, ViewId: s.ViewID, ColumnName: s.ColumnName, OriginType: originType, OriginId: s.OriginID, ValueType: valueType, OnlineTime: s.OnlineTime, SortOrder: s.SortOrder, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Attributes: s.Attributes}, nil
}

func (s seedPrimaryStoreNode) toPB() *pb.PrimaryStoreNode {
	return &pb.PrimaryStoreNode{NodeId: s.NodeID, Name: s.Name, Endpoint: s.Endpoint, Weight: s.Weight, ConfigJson: s.ConfigJSON, Status: s.status(), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Attributes: s.Attributes}
}

func (s seedDevice) toPB() *pb.Device {
	return &pb.Device{DeviceId: s.DeviceID, NodeId: s.NodeID, Name: s.Name, Engine: s.Engine, Endpoint: s.Endpoint, ConfigJson: s.ConfigJSON, Status: s.status(), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Attributes: s.Attributes}
}

func (s seedPrimaryStoreRoute) toPB() *pb.PrimaryStoreRoute {
	return &pb.PrimaryStoreRoute{SpaceId: s.SpaceID, RouteId: s.RouteID, DatasetId: s.DatasetID, SubjectId: s.SubjectID, SubjectPattern: s.SubjectPattern, HashRule: s.HashRule, NodeId: s.NodeID, Priority: s.Priority, Status: s.status(), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Attributes: s.Attributes}
}

func parseDataKind(value string) (pb.DataKind, error) {
	switch normalizeEnum(value) {
	case "", "UNSPECIFIED":
		return pb.DataKind_DATA_KIND_UNSPECIFIED, nil
	case "RECORD":
		return pb.DataKind_DATA_KIND_RECORD, nil
	case "TIME_SERIES":
		return pb.DataKind_DATA_KIND_TIME_SERIES, nil
	case "SNAPSHOT":
		return pb.DataKind_DATA_KIND_SNAPSHOT, nil
	case "EVENT":
		return pb.DataKind_DATA_KIND_EVENT, nil
	case "DOCUMENT":
		return pb.DataKind_DATA_KIND_DOCUMENT, nil
	case "TABLE":
		return pb.DataKind_DATA_KIND_TABLE, nil
	default:
		return pb.DataKind_DATA_KIND_UNSPECIFIED, fmt.Errorf("unsupported data_kind %q", value)
	}
}

func parseFieldValueType(value string) (pb.FieldValueType, error) {
	switch normalizeEnum(value) {
	case "", "UNSPECIFIED":
		return pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED, nil
	case "STRING":
		return pb.FieldValueType_FIELD_VALUE_TYPE_STRING, nil
	case "INT":
		return pb.FieldValueType_FIELD_VALUE_TYPE_INT, nil
	case "DOUBLE":
		return pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, nil
	case "BOOL":
		return pb.FieldValueType_FIELD_VALUE_TYPE_BOOL, nil
	case "TIME":
		return pb.FieldValueType_FIELD_VALUE_TYPE_TIME, nil
	case "JSON":
		return pb.FieldValueType_FIELD_VALUE_TYPE_JSON, nil
	case "BYTES":
		return pb.FieldValueType_FIELD_VALUE_TYPE_BYTES, nil
	default:
		return pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED, fmt.Errorf("unsupported value_type %q", value)
	}
}

func parseDatasetColumnOriginType(value string) (pb.DatasetColumnOriginType, error) {
	switch normalizeEnum(value) {
	case "", "UNSPECIFIED":
		return pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_UNSPECIFIED, nil
	case "FIELD":
		return pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, nil
	case "FACTOR":
		return pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FACTOR, nil
	case "SYSTEM":
		return pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_SYSTEM, nil
	default:
		return pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_UNSPECIFIED, fmt.Errorf("unsupported dataset column origin_type %q", value)
	}
}

func parseColumnOriginType(value string) (pb.ColumnOriginType, error) {
	switch normalizeEnum(value) {
	case "", "UNSPECIFIED":
		return pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_UNSPECIFIED, nil
	case "DATASET_COLUMN":
		return pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, nil
	case "EXPRESSION":
		return pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_EXPRESSION, nil
	case "SYSTEM":
		return pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_SYSTEM, nil
	default:
		return pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_UNSPECIFIED, fmt.Errorf("unsupported column origin_type %q", value)
	}
}

func parseDatasetColumnAndValueTypes(origin string, value string) (pb.DatasetColumnOriginType, pb.FieldValueType, error) {
	originType, err := parseDatasetColumnOriginType(origin)
	if err != nil {
		return originType, pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED, err
	}
	valueType, err := parseFieldValueType(value)
	if err != nil {
		return originType, valueType, err
	}
	return originType, valueType, nil
}

func parseColumnAndValueTypes(origin string, value string) (pb.ColumnOriginType, pb.FieldValueType, error) {
	originType, err := parseColumnOriginType(origin)
	if err != nil {
		return originType, pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED, err
	}
	valueType, err := parseFieldValueType(value)
	if err != nil {
		return originType, valueType, err
	}
	return originType, valueType, nil
}

func normalizeEnum(value string) string {
	value = strings.TrimSpace(strings.ToUpper(value))
	value = strings.TrimPrefix(value, "DATA_KIND_")
	value = strings.TrimPrefix(value, "FIELD_VALUE_TYPE_")
	value = strings.TrimPrefix(value, "DATASET_COLUMN_ORIGIN_TYPE_")
	value = strings.TrimPrefix(value, "COLUMN_ORIGIN_TYPE_")
	value = strings.ReplaceAll(value, "-", "_")
	return value
}

func init() {
	rootCmd.AddCommand(metadataCmd)
	metadataCmd.AddCommand(metadataImportCmd)

	metadataImportCmd.Flags().StringVarP(&metadataImportFile, "file", "f", "", "metadata seed YAML 文件路径")
	metadataImportCmd.Flags().StringVar(&metadataImportURL, "metadata-url", "", "moox-storage MetadataService HTTP 地址，例如 http://127.0.0.1:19101")
	metadataImportCmd.Flags().BoolVar(&metadataImportDryRun, "dry-run", false, "只解析并输出导入计划，不发送 RPC")
	metadataImportCmd.Flags().BoolVar(&metadataImportIfNotExists, "if-not-exists", false, "资源已存在时跳过 create 类调用")
}
