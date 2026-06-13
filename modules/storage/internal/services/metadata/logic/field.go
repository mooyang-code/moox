package logic

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
	"github.com/mooyang-code/moox/modules/storage/internal/services/common/errors"
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao/model"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/utils"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

// CreateField 创建字段
func (x *MetaServicerImpl) CreateField(ctx context.Context, req *pb.CreateFieldReq) (*pb.CreateFieldRsp, error) {
	log.InfoContextf(ctx, "CreateField enter:%+v", req)
	rsp := &pb.CreateFieldRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "字段创建成功",
		},
	}
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "CreateField failed: %s", rsp.RetInfo.Msg)
		}
	}()

	// 1. 参数校验
	if err := validateCreateFieldReq(req); err != nil {
		rsp.RetInfo.Code = err.Code
		rsp.RetInfo.Msg = err.Msg
		return rsp, nil
	}

	// 2. 验证项目和数据集（移除数据类型校验）
	// 这里可以添加其他必要的项目和数据集验证逻辑

	// 3. 生成字段ID
	fieldID, e := x.generateFieldID(int(req.GetFieldDetailInfo().GetProjId()))
	if e != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_UPDATE
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, e)
		return rsp, nil
	}

	// 4. 构建并保存字段
	field := x.buildField(req.GetFieldDetailInfo(), fieldID)
	if err := x.dbDAO.AddField(field); err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_UPDATE
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err)
		return rsp, nil
	}

	rsp.FieldId = int32(fieldID)
	log.InfoContextf(ctx, "CreateField response: %+v", rsp)
	return rsp, nil
}

// UpdateField 更新字段
func (x *MetaServicerImpl) UpdateField(ctx context.Context, req *pb.UpdateFieldReq) (*pb.UpdateFieldRsp, error) {
	log.InfoContextf(ctx, "UpdateField enter:%+v", req)
	rsp := &pb.UpdateFieldRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "字段更新成功",
		},
	}
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "UpdateField failed: %s", rsp.RetInfo.Msg)
		}
	}()

	// 1. 参数校验
	if err := validateUpdateFieldReq(req); err != nil {
		rsp.RetInfo.Code = err.Code
		rsp.RetInfo.Msg = err.Msg
		return rsp, nil
	}

	// 2. 查找目标字段
	targetField, err := x.findFieldByIDAndProject(req.GetFieldId(), req.GetProjId())
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_SELECT
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_SELECT, err)
		return rsp, nil
	}
	if targetField == nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FIELD_INFO_NOT_EXIST
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FIELD_INFO_NOT_EXIST,
			fmt.Errorf("字段不存在或不属于指定项目"))
		return rsp, nil
	}

	// 3. 验证数据集（如果有更新，移除数据类型校验）
	updateInfo := req.GetFieldUpdateInfo()
	// 这里可以添加其他必要的数据集验证逻辑

	// 4. 更新字段信息
	x.updateFieldFromDetailInfo(targetField, updateInfo)
	log.InfoContextf(ctx, "UpdateField targetField: %+v", targetField)

	// 5. 保存到数据库
	if err := x.dbDAO.UpdateField(targetField); err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_UPDATE
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err)
		return rsp, nil
	}

	log.InfoContextf(ctx, "UpdateField response: %+v", rsp)
	return rsp, nil
}

// GetField 拉取字段详情信息接口
func (x *MetaServicerImpl) GetField(ctx context.Context, req *pb.GetFieldReq) (*pb.GetFieldRsp, error) {
	log.InfoContextf(ctx, "GetField enter:%+v", req)
	rsp := &pb.GetFieldRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "获取字段成功",
		},
	}

	// 参数校验
	if req.GetProjId() == 0 || req.GetFieldId() == 0 {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("项目ID和字段ID不能为空"))
		return rsp, nil
	}

	// 查找字段信息
	targetField, err := x.findFieldByIDAndProject(req.GetFieldId(), req.GetProjId())
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_SELECT
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_SELECT, err)
		return rsp, nil
	}
	if targetField == nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FIELD_INFO_NOT_EXIST
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FIELD_INFO_NOT_EXIST, fmt.Errorf("字段不存在"))
		return rsp, nil
	}

	// 构造返回数据
	rsp.FieldDetailInfo = x.buildSingleFieldDetailInfo(*targetField)

	log.InfoContextf(ctx, "GetField response: %+v", rsp)
	return rsp, nil
}

// DeleteField 删除字段
func (x *MetaServicerImpl) DeleteField(ctx context.Context, req *pb.DeleteFieldReq) (*pb.DeleteFieldRsp, error) {
	log.InfoContextf(ctx, "DeleteField enter:%+v", req)
	rsp := &pb.DeleteFieldRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "字段删除成功",
		},
	}
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "DeleteField failed: %s", rsp.RetInfo.Msg)
		}
	}()

	// 1. 参数校验
	if req.GetProjId() == 0 || req.GetFieldId() == 0 {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("项目ID和字段ID不能为空"))
		return rsp, nil
	}

	// 2. 验证字段是否存在并属于指定项目
	targetField, err := x.findFieldByIDAndProject(req.GetFieldId(), req.GetProjId())
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_SELECT
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_SELECT, err)
		return rsp, nil
	}
	if targetField == nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FIELD_INFO_NOT_EXIST
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FIELD_INFO_NOT_EXIST,
			fmt.Errorf("字段不存在或不属于指定项目"))
		return rsp, nil
	}

	// 3. 执行禁用（设置Enabled=false）
	if err := x.dbDAO.DeleteField(int(req.GetFieldId())); err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_UPDATE
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err)
		return rsp, nil
	}

	log.InfoContextf(ctx, "DeleteField response: %+v", rsp)
	return rsp, nil
}

// SearchField 字段搜索接口
func (x *MetaServicerImpl) SearchField(ctx context.Context, req *pb.SearchFieldReq) (*pb.SearchFieldRsp, error) {
	log.InfoContextf(ctx, "SearchField enter:%+v", req)
	rsp := &pb.SearchFieldRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "字段搜索成功",
		},
	}

	// 1. 参数校验
	if err := x.validateSearchFieldReq(req); err != nil {
		rsp.RetInfo = err
		return rsp, nil
	}

	// 2. 获取并过滤字段
	filteredFields, err := x.getFilteredFields(req)
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_SELECT
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_SELECT, err)
		return rsp, nil
	}

	// 3. 分页处理
	paginatedFields, pageInfo := x.paginateFields(filteredFields, req.GetPageInfo())

	// 4. 构造返回数据
	fieldDetailInfos := x.buildFieldDetailInfos(paginatedFields)
	rsp.FieldDetailInfos = fieldDetailInfos
	rsp.CurPage = pageInfo.CurPage
	rsp.TotalPage = pageInfo.TotalPage
	rsp.TotalNum = pageInfo.TotalNum

	log.InfoContextf(ctx, "SearchField response: fields count=%d, total=%d", len(fieldDetailInfos), pageInfo.TotalNum)
	return rsp, nil
}

// UpsertField 更新或创建字段，复用现有的CreateField和UpdateField接口
func (x *MetaServicerImpl) UpsertField(ctx context.Context, req *pb.UpsertFieldReq) (*pb.UpsertFieldRsp, error) {
	log.InfoContextf(ctx, "UpsertField enter:%+v", req)
	rsp := &pb.UpsertFieldRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "字段操作成功",
		},
	}
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "UpsertField failed: %s", rsp.RetInfo.Msg)
		}
	}()

	// 1. 参数校验
	if err := validateUpsertFieldReq(req); err != nil {
		rsp.RetInfo.Code = err.Code
		rsp.RetInfo.Msg = err.Msg
		return rsp, nil
	}

	// 2. 检查字段是否已存在（根据项目ID和接口名查找）
	existingFieldID, err := x.findFieldIDByProjectAndInterface(ctx, int(req.GetProjId()), req.GetInterfaceName())
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_SELECT
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_SELECT, err)
		return rsp, nil
	}

	// 3. 根据字段是否存在选择处理方式
	if existingFieldID > 0 {
		return x.handleFieldUpdate(ctx, req, existingFieldID)
	} else {
		return x.handleFieldCreate(ctx, req)
	}
}

// generateFieldID 生成字段ID
func (x *MetaServicerImpl) generateFieldID(projID int) (int, error) {
	minFieldID := projID * 1000
	maxFieldID := (projID+1)*1000 - 1

	// 获取当前范围内的最大字段ID
	maxID, err := x.dbDAO.GetMaxFieldIDInRange(minFieldID, maxFieldID)
	if err != nil {
		return 0, err
	}

	// 计算新的字段ID
	fieldID := minFieldID
	if maxID >= minFieldID {
		fieldID = maxID + 1
	}
	return fieldID, nil
}

// buildFieldFromRequest 从请求构建字段对象
func (x *MetaServicerImpl) buildFieldFromRequest(fieldDetailInfo *pb.FieldDetailInfo, fieldID int) *model.Field {
	datasetIDsStr := strings.Trim(strings.Join(strings.Fields(fmt.Sprint(fieldDetailInfo.GetDatasetIds())), "+"), "[]")
	required := fieldDetailInfo.GetRequired()
	if required == "" {
		required = constants.DisabledValue
	}
	unique := fieldDetailInfo.GetUnique()
	if unique == "" {
		unique = constants.DisabledValue
	}

	return &model.Field{
		FieldID:       fieldID,
		ProjID:        int(fieldDetailInfo.GetProjId()),
		DatasetIDs:    datasetIDsStr,
		FieldName:     fieldDetailInfo.GetFieldName(),
		InterfaceName: fieldDetailInfo.GetInterfaceName(),

		Desc:                 fieldDetailInfo.GetDesc(),
		TableType:            int(fieldDetailInfo.GetTableType()),
		Required:             required,
		Unique:               unique,
		ParentFieldID:        int(fieldDetailInfo.GetParentFieldId()),
		LevelInfo:            "", // 根据需要可以处理level_info
		FieldPrimaryFormat:   int(fieldDetailInfo.GetFieldFormatType().GetFieldPrimaryFormat()),
		FieldSecondaryFormat: int(fieldDetailInfo.GetFieldFormatType().GetFieldSecondaryFormat()),
		ValueLibID:           int(fieldDetailInfo.GetValueLibId()),
		ValidationRule:       serializeValidationRule(fieldDetailInfo.GetValidationRule()),
		WriteExample:         fieldDetailInfo.GetWriteExample(),
		Remark:               fieldDetailInfo.GetRemark(),
		CreateTime:           time.Now(),
		ModifyTime:           time.Now(),
	}
}

// findFieldByIDAndProject 根据字段ID和项目ID查找字段
func (x *MetaServicerImpl) findFieldByIDAndProject(fieldID int32, projID int32) (*model.Field, error) {
	return x.findFieldByCondition(func(field model.Field) bool {
		return field.FieldID == int(fieldID) && field.ProjID == int(projID)
	})
}

// findFieldByCondition 通用字段查找方法
func (x *MetaServicerImpl) findFieldByCondition(condition func(model.Field) bool) (*model.Field, error) {
	fields, err := x.dbDAO.GetAllFieldList()
	if err != nil {
		return nil, err
	}

	for _, field := range fields {
		if condition(field) {
			return &field, nil
		}
	}
	return nil, nil
}

// validateSearchFieldReq 验证搜索字段请求参数
func (x *MetaServicerImpl) validateSearchFieldReq(req *pb.SearchFieldReq) *pb.RetInfo {
	if req.GetProjId() == 0 {
		return &pb.RetInfo{
			Code: pb.EnumErrorCode_INVALID_PARAM,
			Msg:  errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("项目ID不能为空")),
		}
	}
	return nil
}

// getFilteredFields 获取并过滤字段列表
func (x *MetaServicerImpl) getFilteredFields(req *pb.SearchFieldReq) ([]model.Field, error) {
	// 获取字段列表(获取全部列表，在内存中过滤；因为字段不会太多，这样底层处理比较简单，无需拼SQL。方便未来更换底层存储-只需实现getAll即可)
	fields, err := x.dbDAO.GetValidFieldList()
	if err != nil {
		return nil, err
	}

	// 过滤字段
	var filteredFields []model.Field
	for _, field := range fields {
		if x.shouldIncludeField(field, req) {
			filteredFields = append(filteredFields, field)
		}
	}
	return filteredFields, nil
}

// shouldIncludeField 判断字段是否应该包含在搜索结果中
func (x *MetaServicerImpl) shouldIncludeField(field model.Field, req *pb.SearchFieldReq) bool {
	// 基本过滤：项目ID不匹配，或已禁用的
	if field.ProjID != int(req.GetProjId()) || field.Enabled != constants.EnabledValue {
		return false
	}

	// 数据集ID过滤
	if req.GetDatasetId() != 0 {
		datasetIDStr := strconv.Itoa(int(req.GetDatasetId()))
		if !strings.Contains(field.DatasetIDs, datasetIDStr) {
			return false
		}
	}

	// 字段名模糊匹配
	if req.GetFieldName() != "" && !strings.Contains(field.FieldName, req.GetFieldName()) {
		return false
	}

	// 接口名模糊匹配
	if req.GetInterfaceName() != "" && !strings.Contains(field.InterfaceName, req.GetInterfaceName()) {
		return false
	}

	// 表类型过滤
	if req.GetTableType() != pb.EnumTableType_INVALID_TABLE_TYPE {
		if field.TableType != int(req.GetTableType()) {
			return false
		}
	}

	// 字段ID列表过滤
	if len(req.GetFieldIds()) > 0 {
		found := false
		for _, fieldID := range req.GetFieldIds() {
			if field.FieldID == int(fieldID) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// PaginationInfo 分页信息结构
type PaginationInfo struct {
	CurPage   int32
	TotalPage int32
	TotalNum  int32
}

// paginateFields 对字段列表进行分页处理
func (x *MetaServicerImpl) paginateFields(fields []model.Field, pageInfo *pb.PageInfo) ([]model.Field, PaginationInfo) {
	totalNum := len(fields)

	// 计算页大小
	pageSize := 50 // 默认页大小
	if pageInfo != nil && pageInfo.GetSize() > 0 {
		pageSize = int(pageInfo.GetSize())
		if pageSize > 200 {
			pageSize = 200 // 最大200
		}
	}

	// 计算当前页
	currentPage := 1
	if pageInfo != nil && pageInfo.GetPageIdx() > 0 {
		currentPage = int(pageInfo.GetPageIdx())
	}

	// 计算总页数
	totalPages := (totalNum + pageSize - 1) / pageSize
	if totalPages == 0 {
		totalPages = 1
	}

	// 计算分页范围
	startIndex := (currentPage - 1) * pageSize
	endIndex := startIndex + pageSize
	if endIndex > totalNum {
		endIndex = totalNum
	}

	// 获取当前页数据
	var pageFields []model.Field
	if startIndex < totalNum {
		pageFields = fields[startIndex:endIndex]
	}

	return pageFields, PaginationInfo{
		CurPage:   int32(currentPage),
		TotalPage: int32(totalPages),
		TotalNum:  int32(totalNum),
	}
}

// buildFieldDetailInfos 构造字段详情信息列表
func (x *MetaServicerImpl) buildFieldDetailInfos(fields []model.Field) []*pb.FieldDetailInfo {
	var fieldDetailInfos []*pb.FieldDetailInfo
	for _, field := range fields {
		fieldInfo := x.buildSingleFieldDetailInfo(field)
		fieldDetailInfos = append(fieldDetailInfos, fieldInfo)
	}
	return fieldDetailInfos
}

// buildSingleFieldDetailInfo 构造单个字段详情信息
func (x *MetaServicerImpl) buildSingleFieldDetailInfo(field model.Field) *pb.FieldDetailInfo {
	return &pb.FieldDetailInfo{
		FieldId:    int32(field.FieldID),
		ProjId:     int32(field.ProjID),
		DatasetIds: utils.StringArray2Int32Array(field.DatasetIDs, "+"),
		FieldName:  field.FieldName,

		InterfaceName: field.InterfaceName,
		Desc:          field.Desc,
		Required:      field.Required,
		Unique:        field.Unique,
		TableType:     pb.EnumTableType(field.TableType),
		ParentFieldId: int32(field.ParentFieldID),
		FieldFormatType: &pb.FieldFormatType{
			FieldPrimaryFormat:   pb.EnumFieldPrimaryFormat(field.FieldPrimaryFormat),
			FieldSecondaryFormat: pb.EnumFieldSecondaryFormat(field.FieldSecondaryFormat),
		},
		ValueLibId:     int32(field.ValueLibID),
		ValidationRule: parseValidationRule(field.ValidationRule),
		WriteExample:   field.WriteExample,
		Remark:         field.Remark,
		Ctime:          field.CreateTime.Format("2006-01-02 15:04:05"),
		Mtime:          field.ModifyTime.Format("2006-01-02 15:04:05"),
		Enabled:        field.Enabled,
	}
}

// validateCreateFieldReq 验证创建字段请求
func validateCreateFieldReq(req *pb.CreateFieldReq) *validateError {
	if req.GetFieldDetailInfo() == nil {
		return &validateError{
			Code: pb.EnumErrorCode_INVALID_PARAM,
			Msg:  errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("字段详情信息不能为空")),
		}
	}
	if req.GetFieldDetailInfo().GetProjId() == 0 {
		return &validateError{
			Code: pb.EnumErrorCode_INVALID_PARAM,
			Msg:  errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("项目ID不能为空")),
		}
	}
	if req.GetFieldDetailInfo().GetFieldName() == "" {
		return &validateError{
			Code: pb.EnumErrorCode_INVALID_PARAM,
			Msg:  errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("字段名称不能为空")),
		}
	}
	if req.GetFieldDetailInfo().GetInterfaceName() == "" {
		return &validateError{
			Code: pb.EnumErrorCode_INVALID_PARAM,
			Msg:  errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("字段接口名不能为空")),
		}
	}
	if len(req.GetFieldDetailInfo().GetDatasetIds()) == 0 {
		return &validateError{
			Code: pb.EnumErrorCode_INVALID_PARAM,
			Msg:  errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("字段必须关联至少一个数据集")),
		}
	}
	return nil
}

// validateUpdateFieldReq 验证更新字段请求
func validateUpdateFieldReq(req *pb.UpdateFieldReq) *validateError {
	if req.GetProjId() == 0 {
		return &validateError{
			Code: pb.EnumErrorCode_INVALID_PARAM,
			Msg:  errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("项目ID不能为空")),
		}
	}
	if req.GetFieldId() == 0 {
		return &validateError{
			Code: pb.EnumErrorCode_INVALID_PARAM,
			Msg:  errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("字段ID不能为空")),
		}
	}
	if req.GetFieldUpdateInfo() == nil {
		return &validateError{
			Code: pb.EnumErrorCode_INVALID_PARAM,
			Msg:  errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("字段更新信息不能为空")),
		}
	}
	return nil
}

// validateUpsertFieldReq 验证更新/创建字段请求
func validateUpsertFieldReq(req *pb.UpsertFieldReq) *validateError {
	if req.GetProjId() == 0 {
		return &validateError{
			Code: pb.EnumErrorCode_INVALID_PARAM,
			Msg:  errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("项目ID不能为空")),
		}
	}
	if req.GetInterfaceName() == "" {
		return &validateError{
			Code: pb.EnumErrorCode_INVALID_PARAM,
			Msg:  errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("字段接口名不能为空")),
		}
	}
	if req.GetFieldDetailInfo() == nil {
		return &validateError{
			Code: pb.EnumErrorCode_INVALID_PARAM,
			Msg:  errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("字段详情信息不能为空")),
		}
	}
	return nil
}

// serializeValidationRule 将ValidationRule结构序列化为JSON字符串
// 直接序列化内部的具体规则，避免包装格式
func serializeValidationRule(rule *pb.ValidationRule) string {
	if rule == nil || rule.Rule == nil {
		return ""
	}

	// 直接序列化具体的规则类型，避免外层包装
	var data []byte
	var err error

	switch r := rule.Rule.(type) {
	case *pb.ValidationRule_StringRule:
		data, err = json.Marshal(map[string]*pb.StringRule{"string_rule": r.StringRule})
	case *pb.ValidationRule_IntegerRule:
		data, err = json.Marshal(map[string]*pb.IntegerRule{"integer_rule": r.IntegerRule})
	case *pb.ValidationRule_DoubleRule:
		data, err = json.Marshal(map[string]*pb.DoubleRule{"double_rule": r.DoubleRule})
	case *pb.ValidationRule_OptionRule:
		data, err = json.Marshal(map[string]*pb.OptionRule{"option_rule": r.OptionRule})
	default:
		log.Errorf("未知的ValidationRule类型: %T", rule.Rule)
		return ""
	}

	if err != nil {
		log.Errorf("序列化ValidationRule失败: %v", err)
		return ""
	}
	return string(data)
}

// parseValidationRule 将JSON字符串反序列化为ValidationRule结构
// 只支持标准下划线格式: {"string_rule":{"min_length":3,"max_length":20}}
func parseValidationRule(ruleStr string) *pb.ValidationRule {
	if ruleStr == "" {
		return nil
	}
	log.Debugf("开始解析ValidationRule: %s", ruleStr)

	var rule pb.ValidationRule
	var ruleMap map[string]json.RawMessage

	if err := json.Unmarshal([]byte(ruleStr), &ruleMap); err != nil {
		log.Errorf("反序列化ValidationRule失败: %v, 原始字符串: %s", err, ruleStr)
		return nil
	}

	// 解析标准下划线格式
	if stringRule, ok := ruleMap["string_rule"]; ok {
		var sr pb.StringRule
		if err := json.Unmarshal(stringRule, &sr); err == nil {
			rule.Rule = &pb.ValidationRule_StringRule{StringRule: &sr}
			log.Debugf("解析string_rule成功: %+v", rule)
			return &rule
		}
	}
	if integerRule, ok := ruleMap["integer_rule"]; ok {
		var ir pb.IntegerRule
		if err := json.Unmarshal(integerRule, &ir); err == nil {
			rule.Rule = &pb.ValidationRule_IntegerRule{IntegerRule: &ir}
			log.Debugf("解析integer_rule成功: %+v", rule)
			return &rule
		}
	}
	if doubleRule, ok := ruleMap["double_rule"]; ok {
		var dr pb.DoubleRule
		if err := json.Unmarshal(doubleRule, &dr); err == nil {
			rule.Rule = &pb.ValidationRule_DoubleRule{DoubleRule: &dr}
			log.Debugf("解析double_rule成功: %+v", rule)
			return &rule
		}
	}
	if optionRule, ok := ruleMap["option_rule"]; ok {
		var or pb.OptionRule
		if err := json.Unmarshal(optionRule, &or); err == nil {
			rule.Rule = &pb.ValidationRule_OptionRule{OptionRule: &or}
			log.Debugf("解析option_rule成功: %+v", rule)
			return &rule
		}
	}

	log.Errorf("ValidationRule格式错误，不支持的格式: %s", ruleStr)
	return nil
}

// findFieldIDByProjectAndInterface 根据项目ID和接口名查找字段ID（包括已删除的字段）
func (x *MetaServicerImpl) findFieldIDByProjectAndInterface(_ context.Context, projID int, interfaceName string) (int, error) {
	field, err := x.findFieldByCondition(func(field model.Field) bool {
		return field.ProjID == projID && field.InterfaceName == interfaceName
	})
	if err != nil {
		return 0, err
	}
	if field == nil {
		return 0, nil // 未找到字段
	}
	return field.FieldID, nil
}

// convertUpsertToCreateRequest 将UpsertFieldReq转换为CreateFieldReq
func convertUpsertToCreateRequest(req *pb.UpsertFieldReq) *pb.CreateFieldReq {
	fieldDetailInfo := req.GetFieldDetailInfo()
	// 确保接口名称一致
	fieldDetailInfo.InterfaceName = req.GetInterfaceName()

	return &pb.CreateFieldReq{
		AuthInfo:        req.GetAuthInfo(),
		Operator:        req.GetOperator(), // 传递操作人信息
		FieldDetailInfo: fieldDetailInfo,
	}
}

// convertUpsertToUpdateRequest 将UpsertFieldReq转换为UpdateFieldReq
func convertUpsertToUpdateRequest(req *pb.UpsertFieldReq, fieldID int32) *pb.UpdateFieldReq {
	fieldDetailInfo := req.GetFieldDetailInfo()

	// 确保Enabled字段有默认值（默认启用）
	if fieldDetailInfo.GetEnabled() == "" {
		fieldDetailInfo.Enabled = constants.EnabledValue
	}

	return &pb.UpdateFieldReq{
		AuthInfo:        req.GetAuthInfo(),
		ProjId:          req.GetProjId(),
		FieldId:         fieldID,
		Operator:        req.GetOperator(), // 传递操作人信息
		FieldUpdateInfo: fieldDetailInfo,
	}
}

// handleFieldUpdate 处理字段更新逻辑
func (x *MetaServicerImpl) handleFieldUpdate(ctx context.Context, req *pb.UpsertFieldReq, existingFieldID int) (*pb.UpsertFieldRsp, error) {
	log.InfoContextf(ctx, "Field exists, updating field_id=%d", existingFieldID)
	rsp := &pb.UpsertFieldRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "字段更新成功",
		},
	}

	// 调用标准更新接口处理所有字段
	updateReq := convertUpsertToUpdateRequest(req, int32(existingFieldID))
	updateRsp, err := x.UpdateField(ctx, updateReq)
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_UPDATE
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err)
		return rsp, err
	}

	// 检查更新接口的返回码
	if updateRsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
		rsp.RetInfo.Code = updateRsp.RetInfo.Code
		rsp.RetInfo.Msg = updateRsp.RetInfo.Msg
		return rsp, nil
	}

	// 复制更新结果
	rsp.RetInfo = updateRsp.RetInfo
	rsp.FieldId = int32(existingFieldID)
	return rsp, nil
}

// handleFieldCreate 处理字段创建逻辑
func (x *MetaServicerImpl) handleFieldCreate(ctx context.Context, req *pb.UpsertFieldReq) (*pb.UpsertFieldRsp, error) {
	log.InfoContextf(ctx, "Field does not exist, creating new field:%+v", req)
	rsp := &pb.UpsertFieldRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "字段创建成功",
		},
	}

	createReq := convertUpsertToCreateRequest(req)
	createRsp, err := x.CreateField(ctx, createReq)
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_UPDATE
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err)
		return rsp, err
	}

	// 检查创建接口的返回码
	if createRsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
		rsp.RetInfo.Code = createRsp.RetInfo.Code
		rsp.RetInfo.Msg = createRsp.RetInfo.Msg
		return rsp, nil
	}

	// 复制创建结果
	rsp.RetInfo = createRsp.RetInfo
	rsp.FieldId = createRsp.FieldId
	return rsp, nil
}

// validateProject 验证项目是否存在
func (x *MetaServicerImpl) validateProject(projID int32) *pb.RetInfo {
	project, err := x.dbDAO.GetProjectByID(int(projID))
	if err != nil || project == nil {
		return &pb.RetInfo{
			Code: pb.EnumErrorCode_INVALID_PARAM,
			Msg: errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM,
				fmt.Errorf("项目ID=%d 不存在，请检查项目是否已创建", projID)),
		}
	}
	return nil
}

// validateDatasetsExistence 验证数据集存在性和归属关系
func (x *MetaServicerImpl) validateDatasetsExistence(projID int32, datasetIDs []int32) ([]*model.Dataset, *pb.RetInfo) {
	var datasets []*model.Dataset
	for _, datasetID := range datasetIDs {
		dataset, err := x.dbDAO.GetDatasetByID(int(datasetID))
		if err != nil || dataset == nil || dataset.ProjID != int(projID) {
			return nil, &pb.RetInfo{
				Code: pb.EnumErrorCode_INVALID_DATA_SET,
				Msg: errors.GetErrMsg(pb.EnumErrorCode_INVALID_DATA_SET,
					fmt.Errorf("数据集ID=%d 不存在或不属于项目ID=%d", datasetID, projID)),
			}
		}
		datasets = append(datasets, dataset)
	}
	return datasets, nil
}

// buildField 根据请求构建字段模型
func (x *MetaServicerImpl) buildField(fieldDetailInfo *pb.FieldDetailInfo, fieldID int) *model.Field {
	// 将数据集ID数组转换为字符串
	datasetIDsStr := utils.Int32Array2String(fieldDetailInfo.GetDatasetIds(), "+")

	enabled := fieldDetailInfo.GetEnabled()
	if enabled == "" {
		enabled = constants.EnabledValue
	}
	required := fieldDetailInfo.GetRequired()
	if required == "" {
		required = constants.DisabledValue
	}
	unique := fieldDetailInfo.GetUnique()
	if unique == "" {
		unique = constants.DisabledValue
	}

	return &model.Field{
		FieldID:              fieldID,
		ProjID:               int(fieldDetailInfo.GetProjId()),
		DatasetIDs:           datasetIDsStr,
		FieldName:            fieldDetailInfo.GetFieldName(),
		InterfaceName:        fieldDetailInfo.GetInterfaceName(),
		Desc:                 fieldDetailInfo.GetDesc(),
		TableType:            int(fieldDetailInfo.GetTableType()),
		Required:             required,
		Unique:               unique,
		ParentFieldID:        int(fieldDetailInfo.GetParentFieldId()),
		LevelInfo:            "", // 根据需要可以处理level_info
		FieldPrimaryFormat:   int(fieldDetailInfo.GetFieldFormatType().GetFieldPrimaryFormat()),
		FieldSecondaryFormat: int(fieldDetailInfo.GetFieldFormatType().GetFieldSecondaryFormat()),
		ValueLibID:           int(fieldDetailInfo.GetValueLibId()),
		ValidationRule:       serializeValidationRule(fieldDetailInfo.GetValidationRule()),
		WriteExample:         fieldDetailInfo.GetWriteExample(),
		Remark:               fieldDetailInfo.GetRemark(),
		Enabled:              enabled,
		CreateTime:           time.Now(),
		ModifyTime:           time.Now(),
	}
}

// updateFieldFromDetailInfo 应用字段详细信息更新，支持更新所有字段
func (x *MetaServicerImpl) updateFieldFromDetailInfo(targetField *model.Field, updateInfo *pb.FieldDetailInfo) {
	// 更新启用状态
	if updateInfo.GetEnabled() == "" {
		targetField.Enabled = constants.EnabledValue
	} else {
		targetField.Enabled = updateInfo.GetEnabled()
	}

	// 更新数据集信息
	if len(updateInfo.GetDatasetIds()) > 0 {
		datasetIDsStr := strings.Trim(strings.Join(strings.Fields(fmt.Sprint(updateInfo.GetDatasetIds())), "+"), "[]")
		targetField.DatasetIDs = datasetIDsStr
	}

	// 更新基本信息
	if updateInfo.GetFieldName() != "" {
		targetField.FieldName = updateInfo.GetFieldName()
	}
	if updateInfo.GetInterfaceName() != "" {
		targetField.InterfaceName = updateInfo.GetInterfaceName()
	}
	if updateInfo.GetDesc() != "" {
		targetField.Desc = updateInfo.GetDesc()
	}
	if updateInfo.GetWriteExample() != "" {
		targetField.WriteExample = updateInfo.GetWriteExample()
	}
	if updateInfo.GetRemark() != "" {
		targetField.Remark = updateInfo.GetRemark()
	}

	// 更新约束信息
	if updateInfo.GetRequired() == "" {
		targetField.Required = constants.DisabledValue
	} else {
		targetField.Required = updateInfo.GetRequired()
	}
	if updateInfo.GetUnique() == "" {
		targetField.Unique = constants.DisabledValue
	} else {
		targetField.Unique = updateInfo.GetUnique()
	}
	targetField.TableType = int(updateInfo.GetTableType())
	if updateInfo.GetValueLibId() != 0 {
		targetField.ValueLibID = int(updateInfo.GetValueLibId())
	}
	if updateInfo.GetValidationRule() != nil {
		targetField.ValidationRule = serializeValidationRule(updateInfo.GetValidationRule())
	}

	// 字段类型信息更新已移除
	if updateInfo.GetParentFieldId() != 0 {
		targetField.ParentFieldID = int(updateInfo.GetParentFieldId())
	}

	// 更新字段格式类型
	if updateInfo.GetFieldFormatType() != nil {
		targetField.FieldPrimaryFormat = int(updateInfo.GetFieldFormatType().GetFieldPrimaryFormat())
		targetField.FieldSecondaryFormat = int(updateInfo.GetFieldFormatType().GetFieldSecondaryFormat())
	}

	// 更新修改时间
	targetField.ModifyTime = time.Now()
}
