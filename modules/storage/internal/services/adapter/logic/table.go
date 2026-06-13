// Package logic 表操作接口实现
package logic

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/services/access/config"
	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter/dao"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/cache"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/utils"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-database/localcache"
	"trpc.group/trpc-go/trpc-go/log"
)

// CreateTable 创建表接口
//
// 功能说明：
//  1. 根据实体ID获取对应的存储设备配置信息
//  2. 校验请求参数，并根据存储设备配置创建表
//  3. 支持强制创建模式（覆盖已存在的表）
//
// 参数：
//   - ctx: 上下文
//   - req: 请求参数，包含实体ID、表ID、数据类型、描述信息、强制创建标志等
//
// 返回值：
//   - *pb.CreateTableRsp: 响应结果，包含创建的表ID
//   - error: 错误信息
func (a *AdapterImpl) CreateTable(ctx context.Context, req *pb.CreateTableReq) (*pb.CreateTableRsp, error) {
	log.DebugContextf(ctx, "####### Adapter CreateTable : %+v #######", req)
	rsp := &pb.CreateTableRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "success",
		},
	}

	// 1. 参数校验
	if err := validateCreateTableParams(req); err != nil {
		log.ErrorContextf(ctx, "CreateTable: 参数校验失败: %v", err)
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = err.Error()
		return rsp, nil
	}

	// 2. 创建存储设备实例
	device, err := dao.NewStoreDevice(ctx, int(req.GetDeviceId()))
	if err != nil {
		errMsg := fmt.Sprintf("创建存储设备实例失败: %v", err)
		log.ErrorContextf(ctx, errMsg)
		rsp.RetInfo.Code = pb.EnumErrorCode_INNER_ERR
		rsp.RetInfo.Msg = errMsg
		return rsp, nil
	}

	// 3. 准备创建表参数
	createParams := &dao.CreateTableParams{
		TableID:     utils.EscapeTableIDDash(req.GetTableId()),
		DataType:    req.GetDataType(),
		Description: req.GetDescription(),
		ForceCreate: req.GetForceCreate(),
	}

	// 4. 执行创建表操作
	if err := device.CreateTable(ctx, createParams); err != nil {
		errMsg := fmt.Sprintf("创建表失败: %v", err)
		log.ErrorContextf(ctx, errMsg)
		rsp.RetInfo.Code = pb.EnumErrorCode_INNER_ERR
		rsp.RetInfo.Msg = errMsg
		return rsp, nil
	}
	log.InfoContextf(ctx, "表[%s]创建成功，设备ID: %d", req.GetTableId(), req.GetDeviceId())
	return rsp, nil
}

// DropTable 删除表接口
//
// 功能说明：
//  1. 根据实体ID获取对应的存储设备配置信息
//  2. 校验请求参数，并根据存储设备配置删除表
//  3. 支持强制删除模式（即使表中有数据）
//
// 参数：
//   - ctx: 上下文
//   - req: 请求参数，包含实体ID、表ID、强制删除标志等
//
// 返回值：
//   - *pb.DropTableRsp: 响应结果
//   - error: 错误信息
func (a *AdapterImpl) DropTable(ctx context.Context, req *pb.DropTableReq) (*pb.DropTableRsp, error) {
	log.DebugContextf(ctx, "DropTable: req=%+v", req)
	rsp := &pb.DropTableRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "success",
		},
	}
	tableID := utils.EscapeTableIDDash(req.GetTableId())

	// 1. 参数校验
	if err := validateDropTableParams(req); err != nil {
		log.ErrorContextf(ctx, "DropTable: 参数校验失败: %v", err)
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = err.Error()
		return rsp, nil
	}

	// 2. 创建存储设备实例
	device, err := dao.NewStoreDevice(ctx, int(req.GetDeviceId()))
	if err != nil {
		errMsg := fmt.Sprintf("创建存储设备实例失败: %v", err)
		log.ErrorContextf(ctx, errMsg)
		rsp.RetInfo.Code = pb.EnumErrorCode_INNER_ERR
		rsp.RetInfo.Msg = errMsg
		return rsp, nil
	}

	// 3. 检查表是否存在（如果不是强制删除）
	if !req.GetForceDrop() {
		exists, err := device.CheckTable(ctx, tableID)
		if err != nil {
			errMsg := fmt.Sprintf("检查表是否存在失败: %v", err)
			log.ErrorContextf(ctx, errMsg)
			rsp.RetInfo.Code = pb.EnumErrorCode_INNER_ERR
			rsp.RetInfo.Msg = errMsg
			return rsp, nil
		}
		if !exists {
			errMsg := fmt.Sprintf("表[%s]不存在", tableID)
			log.WarnContextf(ctx, errMsg)
			rsp.RetInfo.Code = pb.EnumErrorCode_INNER_ERR
			rsp.RetInfo.Msg = errMsg
			return rsp, nil
		}
	}

	// 4. 执行删除表操作
	if err := device.DropTable(ctx, tableID); err != nil {
		errMsg := fmt.Sprintf("删除表失败: %v", err)
		log.ErrorContextf(ctx, errMsg)
		rsp.RetInfo.Code = pb.EnumErrorCode_INNER_ERR
		rsp.RetInfo.Msg = errMsg
		return rsp, nil
	}
	log.InfoContextf(ctx, "表[%s]删除成功，设备ID: %d", req.GetTableId(), req.GetDeviceId())
	return rsp, nil
}

// CheckTable 检查表状态接口
//
// 功能说明：
//  1. 根据实体ID获取对应的存储设备配置信息
//  2. 校验请求参数，并根据存储设备配置检查表是否存在
//
// 参数：
//   - ctx: 上下文
//   - req: 请求参数，包含实体ID、表ID等
//
// 返回值：
//   - *pb.CheckTableRsp: 响应结果，包含表是否存在的标志
//   - error: 错误信息
func (a *AdapterImpl) CheckTable(ctx context.Context, req *pb.CheckTableReq) (*pb.CheckTableRsp, error) {
	log.DebugContextf(ctx, "CheckTable: req=%+v", req)
	rsp := &pb.CheckTableRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "success",
		},
		TableExists: false,
	}

	// 1. 参数校验
	if err := validateCheckTableParams(req); err != nil {
		log.ErrorContextf(ctx, "CheckTable: 参数校验失败: %v", err)
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = err.Error()
		return rsp, nil
	}

	// 2. 创建存储设备实例
	device, err := dao.NewStoreDevice(ctx, int(req.GetDeviceId()))
	if err != nil {
		errMsg := fmt.Sprintf("创建存储设备实例失败: %v", err)
		log.ErrorContextf(ctx, errMsg)
		rsp.RetInfo.Code = pb.EnumErrorCode_INNER_ERR
		rsp.RetInfo.Msg = errMsg
		return rsp, nil
	}

	// 3. 执行检查表操作
	tableID := utils.EscapeTableIDDash(req.GetTableId())
	exists, err := device.CheckTable(ctx, tableID)
	if err != nil {
		errMsg := fmt.Sprintf("检查表状态失败: %v", err)
		log.ErrorContextf(ctx, errMsg)
		rsp.RetInfo.Code = pb.EnumErrorCode_INNER_ERR
		rsp.RetInfo.Msg = errMsg
		return rsp, nil
	}

	rsp.TableExists = exists
	log.DebugContextf(ctx, "表[%s]存在性检查结果: %v，设备ID: %d", req.GetTableId(), exists, req.GetDeviceId())
	return rsp, nil
}

// validateCreateTableParams 校验创建表请求参数
func validateCreateTableParams(req *pb.CreateTableReq) error {
	if req == nil {
		return fmt.Errorf("请求参数不能为空")
	}
	if req.GetDeviceId() == 0 {
		return fmt.Errorf("设备ID不能为空")
	}
	if req.GetTableId() == "" {
		return fmt.Errorf("表ID不能为空")
	}
	if !ValidateDataType(req.GetDataType()) {
		return fmt.Errorf("数据类型不能为空或非法")
	}
	return nil
}

// validateDropTableParams 校验删除表请求参数
func validateDropTableParams(req *pb.DropTableReq) error {
	if req == nil {
		return fmt.Errorf("请求参数不能为空")
	}
	if req.GetDeviceId() == 0 {
		return fmt.Errorf("设备ID不能为空")
	}
	if req.GetTableId() == "" {
		return fmt.Errorf("表ID不能为空")
	}
	return nil
}

// validateCheckTableParams 校验检查表请求参数
func validateCheckTableParams(req *pb.CheckTableReq) error {
	if req == nil {
		return fmt.Errorf("请求参数不能为空")
	}
	if req.GetDeviceId() == 0 {
		return fmt.Errorf("设备ID不能为空")
	}
	if req.GetTableId() == "" {
		return fmt.Errorf("表ID不能为空")
	}
	return nil
}

// ============================================================================
// 数据库表定时任务相关实现
// ============================================================================

// TableScheduleManager 数据库表定时任务管理器
type TableScheduleManager struct {
	config *config.TableSchedulerConfig
}

// TableCacheValue 表缓存值
type TableCacheValue struct {
	TableID    string    `json:"table_id"`
	CreateTime time.Time `json:"create_time"`
	Status     string    `json:"status"` // "created", "exists", "failed"
}

// datasetProcessResult 数据集处理结果
type datasetProcessResult struct {
	projectID   uint32
	datasetID   uint32
	datasetName string
	tableID     string // 生成的表ID
	err         error  // 错误信息
}

// 全局实例
var (
	tableScheduleManager *TableScheduleManager
	tableScheduleOnce    sync.Once
)

// InitScheduler 初始化数据库表管理器
func InitScheduler() error {
	var initErr error
	tableScheduleOnce.Do(func() {
		// 加载配置
		cfg, err := config.LoadConfig()
		if err != nil {
			initErr = fmt.Errorf("加载访问服务配置失败: %v", err)
			return
		}

		if !cfg.TableScheduler.Enable {
			log.Info("数据库表定时任务已禁用")
			return
		}

		log.Info("数据库表管理器配置: %+v", cfg.TableScheduler)

		tableScheduleManager = &TableScheduleManager{
			config: &cfg.TableScheduler,
		}
		log.Info("数据库表管理器初始化完成")
	})
	return initErr
}

// HandleTableSchedule 定时器入口函数 - 定时检查并创建数据库表
func HandleTableSchedule(ctx context.Context, params string) error {
	log.DebugContextf(ctx, "数据库表管理定时任务开始执行，参数: %s", params)

	// 确保管理器已初始化
	if err := InitScheduler(); err != nil {
		return err
	}

	// 检查是否启用
	if tableScheduleManager == nil {
		log.InfoContextf(ctx, "数据库表定时任务未启用，跳过执行")
		return nil
	}

	// 1. 获取所有数据集
	datasets, err := tableScheduleManager.getAllDatasets()
	if err != nil {
		log.ErrorContextf(ctx, "获取数据集列表失败: %v", err)
		return err
	}
	log.DebugContextf(ctx, "获取到 %d 个数据集", len(datasets))

	// 2. 为每个数据集处理对象路由
	results, err := tableScheduleManager.executeDatasetTasks(ctx, datasets)
	if err != nil {
		log.ErrorContextf(ctx, "执行数据集处理任务失败: %v", err)
		return err
	}
	log.DebugContextf(ctx, "数据库表管理定时任务执行完成: %+v ", results)
	return nil
}

// getAllDatasets 获取所有数据集
func (m *TableScheduleManager) getAllDatasets() ([]*cache.Dataset, error) {
	datasets := cache.GetAllDatasetInfo()
	if datasets == nil {
		return nil, fmt.Errorf("获取数据集信息失败")
	}
	return datasets, nil
}

// executeDatasetTasks 执行数据集处理任务
func (m *TableScheduleManager) executeDatasetTasks(ctx context.Context, datasets []*cache.Dataset) ([]datasetProcessResult, error) {
	if len(datasets) == 0 {
		return nil, nil
	}

	// 串行执行任务
	var results []datasetProcessResult
	for _, dataset := range datasets {
		log.DebugContextf(ctx, "开始处理数据集: 项目ID=%d, 数据集ID=%d, 数据集名=%s",
			dataset.ProjID, dataset.DatasetID, dataset.DatasetName)

		tableID, err := m.createObjectTable(ctx, uint32(dataset.ProjID), uint32(dataset.DatasetID))

		// 创建结果记录
		result := datasetProcessResult{
			projectID:   uint32(dataset.ProjID),
			datasetID:   uint32(dataset.DatasetID),
			datasetName: dataset.DatasetName,
			tableID:     tableID,
			err:         err,
		}
		if err != nil {
			log.ErrorContextf(ctx, "数据集处理失败: 项目ID=%d, 数据集ID=%d, 数据集名=%s, 错误=%v",
				dataset.ProjID, dataset.DatasetID, dataset.DatasetName, err)
		} else {
			log.DebugContextf(ctx, "数据集处理成功: 项目ID=%d, 数据集ID=%d, 数据集名=%s, 表ID=%s",
				dataset.ProjID, dataset.DatasetID, dataset.DatasetName, tableID)
		}
		results = append(results, result)
	}
	return results, nil
}

// createObjectTable 创建对象表
func (m *TableScheduleManager) createObjectTable(ctx context.Context, projectID uint32, datasetID uint32) (string, error) {
	// 检查缓存
	cacheKey := m.genTableIDCacheKey(projectID, datasetID)
	if m.checkTableCache(ctx, cacheKey) {
		// 从缓存获取表ID
		tableID := utils.GenObjectTableID(int32(datasetID))
		return tableID, nil
	}
	log.InfoContextf(ctx, "createTable 表不存在于缓存中: %s", cacheKey)

	// 获取数据集信息，获取正确的数据类型
	dataset, err := cache.GetDatasetByID(int(datasetID))
	if err != nil {
		errMsg := fmt.Sprintf("获取数据集[%d]信息失败: %v", datasetID, err)
		log.ErrorContextf(ctx, errMsg)
		return "", fmt.Errorf("%s", errMsg)
	}

	// 将数据集的 DataType 转换为 EnumDataTypeCategory
	dataType := pb.EnumDataTypeCategory(dataset.DataType)
	log.InfoContextf(ctx, "数据集[%d]的数据类型: %d (%s)", datasetID, dataType, dataType.String())

	// 生成表ID
	tableID := utils.GenObjectTableID(int32(datasetID))
	log.InfoContextf(ctx, "生成表ID: %s", tableID)

	// 获取项目的字段路由配置，获取所有存储设备ID
	fieldRoutes, err := cache.GetFieldRouteByPrjID(int(projectID))
	if err != nil {
		errMsg := fmt.Sprintf("获取项目[%d]字段路由配置失败: %v", projectID, err)
		log.ErrorContextf(ctx, errMsg)
		return "", fmt.Errorf("%s", errMsg)
	}

	// 收集所有唯一的设备ID
	deviceIDMap := make(map[int]bool)
	for _, route := range fieldRoutes {
		deviceIDMap[route.DeviceID] = true
	}

	if len(deviceIDMap) == 0 {
		errMsg := fmt.Sprintf("项目[%d]没有配置字段路由", projectID)
		log.ErrorContextf(ctx, errMsg)
		return "", fmt.Errorf("%s", errMsg)
	}

	// 为每个设备创建表，使用从数据集中获取的正确数据类型
	var failedDevices []int
	var lastError error
	for deviceID := range deviceIDMap {
		success := m.createTableOnDevice(ctx, deviceID, tableID, false, dataType)
		if !success {
			failedDevices = append(failedDevices, deviceID)
			lastError = fmt.Errorf("在设备[%d]上创建表[%s]失败", deviceID, tableID)
			log.ErrorContextf(ctx, lastError.Error())
		} else {
			log.InfoContextf(ctx, "在设备[%d]上创建表[%s]成功", deviceID, tableID)
		}
	}

	if len(failedDevices) == 0 {
		// 全部成功
		m.cacheTableResult(cacheKey, tableID, "created")
		return tableID, nil
	}

	// 部分或全部失败
	if len(failedDevices) == len(deviceIDMap) {
		// 全部失败
		errMsg := fmt.Sprintf("所有设备创建表[%s]失败，失败设备: %v", tableID, failedDevices)
		log.ErrorContextf(ctx, errMsg)
		m.cacheTableResult(cacheKey, tableID, "failed")
		return "", fmt.Errorf("%s", errMsg)
	}

	// 部分失败
	errMsg := fmt.Sprintf("部分设备创建表[%s]失败，失败设备: %v", tableID, failedDevices)
	log.WarnContextf(ctx, errMsg)
	m.cacheTableResult(cacheKey, tableID, "partial")
	return tableID, fmt.Errorf("%s", errMsg)
}

// createTableOnDevice 在指定设备上创建表
func (m *TableScheduleManager) createTableOnDevice(ctx context.Context, deviceID int, tableID string, forceCreate bool, dataType pb.EnumDataTypeCategory) bool {
	if !ValidateDataType(dataType) {
		log.ErrorContextf(ctx, "自动创建表[%s]失败: 数据类型不能为空或非法: %d", tableID, dataType)
		return false
	}

	// 创建存储设备实例
	device, err := dao.NewStoreDevice(ctx, deviceID)
	if err != nil {
		log.ErrorContextf(ctx, "创建存储设备[%d]实例失败: %v", deviceID, err)
		return false
	}

	// 准备创建表参数
	createParams := &dao.CreateTableParams{
		TableID:     tableID,
		DataType:    dataType,
		Description: "Adapter 定时任务创建的表",
		ForceCreate: forceCreate,
	}

	// 执行创建表操作
	if err := device.CreateTable(ctx, createParams); err != nil {
		// 检查是否是表已存在的错误
		if m.isTableExistsError(err.Error()) {
			log.InfoContextf(ctx, "表[%s]在设备[%d]上已存在，跳过创建", tableID, deviceID)
			return true
		}
		log.ErrorContextf(ctx, "在设备[%d]上创建表[%s]失败: %v", deviceID, tableID, err)
		return false
	}
	log.InfoContextf(ctx, "表[%s]在设备[%d]上创建成功", tableID, deviceID)
	return true
}

// genTableIDCacheKey 生成表ID缓存键
func (m *TableScheduleManager) genTableIDCacheKey(projectID uint32, datasetID uint32) string {
	return fmt.Sprintf("table:%d:%d", projectID, datasetID)
}

// checkTableCache 检查表缓存
func (m *TableScheduleManager) checkTableCache(ctx context.Context, cacheKey string) bool {
	if cached, found := localcache.Get(cacheKey); found {
		if cacheValue, ok := cached.(*TableCacheValue); ok {
			if cacheValue.Status == "created" || cacheValue.Status == "exists" {
				log.DebugContextf(ctx, "表已存在于缓存中: %s", cacheKey)
				return true
			}
		}
	}
	return false
}

// cacheTableResult 缓存表创建结果
func (m *TableScheduleManager) cacheTableResult(cacheKey string, tableID string, status string) {
	cacheValue := &TableCacheValue{
		TableID:    tableID,
		Status:     status,
		CreateTime: time.Now(),
	}
	localcache.Set(cacheKey, cacheValue, int64(m.config.CacheTTLMinutes*60))
}

// isTableExistsError 检查是否是表已存在的错误
func (m *TableScheduleManager) isTableExistsError(errMsg string) bool {
	// 检查常见的表已存在错误信息
	existsKeywords := []string{
		"already exists",
		"已存在",
		"duplicate",
		"重复",
		"exists",
	}

	for _, keyword := range existsKeywords {
		if fmt.Sprintf("%s", errMsg) != "" && len(errMsg) > 0 {
			// 简单的字符串包含检查
			if len(keyword) <= len(errMsg) {
				for i := 0; i <= len(errMsg)-len(keyword); i++ {
					if errMsg[i:i+len(keyword)] == keyword {
						return true
					}
				}
			}
		}
	}
	return false
}
