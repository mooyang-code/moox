package utils

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/cache"
)

// StringArray2Int32Array 将以特定分隔符分隔的字符串转换为int32数组
func StringArray2Int32Array(str string, separator string) []int32 {
	if str == "" {
		return []int32{}
	}

	strArray := strings.Split(str, separator)
	result := make([]int32, len(strArray))
	for i, s := range strArray {
		if id, err := strconv.Atoi(s); err == nil {
			result[i] = int32(id)
		}
	}
	return result
}

// Int32Array2String 将int32数组转换为以特定分隔符分隔的字符串
func Int32Array2String(arr []int32, separator string) string {
	if len(arr) == 0 {
		return ""
	}

	strArray := make([]string, len(arr))
	for i, id := range arr {
		strArray[i] = strconv.Itoa(int(id))
	}
	return strings.Join(strArray, separator)
}

// StringToTime 将字符串转换为time.Time，支持多种时间格式
func StringToTime(timeStr string) (time.Time, error) {
	// 尝试多种常用时间格式
	formats := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006/01/02 15:04:05",
		"2006-01-02",
		"2006/01/02",
	}

	var err error
	for _, format := range formats {
		t, e := time.Parse(format, timeStr)
		if e == nil {
			return t, nil
		}
		err = e
	}
	return time.Time{}, err
}

// StringPtrToTimePtr 将字符串指针转换为time.Time指针
func StringPtrToTimePtr(timeStrPtr *string) (*time.Time, error) {
	if timeStrPtr == nil {
		return nil, nil
	}

	t, err := StringToTime(*timeStrPtr)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// NormalizeTimeString 将各种时间格式的字符串统一转换为标准格式 "2006-01-02 15:04:05"
// 支持的输入格式：RFC3339、"2006-01-02 15:04:05"、以及其他常见格式
func NormalizeTimeString(timeStr string) string {
	if timeStr == "" {
		return timeStr
	}

	timeStr = strings.TrimSpace(timeStr)

	// 如果已经是目标格式，直接返回
	if _, err := time.Parse("2006-01-02 15:04:05", timeStr); err == nil {
		return timeStr
	}

	// 尝试解析 RFC3339 格式，转换为目标格式
	if t, err := time.Parse(time.RFC3339, timeStr); err == nil {
		return t.UTC().Format("2006-01-02 15:04:05")
	}

	// 尝试使用 StringToTime 函数解析其他格式
	if t, err := StringToTime(timeStr); err == nil {
		return t.UTC().Format("2006-01-02 15:04:05")
	}

	// 无法解析，返回原字符串
	return timeStr
}

// GetFormattedCurrentTime 获取当前时间并格式化为指定格式的字符串
func GetFormattedCurrentTime(format string) string {
	if format == "" {
		format = "2006-01-02 15:04:05" // 默认使用普通时间格式
	}
	return time.Now().Format(format)
}

// GetCurrentTimeStandard 获取当前时间并格式化为标准格式的字符串
func GetCurrentTimeStandard() string {
	return GetFormattedCurrentTime("2006-01-02 15:04:05")
}

// GenObjectTableID 生成数据对象表ID
// 命名规则：t_object_datasetID
func GenObjectTableID(datasetID int32) string {
	return fmt.Sprintf("t_object_%d", datasetID)
}

// EncodeSymbol 对标识符进行编码，避免SQL中的非法字符
// 规则：允许 A-Z a-z 0-9 _，其他字符按UTF-8字节编码为 _xHH_ 格式
func EncodeSymbol(sym string) string {
	var b strings.Builder
	for _, r := range sym {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') ||
			(r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			// 将rune转换为UTF-8字节序列
			utf8Bytes := []byte(string(r))
			for _, byteVal := range utf8Bytes {
				b.WriteString(fmt.Sprintf("_x%02X_", byteVal))
			}
		}
	}
	return b.String()
}

// EscapeTableIDDash 只转义TableID中的横线字符，其他字符保持不变
// 规则：将横线 '-' 转换为 '_x2D_'，其他字符不变
func EscapeTableIDDash(tableID string) string {
	return strings.ReplaceAll(tableID, "-", "_x2D_")
}

// UnescapeTableIDDash 反转义TableID中的横线字符
// 规则：将 '_x2D_' 转换回横线 '-'
func UnescapeTableIDDash(escapedTableID string) string {
	return strings.ReplaceAll(escapedTableID, "_x2D_", "-")
}

// DecodeSymbol 对编码的标识符进行解码
func DecodeSymbol(encoded string) (string, error) {
	re := regexp.MustCompile(`_x([0-9A-Fa-f]{2})_`)
	var result strings.Builder
	var bytes []byte
	lastEnd := 0

	matches := re.FindAllStringSubmatchIndex(encoded, -1)

	for i, match := range matches {
		// 如果当前匹配不是紧接着上一个匹配，说明有普通字符
		if match[0] > lastEnd {
			// 先处理之前收集的字节
			if len(bytes) > 0 {
				result.WriteString(string(bytes))
				bytes = nil
			}
			// 添加普通字符
			result.WriteString(encoded[lastEnd:match[0]])
		}

		// 解析十六进制字节
		hexPart := encoded[match[2]:match[3]]
		v, err := strconv.ParseUint(hexPart, 16, 8)
		if err != nil {
			return "", err
		}
		bytes = append(bytes, byte(v))

		// 检查下一个匹配是否紧接着当前匹配
		isLastMatch := i == len(matches)-1
		nextMatchIsAdjacent := !isLastMatch && matches[i+1][0] == match[1]

		// 如果这是最后一个匹配，或者下一个匹配不相邻，则处理收集的字节
		if isLastMatch || !nextMatchIsAdjacent {
			result.WriteString(string(bytes))
			bytes = nil
		}

		lastEnd = match[1]
	}

	// 添加剩余的普通字符
	if lastEnd < len(encoded) {
		result.WriteString(encoded[lastEnd:])
	}

	return result.String(), nil
}

// GenDataTableID 生成数据详情表ID（带对象ID和频率）
// 命名规则：t_data_datasetID_encodedObjectID_freq
// 注意：如果datasetID为0，或objectID、freq为空，则不追加前下划线到表名中
// objectID会被编码以避免SQL中的非法字符
func GenDataTableID(datasetID int32, objectID string, freq string) string {
	result := "t_data"
	if datasetID != 0 {
		result += fmt.Sprintf("_%d", datasetID)
	}

	if objectID != "" {
		result += fmt.Sprintf("_%s", EncodeSymbol(objectID))
	}

	if freq != "" {
		result += fmt.Sprintf("_%s", freq)
	}
	return result
}

// ParseDatasetIDFromTableID 从表ID中解析出数据集ID
// 支持格式：t_data_datasetID_... 或 t_object_datasetID
// 返回：datasetID, error
func ParseDatasetIDFromTableID(tableID string) (int32, error) {
	// 使用正则表达式匹配表ID格式并提取datasetID
	// 匹配 t_data 或 t_object 后跟可选的 _datasetID (支持负数)
	re := regexp.MustCompile(`^t_(data|object)(?:_(-?\d+)(?:_.*)?)?$`)
	matches := re.FindStringSubmatch(tableID)
	if matches == nil {
		return 0, fmt.Errorf("invalid table ID format: %s, expected t_data or t_object prefix", tableID)
	}

	// matches[1] 是 "data" 或 "object"
	// matches[2] 是 datasetID (可能为空)
	if len(matches) < 3 || matches[2] == "" {
		// 没有datasetID部分，返回0
		return 0, nil
	}

	// 解析datasetID
	datasetID, err := strconv.ParseInt(matches[2], 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid datasetID in table ID %s: %v", tableID, err)
	}
	return int32(datasetID), nil
}

// ParseTableProjectID 从表ID中解析项目ID（基于缓存映射）
func ParseTableProjectID(tableID string) (int, error) {
	return cache.GetProjectIDByTableID(tableID)
}

// ParseTableDatasetID 从表ID中解析数据集ID
func ParseTableDatasetID(tableID string) (int32, error) {
	return ParseDatasetIDFromTableID(tableID)
}

// ParseDataTableParts 从数据表ID中解析数据集ID、对象ID、频率
// 支持格式：t_data_datasetID_encodedObjectID_freq 或 t_data_datasetID_encodedObjectID
func ParseDataTableParts(tableID string) (int32, string, string, error) {
	if !strings.HasPrefix(tableID, "t_data_") {
		return 0, "", "", fmt.Errorf("invalid data table ID format: %s", tableID)
	}

	parts := strings.Split(tableID, "_")
	if len(parts) < 3 {
		return 0, "", "", fmt.Errorf("invalid data table ID format: %s", tableID)
	}

	datasetID, err := strconv.ParseInt(parts[2], 10, 32)
	if err != nil {
		return 0, "", "", fmt.Errorf("invalid datasetID in table ID %s: %v", tableID, err)
	}

	if len(parts) == 3 {
		return int32(datasetID), "", "", nil
	}

	var encodedObjectID string
	var freq string
	if len(parts) == 4 {
		if looksLikeFreq(parts[3]) {
			freq = parts[3]
		} else {
			encodedObjectID = parts[3]
		}
	} else {
		freq = parts[len(parts)-1]
		encodedObjectID = strings.Join(parts[3:len(parts)-1], "_")
	}

	if encodedObjectID == "" {
		return int32(datasetID), "", freq, fmt.Errorf("empty objectID in table ID: %s", tableID)
	}

	objectID, err := decodeTableObjectID(encodedObjectID)
	if err != nil {
		return int32(datasetID), "", freq, err
	}
	return int32(datasetID), objectID, freq, nil
}

func looksLikeFreq(value string) bool {
	if value == "" {
		return false
	}
	re := regexp.MustCompile(`^\d+(m|M|h|H|d|D|w|W|mo|MO|y|Y)$`)
	return re.MatchString(value)
}

func decodeTableObjectID(encodedObjectID string) (string, error) {
	objectID, err := DecodeSymbol(encodedObjectID)
	if err == nil && !strings.Contains(objectID, "_x") {
		return objectID, nil
	}

	incompleteEncodedByte := regexp.MustCompile(`_x[0-9A-Fa-f]{2}$`)
	if incompleteEncodedByte.MatchString(encodedObjectID) {
		return DecodeSymbol(encodedObjectID + "_")
	}
	return objectID, err
}

// ParseDataTableObjectID 从数据表ID中解析并解码对象ID
// 支持格式：t_data_datasetID_encodedObjectID_freq
// 返回：objectID(已解码), error
func ParseDataTableObjectID(tableID string) (string, error) {
	_, objectID, _, err := ParseDataTableParts(tableID)
	if err != nil {
		return "", err
	}
	if objectID == "" {
		return "", fmt.Errorf("empty objectID in table ID: %s", tableID)
	}
	return objectID, nil
}

// ParseDataTableFreq 从数据表ID中解析频率
// 支持格式：t_data_datasetID_encodedObjectID_freq
func ParseDataTableFreq(tableID string) (string, error) {
	_, _, freq, err := ParseDataTableParts(tableID)
	if err != nil {
		return "", err
	}
	if freq == "" {
		return "", fmt.Errorf("empty freq in table ID: %s", tableID)
	}
	return freq, nil
}

// GenDataKeyStr 生成数据键字符串
// 格式：projectID_datasetID_objectID_freq
func GenDataKeyStr(projectID int32, datasetID int32, objectID string, freq string) string {
	return fmt.Sprintf("%d_%d_%s_%s", projectID, datasetID, objectID, freq)
}
