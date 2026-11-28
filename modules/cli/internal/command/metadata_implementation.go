package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
)

const (
	maxMetadataSeedBytes  = 8 << 20
	metadataApplyPageSize = 500
	metadataApplyMaxPages = 1000
)

func loadMetadataSeed(path string) (metadataSeed, error) {
	file, err := os.Open(path)
	if err != nil {
		return metadataSeed{}, fmt.Errorf("读取 metadata seed 失败 %s: %w", path, err)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxMetadataSeedBytes+1))
	if err != nil {
		return metadataSeed{}, fmt.Errorf("读取 metadata seed 失败 %s: %w", path, err)
	}
	if len(raw) > maxMetadataSeedBytes {
		return metadataSeed{}, fmt.Errorf("metadata seed exceeds %d bytes", maxMetadataSeedBytes)
	}
	var seed metadataSeed
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&seed); err != nil {
		return metadataSeed{}, fmt.Errorf("解析 metadata seed 失败 %s: %w", path, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return metadataSeed{}, fmt.Errorf("metadata seed must contain exactly one YAML document")
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
			Method:   "UpsertDataSource",
			Request:  &pb.UpsertDataSourceReq{DataSource: source},
			Response: &pb.UpsertDataSourceRsp{},
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
	for _, item := range seed.Tags {
		tag := item.toPB()
		calls = append(calls, metadataImportCall{
			Resource: "tags", Method: "UpsertTag", Request: &pb.UpsertTagReq{Tag: tag, Seed: true}, Response: &pb.UpsertTagRsp{},
			Exists: &metadataExistsProbe{Method: "GetTag", Request: &pb.GetTagReq{SpaceId: tag.GetSpaceId(), TagId: tag.GetTagId()}, Response: &pb.GetTagRsp{}},
		})
	}
	for _, item := range seed.Datasets {
		dataset, err := item.toPB()
		if err != nil {
			return nil, err
		}
		calls = append(calls, metadataImportCall{
			Resource: "datasets",
			Method:   "UpsertDataset",
			Request:  &pb.UpsertDatasetReq{Dataset: dataset},
			Response: &pb.UpsertDatasetRsp{},
			Exists: &metadataExistsProbe{
				Method:   "GetDataset",
				Request:  &pb.GetDatasetReq{SpaceId: dataset.GetSpaceId(), DatasetId: dataset.GetDatasetId()},
				Response: &pb.GetDatasetRsp{},
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
	return calls, nil
}

// metadataApplyConcurrency 是元数据导入同时进行的调用数。
const metadataApplyConcurrency = 16

// runMetadataApply 按依赖顺序逐类导入，同类资源并发。列要组内串行：新列的排序依赖写入顺序，
// 所以按数据集分组，每组只查一次列表。
func runMetadataApply(ctx context.Context, gateway gatewayclient.Invoker, calls []metadataImportCall) (metadataImportSummary, error) {
	summary := metadataImportSummary{Status: "ok", Service: metadataServiceName, Planned: len(calls), Resources: countMetadataCalls(calls)}
	var mu sync.Mutex
	record := func(applied bool) {
		mu.Lock()
		defer mu.Unlock()
		if applied {
			summary.Applied++
			return
		}
		summary.Skipped++
		summary.Unchanged++
	}
	for _, stage := range metadataApplyStages(calls) {
		group, groupCtx := errgroup.WithContext(ctx)
		group.SetLimit(metadataApplyConcurrency)
		for _, batch := range stage {
			group.Go(func() error {
				if batch[0].Resource == "dataset_columns" {
					return applyMetadataColumns(groupCtx, gateway, batch, record)
				}
				return applyMetadataCall(groupCtx, gateway, batch[0], record)
			})
		}
		if err := group.Wait(); err != nil {
			return summary, err
		}
	}
	return summary, nil
}

// metadataApplyStages 把调用分成按顺序执行的阶段，每个阶段里的批次可以并发，批次内串行。
func metadataApplyStages(calls []metadataImportCall) [][][]metadataImportCall {
	var stages [][][]metadataImportCall
	for start := 0; start < len(calls); {
		end := start
		for end < len(calls) && calls[end].Resource == calls[start].Resource {
			end++
		}
		same := calls[start:end]
		start = end
		switch same[0].Resource {
		case "dataset_columns":
			index := map[string]int{}
			var stage [][]metadataImportCall
			for _, call := range same {
				key := metadataColumnOwner(call)
				position, seen := index[key]
				if !seen {
					position = len(stage)
					index[key] = position
					stage = append(stage, nil)
				}
				stage[position] = append(stage[position], call)
			}
			stages = append(stages, stage)
		default:
			stage := make([][]metadataImportCall, 0, len(same))
			for _, call := range same {
				stage = append(stage, []metadataImportCall{call})
			}
			stages = append(stages, stage)
		}
	}
	return stages
}

// metadataColumnOwner 返回列所属的数据集；格式不对的请求返回空，到执行时报错。
func metadataColumnOwner(call metadataImportCall) string {
	if request, ok := call.Request.(*pb.UpsertDatasetColumnReq); ok {
		return request.GetColumn().GetSpaceId() + "\x00" + request.GetColumn().GetDatasetId()
	}
	return ""
}

func applyMetadataCall(ctx context.Context, gateway gatewayclient.Invoker, call metadataImportCall, record func(bool)) error {
	if call.Exists == nil {
		return fmt.Errorf("apply does not support resource %s without read probe", call.Resource)
	}
	found, actual, _, err := inspectMetadataApplyPage(ctx, gateway, call.Resource, call.Exists, call.Request)
	if err != nil {
		return err
	}
	return finishMetadataCall(ctx, gateway, call, found, actual, record)
}

func finishMetadataCall(ctx context.Context, gateway gatewayclient.Invoker, call metadataImportCall, found bool, actual proto.Message, record func(bool)) error {
	if !found {
		if err := postStorage(ctx, gateway, metadataServiceName, call.Method, call.Request, call.Response); err != nil {
			return err
		}
		record(true)
		return nil
	}
	// 空间与标签在管理台可改（空间的名称、时区、停用，标签成员），已有的以管理台为准，不比对。
	if call.Resource != "tags" && call.Resource != "spaces" {
		if err := verifyMetadataResource(call.Resource, call.Request, actual); err != nil {
			return err
		}
	}
	record(false)
	return nil
}

// applyMetadataColumns 处理一个数据集的全部列：查一次列表，再逐列比对，缺的按顺序写入。
func applyMetadataColumns(ctx context.Context, gateway gatewayclient.Invoker, calls []metadataImportCall, record func(bool)) error {
	pages, err := listMetadataColumns(ctx, gateway, calls[0])
	if err != nil {
		return err
	}
	for _, call := range calls {
		found, actual := false, proto.Message(nil)
		for _, page := range pages {
			if found, actual = applyProbeResult(call.Resource, page, call.Request); found {
				break
			}
		}
		if err := finishMetadataCall(ctx, gateway, call, found, actual, record); err != nil {
			return err
		}
	}
	return nil
}

// listMetadataColumns 读完列的全部分页。
func listMetadataColumns(ctx context.Context, gateway gatewayclient.Invoker, call metadataImportCall) ([]*metadataExistsProbe, error) {
	var pages []*metadataExistsProbe
	for page := uint32(1); page <= metadataApplyMaxPages; page++ {
		current := &metadataExistsProbe{}
		number := &commonpb.Page{Page: page, Size: metadataApplyPageSize}
		switch request := call.Request.(type) {
		case *pb.UpsertDatasetColumnReq:
			if request.GetColumn() == nil {
				return nil, errors.New("invalid dataset column apply call")
			}
			current.Method, current.Response = "ListDatasetColumns", &pb.ListDatasetColumnsRsp{}
			current.Request = &pb.ListDatasetColumnsReq{SpaceId: request.GetColumn().GetSpaceId(), DatasetId: request.GetColumn().GetDatasetId(), Page: number}
		default:
			return nil, fmt.Errorf("invalid %s apply call", call.Resource)
		}
		_, _, hasMore, err := inspectMetadataApplyPage(ctx, gateway, call.Resource, current, call.Request)
		if err != nil {
			return nil, err
		}
		pages = append(pages, current)
		if !hasMore {
			return pages, nil
		}
	}
	return nil, fmt.Errorf("metadata %s probe exceeds %d pages", call.Resource, metadataApplyMaxPages)
}

func inspectMetadataApplyPage(
	ctx context.Context,
	gateway gatewayclient.Invoker,
	resource string,
	probe *metadataExistsProbe,
	expectedRequest proto.Message,
) (bool, proto.Message, bool, error) {
	if err := postStorageRaw(ctx, gateway, metadataServiceName, probe.Method, probe.Request, probe.Response); err != nil {
		return false, nil, false, err
	}
	ret, ok := responseRetInfo(probe.Response)
	if !ok || ret == nil {
		return false, nil, false, fmt.Errorf("%s/%s failed: missing ret_info", metadataServiceName, probe.Method)
	}
	if ret.GetCode() != pb.ErrorCode_SUCCESS {
		if metadataNotFound(ret) {
			return false, nil, false, nil
		}
		return false, nil, false, fmt.Errorf("%s/%s failed: %s", metadataServiceName, probe.Method, ret.GetMsg())
	}
	found, actual := applyProbeResult(resource, probe, expectedRequest)
	switch response := probe.Response.(type) {
	case *pb.ListDatasetColumnsRsp:
		return found, actual, response.GetPageResult().GetHasMore(), nil
	default:
		return found, actual, false, nil
	}
}

func applyProbeResult(resource string, probe *metadataExistsProbe, expectedRequest proto.Message) (bool, proto.Message) {
	switch resource {
	case "dataset_columns":
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
	case *pb.GetTagRsp:
		return rsp.GetTag() != nil, rsp.GetTag()
	}
	return true, nil
}

func verifyMetadataResource(resource string, request, actual proto.Message) error {
	if actual == nil {
		return fmt.Errorf("%s exists but response omitted resource", resource)
	}
	var expected proto.Message
	switch req := request.(type) {
	case *pb.UpsertDataSourceReq:
		expected = req.GetDataSource()
	case *pb.UpsertDatasetReq:
		expected = req.GetDataset()
	case *pb.UpsertDatasetColumnReq:
		expected = req.GetColumn()
	default:
		return fmt.Errorf("unsupported apply resource request %T", request)
	}
	if !metadataContractsEqual(resource, expected, actual) {
		if source, ok := expected.(*pb.DataSource); ok {
			return fmt.Errorf("metadata data_sources %s/%s exists but contract differs", source.GetSpaceId(), source.GetDataSourceId())
		}
		return fmt.Errorf("metadata %s exists but contract differs", resource)
	}
	return nil
}

func metadataContractsEqual(resource string, a, b proto.Message) bool {
	if resource == "data_sources" {
		x, y := a.(*pb.DataSource), b.(*pb.DataSource)
		return x.GetSpaceId() == y.GetSpaceId() &&
			x.GetDataSourceId() == y.GetDataSourceId() &&
			x.GetName() == y.GetName() &&
			x.GetStatus() == y.GetStatus() &&
			maps.Equal(x.GetAttributes(), y.GetAttributes())
	}
	if resource == "datasets" {
		x, y := a.(*pb.Dataset), b.(*pb.Dataset)
		return x.GetSpaceId() == y.GetSpaceId() &&
			x.GetDatasetId() == y.GetDatasetId() &&
			x.GetDataSourceId() == y.GetDataSourceId() &&
			x.GetName() == y.GetName() &&
			x.GetDescription() == y.GetDescription() &&
			x.GetDataKind() == y.GetDataKind() &&
			x.GetFrequency() == y.GetFrequency() &&
			x.GetStatus() == y.GetStatus() &&
			maps.Equal(x.GetAttributes(), y.GetAttributes())
	}
	if resource == "dataset_columns" {
		x, y := a.(*pb.DatasetColumn), b.(*pb.DatasetColumn)
		return x.GetSpaceId() == y.GetSpaceId() &&
			x.GetDatasetId() == y.GetDatasetId() &&
			x.GetColumnName() == y.GetColumnName() &&
			x.GetValueType() == y.GetValueType() &&
			x.GetDisplayName() == y.GetDisplayName() &&
			x.GetGroup() == y.GetGroup() &&
			x.GetRequired() == y.GetRequired() &&
			slices.Equal(x.GetAliases(), y.GetAliases()) &&
			x.GetStatus() == y.GetStatus() &&
			maps.Equal(x.GetAttributes(), y.GetAttributes())
	}
	return proto.Equal(a, b)
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

func metadataNotFound(retInfo *pb.RetInfo) bool {
	switch retInfo.GetCode() {
	case pb.ErrorCode_NOT_FOUND,
		pb.ErrorCode_SPACE_NOT_FOUND,
		pb.ErrorCode_DATASET_NOT_FOUND,
		pb.ErrorCode_SUBJECT_NOT_FOUND,
		pb.ErrorCode_FACTOR_NOT_FOUND:
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
	return &pb.Space{SpaceId: s.SpaceID, Name: s.Name, Description: s.Description, Owner: s.Owner, Market: s.Market, Timezone: s.Timezone, Status: s.status(), Attributes: s.Attributes}
}

func (s seedDataSource) toPB() *pb.DataSource {
	return &pb.DataSource{SpaceId: s.SpaceID, DataSourceId: s.DataSourceID, Name: s.Name, Status: s.status(), Attributes: s.Attributes}
}

func (s seedSubject) toPB() *pb.Subject {
	return &pb.Subject{SpaceId: s.SpaceID, SubjectId: s.SubjectID, Name: s.Name, Timezone: s.Timezone, Status: s.status(), Attributes: s.Attributes}
}

func (s seedTag) toPB() *pb.Tag {
	return &pb.Tag{SpaceId: s.SpaceID, TagId: s.TagID, TagName: s.TagName, Description: s.Description, Mode: s.Mode, Builtin: s.Builtin, Attributes: s.Attributes}
}

// toPB 生成的数据集总是 active：Storage 建好数据集就启用，种子里数据集的 status 不起作用。
func (s seedDataset) toPB() (*pb.Dataset, error) {
	dataKind, err := parseDataKind(s.DataKind)
	if err != nil {
		return nil, err
	}
	return &pb.Dataset{SpaceId: s.SpaceID, DatasetId: s.DatasetID, DataSourceId: s.DataSourceID, Name: s.Name, Description: s.Description, DataKind: dataKind, Frequency: s.Freq, SubjectTags: s.SubjectTags, Status: "active", Attributes: s.Attributes}, nil
}

func (s seedDatasetColumn) toPB() (*pb.DatasetColumn, error) {
	valueType, err := parseFieldValueType(s.ValueType)
	if err != nil {
		return nil, err
	}
	return &pb.DatasetColumn{SpaceId: s.SpaceID, DatasetId: s.DatasetID, ColumnName: s.ColumnName, ValueType: valueType, DisplayName: s.DisplayName, Group: s.Group, Required: s.Required, Aliases: s.Aliases, Status: s.status(), Attributes: s.Attributes}, nil
}

func parseDataKind(value string) (pb.DataKind, error) {
	switch normalizeEnum(value) {
	case "", "UNSPECIFIED":
		return pb.DataKind_DATA_KIND_UNSPECIFIED, nil
	case "RECORD":
		return pb.DataKind_DATA_KIND_RECORD, nil
	case "TIME_SERIES":
		return pb.DataKind_DATA_KIND_TIME_SERIES, nil
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

func normalizeEnum(value string) string {
	value = strings.TrimSpace(strings.ToUpper(value))
	value = strings.TrimPrefix(value, "DATA_KIND_")
	value = strings.TrimPrefix(value, "FIELD_VALUE_TYPE_")
	value = strings.ReplaceAll(value, "-", "_")
	return value
}

func init() {
	rootCmd.AddCommand(metadataCmd)
	metadataCmd.AddCommand(metadataApplyCmd)
	metadataCmd.AddCommand(metadataSpacesCmd)

	metadataSpacesCmd.Flags().StringVar(&metadataSpacesFile, "seed", "", "metadata seed YAML 文件路径")
	metadataApplyCmd.Flags().StringVar(&metadataApplyFile, "seed", "", "metadata seed YAML 文件路径")
	metadataApplyCmd.Flags().StringVar(&metadataApplyManifest, "file", "./moox.toml", "操作员 SSH 网关使用的部署配置文件")
	metadataApplyCmd.Flags().BoolVar(&metadataApplyDryRun, "dry-run", false, "只解析并输出应用计划，不发送 RPC")
	metadataApplyCmd.Flags().StringSliceVar(&metadataApplySpaces, "spaces", nil, "只导入这些空间（空间 ID 或中文名，逗号分隔）；不写导入全部")
}
