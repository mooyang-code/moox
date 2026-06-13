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

// CreateObjectRoute 创建数据对象路由
func (x *MetaServicerImpl) CreateObjectRoute(ctx context.Context, req *pb.CreateObjectRouteReq) (*pb.CreateObjectRouteRsp, error) {
	log.InfoContextf(ctx, "CreateObjectRoute enter:%+v", req)
	rsp := &pb.CreateObjectRouteRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "创建数据对象路由成功",
		},
	}
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "CreateObjectRoute failed: %s", rsp.RetInfo.Msg)
		}
	}()

	// 参数校验
	if req.GetProjectId() <= 0 {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("项目ID必须大于0"))
		return rsp, nil
	}
	if req.GetDatasetId() <= 0 {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("数据集ID必须大于0"))
		return rsp, nil
	}
	if req.GetObjectId() == "" {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("数据对象ID不能为空"))
		return rsp, nil
	}
	if req.GetEntityId() <= 0 {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("存储实体ID必须大于0"))
		return rsp, nil
	}

	// 验证数据集是否存在
	_, err := x.dbDAO.GetDatasetByID(int(req.GetDatasetId()))
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_DATA_SET
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_DATA_SET, fmt.Errorf("数据集不存在"))
		return rsp, nil
	}

	// 验证存储实体是否存在
	_, err = x.dbDAO.GetEntityByID(int(req.GetEntityId()))
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("存储实体不存在"))
		return rsp, nil
	}

	// 构建数据对象路由模型
	route := &model.ObjectRoute{
		ProjectID:  int(req.GetProjectId()),
		DatasetID:  int(req.GetDatasetId()),
		ObjectID:   req.GetObjectId(),
		EntityID:   int(req.GetEntityId()),
		Enabled:    constants.EnabledValue,
		CreateTime: time.Now().Format("2006-01-02 15:04:05"),
		ModifyTime: time.Now().Format("2006-01-02 15:04:05"),
	}

	// 保存到数据库
	if err := x.dbDAO.AddObjectRoute(route); err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_UPDATE
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err)
		return rsp, nil
	}

	// GORM会自动设置ID字段
	rsp.RouteId = uint32(route.ID)
	log.InfoContextf(ctx, "CreateObjectRoute response: %+v", rsp)
	return rsp, nil
}

// UpdateObjectRoute 更新数据对象路由
func (x *MetaServicerImpl) UpdateObjectRoute(ctx context.Context, req *pb.UpdateObjectRouteReq) (*pb.UpdateObjectRouteRsp, error) {
	log.InfoContextf(ctx, "UpdateObjectRoute enter:%+v", req)
	rsp := &pb.UpdateObjectRouteRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "更新数据对象路由成功",
		},
	}
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "UpdateObjectRoute failed: %s", rsp.RetInfo.Msg)
		}
	}()

	// 参数校验
	if req.GetRouteId() <= 0 {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("路由ID必须大于0"))
		return rsp, nil
	}

	// 获取现有路由
	existRoute, err := x.dbDAO.GetObjectRouteByID(int(req.GetRouteId()))
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("数据对象路由不存在"))
		return rsp, nil
	}

	// 更新允许修改的字段
	if req.DatasetId != nil {
		// 验证数据集是否存在
		_, err := x.dbDAO.GetDatasetByID(int(req.GetDatasetId()))
		if err != nil {
			rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_DATA_SET
			rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_DATA_SET, fmt.Errorf("数据集不存在"))
			return rsp, nil
		}
		existRoute.DatasetID = int(req.GetDatasetId())
	}
	if req.ObjectId != nil {
		existRoute.ObjectID = req.GetObjectId()
	}
	if req.EntityId != nil {
		// 验证存储实体是否存在
		_, err := x.dbDAO.GetEntityByID(int(req.GetEntityId()))
		if err != nil {
			rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
			rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("存储实体不存在"))
			return rsp, nil
		}
		existRoute.EntityID = int(req.GetEntityId())
	}
	existRoute.ModifyTime = time.Now().Format("2006-01-02 15:04:05")

	// 保存到数据库
	if err := x.dbDAO.UpdateObjectRoute(existRoute); err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_UPDATE
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err)
		return rsp, nil
	}

	log.InfoContextf(ctx, "UpdateObjectRoute response: %+v", rsp)
	return rsp, nil
}

// DeleteObjectRoute 删除数据对象路由
func (x *MetaServicerImpl) DeleteObjectRoute(ctx context.Context, req *pb.DeleteObjectRouteReq) (*pb.DeleteObjectRouteRsp, error) {
	log.InfoContextf(ctx, "DeleteObjectRoute enter:%+v", req)
	rsp := &pb.DeleteObjectRouteRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "删除数据对象路由成功",
		},
	}
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "DeleteObjectRoute failed: %s", rsp.RetInfo.Msg)
		}
	}()

	// 参数校验
	if req.GetRouteId() <= 0 {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("路由ID必须大于0"))
		return rsp, nil
	}

	// 检查路由是否存在
	_, err := x.dbDAO.GetObjectRouteByID(int(req.GetRouteId()))
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("数据对象路由不存在"))
		return rsp, nil
	}

	// 执行禁用
	if err := x.dbDAO.DeleteObjectRoute(int(req.GetRouteId())); err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_UPDATE
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err)
		return rsp, nil
	}

	log.InfoContextf(ctx, "DeleteObjectRoute response: %+v", rsp)
	return rsp, nil
}

// ListObjectRoutes 列出数据对象路由
func (x *MetaServicerImpl) ListObjectRoutes(ctx context.Context, req *pb.ListObjectRoutesReq) (*pb.ListObjectRoutesRsp, error) {
	log.InfoContextf(ctx, "ListObjectRoutes enter:%+v", req)
	rsp := &pb.ListObjectRoutesRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "获取数据对象路由列表成功",
		},
	}
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "ListObjectRoutes failed: %s", rsp.RetInfo.Msg)
		}
	}()

	// 设置默认分页参数
	pageSize := int(req.GetPageInfo().GetSize())
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}
	pageNum := int(req.GetPageInfo().GetPageIdx())
	if pageNum <= 0 {
		pageNum = 1
	}
	offset := (pageNum - 1) * pageSize

	// 过滤条件
	projectID := int(req.GetProjectId())
	datasetID := 0
	if req.DatasetId != nil {
		datasetID = int(req.GetDatasetId())
	}
	entityID := 0
	if req.EntityId != nil {
		entityID = int(req.GetEntityId())
	}

	// 查询路由列表
	routes, total, err := x.dbDAO.GetObjectRouteListWithFilter(projectID, datasetID, entityID, pageSize, offset)
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_SELECT
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_SELECT, err)
		return rsp, nil
	}

	// 转换为protobuf格式
	pbRoutes := make([]*pb.ObjectRouteInfo, 0, len(routes))
	for _, route := range routes {
		pbRoute := &pb.ObjectRouteInfo{
			RouteId:   uint32(route.ID),
			DatasetId: uint32(route.DatasetID),
			ObjectId:  route.ObjectID,
			EntityId:  uint32(route.EntityID),
			Ctime:     route.CreateTime,
			Mtime:     route.ModifyTime,
			Enabled:   route.Enabled,
		}
		pbRoutes = append(pbRoutes, pbRoute)
	}

	// 计算分页信息
	totalPages := (total + pageSize - 1) / pageSize
	rsp.Routes = pbRoutes
	rsp.CurPage = int32(pageNum)
	rsp.TotalPage = int32(totalPages)
	rsp.TotalNum = int32(total)

	log.InfoContextf(ctx, "ListObjectRoutes response: %+v", rsp)
	return rsp, nil
}

// CreateFieldRoute 创建字段路由
func (x *MetaServicerImpl) CreateFieldRoute(ctx context.Context, req *pb.CreateFieldRouteReq) (*pb.CreateFieldRouteRsp, error) {
	log.InfoContextf(ctx, "CreateFieldRoute enter:%+v", req)
	rsp := &pb.CreateFieldRouteRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "创建字段路由成功",
		},
	}
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "CreateFieldRoute failed: %s", rsp.RetInfo.Msg)
		}
	}()

	// 参数校验
	if req.GetProjectId() <= 0 {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("项目ID必须大于0"))
		return rsp, nil
	}
	if req.GetDatasetId() < 0 {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM,
			fmt.Errorf("数据集ID必须大于等于0，其中0表示该项目下所有的数据集"))
		return rsp, nil
	}
	// req.GetFieldId() 为999999999表示所有字段，或者为具体的字段ID
	if req.GetFieldId() <= 0 && req.GetFieldId() != uint32(constants.AllFieldsMarker) {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM,
			fmt.Errorf("字段ID必须大于0或使用%d表示所有字段", constants.AllFieldsMarker))
		return rsp, nil
	}
	if req.GetDeviceId() <= 0 {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("存储设备ID必须大于0"))
		return rsp, nil
	}

	// 验证存储设备是否存在
	_, err := x.dbDAO.GetDeviceByID(int(req.GetDeviceId()))
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_NO_DEV_CFG
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_NO_DEV_CFG, fmt.Errorf("存储设备不存在"))
		return rsp, nil
	}

	// 构建字段路由模型
	route := &model.FieldRoute{
		ProjectID:  int(req.GetProjectId()),
		FieldID:    int(req.GetFieldId()),
		DatasetID:  int(req.GetDatasetId()),
		DeviceID:   int(req.GetDeviceId()),
		Enabled:    constants.EnabledValue,
		CreateTime: time.Now().Format("2006-01-02 15:04:05"),
		ModifyTime: time.Now().Format("2006-01-02 15:04:05"),
	}

	// 保存到数据库
	if err := x.dbDAO.AddFieldRoute(route); err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_UPDATE
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err)
		return rsp, nil
	}

	// GORM会自动设置ID字段
	rsp.RouteId = uint32(route.ID)
	log.InfoContextf(ctx, "CreateFieldRoute response: %+v", rsp)
	return rsp, nil
}

// UpdateFieldRoute 更新字段路由
func (x *MetaServicerImpl) UpdateFieldRoute(ctx context.Context, req *pb.UpdateFieldRouteReq) (*pb.UpdateFieldRouteRsp, error) {
	log.InfoContextf(ctx, "UpdateFieldRoute enter:%+v", req)
	rsp := &pb.UpdateFieldRouteRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "更新字段路由成功",
		},
	}
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "UpdateFieldRoute failed: %s", rsp.RetInfo.Msg)
		}
	}()

	// 参数校验
	if req.GetRouteId() <= 0 {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("路由ID必须大于0"))
		return rsp, nil
	}

	// 获取现有路由
	existRoute, err := x.dbDAO.GetFieldRouteByID(int(req.GetRouteId()))
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("字段路由不存在"))
		return rsp, nil
	}

	// 更新允许修改的字段
	if req.FieldId != nil {
		existRoute.FieldID = int(req.GetFieldId())
	}
	if req.DatasetId != nil {
		existRoute.DatasetID = int(req.GetDatasetId())
	}
	if req.DeviceId != nil {
		// 验证存储设备是否存在
		_, err := x.dbDAO.GetDeviceByID(int(req.GetDeviceId()))
		if err != nil {
			rsp.RetInfo.Code = pb.EnumErrorCode_NO_DEV_CFG
			rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_NO_DEV_CFG, fmt.Errorf("存储设备不存在"))
			return rsp, nil
		}
		existRoute.DeviceID = int(req.GetDeviceId())
	}
	existRoute.ModifyTime = time.Now().Format("2006-01-02 15:04:05")

	// 保存到数据库
	if err := x.dbDAO.UpdateFieldRoute(existRoute); err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_UPDATE
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err)
		return rsp, nil
	}

	log.InfoContextf(ctx, "UpdateFieldRoute response: %+v", rsp)
	return rsp, nil
}

// DeleteFieldRoute 删除字段路由
func (x *MetaServicerImpl) DeleteFieldRoute(ctx context.Context, req *pb.DeleteFieldRouteReq) (*pb.DeleteFieldRouteRsp, error) {
	log.InfoContextf(ctx, "DeleteFieldRoute enter:%+v", req)
	rsp := &pb.DeleteFieldRouteRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "删除字段路由成功",
		},
	}
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "DeleteFieldRoute failed: %s", rsp.RetInfo.Msg)
		}
	}()

	// 参数校验
	if req.GetRouteId() <= 0 {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("路由ID必须大于0"))
		return rsp, nil
	}

	// 检查路由是否存在
	_, err := x.dbDAO.GetFieldRouteByID(int(req.GetRouteId()))
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("字段路由不存在"))
		return rsp, nil
	}

	// 执行禁用
	if err := x.dbDAO.DeleteFieldRoute(int(req.GetRouteId())); err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_UPDATE
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err)
		return rsp, nil
	}

	log.InfoContextf(ctx, "DeleteFieldRoute response: %+v", rsp)
	return rsp, nil
}

// ListFieldRoutes 列出字段路由
func (x *MetaServicerImpl) ListFieldRoutes(ctx context.Context, req *pb.ListFieldRoutesReq) (*pb.ListFieldRoutesRsp, error) {
	log.InfoContextf(ctx, "ListFieldRoutes enter:%+v", req)
	rsp := &pb.ListFieldRoutesRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "获取字段路由列表成功",
		},
	}
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "ListFieldRoutes failed: %s", rsp.RetInfo.Msg)
		}
	}()

	// 设置默认分页参数
	pageSize := int(req.GetPageInfo().GetSize())
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}
	pageNum := int(req.GetPageInfo().GetPageIdx())
	if pageNum <= 0 {
		pageNum = 1
	}
	offset := (pageNum - 1) * pageSize

	// 过滤条件
	projectID := int(req.GetProjectId())
	fieldID := 0
	if req.FieldId != nil {
		fieldID = int(req.GetFieldId())
	}
	datasetID := -1 // 默认值-1表示不过滤
	if req.DatasetId != nil {
		datasetID = int(req.GetDatasetId())
	}
	deviceID := 0
	if req.DeviceId != nil {
		deviceID = int(req.GetDeviceId())
	}

	// 查询路由列表
	routes, total, err := x.dbDAO.GetFieldRouteListWithFilter(projectID, fieldID, datasetID, deviceID, pageSize, offset)
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_SELECT
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_SELECT, err)
		return rsp, nil
	}

	// 转换为protobuf格式
	pbRoutes := make([]*pb.FieldRouteInfo, 0, len(routes))
	for _, route := range routes {
		pbRoute := &pb.FieldRouteInfo{
			RouteId:   uint32(route.ID),
			FieldId:   uint32(route.FieldID),
			DatasetId: uint32(route.DatasetID),
			DeviceId:  uint32(route.DeviceID),
			Ctime:     route.CreateTime,
			Mtime:     route.ModifyTime,
			Enabled:   route.Enabled,
		}
		pbRoutes = append(pbRoutes, pbRoute)
	}

	// 计算分页信息
	totalPages := (total + pageSize - 1) / pageSize
	rsp.Routes = pbRoutes
	rsp.CurPage = int32(pageNum)
	rsp.TotalPage = int32(totalPages)
	rsp.TotalNum = int32(total)

	log.InfoContextf(ctx, "ListFieldRoutes response: %+v", rsp)
	return rsp, nil
}
