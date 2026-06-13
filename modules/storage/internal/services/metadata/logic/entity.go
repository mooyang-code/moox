package logic

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
	"github.com/mooyang-code/moox/modules/storage/internal/services/common/errors"
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao/model"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

// CreateStorageEntity 创建存储实体
func (x *MetaServicerImpl) CreateStorageEntity(ctx context.Context, req *pb.CreateStorageEntityReq) (*pb.CreateStorageEntityRsp, error) {
	log.InfoContextf(ctx, "CreateStorageEntity enter:%+v", req)
	rsp := &pb.CreateStorageEntityRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "创建存储实体成功",
		},
	}
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "CreateStorageEntity failed: %s", rsp.RetInfo.Msg)
		}
	}()

	// 参数校验
	if req.GetEntityAlias() == "" {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("存储实体别名不能为空"))
		return rsp, nil
	}
	if req.GetEntitySrvConn() == "" {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("存储实体连接信息不能为空"))
		return rsp, nil
	}

	// 生成新的存储实体ID
	maxEntityID, err := x.dbDAO.GetMaxEntityID()
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_INNER_ERR
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INNER_ERR, err)
		return rsp, nil
	}
	newEntityID := maxEntityID + 1

	// 构建存储实体模型
	entity := &model.StorageEntity{
		EntityID:      newEntityID,
		EntityAlias:   req.GetEntityAlias(),
		EntitySrvConn: req.GetEntitySrvConn(),
		Enabled:       constants.EnabledValue,
		CreateTime:    time.Now().Format("2006-01-02 15:04:05"),
		ModifyTime:    time.Now().Format("2006-01-02 15:04:05"),
	}

	// 保存到数据库
	if err := x.dbDAO.AddEntity(entity); err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_UPDATE
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err)
		return rsp, nil
	}

	// 创建默认数据对象路由：为系统中所有数据集创建路由到新存储实体的默认条目
	x.createDefaultObjectRoutes(ctx, newEntityID)

	rsp.EntityId = uint32(newEntityID)
	log.InfoContextf(ctx, "CreateStorageEntity response: %+v", rsp)
	return rsp, nil
}

// UpdateStorageEntity 更新存储实体（只能修改别名）
func (x *MetaServicerImpl) UpdateStorageEntity(ctx context.Context, req *pb.UpdateStorageEntityReq) (*pb.UpdateStorageEntityRsp, error) {
	log.InfoContextf(ctx, "UpdateStorageEntity enter:%+v", req)
	rsp := &pb.UpdateStorageEntityRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "更新存储实体成功",
		},
	}
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "UpdateStorageEntity failed: %s", rsp.RetInfo.Msg)
		}
	}()

	// 参数校验
	if req.GetEntityId() <= 0 {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("存储实体ID必须大于0"))
		return rsp, nil
	}
	if req.GetEntityAlias() == "" {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("存储实体别名不能为空"))
		return rsp, nil
	}

	// 检查存储实体是否存在
	existEntity, err := x.dbDAO.GetEntityByID(int(req.GetEntityId()))
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("存储实体不存在"))
		return rsp, nil
	}

	// 更新字段（只允许修改别名）
	existEntity.EntityAlias = req.GetEntityAlias()
	existEntity.ModifyTime = time.Now().Format("2006-01-02 15:04:05")

	// 保存到数据库
	if err := x.dbDAO.UpdateEntity(existEntity); err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_UPDATE
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err)
		return rsp, nil
	}

	log.InfoContextf(ctx, "UpdateStorageEntity response: %+v", rsp)
	return rsp, nil
}

// DeleteStorageEntity 删除存储实体（检查路由引用）
func (x *MetaServicerImpl) DeleteStorageEntity(ctx context.Context, req *pb.DeleteStorageEntityReq) (*pb.DeleteStorageEntityRsp, error) {
	log.InfoContextf(ctx, "DeleteStorageEntity enter:%+v", req)
	rsp := &pb.DeleteStorageEntityRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "删除存储实体成功",
		},
	}
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "DeleteStorageEntity failed: %s", rsp.RetInfo.Msg)
		}
	}()

	// 参数校验
	if req.GetEntityId() <= 0 {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("存储实体ID必须大于0"))
		return rsp, nil
	}

	entityID := int(req.GetEntityId())

	// 检查存储实体是否存在
	_, err := x.dbDAO.GetEntityByID(entityID)
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("存储实体不存在"))
		return rsp, nil
	}

	// 检查是否被数据对象路由引用
	isReferencedByObjectRoute, err := x.dbDAO.IsEntityReferencedByObjectRoute(entityID)
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_INNER_ERR
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INNER_ERR, err)
		return rsp, nil
	}
	if isReferencedByObjectRoute {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("存储实体已被数据对象路由引用，无法删除"))
		return rsp, nil
	}

	// 执行禁用
	if err := x.dbDAO.DeleteEntity(entityID); err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_UPDATE
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err)
		return rsp, nil
	}

	log.InfoContextf(ctx, "DeleteStorageEntity response: %+v", rsp)
	return rsp, nil
}

// ListStorageEntities 列出所有存储实体
func (x *MetaServicerImpl) ListStorageEntities(ctx context.Context, req *pb.ListStorageEntitiesReq) (*pb.ListStorageEntitiesRsp, error) {
	log.InfoContextf(ctx, "ListStorageEntities enter:%+v", req)
	rsp := &pb.ListStorageEntitiesRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "获取存储实体列表成功",
		},
		Entities: []*pb.StorageEntityInfo{},
	}
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "ListStorageEntities failed: %s", rsp.RetInfo.Msg)
		}
	}()

	// 从数据库获取存储实体列表
	entities, err := x.dbDAO.GetEntityList()
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_INNER_ERR
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INNER_ERR, err)
		return rsp, nil
	}

	// 转换为proto格式
	for _, entity := range entities {
		entityInfo := &pb.StorageEntityInfo{
			EntityId:      uint32(entity.EntityID),
			EntityAlias:   entity.EntityAlias,
			EntitySrvConn: entity.EntitySrvConn,
			Ctime:         entity.CreateTime,
			Mtime:         entity.ModifyTime,
			Enabled:       entity.Enabled,
		}
		rsp.Entities = append(rsp.Entities, entityInfo)
	}

	log.InfoContextf(ctx, "ListStorageEntities response: count=%d", len(rsp.Entities))
	return rsp, nil
}

// createDefaultObjectRoutes 为新创建的存储实体创建默认数据对象路由
// 为系统中所有数据集创建默认路由条目，路由到指定的存储实体
// 如果创建失败，仅记录日志，不影响存储实体的创建
func (x *MetaServicerImpl) createDefaultObjectRoutes(ctx context.Context, entityID int) {
	log.InfoContextf(ctx, "开始为存储实体[%d]创建默认数据对象路由", entityID)

	// 获取系统中所有数据集
	datasets, err := x.dbDAO.GetDatasetList()
	if err != nil {
		log.ErrorContextf(ctx, "获取数据集列表失败，跳过创建默认路由: %v", err)
		return
	}

	log.InfoContextf(ctx, "找到%d个数据集，开始创建默认路由", len(datasets))

	// 为每个数据集创建默认路由
	successCount := 0
	for _, dataset := range datasets {
		// 构造创建数据对象路由的请求
		routeReq := &pb.CreateObjectRouteReq{
			DatasetId: uint32(dataset.DatasetID),
			ObjectId:  "*", // 使用"*"表示默认路由
			EntityId:  uint32(entityID),
		}

		// 调用内部方法创建路由，忽略错误
		_, err := x.CreateObjectRoute(ctx, routeReq)
		if err != nil {
			log.WarnContextf(ctx, "为数据集[%d]创建默认路由失败，忽略错误: %v", dataset.DatasetID, err)
		} else {
			successCount++
			log.DebugContextf(ctx, "为数据集[%d]成功创建默认路由到存储实体[%d]", dataset.DatasetID, entityID)
		}
	}

	log.InfoContextf(ctx, "默认数据对象路由创建完成，成功创建%d个，总共%d个数据集", successCount, len(datasets))
}
