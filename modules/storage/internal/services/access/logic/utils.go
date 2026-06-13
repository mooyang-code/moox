package logic

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/cache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/log"
)

var localAdapterService pb.AdapterService

// SetLocalAdapterService 设置本地适配层服务实例（同进程优化）
func SetLocalAdapterService(svc pb.AdapterService) {
	localAdapterService = svc
}

type localAdapterClient struct {
	svc pb.AdapterService
}

func (c *localAdapterClient) GetFieldInfos(ctx context.Context, req *pb.GetFieldInfosReq, _ ...client.Option) (*pb.GetFieldInfosRsp, error) {
	return c.svc.GetFieldInfos(ctx, req)
}

func (c *localAdapterClient) SetFieldInfos(ctx context.Context, req *pb.SetFieldInfosReq, _ ...client.Option) (*pb.SetFieldInfosRsp, error) {
	return c.svc.SetFieldInfos(ctx, req)
}

func (c *localAdapterClient) SearchFieldInfos(ctx context.Context, req *pb.SearchFieldInfosReq, _ ...client.Option) (*pb.SearchFieldInfosRsp, error) {
	return c.svc.SearchFieldInfos(ctx, req)
}

func (c *localAdapterClient) DeleteRows(ctx context.Context, req *pb.DeleteRowsReq, _ ...client.Option) (*pb.DeleteRowsRsp, error) {
	return c.svc.DeleteRows(ctx, req)
}

func (c *localAdapterClient) CreateTable(ctx context.Context, req *pb.CreateTableReq, _ ...client.Option) (*pb.CreateTableRsp, error) {
	return c.svc.CreateTable(ctx, req)
}

func (c *localAdapterClient) DropTable(ctx context.Context, req *pb.DropTableReq, _ ...client.Option) (*pb.DropTableRsp, error) {
	return c.svc.DropTable(ctx, req)
}

func (c *localAdapterClient) CheckTable(ctx context.Context, req *pb.CheckTableReq, _ ...client.Option) (*pb.CheckTableRsp, error) {
	return c.svc.CheckTable(ctx, req)
}

// BuildFieldMappings 从字段列表构建映射关系
// 参数:
//   - fields: 字段定义列表
//   - includeFieldMap: 是否同时构建字段名到字段结构的映射
//
// 返回值:
//   - nameToID: 字段名到ID的映射
//   - idToName: ID到字段名的映射
//   - fieldMap: 字段名到字段结构的映射(如果includeFieldMap为true)
func BuildFieldMappings(fields []*cache.Field, includeFieldMap bool,
) (map[string]uint32, map[uint32]string, map[string]*cache.Field) {
	nameToID := make(map[string]uint32)
	idToName := make(map[uint32]string)
	var fieldMap map[string]*cache.Field

	if includeFieldMap {
		fieldMap = make(map[string]*cache.Field)
	}

	for _, field := range fields {
		nameToID[field.InterfaceName] = uint32(field.FieldID)
		idToName[uint32(field.FieldID)] = field.InterfaceName

		if includeFieldMap {
			fieldMap[field.InterfaceName] = field
		}
	}
	return nameToID, idToName, fieldMap
}

// BuildNameToIDMap 根据字段ID到名称的映射，构建反向映射
// 参数:
//   - idToName: ID到字段名的映射
//
// 返回值:
//   - nameToID: 字段名到ID的映射
func BuildNameToIDMap(idToName map[uint32]string) map[string]uint32 {
	nameToID := make(map[string]uint32)
	for id, name := range idToName {
		nameToID[name] = id
	}
	return nameToID
}

// extractValueFromUpdateField 从UpdateField中提取用于验证的字符串值
// 参数:
//   - updateField: 更新的字段值
//
// 返回值:
//   - string: 提取的字符串值
//   - error: 提取失败时返回错误
func extractValueFromUpdateField(updateField *pb.UpdateField) (string, error) {
	switch updateField.GetFieldType() {
	case pb.EnumFieldType_STR_FIELD:
		if updateField.SimpleValue != nil {
			return updateField.SimpleValue.GetStr(), nil
		}
	case pb.EnumFieldType_INT_FIELD:
		if updateField.SimpleValue != nil {
			return fmt.Sprintf("%d", updateField.SimpleValue.GetInt()), nil
		}
	case pb.EnumFieldType_FLOAT_FIELD:
		if updateField.SimpleValue != nil {
			return fmt.Sprintf("%f", updateField.SimpleValue.GetFloat()), nil
		}
	case pb.EnumFieldType_TIME_FIELD:
		if updateField.SimpleValue != nil {
			return updateField.SimpleValue.GetTime(), nil
		}
	case pb.EnumFieldType_INT_VEC_FIELD:
		if updateField.SimpleValue != nil && updateField.SimpleValue.GetIntList() != nil {
			var values []string
			for _, num := range updateField.SimpleValue.GetIntList().Values {
				values = append(values, fmt.Sprintf("%d", num))
			}
			return strings.Join(values, ","), nil
		}
	case pb.EnumFieldType_SET_FIELD:
		if updateField.SimpleValue != nil && updateField.SimpleValue.GetStrList() != nil {
			return strings.Join(updateField.SimpleValue.GetStrList().Values, ","), nil
		}
	case pb.EnumFieldType_MAP_KV_FIELD:
		return extractMapValue(updateField)
	default:
		return "", fmt.Errorf("不支持的字段类型: %v", updateField.GetFieldType())
	}
	return "", nil
}

// extractMapValue 从Map类型字段中提取值
func extractMapValue(updateField *pb.UpdateField) (string, error) {
	if updateField.MapValue == nil {
		return "", nil
	}

	var pairs []string
	for k, val := range updateField.MapValue.Entries {
		var valueStr string
		if val.Value != nil {
			switch val.Type {
			case pb.EnumFieldType_STR_FIELD:
				valueStr = val.Value.GetStr()
			case pb.EnumFieldType_INT_FIELD:
				valueStr = fmt.Sprintf("%d", val.Value.GetInt())
			case pb.EnumFieldType_FLOAT_FIELD:
				valueStr = fmt.Sprintf("%f", val.Value.GetFloat())
			case pb.EnumFieldType_TIME_FIELD:
				valueStr = val.Value.GetTime()
			default:
				valueStr = fmt.Sprintf("%v", val.Value)
			}
		}
		pairs = append(pairs, fmt.Sprintf("%s=%s", k, valueStr))
	}
	return strings.Join(pairs, ","), nil
}

// ValidateFieldValue 根据字段的验证规则校验字段值的合法性
// 参数:
//   - field: 字段定义，包含验证规则
//   - updateField: 更新的字段值
//
// 返回值:
//   - error: 校验失败时返回错误，校验成功返回nil
func ValidateFieldValue(field *cache.Field, updateField *pb.UpdateField) error {
	if field == nil || field.ValidationRule == "" {
		return nil
	}

	valueToValidate, err := extractValueFromUpdateField(updateField)
	if err != nil {
		return err
	}

	if valueToValidate == "" && field.Required == constants.EnabledValue {
		return fmt.Errorf("字段'%s'为必填项", field.InterfaceName)
	}

	if valueToValidate == "" {
		return nil
	}

	validationRule := parseValidationRuleFromJSON(field.ValidationRule)
	if validationRule == nil {
		return validateWithRegex(field, valueToValidate)
	}

	return validateByRule(validationRule, field.InterfaceName, valueToValidate)
}

// validateWithRegex 使用正则表达式进行验证（向后兼容）
func validateWithRegex(field *cache.Field, value string) error {
	re, regexErr := regexp.Compile(field.ValidationRule)
	if regexErr != nil {
		return fmt.Errorf("字段'%s'的验证规则无效: JSON解析失败, 正则表达式编译失败: %v",
			field.InterfaceName, regexErr)
	}
	if !re.MatchString(value) {
		return fmt.Errorf("字段'%s'的值'%s'不符合验证规则'%s'",
			field.InterfaceName, value, field.ValidationRule)
	}
	return nil
}

// parseValidationRuleFromJSON 将JSON字符串反序列化为ValidationRule结构
// 只支持标准下划线格式: {"string_rule":{"min_length":3,"max_length":20}}
func parseValidationRuleFromJSON(ruleStr string) *pb.ValidationRule {
	if ruleStr == "" {
		return nil
	}

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
			return &rule
		}
	}
	if integerRule, ok := ruleMap["integer_rule"]; ok {
		var ir pb.IntegerRule
		if err := json.Unmarshal(integerRule, &ir); err == nil {
			rule.Rule = &pb.ValidationRule_IntegerRule{IntegerRule: &ir}
			return &rule
		}
	}
	if doubleRule, ok := ruleMap["double_rule"]; ok {
		var dr pb.DoubleRule
		if err := json.Unmarshal(doubleRule, &dr); err == nil {
			rule.Rule = &pb.ValidationRule_DoubleRule{DoubleRule: &dr}
			return &rule
		}
	}
	if optionRule, ok := ruleMap["option_rule"]; ok {
		var or pb.OptionRule
		if err := json.Unmarshal(optionRule, &or); err == nil {
			rule.Rule = &pb.ValidationRule_OptionRule{OptionRule: &or}
			return &rule
		}
	}

	log.Errorf("ValidationRule格式错误，不支持的格式: %s", ruleStr)
	return nil
}

// validateByRule 根据ValidationRule结构进行字段值验证
func validateByRule(rule *pb.ValidationRule, fieldName, value string) error {
	if rule == nil {
		return nil
	}

	switch r := rule.Rule.(type) {
	case *pb.ValidationRule_StringRule:
		return validateStringRule(r.StringRule, fieldName, value)
	case *pb.ValidationRule_IntegerRule:
		return validateIntegerRule(r.IntegerRule, fieldName, value)
	case *pb.ValidationRule_DoubleRule:
		return validateDoubleRule(r.DoubleRule, fieldName, value)
	case *pb.ValidationRule_OptionRule:
		return validateOptionRule(r.OptionRule, fieldName, value)
	default:
		return fmt.Errorf("字段'%s'的验证规则类型不支持", fieldName)
	}
}

// validateStringRule 验证字符串规则
func validateStringRule(rule *pb.StringRule, fieldName, value string) error {
	if rule == nil {
		return nil
	}

	// 检查长度限制
	if err := validateStringLength(rule, fieldName, value); err != nil {
		return err
	}

	// 检查固定值
	if rule.Const != nil && value != *rule.Const {
		return fmt.Errorf("字段'%s'的值'%s'必须等于'%s'", fieldName, value, *rule.Const)
	}

	// 检查正则表达式模式
	if err := validateStringPatterns(rule, fieldName, value); err != nil {
		return err
	}

	// 检查字符串位置相关验证
	if err := validateStringPosition(rule, fieldName, value); err != nil {
		return err
	}

	// 检查枚举值
	if err := validateStringEnumeration(rule, fieldName, value); err != nil {
		return err
	}

	// 检查格式（主要用于时间格式）
	if rule.Format != nil {
		if err := validateFormat(*rule.Format, fieldName, value); err != nil {
			return err
		}
	}
	return nil
}

// validateStringLength 验证字符串长度限制
func validateStringLength(rule *pb.StringRule, fieldName, value string) error {
	valueLen := int64(len(value))

	if rule.MinLength != nil && valueLen < *rule.MinLength {
		return fmt.Errorf("字段'%s'的值'%s'长度不能少于%d个字符", fieldName, value, *rule.MinLength)
	}
	if rule.MaxLength != nil && valueLen > *rule.MaxLength {
		return fmt.Errorf("字段'%s'的值'%s'长度不能超过%d个字符", fieldName, value, *rule.MaxLength)
	}
	if rule.Length != nil && valueLen != *rule.Length {
		return fmt.Errorf("字段'%s'的值'%s'长度必须为%d个字符", fieldName, value, *rule.Length)
	}
	return nil
}

// validateStringPatterns 验证字符串正则表达式模式
func validateStringPatterns(rule *pb.StringRule, fieldName, value string) error {
	// 检查正则表达式模式
	if rule.Pattern != nil {
		re, err := regexp.Compile(*rule.Pattern)
		if err != nil {
			return fmt.Errorf("字段'%s'的正则表达式模式无效: %v", fieldName, err)
		}
		if !re.MatchString(value) {
			return fmt.Errorf("字段'%s'的值'%s'不符合模式'%s'", fieldName, value, *rule.Pattern)
		}
	}

	// 检查反向正则表达式
	if rule.NotPattern != nil {
		re, err := regexp.Compile(*rule.NotPattern)
		if err != nil {
			return fmt.Errorf("字段'%s'的反向正则表达式模式无效: %v", fieldName, err)
		}
		if re.MatchString(value) {
			return fmt.Errorf("字段'%s'的值'%s'不能匹配模式'%s'", fieldName, value, *rule.NotPattern)
		}
	}
	return nil
}

// validateStringPosition 验证字符串位置相关规则
func validateStringPosition(rule *pb.StringRule, fieldName, value string) error {
	// 检查前缀
	if rule.Prefix != nil && !strings.HasPrefix(value, *rule.Prefix) {
		return fmt.Errorf("字段'%s'的值'%s'必须以'%s'开头", fieldName, value, *rule.Prefix)
	}

	// 检查后缀
	if rule.Suffix != nil && !strings.HasSuffix(value, *rule.Suffix) {
		return fmt.Errorf("字段'%s'的值'%s'必须以'%s'结尾", fieldName, value, *rule.Suffix)
	}

	// 检查包含
	if rule.Contains != nil && !strings.Contains(value, *rule.Contains) {
		return fmt.Errorf("字段'%s'的值'%s'必须包含'%s'", fieldName, value, *rule.Contains)
	}

	// 检查不包含
	if rule.NotContains != nil && strings.Contains(value, *rule.NotContains) {
		return fmt.Errorf("字段'%s'的值'%s'不能包含'%s'", fieldName, value, *rule.NotContains)
	}
	return nil
}

// validateStringEnumeration 验证字符串枚举值
func validateStringEnumeration(rule *pb.StringRule, fieldName, value string) error {
	// 检查枚举值
	if len(rule.In) > 0 {
		found := false
		for _, allowedValue := range rule.In {
			if value == allowedValue {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("字段'%s'的值'%s'必须是以下值之一: %v", fieldName, value, rule.In)
		}
	}

	// 检查排除值
	if len(rule.NotIn) > 0 {
		for _, excludedValue := range rule.NotIn {
			if value == excludedValue {
				return fmt.Errorf("字段'%s'的值'%s'不能是以下值之一: %v", fieldName, value, rule.NotIn)
			}
		}
	}
	return nil
}

// validateIntegerRule 验证整数规则
func validateIntegerRule(rule *pb.IntegerRule, fieldName, value string) error {
	if rule == nil {
		return nil
	}

	intValue, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fmt.Errorf("字段'%s'的值'%s'不是有效的整数", fieldName, value)
	}

	// 检查各种数值限制
	if rule.Min != nil && intValue < *rule.Min {
		return fmt.Errorf("字段'%s'的值%d不能小于%d", fieldName, intValue, *rule.Min)
	}
	if rule.Max != nil && intValue > *rule.Max {
		return fmt.Errorf("字段'%s'的值%d不能大于%d", fieldName, intValue, *rule.Max)
	}
	if rule.Const != nil && intValue != *rule.Const {
		return fmt.Errorf("字段'%s'的值%d必须等于%d", fieldName, intValue, *rule.Const)
	}
	if rule.Lt != nil && intValue >= *rule.Lt {
		return fmt.Errorf("字段'%s'的值%d必须小于%d", fieldName, intValue, *rule.Lt)
	}
	if rule.Lte != nil && intValue > *rule.Lte {
		return fmt.Errorf("字段'%s'的值%d必须小于等于%d", fieldName, intValue, *rule.Lte)
	}
	if rule.Gt != nil && intValue <= *rule.Gt {
		return fmt.Errorf("字段'%s'的值%d必须大于%d", fieldName, intValue, *rule.Gt)
	}
	if rule.Gte != nil && intValue < *rule.Gte {
		return fmt.Errorf("字段'%s'的值%d必须大于等于%d", fieldName, intValue, *rule.Gte)
	}

	// 检查枚举值
	if len(rule.In) > 0 {
		found := false
		for _, allowedValue := range rule.In {
			if intValue == allowedValue {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("字段'%s'的值%d必须是以下值之一: %v", fieldName, intValue, rule.In)
		}
	}

	// 检查排除值
	if len(rule.NotIn) > 0 {
		for _, excludedValue := range rule.NotIn {
			if intValue == excludedValue {
				return fmt.Errorf("字段'%s'的值%d不能是以下值之一: %v", fieldName, intValue, rule.NotIn)
			}
		}
	}
	return nil
}

// validateDoubleRule 验证浮点数规则
func validateDoubleRule(rule *pb.DoubleRule, fieldName, value string) error {
	if rule == nil {
		return nil
	}

	floatValue, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fmt.Errorf("字段'%s'的值'%s'不是有效的浮点数", fieldName, value)
	}

	// 检查各种数值限制
	if rule.Min != nil && floatValue < *rule.Min {
		return fmt.Errorf("字段'%s'的值%f不能小于%f", fieldName, floatValue, *rule.Min)
	}
	if rule.Max != nil && floatValue > *rule.Max {
		return fmt.Errorf("字段'%s'的值%f不能大于%f", fieldName, floatValue, *rule.Max)
	}
	if rule.Const != nil && floatValue != *rule.Const {
		return fmt.Errorf("字段'%s'的值%f必须等于%f", fieldName, floatValue, *rule.Const)
	}
	if rule.Lt != nil && floatValue >= *rule.Lt {
		return fmt.Errorf("字段'%s'的值%f必须小于%f", fieldName, floatValue, *rule.Lt)
	}
	if rule.Lte != nil && floatValue > *rule.Lte {
		return fmt.Errorf("字段'%s'的值%f必须小于等于%f", fieldName, floatValue, *rule.Lte)
	}
	if rule.Gt != nil && floatValue <= *rule.Gt {
		return fmt.Errorf("字段'%s'的值%f必须大于%f", fieldName, floatValue, *rule.Gt)
	}
	if rule.Gte != nil && floatValue < *rule.Gte {
		return fmt.Errorf("字段'%s'的值%f必须大于等于%f", fieldName, floatValue, *rule.Gte)
	}

	// 检查枚举值
	if len(rule.In) > 0 {
		found := false
		for _, allowedValue := range rule.In {
			if floatValue == allowedValue {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("字段'%s'的值%f必须是以下值之一: %v", fieldName, floatValue, rule.In)
		}
	}

	// 检查排除值
	if len(rule.NotIn) > 0 {
		for _, excludedValue := range rule.NotIn {
			if floatValue == excludedValue {
				return fmt.Errorf("字段'%s'的值%f不能是以下值之一: %v", fieldName, floatValue, rule.NotIn)
			}
		}
	}
	return nil
}

// validateOptionRule 验证选项规则
func validateOptionRule(rule *pb.OptionRule, fieldName, value string) error {
	if rule == nil {
		return nil
	}
	// TODO: 实现值库验证逻辑
	return nil
}

// validateFormat 验证格式
func validateFormat(format, fieldName, value string) error {
	switch format {
	case "YYYY-MM-DD HH:mm:ss":
		_, err := time.Parse("2006-01-02 15:04:05", value)
		if err != nil {
			return fmt.Errorf("字段'%s'的值'%s'不符合时间格式'YYYY-MM-DD HH:mm:ss'", fieldName, value)
		}
	case "YYYY-MM-DD":
		_, err := time.Parse("2006-01-02", value)
		if err != nil {
			return fmt.Errorf("字段'%s'的值'%s'不符合日期格式'YYYY-MM-DD'", fieldName, value)
		}
	case "HH:mm:ss":
		_, err := time.Parse("15:04:05", value)
		if err != nil {
			return fmt.Errorf("字段'%s'的值'%s'不符合时间格式'HH:mm:ss'", fieldName, value)
		}
	case "YYYY-MM":
		_, err := time.Parse("2006-01", value)
		if err != nil {
			return fmt.Errorf("字段'%s'的值'%s'不符合月份格式'YYYY-MM'", fieldName, value)
		}
	default:
		// 对于不支持的格式，记录警告但不阻止验证通过
		log.Warnf("字段'%s'使用了未明确支持的格式'%s'，将跳过格式验证", fieldName, format)
		return nil
	}
	return nil
}

// 将字段名转换为字段ID - 使用BuildFieldMappings实现
func convertNamesToIDs(fieldNames []string, fields []*cache.Field) ([]uint32, map[uint32]string, error) {
	// 构建字段映射
	nameToID, idToName, _ := BuildFieldMappings(fields, false)
	var fieldIDs []uint32

	// 转换字段名到ID
	for _, name := range fieldNames {
		id, ok := nameToID[name]
		if !ok {
			// 如果找不到对应的字段ID，返回错误
			return nil, nil, fmt.Errorf("field name not found: %s", name)
		}
		fieldIDs = append(fieldIDs, id)
	}
	return fieldIDs, idToName, nil
}

// 转换MapKeys中的字段名为字段ID - 使用BuildNameToIDMap实现
func convertMapKeys(mapKeys map[string]*pb.KeyList, fieldID2Name map[uint32]string) (map[uint32]*pb.KeyList, error) {
	if len(mapKeys) == 0 {
		return nil, nil
	}

	result := make(map[uint32]*pb.KeyList)

	// 使用公共函数构建反向映射
	nameToID := BuildNameToIDMap(fieldID2Name)

	// 转换字段名到ID
	for fieldName, keyList := range mapKeys {
		id, ok := nameToID[fieldName]
		if !ok {
			return nil, fmt.Errorf("field name not found in map_keys: %s", fieldName)
		}
		result[id] = keyList
	}
	return result, nil
}

// ValidateObjectID 验证ObjectID的合法性
// 规则：
//   - 只允许大小写字母、数字、下划线、横线
//   - 开头不允许下划线和横线
//   - 总字符在50个字符以内
//
// 参数:
//   - objectID: 待验证的ObjectID字符串
//
// 返回值:
//   - error: 验证失败时返回错误，验证成功返回nil
func ValidateObjectID(objectID string) error {
	if objectID == "" {
		return fmt.Errorf("ObjectID不能为空")
	}

	// 检查长度限制
	if len(objectID) > 50 {
		return fmt.Errorf("ObjectID长度不能超过50个字符，当前长度: %d", len(objectID))
	}

	// 检查开头字符：不能是下划线或横线
	firstChar := objectID[0]
	if firstChar == '_' || firstChar == '-' {
		return fmt.Errorf("ObjectID不能以下划线或横线开头")
	}
	return nil
}

// ValidateFreq 验证频率参数格式是否正确
// 格式：数值x频率单位（例如：1m）
// 频率单位的合法值如下：
// 秒-小写s
// 分钟-小写m
// 小时-大写H
// 天-大写D
// 周-大写W
// 月-大写M（区别于分钟）
// 年-大写Y
func ValidateFreq(freq string) error {
	if freq == "" {
		return fmt.Errorf("频率不能为空")
	}

	// 使用正则表达式校验格式
	pattern := `^([1-9]\d*)([smHDWMY])$`
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("编译正则表达式失败: %v", err)
	}
	matches := re.FindStringSubmatch(freq)
	if matches == nil {
		return fmt.Errorf("频率格式不正确，应为'数值x频率单位'，例如: 1s, 1m, 4H, 1D, 1W, 1M, 1Y")
	}

	// 提取数值和单位
	value := matches[1]
	unit := matches[2]

	// 校验单位
	switch unit {
	case "s": // 秒
	case "m": // 分钟
	case "H": // 小时
	case "D": // 天
	case "W": // 周
	case "M": // 月
	case "Y": // 年
	default:
		return fmt.Errorf("不支持的频率单位: %s", unit)
	}

	// 校验数值
	numValue, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("频率数值不是有效整数: %s", value)
	}
	if numValue <= 0 {
		return fmt.Errorf("频率数值必须大于0: %d", numValue)
	}
	return nil
}

// CalculateEndTime 根据开始时间、频率和周期数计算结束时间
// 参数:
//   - startTime: 开始时间字符串，格式 YYYY-MM-DD HH:MM:SS
//   - freq: 频率字符串，格式 数值+频率单位，例如 1s, 1m, 4H, 1D 等
//   - periods: 周期数
//
// 返回值:
//   - endTime: 计算后的结束时间字符串，格式 YYYY-MM-DD HH:MM:SS
//   - error: 计算过程中的错误
func CalculateEndTime(startTime, freq string, periods int32) (string, error) {
	// 解析开始时间
	start, err := time.Parse("2006-01-02 15:04:05", startTime)
	if err != nil {
		return "", fmt.Errorf("解析开始时间失败: %v", err)
	}

	// 验证并解析频率
	if err := ValidateFreq(freq); err != nil {
		return "", fmt.Errorf("频率格式无效: %v", err)
	}

	// 使用正则表达式提取频率中的数值和单位
	re := regexp.MustCompile(`^([1-9]\d*)([smHDWMY])$`)
	matches := re.FindStringSubmatch(freq)
	if len(matches) != 3 {
		return "", fmt.Errorf("无法解析频率: %s", freq)
	}

	// 提取数值和单位
	value, _ := strconv.Atoi(matches[1]) // 错误已在ValidateFreq中检查
	unit := matches[2]

	// 计算单个周期的时间增量
	var duration time.Duration
	var end time.Time

	// 根据频率单位计算结束时间
	switch unit {
	case "s": // 秒
		duration = time.Duration(value) * time.Second
		end = start.Add(duration * time.Duration(periods))
	case "m": // 分钟
		duration = time.Duration(value) * time.Minute
		end = start.Add(duration * time.Duration(periods))
	case "H": // 小时
		duration = time.Duration(value) * time.Hour
		end = start.Add(duration * time.Duration(periods))
	case "D": // 天
		end = start.AddDate(0, 0, int(periods)*value)
	case "W": // 周
		end = start.AddDate(0, 0, int(periods)*value*7)
	case "M": // 月
		end = start.AddDate(0, int(periods)*value, 0)
	case "Y": // 年
		end = start.AddDate(int(periods)*value, 0, 0)
	default:
		return "", fmt.Errorf("不支持的频率单位: %s", unit)
	}

	// 格式化结束时间并返回
	return end.Format("2006-01-02 15:04:05"), nil
}

// BuildTimeInterval 构建时间区间
// 参数:
//   - ctx: 上下文
//   - timeRange: 时间范围参数
//   - freq: 频率字符串，格式 数值+频率单位，用于处理周期数
//
// 返回值:
//   - timeInterval: 构建好的时间区间
func BuildTimeInterval(ctx context.Context, timeRange *pb.TimeRange, freq string) *pb.TimeInterval {
	if timeRange == nil {
		return nil
	}

	log.DebugContextf(ctx, "DEBUG: BuildTimeInterval - 输入 timeRange.Start=%s, freq=%s", timeRange.Start, freq)

	// 初始化时间区间，设置开始时间
	timeInterval := &pb.TimeInterval{
		Start: timeRange.Start,
	}

	// 根据范围类型设置结束时间
	switch endType := timeRange.RangeType.(type) {
	case *pb.TimeRange_End:
		// 直接使用指定的结束时间
		log.DebugContextf(ctx, "DEBUG: BuildTimeInterval - 使用TimeRange_End分支, End=%s", endType.End)
		timeInterval.End = endType.End
	case *pb.TimeRange_Periods:
		// 使用周期数计算结束时间
		log.DebugContextf(ctx, "DEBUG: BuildTimeInterval - 使用TimeRange_Periods分支, Periods=%d, freq=%s", endType.Periods, freq)
		if freq != "" {
			endTime, err := CalculateEndTime(timeRange.Start, freq, endType.Periods)
			if err != nil {
				log.WarnContextf(ctx, "计算结束时间失败: %v", err)
				return nil
			}
			timeInterval.End = endTime
			log.DebugContextf(ctx, "根据周期数计算的结束时间: %s", endTime)
		} else {
			log.WarnContextf(ctx, "缺少频率信息，无法计算周期结束时间: periods=%d", endType.Periods)
		}
	default:
		log.WarnContextf(ctx, "DEBUG: BuildTimeInterval - 未知的RangeType类型: %T", endType)
	}

	log.DebugContextf(ctx, "DEBUG: BuildTimeInterval - 最终输出 Start=%s, End=%s", timeInterval.Start, timeInterval.End)
	return timeInterval
}

// getDataTypeFromDataset 根据数据集ID获取对应的数据类型枚举
func getDataTypeFromDataset(datasetID int) pb.EnumDataTypeCategory {
	// 从缓存中获取数据集信息
	dataset := cache.GetDatasetInfo(datasetID)
	if dataset == nil {
		// 如果找不到数据集信息，默认返回静态数据类型作为保底
		log.Warnf("未找到数据集 %d 的信息，默认使用静态数据类型", datasetID)
		return pb.EnumDataTypeCategory_STATIC_DATA_TYPE
	}

	// 根据数据集中的DataType字段判断类型
	switch dataset.DataType {
	case int(pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE):
		return pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE
	case int(pb.EnumDataTypeCategory_STATIC_DATA_TYPE):
		return pb.EnumDataTypeCategory_STATIC_DATA_TYPE
	default:
		// 对于未知类型，记录警告并返回静态数据类型作为默认值
		log.Warnf("数据集 %d 的数据类型 %d 未知，默认使用静态数据类型", datasetID, dataset.DataType)
		return pb.EnumDataTypeCategory_STATIC_DATA_TYPE
	}
}

// GetDataRoute 获取数据集的存储路由(返回存储实体ID)
func GetDataRoute(datasetID int32, objectID string) (int, error) {
	entityID := cache.GetObjectRouteByID(int(datasetID), objectID)
	if entityID == 0 {
		return 0, fmt.Errorf("未找到数据集 %d 对象 %s 的合法存储实体", datasetID, objectID)
	}
	return entityID, nil
}

// CreateDynamicAdapterClient 根据实体ID创建动态适配层客户端
func CreateDynamicAdapterClient(entityID int) pb.AdapterClientProxy {
	// 根据实体ID获取存储实体信息
	entityInfo := cache.GetStorageEntityInfo(entityID)
	if entityInfo == nil {
		log.Errorf("存储实体 %d 不存在", entityID)
		return nil
	}

	// 根据实体信息动态创建adapterClient
	target := entityInfo.EntitySrvConn
	if isLocalAdapterTarget(target) && localAdapterService != nil {
		log.Infof("Use local adapter client for entity %d target %s", entityID, target)
		return &localAdapterClient{svc: localAdapterService}
	}

	opt := []client.Option{
		client.WithServiceName("trpc.storage.adapter.Adapter"),
		client.WithDisableServiceRouter(),
		client.WithTarget(target),
	}
	return pb.NewAdapterClientProxy(opt...)
}

func isLocalAdapterTarget(target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}

	host := ""
	if strings.Contains(target, "://") {
		u, err := url.Parse(target)
		if err == nil {
			host = u.Hostname()
		}
	}
	if host == "" {
		if h, _, err := net.SplitHostPort(target); err == nil {
			host = h
		} else {
			host = target
		}
	}
	host = strings.Trim(host, "[]")
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

// ConvertToFieldValue 将UpdateField转换为FieldValue
func ConvertToFieldValue(fieldName string, updateField *pb.UpdateField) *pb.FieldValue {
	if updateField == nil {
		return nil
	}

	// 创建FieldValue基本结构
	fieldValue := &pb.FieldValue{
		FieldKey:  fieldName,
		FieldType: updateField.FieldType,
	}

	// 根据不同类型处理值
	if updateField.SimpleValue != nil {
		fieldValue.SimpleValue = updateField.SimpleValue
	} else if updateField.MapValue != nil {
		fieldValue.MapValue = updateField.MapValue
	}
	return fieldValue
}
