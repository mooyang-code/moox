package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
)

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
	for _, item := range seed.FieldGroups {
		if err := check("field_groups", item.SpaceID); err != nil {
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
	if err := validateSeedDatasets(seed.Datasets); err != nil {
		return nil, err
	}
	var err error
	seed, err = normalizeFieldGroups(seed)
	if err != nil {
		return nil, err
	}
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
	for _, item := range seed.FieldGroups {
		group := item.toPB()
		calls = append(calls, metadataImportCall{
			Resource: "field_groups", Method: "CreateFieldGroup",
			Request: &pb.CreateFieldGroupReq{FieldGroup: group}, Response: &pb.CreateFieldGroupRsp{},
			Exists: &metadataExistsProbe{Method: "GetFieldGroup", Request: &pb.GetFieldGroupReq{SpaceId: group.GetSpaceId(), GroupId: group.GetGroupId()}, Response: &pb.GetFieldGroupRsp{}},
		})
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
	displayNames := make(map[string]string, len(seed.Fields)+len(seed.Factors))
	for _, item := range seed.Fields {
		displayNames[item.FieldID] = item.Name
	}
	for _, item := range seed.Factors {
		displayNames[item.FactorID] = item.Name
	}
	for _, item := range seed.DatasetColumns {
		if strings.TrimSpace(item.Attributes["display_name"]) == "" {
			if displayName := strings.TrimSpace(displayNames[item.OriginID]); displayName != "" {
				item.Attributes = cloneStringMap(item.Attributes)
				item.Attributes["display_name"] = displayName
			}
		}
		column, err := item.toPB()
		if err != nil {
			return nil, err
		}
		calls = append(calls, metadataImportCall{Resource: "dataset_columns", Method: "UpsertDatasetColumn", Request: &pb.UpsertDatasetColumnReq{Column: column}, Response: &pb.UpsertDatasetColumnRsp{}})
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

func validateSeedDatasets(datasets []seedDataset) error {
	for _, item := range datasets {
		if strings.TrimSpace(item.DataNodeID) == "" {
			return fmt.Errorf("dataset %q data_node_id is required", item.DatasetID)
		}
		if strings.TrimSpace(item.KeepDuration) == "" {
			return fmt.Errorf("dataset %q keep_duration is required", item.DatasetID)
		}
	}
	return nil
}

func normalizeFieldGroups(seed metadataSeed) (metadataSeed, error) {
	existing := make(map[string]seedFieldGroup, len(seed.FieldGroups))
	for _, group := range seed.FieldGroups {
		key := group.SpaceID + "\x00" + group.GroupID
		if strings.TrimSpace(group.SpaceID) == "" || strings.TrimSpace(group.GroupID) == "" || strings.TrimSpace(group.Name) == "" {
			return metadataSeed{}, errors.New("field_group space_id, group_id and name are required")
		}
		if _, duplicate := existing[key]; duplicate {
			return metadataSeed{}, fmt.Errorf("duplicate field_group %s/%s", group.SpaceID, group.GroupID)
		}
		existing[key] = group
	}
	for _, group := range seed.FieldGroups {
		if strings.TrimSpace(group.ParentGroupID) == "" {
			continue
		}
		parent, ok := existing[group.SpaceID+"\x00"+group.ParentGroupID]
		if !ok {
			return metadataSeed{}, fmt.Errorf("field_group %s/%s references undefined parent %q", group.SpaceID, group.GroupID, group.ParentGroupID)
		}
		if strings.TrimSpace(parent.ParentGroupID) != "" {
			return metadataSeed{}, fmt.Errorf("field_group %s/%s exceeds the two-level hierarchy", group.SpaceID, group.GroupID)
		}
	}
	for i := range seed.Fields {
		if strings.TrimSpace(seed.Fields[i].GroupID) == "" {
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
			return metadataSeed{}, fmt.Errorf("field %s/%s references undefined field_group %q", seed.Fields[i].SpaceID, seed.Fields[i].FieldID, seed.Fields[i].GroupID)
		}
	}
	return seed, nil
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
	case *pb.GetFieldGroupRsp:
		return true, rsp.GetFieldGroup()
	case *pb.GetFieldRsp:
		return true, rsp.GetField()
	case *pb.GetFactorRsp:
		return true, rsp.GetFactor()
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
	case *pb.CreateFieldGroupReq:
		expected = req.GetFieldGroup()
	case *pb.CreateFieldReq:
		expected = req.GetField()
	case *pb.UpsertDatasetColumnReq:
		expected = req.GetColumn()
	case *pb.CreateFactorReq:
		expected = req.GetFactor()
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
		return x.GetSpaceId() == y.GetSpaceId() && x.GetName() == y.GetName() && x.GetDescription() == y.GetDescription() && x.GetOwner() == y.GetOwner() && x.GetStatus() == y.GetStatus() && maps.Equal(x.GetAttributes(), y.GetAttributes())
	}
	if resource == "data_sources" {
		x, y := a.(*pb.DataSource), b.(*pb.DataSource)
		return x.GetSpaceId() == y.GetSpaceId() && x.GetDataSourceId() == y.GetDataSourceId() && x.GetName() == y.GetName() && x.GetKind() == y.GetKind() && x.GetTimezone() == y.GetTimezone() && x.GetStatus() == y.GetStatus() && maps.Equal(x.GetAttributes(), y.GetAttributes())
	}
	if resource == "datasets" {
		x, y := a.(*pb.Dataset), b.(*pb.Dataset)
		return x.GetSpaceId() == y.GetSpaceId() && x.GetDatasetId() == y.GetDatasetId() && x.GetDataSourceId() == y.GetDataSourceId() && x.GetDataKind() == y.GetDataKind() && slices.Equal(x.GetFreqs(), y.GetFreqs()) && x.GetStatus() == y.GetStatus()
	}
	if resource == "fields" {
		x, y := a.(*pb.Field), b.(*pb.Field)
		return x.GetSpaceId() == y.GetSpaceId() && x.GetFieldId() == y.GetFieldId() && x.GetGroupId() == y.GetGroupId() && x.GetValueType() == y.GetValueType() && x.GetSortOrder() == y.GetSortOrder() && x.GetStatus() == y.GetStatus()
	}
	if resource == "field_groups" {
		x, y := a.(*pb.FieldGroup), b.(*pb.FieldGroup)
		return x.GetSpaceId() == y.GetSpaceId() && x.GetGroupId() == y.GetGroupId() && x.GetParentGroupId() == y.GetParentGroupId() && x.GetSortOrder() == y.GetSortOrder() && x.GetStatus() == y.GetStatus()
	}
	if resource == "dataset_columns" {
		x, y := a.(*pb.DatasetColumn), b.(*pb.DatasetColumn)
		return x.GetSpaceId() == y.GetSpaceId() && x.GetDatasetId() == y.GetDatasetId() && x.GetColumnName() == y.GetColumnName() && x.GetOriginType() == y.GetOriginType() && x.GetOriginId() == y.GetOriginId() && x.GetValueType() == y.GetValueType() && x.GetRequired() == y.GetRequired() && x.GetStatus() == y.GetStatus()
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
		pb.ErrorCode_VIEW_COLUMN_NOT_FOUND:
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
	return &pb.Dataset{SpaceId: s.SpaceID, DatasetId: s.DatasetID, DataSourceId: s.DataSourceID, Name: s.Name, Description: s.Description, DataKind: dataKind, DataNodeId: strings.TrimSpace(s.DataNodeID), KeepDuration: strings.TrimSpace(s.KeepDuration), Freqs: s.Freqs, Status: "disabled", CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Attributes: s.Attributes}, nil
}

func (s seedDatasetSubject) toPB() *pb.DatasetSubject {
	return &pb.DatasetSubject{SpaceId: s.SpaceID, DatasetId: s.DatasetID, SubjectId: s.SubjectID, SubjectRole: s.SubjectRole, EffectiveStartTime: s.EffectiveStartTime, EffectiveEndTime: s.EffectiveEndTime, Status: s.status(), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Attributes: s.Attributes}
}

func (s seedField) toPB() (*pb.Field, error) {
	valueType, err := parseFieldValueType(s.ValueType)
	if err != nil {
		return nil, err
	}
	return &pb.Field{SpaceId: s.SpaceID, GroupId: s.GroupID, FieldId: s.FieldID, Name: s.Name, Description: s.Description, ValueType: valueType, Unit: s.Unit, ValidationRuleJson: s.ValidationRuleJSON, WriteExample: s.WriteExample, SortOrder: s.SortOrder, Status: s.status(), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Attributes: s.Attributes}, nil
}

func (s seedFieldGroup) toPB() *pb.FieldGroup {
	return &pb.FieldGroup{SpaceId: s.SpaceID, GroupId: s.GroupID, Name: s.Name, Description: s.Description, ParentGroupId: s.ParentGroupID, SortOrder: s.SortOrder, Status: s.status(), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Attributes: s.Attributes}
}

func (s seedFactor) toPB() (*pb.Factor, error) {
	valueType, err := parseFieldValueType(s.ValueType)
	if err != nil {
		return nil, err
	}
	return &pb.Factor{SpaceId: s.SpaceID, FactorId: s.FactorID, Name: s.Name, Description: s.Description, Algorithm: s.Algorithm, ParamsJson: s.ParamsJSON, ValueType: valueType, Status: s.status(), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Attributes: s.Attributes}, nil
}

func cloneStringMap(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src)+1)
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func (s seedDatasetColumn) toPB() (*pb.DatasetColumn, error) {
	originType, valueType, err := parseDatasetColumnAndValueTypes(s.OriginType, s.ValueType)
	if err != nil {
		return nil, err
	}
	return &pb.DatasetColumn{SpaceId: s.SpaceID, DatasetId: s.DatasetID, ColumnName: s.ColumnName, OriginType: originType, OriginId: s.OriginID, ValueType: valueType, Required: s.Required, IsUnique: s.IsUnique, Aliases: s.Aliases, Status: s.status(), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Attributes: s.Attributes}, nil
}

func (s seedView) toPB() *pb.View {
	return &pb.View{SpaceId: s.SpaceID, ViewId: s.ViewID, Name: s.Name, Description: s.Description, PrimaryDatasetId: s.PrimaryDatasetID, DatasetIds: s.DatasetIDs, GrainKeys: s.GrainKeys, FilterJson: s.FilterJSON, Engine: s.Engine, KeepDuration: s.KeepDuration, Status: s.status(), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Attributes: s.Attributes}
}

func (s seedViewColumn) toPB() (*pb.ViewColumn, error) {
	originType, valueType, err := parseColumnAndValueTypes(s.OriginType, s.ValueType)
	if err != nil {
		return nil, err
	}
	return &pb.ViewColumn{SpaceId: s.SpaceID, ViewId: s.ViewID, ColumnName: s.ColumnName, OriginType: originType, OriginId: s.OriginID, ValueType: valueType, OnlineTime: s.OnlineTime, SortOrder: s.SortOrder, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Attributes: s.Attributes}, nil
}

func (s seedDevice) toPB() *pb.Device {
	return &pb.Device{DeviceId: s.DeviceID, Name: s.Name, Engine: s.Engine, Endpoint: s.Endpoint, ConfigJson: s.ConfigJSON, Status: s.status(), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Attributes: s.Attributes}
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
	metadataCmd.AddCommand(metadataSpacesCmd)

	metadataImportCmd.Flags().StringVarP(&metadataImportFile, "file", "f", "", "metadata seed YAML 文件路径")
	metadataImportCmd.Flags().StringVar(&metadataImportURL, "metadata-url", "", "moox-storage MetadataService HTTP 地址，例如 http://127.0.0.1:20200")
	metadataImportCmd.Flags().BoolVar(&metadataImportDryRun, "dry-run", false, "只解析并输出导入计划，不发送 RPC")
	metadataImportCmd.Flags().BoolVar(&metadataImportIfNotExists, "if-not-exists", false, "资源已存在时跳过 create 类调用")
	metadataImportCmd.Flags().StringSliceVar(&metadataImportSpaces, "spaces", nil, "仅导入指定 Space ID 或中文名，逗号分隔")
	metadataSpacesCmd.Flags().StringVarP(&metadataSpacesFile, "file", "f", "", "metadata seed YAML 文件路径")
	metadataApplyCmd.Flags().StringVarP(&metadataApplyFile, "file", "f", "", "metadata seed YAML 文件路径")
	metadataApplyCmd.Flags().StringVar(&metadataApplyURL, "metadata-url", "", "moox-storage MetadataService HTTP 地址")
	metadataApplyCmd.Flags().BoolVar(&metadataApplyDryRun, "dry-run", false, "只解析并输出应用计划，不发送 RPC")
}
