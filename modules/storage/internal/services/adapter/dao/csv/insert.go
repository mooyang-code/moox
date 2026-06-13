package csv

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/helper"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/cache"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/utils"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

// SetFieldInfos 统一更新数据接口(仅支持时序数据的顺序插入)
func (c *CSV) SetFieldInfos(ctx context.Context, params *dao.SetFieldParams) (*pb.SetFieldInfosRsp, error) {
	log.DebugContextf(ctx, "+++++++ CSV SetFieldInfos: %+v +++++++", params)
	// 初始化响应
	rsp := c.initResponse()

	if len(params.UpdateDocRows) == 0 {
		return rsp, nil
	}

	// 检查数据类型
	if params.DataType != pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE {
		return nil, fmt.Errorf("CSV adapter only supports time series data")
	}

	// 处理时序数据
	return c.processTimingDataUpdate(ctx, params.TableID, params.UpdateDocRows, params.HistoricalRowsLimit)
}

// initResponse 初始化响应对象
func (c *CSV) initResponse() *pb.SetFieldInfosRsp {
	return &pb.SetFieldInfosRsp{
		RetInfo: &pb.RetInfo{
			Code: 0,
			Msg:  "success",
		},
		ModifyInfos: []*pb.ModifyFieldInfo{},
		LastRows:    []*pb.DocRow{},
		FailedRows:  []*pb.FailedDocRow{},
	}
}

// processTimingDataUpdate 处理时序数据更新
func (c *CSV) processTimingDataUpdate(ctx context.Context, tableID string, updateDocRows []*pb.UpdateDocRow, historicalRowsLimit uint32) (*pb.SetFieldInfosRsp, error) {
	log.InfoContextf(ctx, "processTimingDataUpdate:%+v", updateDocRows)
	// 初始化响应
	rsp := c.initResponse()

	// 准备CSV文件
	filename, headers, lastRows, err := c.prepareCSVFile(tableID, historicalRowsLimit)
	if err != nil {
		return nil, err
	}

	// 查找所有新字段
	allNewHeaders := make(map[string]struct{})
	for _, row := range updateDocRows {
		newHeaders := c.findNewHeaders(row, headers)
		for _, header := range newHeaders {
			allNewHeaders[header] = struct{}{}
		}
	}

	// 如果有新字段，更新headers
	if len(allNewHeaders) > 0 {
		// 更新headers（保持原有顺序，在末尾追加新字段）
		updatedHeaders := make([]string, len(headers))
		copy(updatedHeaders, headers)
		for header := range allNewHeaders {
			updatedHeaders = append(updatedHeaders, header)
		}

		// 重写文件头
		if err := c.rewriteCSVHeaders(filename, updatedHeaders); err != nil {
			return nil, fmt.Errorf("failed to update CSV headers: %v", err)
		}
		headers = updatedHeaders
	}

	// 准备所有数据行
	allData := make([][]string, 0, len(updateDocRows))
	failedRows := 0
	for _, row := range updateDocRows {
		// 确保行ID和时间戳存在
		c.ensureRowIDAndTimestamp(row)

		// 检查时序顺序
		if err := c.validateTimeSeries(ctx, row, lastRows, tableID); err != nil {
			failedRows++
			// 记录失败的行
			c.handleAppendError(rsp, row, err)
			continue
		}

		// 准备数据行
		data := c.prepareRowData(row, headers)
		allData = append(allData, data)

		// 记录成功的行
		c.recordSuccessfulRow(rsp, row)
	}

	// 如果所有行都失败了，返回错误
	if failedRows == len(updateDocRows) {
		return nil, fmt.Errorf("all rows failed time series validation")
	}

	// 批量写入数据
	if len(allData) > 0 {
		if err := c.appendToCSV(filename, allData); err != nil {
			return nil, fmt.Errorf("failed to append data to CSV: %v", err)
		}
	}
	return rsp, nil
}

// prepareCSVFile 准备CSV文件，返回文件名、表头和最后几行数据
func (c *CSV) prepareCSVFile(tableID string, historicalRowsLimit uint32) (string, []string, [][]string, error) {
	// 生成CSV文件名
	filename := c.getCSVFilename(tableID)

	// 获取现有文件的表头
	headers, err := c.getCSVHeaders(filename)
	if err != nil {
		return "", nil, nil, fmt.Errorf("getCSVHeaders:failed to get CSV headers: %v", err)
	}

	// 确保historicalRowsLimit至少为1
	if historicalRowsLimit < 1 {
		historicalRowsLimit = 1
	}

	// 获取末尾数据
	lastRows, err := c.getLastRowsFromCSV(filename, int(historicalRowsLimit))
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to get last rows: %v", err)
	}
	return filename, headers, lastRows, nil
}

// validateTimeSeries 验证时序关系
func (c *CSV) validateTimeSeries(ctx context.Context, row *pb.UpdateDocRow, lastRows [][]string, tableID string) error {
	if len(lastRows) > 0 {
		lastRow := lastRows[len(lastRows)-1]
		lastTime := lastRow[0] // 假设_times是第一列
		currentTime := row.Times

		// 使用 checkTimeSeriesOrder 检查时序关系
		isValid, err := c.checkTimeSeriesOrder(ctx, tableID, lastTime, currentTime)
		if err != nil {
			log.ErrorContextf(ctx, "Time series validation error: %v", err)
			return err
		}
		if !isValid {
			log.ErrorContextf(ctx, "Invalid time series order: current time %s is not after last time %s", currentTime, lastTime)
			return fmt.Errorf("invalid time series order: current time %s is not after last time %s", currentTime, lastTime)
		}
	}
	return nil
}

// prepareRowData 准备行数据
func (c *CSV) prepareRowData(row *pb.UpdateDocRow, headers []string) []string {
	// 准备数据数组
	data := make([]string, len(headers))

	// 创建字段值映射
	fieldMap := make(map[string]string)
	for _, field := range row.Fields {
		if field.FieldInfo != nil {
			colName := c.fieldID2ColName(uint64(field.FieldInfo.FieldId))
			fieldMap[colName] = c.convertFieldToString(field.FieldInfo)
		}
	}

	// 遍历headers，组装数据
	for i, header := range headers {
		if header == "_times" {
			data[i] = row.Times
		} else if value, exists := fieldMap[header]; exists {
			data[i] = value
		}
	}
	return data
}

// findNewHeaders 查找需要追加的新字段
func (c *CSV) findNewHeaders(row *pb.UpdateDocRow, headers []string) []string {
	// 创建headers映射，用于快速查找
	headerMap := make(map[string]int)
	for i, header := range headers {
		headerMap[header] = i
	}

	// 检查是否有新字段需要追加
	newHeaders := make([]string, 0)
	for _, field := range row.Fields {
		if field.FieldInfo == nil {
			continue
		}
		colName := c.fieldID2ColName(uint64(field.FieldInfo.FieldId))
		if _, exists := headerMap[colName]; !exists {
			// 这是一个新字段，需要追加到headers
			newHeaders = append(newHeaders, colName)
		}
	}
	return newHeaders
}

// handleAppendError 处理追加数据失败的情况
func (c *CSV) handleAppendError(rsp *pb.SetFieldInfosRsp, row *pb.UpdateDocRow, err error) {
	failedRow := &pb.FailedDocRow{
		RowId: row.RowId,
		FailedList: map[uint32]*pb.FailedInfo{
			0: {
				Code: pb.EnumErrorCode_INVALID_OP_TYPE,
				Msg:  fmt.Sprintf("failed to append data: %v", err),
			},
		},
	}
	rsp.FailedRows = append(rsp.FailedRows, failedRow)
}

// recordSuccessfulRow 记录成功处理的行
func (c *CSV) recordSuccessfulRow(rsp *pb.SetFieldInfosRsp, row *pb.UpdateDocRow) {
	docRow := &pb.DocRow{
		RowId:  row.RowId,
		Fields: make(map[uint32]*pb.FieldInfo),
	}
	// 转换字段类型
	for fieldID, field := range row.Fields {
		if field.FieldInfo != nil {
			docRow.Fields[fieldID] = field.FieldInfo
		}
	}
	rsp.LastRows = append(rsp.LastRows, docRow)
}

// getCSVFilename 生成CSV文件名
func (c *CSV) getCSVFilename(tableID string) string {
	if c.connInfo == "" {
		// 如果未设置连接信息，使用默认路径
		return filepath.Join("../data", "csv", fmt.Sprintf("%s.csv", tableID))
	}
	return filepath.Join(c.connInfo, fmt.Sprintf("%s.csv", tableID))
}

// getCSVHeaders 获取CSV文件的表头
func (c *CSV) getCSVHeaders(filename string) ([]string, error) {
	// 确保目录存在
	dir := filepath.Dir(filename)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %v", err)
	}

	file, err := os.OpenFile(filename, os.O_RDONLY|os.O_CREATE, 0644)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	reader := csv.NewReader(file)
	headers, err := reader.Read()
	if err != nil {
		// 如果文件为空，创建默认表头
		return []string{"_times"}, nil
	}
	return headers, nil
}

// getLastRowsFromCSV 从CSV文件获取最后N行数据
func (c *CSV) getLastRowsFromCSV(filename string, n int) ([][]string, error) {
	// 打开文件
	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %v", err)
	}
	defer file.Close()

	// 获取文件大小
	fileInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to get file info: %v", err)
	}
	fileSize := fileInfo.Size()

	// 如果文件为空，返回空结果
	if fileSize == 0 {
		return nil, nil
	}

	// 确保n至少为1
	if n < 1 {
		n = 1
	}

	// 估算每行平均长度（假设为1000字节），计算需要读取的字节数
	// 为了安全起见，我们读取比预期更多的内容
	estimatedLineLength := 1000
	bytesToRead := n * estimatedLineLength * 5 // 读取5倍的内容以确保安全

	// 如果文件较小，直接读取整个文件
	if fileSize <= int64(bytesToRead) {
		bytesToRead = int(fileSize)
	}

	// 将文件指针移动到文件末尾前的指定位置
	_, err = file.Seek(-int64(bytesToRead), io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("failed to seek file: %v", err)
	}

	// 使用bufio.Scanner读取内容
	scanner := bufio.NewScanner(file)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan file: %v", err)
	}

	// 如果只有一行，说明只有表头，返回空结果
	if len(lines) <= 1 {
		return nil, nil
	}

	// 获取最后n行（不包括表头）
	start := len(lines) - n
	if start < 1 { // 确保不包含表头
		start = 1
	}
	lastLines := lines[start:]

	// 解析 CSV 行
	var result [][]string
	for _, line := range lastLines {
		reader := csv.NewReader(strings.NewReader(line))
		record, err := reader.Read()
		if err != nil {
			return nil, fmt.Errorf("failed to parse CSV line: %v", err)
		}
		result = append(result, record)
	}

	return result, nil
}

// appendToCSV 批量追加数据到CSV文件
func (c *CSV) appendToCSV(filename string, data [][]string) error {
	// 以追加模式打开文件
	file, err := os.OpenFile(filename, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open file: %v", err)
	}
	defer file.Close()

	// 创建CSV写入器
	writer := csv.NewWriter(file)
	// 设置换行符为\n
	writer.UseCRLF = false
	defer writer.Flush()

	// 批量写入数据行
	if err := writer.WriteAll(data); err != nil {
		return fmt.Errorf("failed to write data to CSV: %v", err)
	}
	return nil
}

// ensureRowIDAndTimestamp 确保行ID和时间戳存在
func (c *CSV) ensureRowIDAndTimestamp(row *pb.UpdateDocRow) {
	if row.RowId == "" {
		row.RowId = helper.GenRowID()
	}
}

// fieldID2ColName 将字段ID转换为列名
func (c *CSV) fieldID2ColName(fieldID uint64) string {
	field := cache.GetFieldInfoByID(int(fieldID))
	if field == nil {
		// 如果获取不到字段信息，使用更有意义的默认名称
		return fmt.Sprintf("unknown_field_%d", fieldID)
	}
	return field.InterfaceName // 字段英文名作为列名
}

// convertFieldToString 将FieldInfo转换为字符串
func (c *CSV) convertFieldToString(fieldInfo *pb.FieldInfo) string {
	if fieldInfo == nil {
		return ""
	}

	switch fieldInfo.FieldType {
	case pb.EnumFieldType_STR_FIELD:
		return fieldInfo.GetSimpleValue().GetStr()
	case pb.EnumFieldType_INT_FIELD:
		return fmt.Sprintf("%d", fieldInfo.GetSimpleValue().GetInt())
	case pb.EnumFieldType_FLOAT_FIELD:
		return fmt.Sprintf("%f", fieldInfo.GetSimpleValue().GetFloat())
	case pb.EnumFieldType_TIME_FIELD:
		return fieldInfo.GetSimpleValue().GetTime()
	case pb.EnumFieldType_MAP_KV_FIELD:
		// 将map转换为json字符串
		mapValues := make(map[string]any)
		if mapValue := fieldInfo.GetMapValue(); mapValue != nil {
			for k, entry := range mapValue.GetEntries() {
				switch entry.Type {
				case pb.EnumFieldType_STR_FIELD:
					mapValues[k] = entry.GetValue().GetStr()
				case pb.EnumFieldType_INT_FIELD:
					mapValues[k] = entry.GetValue().GetInt()
				case pb.EnumFieldType_FLOAT_FIELD:
					mapValues[k] = entry.GetValue().GetFloat()
				case pb.EnumFieldType_TIME_FIELD:
					mapValues[k] = entry.GetValue().GetTime()
				default:
					mapValues[k] = fmt.Sprintf("%v", entry)
				}
			}
		}
		jsonBytes, _ := json.Marshal(mapValues)
		return string(jsonBytes)
	default:
		return fmt.Sprintf("%v", fieldInfo)
	}
}

// checkTimeSeriesOrder 检查两个时间是否满足合法的时序关系
func (c *CSV) checkTimeSeriesOrder(ctx context.Context, tableID string, prevTime, currentTime string) (bool, error) {
	log.DebugContextf(ctx, "checkTimeSeriesOrder : %s-%s-%s", tableID, prevTime, currentTime)
	// 从tableID中提取频率信息
	parts := strings.Split(tableID, "_")
	if len(parts) < 5 {
		return false, fmt.Errorf("invalid tableID format: %s", tableID)
	}
	freq := parts[len(parts)-1]

	return utils.CheckTimeSeriesOrder(freq, prevTime, currentTime)
}

// rewriteCSVHeaders 重写CSV文件的表头
func (c *CSV) rewriteCSVHeaders(filename string, newHeaders []string) error {
	// 打开文件
	file, err := os.OpenFile(filename, os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("failed to open file: %v", err)
	}
	defer file.Close()

	// 获取文件大小
	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to get file info: %v", err)
	}

	// 如果文件为空，直接写入新表头
	if fileInfo.Size() == 0 {
		writer := csv.NewWriter(file)
		if err := writer.Write(newHeaders); err != nil {
			return fmt.Errorf("failed to write new headers: %v", err)
		}
		writer.Flush()
		return writer.Error()
	}

	// 读取第一行（表头）
	reader := csv.NewReader(file)
	_, err = reader.Read() // 跳过旧表头
	if err != nil {
		return fmt.Errorf("failed to read headers: %v", err)
	}

	// 获取文件当前位置（第一行之后）
	_, err = file.Seek(0, io.SeekCurrent)
	if err != nil {
		return fmt.Errorf("failed to get file position: %v", err)
	}

	// 读取剩余内容
	remainingData, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("failed to read remaining data: %v", err)
	}

	// 将文件指针移到开始位置
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("failed to seek file: %v", err)
	}

	// 清空文件内容
	if err := file.Truncate(0); err != nil {
		return fmt.Errorf("failed to truncate file: %v", err)
	}

	// 写入新的表头
	writer := csv.NewWriter(file)
	if err := writer.Write(newHeaders); err != nil {
		return fmt.Errorf("failed to write new headers: %v", err)
	}
	writer.Flush()

	// 写入剩余数据
	if _, err := file.Write(remainingData); err != nil {
		return fmt.Errorf("failed to write remaining data: %v", err)
	}

	return nil
}
