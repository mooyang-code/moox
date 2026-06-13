package logic

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/storage/internal/services/dbmanager/config"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/cache"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/utils"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-database/localcache"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/log"
)

// DBTableManagerServiceImpl admin服务实现
type DBTableManagerServiceImpl struct {
	cfg           *config.Config
	adapterClient pb.AdapterClientProxy
}

// InitDBTableManagerServiceImpl 初始化admin服务实现
func InitDBTableManagerServiceImpl(cfg *config.Config) (*DBTableManagerServiceImpl, error) {
	// 初始化adapter客户端
	adapterClient := pb.NewAdapterClientProxy(client.WithServiceName(cfg.Adapter.ServiceName))

	log.Info("admin服务初始化成功")
	return &DBTableManagerServiceImpl{
		cfg:           cfg,
		adapterClient: adapterClient,
	}, nil
}

// CreateDatabaseTable 创建数据库表
func (s *DBTableManagerServiceImpl) CreateDatabaseTable(ctx context.Context,
	req *pb.CreateDatabaseTableReq) (*pb.CreateDatabaseTableRsp, error) {
	log.InfoContextf(ctx, "CreateDatabaseTable 收到创建表请求: %+v", req)

	// 1. 参数验证
	if err := s.validateTableRequest(req.DataKey); err != nil {
		return s.genErrorRsp(pb.EnumErrorCode_INVALID_PARAM, err.Error()), nil
	}

	// 2. 准备表创建参数
	params, err := s.prepareTableCreationParams(ctx, req)
	if err != nil {
		return err, nil
	}

	// 3. 检查缓存并处理强制创建逻辑
	if shouldReturn, rsp := s.checkCacheAndForceCreate(ctx, req, params.CacheKey); shouldReturn {
		return rsp, nil
	}

	// 4. 执行表创建操作
	result, err := s.executeTableCreation(ctx, req, params)
	if err != nil {
		return err, nil
	}

	// 5. 处理创建结果并返回响应
	return s.handleCreationResult(ctx, params, result), nil
}

// TableOperationParams 表操作通用参数
type TableOperationParams struct {
	TableName string
	CacheKey  string
	DeviceIDs []int
}

// OperationResult 操作通用结果
type OperationResult struct {
	SuccessCount int
	FailCount    int
	LastError    error
}

// validateTableRequest 验证表操作请求（通用验证）
func (s *DBTableManagerServiceImpl) validateTableRequest(dataKey *pb.DataKey) error {
	if dataKey == nil {
		return fmt.Errorf("DataKey不能为空")
	}
	return nil
}

// prepareTableCreationParams 准备表创建参数
func (s *DBTableManagerServiceImpl) prepareTableCreationParams(ctx context.Context,
	req *pb.CreateDatabaseTableReq) (*TableOperationParams, *pb.CreateDatabaseTableRsp) {
	// 生成表名
	tableName := s.generateTableName(req.DataKey, req.TableName, req.TableType)
	log.InfoContextf(ctx, "生成表名: %s", tableName)

	// 判断数据类型
	dataType := s.determineDataType(req.DataKey)
	log.InfoContextf(ctx, "根据Freq值[%s]判断数据类型: %v", req.DataKey.Freq, dataType)

	// 获取设备ID列表
	deviceIDs := s.getPrjDeviceIDs(int(req.DataKey.ProjectId))
	if len(deviceIDs) == 0 {
		log.WarnContextf(ctx, "项目[%d]未找到任何设备配置", req.DataKey.ProjectId)
		return nil, s.genErrorRsp(pb.EnumErrorCode_NOT_SUPPORT, "项目未配置存储设备")
	}

	return &TableOperationParams{
		TableName: tableName,
		CacheKey:  fmt.Sprintf("table_exists_%s", tableName),
		DeviceIDs: deviceIDs,
	}, nil
}

// getPrjDeviceIDs 根据项目ID获取设备ID列表
func (s *DBTableManagerServiceImpl) getPrjDeviceIDs(prjID int) []int {
	fieldRoutes, _ := cache.GetFieldRouteByPrjID(prjID)
	if fieldRoutes == nil {
		return []int{}
	}
	deviceIDs := make(map[int]bool)
	for _, route := range fieldRoutes {
		deviceIDs[route.DeviceID] = true
	}
	var result []int
	for deviceID := range deviceIDs {
		result = append(result, deviceID)
	}
	return result
}

// checkCacheAndForceCreate 检查缓存并处理强制创建逻辑
func (s *DBTableManagerServiceImpl) checkCacheAndForceCreate(ctx context.Context,
	req *pb.CreateDatabaseTableReq, cacheKey string) (bool, *pb.CreateDatabaseTableRsp) {
	if exists, found := localcache.Get(cacheKey); found && exists.(bool) {
		if req.ForceCreate != nil && !*req.ForceCreate {
			return true, s.genErrorRsp(pb.EnumErrorCode_NOT_SUPPORT,
				fmt.Sprintf("表[%s]已存在", s.generateTableName(req.DataKey, req.TableName, req.TableType)))
		}
		log.InfoContextf(ctx, "表已存在于缓存中，强制创建模式")
	}
	return false, nil
}

// executeTableCreation 执行表创建操作
func (s *DBTableManagerServiceImpl) executeTableCreation(ctx context.Context,
	req *pb.CreateDatabaseTableReq, params *TableOperationParams) (*OperationResult, *pb.CreateDatabaseTableRsp) {
	result := &OperationResult{}
	dataType := s.determineDataType(req.DataKey)

	for _, deviceID := range params.DeviceIDs {
		createTableReq := &pb.CreateTableReq{
			DeviceId:    uint64(deviceID),
			TableId:     params.TableName,
			Description: req.Description,
			ForceCreate: req.ForceCreate,
			DataType:    dataType,
		}

		createTableRsp, err := s.adapterClient.CreateTable(ctx, createTableReq)
		if err != nil {
			log.ErrorContextf(ctx, "设备[%d]创建表失败: %v", deviceID, err)
			result.LastError = err
			result.FailCount++
			continue
		}

		if createTableRsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "设备[%d]创建表失败: %s", deviceID, createTableRsp.RetInfo.Msg)
			result.LastError = fmt.Errorf("设备[%d]创建表失败: %s", deviceID, createTableRsp.RetInfo.Msg)
			result.FailCount++
			continue
		}

		log.InfoContextf(ctx, "设备[%d]创建表[%s]成功", deviceID, params.TableName)
		result.SuccessCount++
	}
	return result, nil
}

// handleCreationResult 处理创建结果并返回响应
func (s *DBTableManagerServiceImpl) handleCreationResult(ctx context.Context,
	params *TableOperationParams, result *OperationResult) *pb.CreateDatabaseTableRsp {
	// 所有设备都失败
	if result.SuccessCount == 0 {
		return s.genErrorRsp(pb.EnumErrorCode_INNER_ERR,
			fmt.Sprintf("所有设备创建表失败，最后错误: %v", result.LastError))
	}

	// 部分设备失败
	if result.FailCount > 0 {
		msg := fmt.Sprintf("部分设备创建表失败，成功: %d个设备，失败: %d个设备，最后错误: %v",
			result.SuccessCount, result.FailCount, result.LastError)
		log.WarnContextf(ctx, "表[%s]创建结果: %s", params.TableName, msg)
		return s.genErrorRsp(pb.EnumErrorCode_INNER_ERR, msg)
	}

	// 所有设备都成功，更新缓存
	localcache.Set(params.CacheKey, true, int64(s.cfg.Cache.TableCacheExpire))
	log.InfoContextf(ctx, "所有设备创建表成功，已更新缓存")

	msg := fmt.Sprintf("表创建成功，所有 %d 个设备均创建成功", result.SuccessCount)
	log.InfoContextf(ctx, "表[%s]创建结果: %s", params.TableName, msg)

	return &pb.CreateDatabaseTableRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  msg,
		},
		TableId: params.TableName,
	}
}

// DropDatabaseTable 删除数据库表
func (s *DBTableManagerServiceImpl) DropDatabaseTable(ctx context.Context,
	req *pb.DropDatabaseTableReq) (*pb.DropDatabaseTableRsp, error) {
	log.InfoContextf(ctx, "收到删除表请求: %+v", req)

	// 1. 参数验证
	if err := s.validateTableRequest(req.DataKey); err != nil {
		return s.genDropErrorRsp(pb.EnumErrorCode_INVALID_PARAM, err.Error()), nil
	}

	// 2. 准备删除参数
	params, err := s.prepareTableDeletionParams(ctx, req)
	if err != nil {
		return err, nil
	}

	// 3. 执行表删除操作
	result, err := s.executeTableDeletion(ctx, req, params)
	if err != nil {
		return err, nil
	}

	// 4. 处理删除结果并返回响应
	return s.handleDeletionResult(ctx, params, result), nil
}

// CheckDatabaseTable 检查数据库表状态
func (s *DBTableManagerServiceImpl) CheckDatabaseTable(ctx context.Context,
	req *pb.CheckDatabaseTableReq) (*pb.CheckDatabaseTableRsp, error) {
	log.InfoContextf(ctx, "收到检查表状态请求: %+v", req)

	// 1. 参数验证
	if err := s.validateTableRequest(req.DataKey); err != nil {
		return s.genCheckErrorRsp(pb.EnumErrorCode_INVALID_PARAM, err.Error()), nil
	}

	// 2. 准备检查参数
	params, err := s.prepareTableCheckParams(ctx, req)
	if err != nil {
		return err, nil
	}

	// 3. 检查缓存
	if shouldReturn, rsp := s.checkCacheForTableExists(ctx, params); shouldReturn {
		return rsp, nil
	}

	// 4. 执行表状态检查
	result, err := s.executeTableCheck(ctx, params)
	if err != nil {
		return err, nil
	}

	// 5. 处理检查结果并返回响应
	return s.handleCheckResult(ctx, params, result), nil
}

// CheckOperationResult 检查操作结果（扩展了基础操作结果）
type CheckOperationResult struct {
	OperationResult
	ExistsCount   int
	NotExistCount int
	TableExists   bool // 最终的表存在状态
}

// prepareTableDeletionParams 准备表删除参数
func (s *DBTableManagerServiceImpl) prepareTableDeletionParams(ctx context.Context,
	req *pb.DropDatabaseTableReq) (*TableOperationParams, *pb.DropDatabaseTableRsp) {
	// 生成表名
	tableName := s.generateTableName(req.DataKey, req.TableId, req.TableType)
	log.InfoContextf(ctx, "删除表名: %s", tableName)

	// 获取设备ID列表
	deviceIDs := s.getPrjDeviceIDs(int(req.DataKey.ProjectId))
	if len(deviceIDs) == 0 {
		log.WarnContextf(ctx, "项目[%d]未找到任何设备配置", req.DataKey.ProjectId)
		return nil, s.genDropErrorRsp(pb.EnumErrorCode_NOT_SUPPORT, "项目未配置存储设备")
	}

	return &TableOperationParams{
		TableName: tableName,
		CacheKey:  fmt.Sprintf("table_exists_%s", tableName),
		DeviceIDs: deviceIDs,
	}, nil
}

// executeTableDeletion 执行表删除操作
func (s *DBTableManagerServiceImpl) executeTableDeletion(ctx context.Context,
	req *pb.DropDatabaseTableReq, params *TableOperationParams) (*OperationResult, *pb.DropDatabaseTableRsp) {
	result := &OperationResult{}

	for _, deviceID := range params.DeviceIDs {
		dropTableReq := &pb.DropTableReq{
			DeviceId:  uint64(deviceID),
			TableId:   params.TableName,
			ForceDrop: req.ForceDrop,
		}

		dropTableRsp, err := s.adapterClient.DropTable(ctx, dropTableReq)
		if err != nil {
			log.ErrorContextf(ctx, "设备[%d]删除表失败: %v", deviceID, err)
			result.LastError = err
			result.FailCount++
			continue
		}

		if dropTableRsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "设备[%d]删除表失败: %s", deviceID, dropTableRsp.RetInfo.Msg)
			result.LastError = fmt.Errorf("设备[%d]删除表失败: %s", deviceID, dropTableRsp.RetInfo.Msg)
			result.FailCount++
			continue
		}

		log.InfoContextf(ctx, "设备[%d]删除表[%s]成功", deviceID, params.TableName)
		result.SuccessCount++
	}

	return result, nil
}

// handleDeletionResult 处理删除结果并返回响应
func (s *DBTableManagerServiceImpl) handleDeletionResult(ctx context.Context,
	params *TableOperationParams, result *OperationResult) *pb.DropDatabaseTableRsp {
	// 所有设备都失败
	if result.SuccessCount == 0 {
		return s.genDropErrorRsp(pb.EnumErrorCode_INNER_ERR,
			fmt.Sprintf("所有设备删除表失败，最后错误: %v", result.LastError))
	}

	// 部分设备失败
	if result.FailCount > 0 {
		msg := fmt.Sprintf("部分设备删除表失败，成功: %d个设备，失败: %d个设备，最后错误: %v",
			result.SuccessCount, result.FailCount, result.LastError)
		log.WarnContextf(ctx, "表[%s]删除结果: %s", params.TableName, msg)
		return s.genDropErrorRsp(pb.EnumErrorCode_INNER_ERR, msg)
	}

	// 所有设备都成功，清除缓存
	localcache.Del(params.CacheKey)
	log.InfoContextf(ctx, "所有设备删除表成功，已清除缓存")

	msg := fmt.Sprintf("表删除成功，所有 %d 个设备均删除成功", result.SuccessCount)
	log.InfoContextf(ctx, "表[%s]删除结果: %s", params.TableName, msg)

	return &pb.DropDatabaseTableRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  msg,
		},
	}
}

// prepareTableCheckParams 准备表检查参数
func (s *DBTableManagerServiceImpl) prepareTableCheckParams(ctx context.Context,
	req *pb.CheckDatabaseTableReq) (*TableOperationParams, *pb.CheckDatabaseTableRsp) {
	// 生成表名
	tableName := s.generateTableName(req.DataKey, req.TableId, req.TableType)
	log.InfoContextf(ctx, "检查表名: %s", tableName)

	// 获取设备ID列表
	deviceIDs := s.getPrjDeviceIDs(int(req.DataKey.ProjectId))
	if len(deviceIDs) == 0 {
		log.WarnContextf(ctx, "项目[%d]未找到任何设备配置", req.DataKey.ProjectId)
		return nil, s.genCheckErrorRsp(pb.EnumErrorCode_NOT_SUPPORT, "项目未配置存储设备")
	}

	return &TableOperationParams{
		TableName: tableName,
		CacheKey:  fmt.Sprintf("table_exists_%s", tableName),
		DeviceIDs: deviceIDs,
	}, nil
}

// checkCacheForTableExists 检查缓存中的表存在状态
func (s *DBTableManagerServiceImpl) checkCacheForTableExists(ctx context.Context,
	params *TableOperationParams) (bool, *pb.CheckDatabaseTableRsp) {
	if exists, found := localcache.Get(params.CacheKey); found {
		log.InfoContextf(ctx, "从缓存获取表[%s]存在性: %v", params.TableName, exists.(bool))
		return true, &pb.CheckDatabaseTableRsp{
			RetInfo: &pb.RetInfo{
				Code: pb.EnumErrorCode_SUCCESS,
				Msg:  "success",
			},
			TableExists: exists.(bool),
		}
	}
	return false, nil
}

// executeTableCheck 执行表状态检查
func (s *DBTableManagerServiceImpl) executeTableCheck(ctx context.Context,
	params *TableOperationParams) (*CheckOperationResult, *pb.CheckDatabaseTableRsp) {
	result := &CheckOperationResult{}

	for _, deviceID := range params.DeviceIDs {
		checkTableReq := &pb.CheckTableReq{
			DeviceId: uint64(deviceID),
			TableId:  params.TableName,
		}

		checkTableRsp, err := s.adapterClient.CheckTable(ctx, checkTableReq)
		if err != nil {
			log.ErrorContextf(ctx, "设备[%d]检查表状态失败: %v", deviceID, err)
			result.LastError = err
			result.FailCount++
			continue
		}

		if checkTableRsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "设备[%d]检查表状态失败: %s", deviceID, checkTableRsp.RetInfo.Msg)
			result.LastError = fmt.Errorf("设备[%d]检查表状态失败: %s", deviceID, checkTableRsp.RetInfo.Msg)
			result.FailCount++
			continue
		}

		if checkTableRsp.TableExists {
			result.ExistsCount++
			result.TableExists = true // 只要有一个设备上表存在，就认为表存在
		} else {
			result.NotExistCount++
		}
		log.InfoContextf(ctx, "设备[%d]检查表[%s]状态: %v", deviceID, params.TableName, checkTableRsp.TableExists)
	}
	return result, nil
}

// handleCheckResult 处理检查结果并返回响应
func (s *DBTableManagerServiceImpl) handleCheckResult(ctx context.Context,
	params *TableOperationParams, result *CheckOperationResult) *pb.CheckDatabaseTableRsp {
	// 所有设备都失败
	if result.ExistsCount == 0 && result.NotExistCount == 0 {
		return s.genCheckErrorRsp(pb.EnumErrorCode_INNER_ERR,
			fmt.Sprintf("所有设备检查表状态失败，最后错误: %v", result.LastError))
	}

	// 更新缓存（只要有成功的检查结果）
	localcache.Set(params.CacheKey, result.TableExists, int64(s.cfg.Cache.TableCacheExpire))

	msg := fmt.Sprintf("表状态检查完成，存在: %d个设备，不存在: %d个设备，失败: %d个设备",
		result.ExistsCount, result.NotExistCount, result.FailCount)
	log.InfoContextf(ctx, "表[%s]检查结果: %s，最终状态: %v", params.TableName, msg, result.TableExists)

	return &pb.CheckDatabaseTableRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "success",
		},
		TableExists: result.TableExists,
	}
}

// generateTableName 生成表名
func (s *DBTableManagerServiceImpl) generateTableName(dataKey *pb.DataKey,
	customName *string, tableType pb.EnumTableType) string {
	// 如果提供了自定义表名，直接使用
	if customName != nil && *customName != "" {
		return *customName
	}

	// 根据table_type表类型来生成表名
	switch tableType {
	case pb.EnumTableType_DATA_OBJECT_TABLE:
		return utils.GenObjectTableID(dataKey.DatasetId)
	case pb.EnumTableType_DATA_TABLE:
		return utils.GenDataTableID(dataKey.DatasetId, dataKey.ObjectId, dataKey.Freq)
	default:
		// 默认情况下使用数据表命名规则
		log.Warnf("未知的表类型 %v，使用默认数据表命名规则", tableType)
		return utils.GenDataTableID(dataKey.DatasetId, dataKey.ObjectId, dataKey.Freq)
	}
}

// determineDataType 根据DataKey中的Freq值判断数据类型
func (s *DBTableManagerServiceImpl) determineDataType(dataKey *pb.DataKey) pb.EnumDataTypeCategory {
	// 根据DataKey中的Freq值是否为空判断是静态数据还是时序数据
	if dataKey.Freq != "" {
		// Freq值非空为时序数据
		return pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE
	} else {
		// Freq值为空为静态数据
		return pb.EnumDataTypeCategory_STATIC_DATA_TYPE
	}
}

// getEntityIDFromDataKey 根据DataKey获取实体ID
func (s *DBTableManagerServiceImpl) getEntityIDFromDataKey(dataKey *pb.DataKey) uint32 {
	if dataKey == nil {
		log.Warn("getEntityIDFromDataKey: dataKey为空，使用默认实体ID")
		return s.cfg.Adapter.DefaultEntityID
	}

	// 使用与access服务相同的逻辑：通过缓存查询对象路由获取存储实体ID
	entityID := cache.GetObjectRouteByID(int(dataKey.DatasetId), dataKey.ObjectId)
	if entityID == 0 {
		log.Warnf("getEntityIDFromDataKey: 未找到数据集[%d]对象[%s]的路由配置，使用默认实体ID[%d]",
			dataKey.DatasetId, dataKey.ObjectId, s.cfg.Adapter.DefaultEntityID)
		return s.cfg.Adapter.DefaultEntityID
	}

	log.Debugf("getEntityIDFromDataKey: 数据集[%d]对象[%s]映射到实体ID[%d]",
		dataKey.DatasetId, dataKey.ObjectId, entityID)
	return uint32(entityID)
}

// genDropErrorRsp 生成删除表错误响应
func (s *DBTableManagerServiceImpl) genDropErrorRsp(code pb.EnumErrorCode, msg string) *pb.DropDatabaseTableRsp {
	return &pb.DropDatabaseTableRsp{
		RetInfo: &pb.RetInfo{
			Code: code,
			Msg:  msg,
		},
	}
}

// genCheckErrorRsp 生成检查表错误响应
func (s *DBTableManagerServiceImpl) genCheckErrorRsp(code pb.EnumErrorCode, msg string) *pb.CheckDatabaseTableRsp {
	return &pb.CheckDatabaseTableRsp{
		RetInfo: &pb.RetInfo{
			Code: code,
			Msg:  msg,
		},
		TableExists: false,
	}
}

// genErrorRsp 生成错误响应
func (s *DBTableManagerServiceImpl) genErrorRsp(code pb.EnumErrorCode, msg string) *pb.CreateDatabaseTableRsp {
	return &pb.CreateDatabaseTableRsp{
		RetInfo: &pb.RetInfo{
			Code: code,
			Msg:  msg,
		},
		TableId: "",
	}
}
