package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
)

const (
	accessServiceName   = "trpc.moox.storage.Access"
	metadataServiceName = "trpc.moox.storage.Metadata"
)

var (
	metadataImportFile        string
	metadataImportURL         string
	metadataImportDryRun      bool
	metadataImportIfNotExists bool
	metadataApplyFile         string
	metadataApplyURL          string
	metadataApplyDryRun       bool
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
  moox-cli metadata import --file ../../examples/platform-local.seed.yaml --metadata-url http://127.0.0.1:20200 --if-not-exists
  moox-cli metadata import --file ../../examples/metadata-crypto.seed.yaml --metadata-url http://127.0.0.1:20200 --if-not-exists
  moox-cli metadata import --file ../../examples/metadata-crypto.seed.yaml --dry-run`,
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

var metadataApplyCmd = &cobra.Command{
	Use:   "apply",
	Short: "创建并校验存储元数据 seed",
	RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(metadataApplyFile) == "" {
			return fmt.Errorf("必须指定 --file")
		}
		seed, err := loadMetadataSeed(metadataApplyFile)
		if err != nil {
			return err
		}
		if err := validateReservedInternalSpaces(seed); err != nil {
			return err
		}
		calls, err := buildMetadataImportCalls(seed)
		if err != nil {
			return err
		}
		url := defaultMetadataImportURL(metadataApplyURL)
		if metadataApplyDryRun {
			return writeMetadataImportSummary(metadataImportSummary{Status: "dry_run", DryRun: true, MetadataURL: url, Planned: len(calls), Resources: countMetadataCalls(calls)})
		}
		summary, err := runMetadataApply(cmd.Context(), url, calls)
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
	Factors            []seedFactor            `yaml:"factors"`
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

// seedFactor 描述 CLI 可导入的因子元数据。
type seedFactor struct {
	SpaceID     string `yaml:"space_id"`
	FactorID    string `yaml:"factor_id"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Algorithm   string `yaml:"algorithm"`
	ParamsJSON  string `yaml:"params_json"`
	ValueType   string `yaml:"value_type"`
	seedCommon  `yaml:",inline"`
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
	RetentionWindow  string   `yaml:"retention_window"`
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

func validateReservedInternalSpaces(seed metadataSeed) error {
	for _, item := range seed.Spaces {
		if !strings.HasPrefix(item.SpaceID, "moox_") {
			continue
		}
		if item.Attributes["scope"] != "internal" || item.Attributes["owner_module"] == "" || item.Attributes["managed_by"] == "" {
			return fmt.Errorf("reserved internal space %q requires attributes scope=internal, owner_module, and managed_by", item.SpaceID)
		}
	}
	check := func(resource, spaceID string) error {
		spaceID = strings.TrimSpace(spaceID)
		if strings.HasPrefix(spaceID, "moox_") && !hasInternalSpace(seed, spaceID) {
			return fmt.Errorf("seed %s cannot claim reserved space %q", resource, spaceID)
		}
		return nil
	}
	for _, item := range seed.DataSources {
		if err := check("data_sources", item.SpaceID); err != nil {
			return err
		}
	}
	for _, item := range seed.Subjects {
		if err := check("subjects", item.SpaceID); err != nil {
			return err
		}
	}
	for _, item := range seed.SubjectSymbols {
		if err := check("subject_symbols", item.SpaceID); err != nil {
			return err
		}
	}
	for _, item := range seed.Datasets {
		if err := check("datasets", item.SpaceID); err != nil {
			return err
		}
	}
	for _, item := range seed.DatasetSubjects {
		if err := check("dataset_subjects", item.SpaceID); err != nil {
			return err
		}
	}
	for _, item := range seed.Fields {
		if err := check("fields", item.SpaceID); err != nil {
			return err
		}
	}
	for _, item := range seed.Factors {
		if err := check("factors", item.SpaceID); err != nil {
			return err
		}
	}
	for _, item := range seed.DatasetColumns {
		if err := check("dataset_columns", item.SpaceID); err != nil {
			return err
		}
	}
	for _, item := range seed.Views {
		if err := check("views", item.SpaceID); err != nil {
			return err
		}
	}
	for _, item := range seed.ViewColumns {
		if err := check("view_columns", item.SpaceID); err != nil {
			return err
		}
	}
	for _, item := range seed.PrimaryStoreRoutes {
		if err := check("primary_store_routes", item.SpaceID); err != nil {
			return err
		}
	}
	return nil
}
func hasInternalSpace(seed metadataSeed, id string) bool {
	for _, s := range seed.Spaces {
		if s.SpaceID == id && s.Attributes["scope"] == "internal" {
			return true
		}
	}
	return false
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
	Unchanged   int            `json:"unchanged,omitempty"`
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
	for _, item := range seed.Factors {
		factor, err := item.toPB()
		if err != nil {
			return nil, err
		}
		calls = append(calls, metadataImportCall{
			Resource: "factors",
			Method:   "CreateFactor",
			Request:  &pb.CreateFactorReq{Factor: factor},
			Response: &pb.CreateFactorRsp{},
			Exists: &metadataExistsProbe{
				Method:   "GetFactor",
				Request:  &pb.GetFactorReq{SpaceId: factor.GetSpaceId(), FactorId: factor.GetFactorId()},
				Response: &pb.GetFactorRsp{},
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

func runMetadataApply(ctx context.Context, metadataURL string, calls []metadataImportCall) (metadataImportSummary, error) {
	summary := metadataImportSummary{Status: "ok", MetadataURL: metadataURL, Planned: len(calls), Resources: countMetadataCalls(calls)}
	for _, call := range calls {
		probe := call.Exists
		if call.Resource == "dataset_columns" {
			column, ok := call.Request.(*pb.UpsertDatasetColumnReq)
			if !ok || column.GetColumn() == nil {
				return summary, fmt.Errorf("invalid dataset column apply call")
			}
			probe = &metadataExistsProbe{Method: "ListDatasetColumns", Request: &pb.ListDatasetColumnsReq{SpaceId: column.GetColumn().GetSpaceId(), DatasetId: column.GetColumn().GetDatasetId(), Page: &commonpb.Page{Page: 1, Size: 500}}, Response: &pb.ListDatasetColumnsRsp{}}
		}
		if probe == nil {
			return summary, fmt.Errorf("apply does not support resource %s without read probe", call.Resource)
		}
		if err := postStorageRaw(ctx, metadataURL, metadataServiceName, probe.Method, probe.Request, probe.Response); err != nil {
			return summary, err
		}
		if ret, ok := responseRetInfo(probe.Response); !ok || ret == nil {
			return summary, fmt.Errorf("%s/%s failed: missing ret_info", metadataServiceName, probe.Method)
		} else if ret.GetCode() != pb.ErrorCode_SUCCESS && !metadataNotFound(ret) {
			return summary, fmt.Errorf("%s/%s failed: %s", metadataServiceName, probe.Method, ret.GetMsg())
		}
		found, actual := applyProbeResult(call.Resource, probe, call.Request)
		if !found {
			if err := postStorage(ctx, metadataURL, metadataServiceName, call.Method, call.Request, call.Response); err != nil {
				return summary, err
			}
			summary.Applied++
			continue
		}
		if err := verifyMetadataResource(call.Resource, call.Request, actual); err != nil {
			return summary, err
		}
		summary.Skipped++
		summary.Unchanged++
	}
	return summary, nil
}

func applyProbeResult(resource string, probe *metadataExistsProbe, expectedRequest proto.Message) (bool, proto.Message) {
	if resource == "dataset_columns" {
		rsp, _ := probe.Response.(*pb.ListDatasetColumnsRsp)
		if rsp == nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			return false, nil
		}
		req := probe.Request.(*pb.ListDatasetColumnsReq)
		expected := expectedRequest.(*pb.UpsertDatasetColumnReq).GetColumn()
		for _, c := range rsp.GetColumns() {
			if c.GetColumnName() == expected.GetColumnName() && c.GetSpaceId() == req.GetSpaceId() && c.GetDatasetId() == req.GetDatasetId() {
				return true, c
			}
		}
		return false, nil
	}
	ret, ok := responseRetInfo(probe.Response)
	if !ok || ret == nil || ret.GetCode() != pb.ErrorCode_SUCCESS {
		return false, nil
	}
	switch rsp := probe.Response.(type) {
	case *pb.GetSpaceRsp:
		return true, rsp.GetSpace()
	case *pb.GetDataSourceRsp:
		return true, rsp.GetDataSource()
	case *pb.GetDatasetRsp:
		return true, rsp.GetDataset()
	case *pb.GetFieldRsp:
		return true, rsp.GetField()
	case *pb.GetPrimaryStoreRouteRsp:
		return true, rsp.GetPrimaryStoreRoute()
	case *pb.GetFactorRsp:
		return true, rsp.GetFactor()
	case *pb.GetPrimaryStoreNodeRsp:
		return true, rsp.GetNode()
	case *pb.GetDeviceRsp:
		return true, rsp.GetDevice()
	case *pb.GetViewRsp:
		return true, rsp.GetView()
	}
	return true, nil
}

func verifyMetadataResource(resource string, request, actual proto.Message) error {
	if actual == nil {
		return fmt.Errorf("%s exists but response omitted resource", resource)
	}
	var expected proto.Message
	switch req := request.(type) {
	case *pb.CreateSpaceReq:
		expected = req.GetSpace()
	case *pb.CreateDataSourceReq:
		expected = req.GetDataSource()
	case *pb.CreateDatasetReq:
		expected = req.GetDataset()
	case *pb.CreateFieldReq:
		expected = req.GetField()
	case *pb.UpsertDatasetColumnReq:
		expected = req.GetColumn()
	case *pb.CreatePrimaryStoreRouteReq:
		expected = req.GetPrimaryStoreRoute()
	case *pb.CreateFactorReq:
		expected = req.GetFactor()
	case *pb.CreatePrimaryStoreNodeReq:
		expected = req.GetNode()
	case *pb.CreateDeviceReq:
		expected = req.GetDevice()
	case *pb.CreateViewReq:
		expected = req.GetView()
	default:
		return fmt.Errorf("unsupported apply resource request %T", request)
	}
	if !metadataContractsEqual(resource, expected, actual) {
		return fmt.Errorf("metadata %s exists but contract differs", resource)
	}
	return nil
}

func metadataContractsEqual(resource string, a, b proto.Message) bool {
	if resource == "spaces" {
		x, y := a.(*pb.Space), b.(*pb.Space)
		return x.GetSpaceId() == y.GetSpaceId() && x.GetName() == y.GetName() && x.GetDescription() == y.GetDescription() && x.GetOwner() == y.GetOwner() && x.GetStatus() == y.GetStatus() && reflect.DeepEqual(x.GetAttributes(), y.GetAttributes())
	}
	if resource == "data_sources" {
		x, y := a.(*pb.DataSource), b.(*pb.DataSource)
		return x.GetSpaceId() == y.GetSpaceId() && x.GetDataSourceId() == y.GetDataSourceId() && x.GetName() == y.GetName() && x.GetKind() == y.GetKind() && x.GetTimezone() == y.GetTimezone() && x.GetStatus() == y.GetStatus() && reflect.DeepEqual(x.GetAttributes(), y.GetAttributes())
	}
	if resource == "datasets" {
		x, y := a.(*pb.Dataset), b.(*pb.Dataset)
		return x.GetSpaceId() == y.GetSpaceId() && x.GetDatasetId() == y.GetDatasetId() && x.GetDataSourceId() == y.GetDataSourceId() && x.GetDataKind() == y.GetDataKind() && slices.Equal(x.GetFreqs(), y.GetFreqs()) && x.GetStatus() == y.GetStatus()
	}
	if resource == "fields" {
		x, y := a.(*pb.Field), b.(*pb.Field)
		return x.GetSpaceId() == y.GetSpaceId() && x.GetFieldId() == y.GetFieldId() && x.GetValueType() == y.GetValueType() && x.GetStatus() == y.GetStatus()
	}
	if resource == "dataset_columns" {
		x, y := a.(*pb.DatasetColumn), b.(*pb.DatasetColumn)
		return x.GetSpaceId() == y.GetSpaceId() && x.GetDatasetId() == y.GetDatasetId() && x.GetColumnName() == y.GetColumnName() && x.GetOriginType() == y.GetOriginType() && x.GetOriginId() == y.GetOriginId() && x.GetValueType() == y.GetValueType() && x.GetRequired() == y.GetRequired() && x.GetStatus() == y.GetStatus()
	}
	if resource == "primary_store_routes" {
		x, y := a.(*pb.PrimaryStoreRoute), b.(*pb.PrimaryStoreRoute)
		return x.GetSpaceId() == y.GetSpaceId() && x.GetRouteId() == y.GetRouteId() && x.GetDatasetId() == y.GetDatasetId() && x.GetSubjectPattern() == y.GetSubjectPattern() && x.GetHashRule() == y.GetHashRule() && x.GetNodeId() == y.GetNodeId() && x.GetPriority() == y.GetPriority() && x.GetStatus() == y.GetStatus()
	}
	return proto.Equal(a, b)
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
		value = "http://127.0.0.1:20200"
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
		pb.ErrorCode_FACTOR_NOT_FOUND,
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

func (s seedFactor) toPB() (*pb.Factor, error) {
	valueType, err := parseFieldValueType(s.ValueType)
	if err != nil {
		return nil, err
	}
	return &pb.Factor{SpaceId: s.SpaceID, FactorId: s.FactorID, Name: s.Name, Description: s.Description, Algorithm: s.Algorithm, ParamsJson: s.ParamsJSON, ValueType: valueType, Status: s.status(), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Attributes: s.Attributes}, nil
}

func (s seedDatasetColumn) toPB() (*pb.DatasetColumn, error) {
	originType, valueType, err := parseDatasetColumnAndValueTypes(s.OriginType, s.ValueType)
	if err != nil {
		return nil, err
	}
	return &pb.DatasetColumn{SpaceId: s.SpaceID, DatasetId: s.DatasetID, ColumnName: s.ColumnName, OriginType: originType, OriginId: s.OriginID, ValueType: valueType, Required: s.Required, IsUnique: s.IsUnique, Aliases: s.Aliases, Status: s.status(), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Attributes: s.Attributes}, nil
}

func (s seedView) toPB() *pb.View {
	return &pb.View{SpaceId: s.SpaceID, ViewId: s.ViewID, Name: s.Name, Description: s.Description, PrimaryDatasetId: s.PrimaryDatasetID, DatasetIds: s.DatasetIDs, GrainKeys: s.GrainKeys, FilterJson: s.FilterJSON, Engine: s.Engine, RetentionWindow: s.RetentionWindow, Status: s.status(), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Attributes: s.Attributes}
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
	metadataCmd.AddCommand(metadataApplyCmd)

	metadataImportCmd.Flags().StringVarP(&metadataImportFile, "file", "f", "", "metadata seed YAML 文件路径")
	metadataImportCmd.Flags().StringVar(&metadataImportURL, "metadata-url", "", "moox-storage MetadataService HTTP 地址，例如 http://127.0.0.1:20200")
	metadataImportCmd.Flags().BoolVar(&metadataImportDryRun, "dry-run", false, "只解析并输出导入计划，不发送 RPC")
	metadataImportCmd.Flags().BoolVar(&metadataImportIfNotExists, "if-not-exists", false, "资源已存在时跳过 create 类调用")
	metadataApplyCmd.Flags().StringVarP(&metadataApplyFile, "file", "f", "", "metadata seed YAML 文件路径")
	metadataApplyCmd.Flags().StringVar(&metadataApplyURL, "metadata-url", "", "moox-storage MetadataService HTTP 地址")
	metadataApplyCmd.Flags().BoolVar(&metadataApplyDryRun, "dry-run", false, "只解析并输出应用计划，不发送 RPC")
}
