package logic

import (
	"context"
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

// CreateDataSet 创建数据集
func (x *MetaServicerImpl) CreateDataSet(ctx context.Context, req *pb.CreateDataSetReq) (*pb.CreateDataSetRsp, error) {
	log.InfoContextf(ctx, "CreateDataSet enter:%+v", req)
	rsp := &pb.CreateDataSetRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "数据集创建成功",
		},
	}
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "CreateDataSet failed: %s", rsp.RetInfo.Msg)
		}
	}()

	// 1. 参数校验
	if err := validateCreateDataSetReq(req); err != nil {
		rsp.RetInfo.Code = err.Code
		rsp.RetInfo.Msg = err.Msg
		return rsp, nil
	}

	// 2. 验证项目是否存在
	project, err := x.dbDAO.GetProjectByID(int(req.GetProjId()))
	if err != nil || project == nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("项目不存在"))
		return rsp, nil
	}

	// 3. 计算数据集ID
	projID := int(req.GetProjId())
	minDatasetID := projID * 100
	maxDatasetID := (projID+1)*100 - 1

	// 获取当前范围内的最大数据集ID
	maxID, err := x.dbDAO.GetMaxDatasetIDInRange(minDatasetID, maxDatasetID)
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_UPDATE
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err)
		return rsp, nil
	}

	// 计算新的数据集ID
	datasetID := minDatasetID
	if maxID >= minDatasetID {
		datasetID = maxID + 1
	}

	// 4. 生成表ID
	objectTableID := utils.GenObjectTableID(int32(datasetID))
	dataTableID := utils.GenDataTableID(int32(datasetID), "", "") // 基础数据表ID，不包含对象ID和频率

	// 5. 创建数据集对象
	dataset := &model.Dataset{
		DatasetID:     datasetID,
		DatasetName:   req.GetDatasetName(),
		ObjectTableID: objectTableID,
		DataTableID:   dataTableID,
		ProjID:        projID,
		DataType:      int(req.GetDataType()),
		Freqs:         req.GetFreqs(),
		CheckRules:    req.GetCheckRules(),
		Comment:       req.GetComment(),
		Enabled:       constants.EnabledValue,
		CreateTime:    time.Now().Format("2006-01-02 15:04:05"),
		ModifyTime:    time.Now().Format("2006-01-02 15:04:05"),
	}

	// 6. 保存到数据库
	if err := x.dbDAO.AddDataset(dataset); err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_UPDATE
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err)
		return rsp, nil
	}

	// 7. 设置返回的数据集ID
	rsp.DatasetId = uint32(datasetID)
	log.InfoContextf(ctx, "CreateDataSet response: %+v", rsp)
	return rsp, nil
}

// UpdateDataSet 更新数据集
func (x *MetaServicerImpl) UpdateDataSet(ctx context.Context, req *pb.UpdateDataSetReq) (*pb.UpdateDataSetRsp, error) {
	log.InfoContextf(ctx, "UpdateDataSet enter:%+v", req)
	rsp := &pb.UpdateDataSetRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "数据集更新成功",
		},
	}
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "UpdateDataSet failed: %s", rsp.RetInfo.Msg)
		}
	}()

	// 1. 参数校验
	if err := validateUpdateDataSetReq(req); err != nil {
		rsp.RetInfo.Code = err.Code
		rsp.RetInfo.Msg = err.Msg
		return rsp, nil
	}

	// 2. 获取现有数据集
	dataset, err := x.dbDAO.GetDatasetByID(int(req.GetDatasetId()))
	if err != nil || dataset == nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_DATA_SET
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_DATA_SET, fmt.Errorf("数据集不存在"))
		return rsp, nil
	}

	// 3. 验证项目ID是否匹配
	if dataset.ProjID != int(req.GetProjId()) {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("数据集不属于指定项目"))
		return rsp, nil
	}

	// 4. 更新允许修改的字段
	if req.DatasetName != nil {
		dataset.DatasetName = req.GetDatasetName()
	}
	if req.Freqs != nil {
		dataset.Freqs = req.GetFreqs()
	}
	if req.CheckRules != nil {
		dataset.CheckRules = req.GetCheckRules()
	}
	if req.Comment != nil {
		dataset.Comment = req.GetComment()
	}

	// 5. 更新表ID（基于数据集ID重新生成）
	dataset.ObjectTableID = utils.GenObjectTableID(int32(dataset.DatasetID))
	dataset.DataTableID = utils.GenDataTableID(int32(dataset.DatasetID), "", "") // 基础数据表ID
	dataset.ModifyTime = time.Now().Format("2006-01-02 15:04:05")

	// 6. 保存到数据库
	if err := x.dbDAO.UpdateDataset(dataset); err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_UPDATE
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err)
		return rsp, nil
	}
	log.InfoContextf(ctx, "UpdateDataSet response: %+v", rsp)
	return rsp, nil
}

// DeleteDataSet 删除数据集
func (x *MetaServicerImpl) DeleteDataSet(ctx context.Context, req *pb.DeleteDataSetReq) (*pb.DeleteDataSetRsp, error) {
	log.InfoContextf(ctx, "DeleteDataSet enter:%+v", req)
	rsp := &pb.DeleteDataSetRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS,
			Msg:  "数据集删除成功",
		},
	}
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "DeleteDataSet failed: %s", rsp.RetInfo.Msg)
		}
	}()

	// 1. 参数校验
	if req.GetProjId() == 0 || req.GetDatasetId() == 0 {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("项目ID和数据集ID不能为空"))
		return rsp, nil
	}

	// 2. 获取数据集信息
	dataset, err := x.dbDAO.GetDatasetByID(int(req.GetDatasetId()))
	if err != nil || dataset == nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_DATA_SET
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_DATA_SET, fmt.Errorf("数据集不存在"))
		return rsp, nil
	}

	// 3. 验证项目ID是否匹配
	if dataset.ProjID != int(req.GetProjId()) {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("数据集不属于指定项目"))
		return rsp, nil
	}

	// 4. 开始事务
	tx, err := x.dbDAO.BeginTx()
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_INNER_ERR
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INNER_ERR, err)
		return rsp, nil
	}

	// 确保事务结束时回滚或提交
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			x.dbDAO.RollbackTx(tx)
		}
	}()

	// 5. 禁用数据集（设置Enabled=false）
	if err := x.dbDAO.DeleteDataset(int(req.GetDatasetId())); err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_UPDATE
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err)
		return rsp, nil
	}

	// 6. 清理字段关联关系
	if err := x.cleanupFieldDatasetRelations(ctx, int(req.GetDatasetId())); err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_UPDATE
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, fmt.Errorf("清理字段关联关系失败: %v", err))
		return rsp, nil
	}

	// 7. 提交事务
	if err := x.dbDAO.CommitTx(tx); err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_INNER_ERR
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_INNER_ERR, err)
		return rsp, nil
	}

	log.InfoContextf(ctx, "DeleteDataSet response: %+v", rsp)
	return rsp, nil
}

// validateCreateDataSetReq 验证创建数据集请求
func validateCreateDataSetReq(req *pb.CreateDataSetReq) *validateError {
	if req.GetProjId() == 0 {
		return &validateError{
			Code: pb.EnumErrorCode_INVALID_PARAM,
			Msg:  errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("项目ID不能为空")),
		}
	}
	if req.GetDatasetName() == "" {
		return &validateError{
			Code: pb.EnumErrorCode_INVALID_PARAM,
			Msg:  errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("数据集名称不能为空")),
		}
	}
	if req.GetDataType() == 0 {
		return &validateError{
			Code: pb.EnumErrorCode_INVALID_PARAM,
			Msg:  errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("数据类型不能为空")),
		}
	}
	return nil
}

// validateUpdateDataSetReq 验证更新数据集请求
func validateUpdateDataSetReq(req *pb.UpdateDataSetReq) *validateError {
	if req.GetProjId() == 0 {
		return &validateError{
			Code: pb.EnumErrorCode_INVALID_PARAM,
			Msg:  errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("项目ID不能为空")),
		}
	}
	if req.GetDatasetId() == 0 {
		return &validateError{
			Code: pb.EnumErrorCode_INVALID_PARAM,
			Msg:  errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("数据集ID不能为空")),
		}
	}
	// 至少要有一个字段需要更新
	if req.DatasetName == nil && req.Freqs == nil && req.CheckRules == nil && req.Comment == nil {
		return &validateError{
			Code: pb.EnumErrorCode_INVALID_PARAM,
			Msg:  errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("至少需要更新一个字段")),
		}
	}
	return nil
}

// cleanupFieldDatasetRelations 清理字段与数据集的关联关系
func (x *MetaServicerImpl) cleanupFieldDatasetRelations(ctx context.Context, datasetID int) error {
	// 获取所有字段
	fields, err := x.dbDAO.GetValidFieldList()
	if err != nil {
		return fmt.Errorf("获取字段列表失败: %v", err)
	}
	datasetIDStr := strconv.Itoa(datasetID)

	// 遍历字段，清理包含该数据集ID的字段
	for _, field := range fields {
		if field.DatasetIDs == "" {
			continue
		}

		// 检查字段是否关联了要删除的数据集
		datasetIDList := strings.Split(field.DatasetIDs, "+")
		var newDatasetIDs []string
		changed := false

		for _, id := range datasetIDList {
			id = strings.TrimSpace(id)
			if id != datasetIDStr && id != "" {
				newDatasetIDs = append(newDatasetIDs, id)
			} else if id == datasetIDStr {
				changed = true
			}
		}

		// 如果有变化，更新字段的DatasetIDs
		if changed {
			field.DatasetIDs = strings.Join(newDatasetIDs, "+")
			if err := x.dbDAO.UpdateField(&field); err != nil {
				log.ErrorContextf(ctx, "更新字段%d的数据集关联失败: %v", field.FieldID, err)
				return fmt.Errorf("更新字段%d的数据集关联失败: %v", field.FieldID, err)
			}
			log.InfoContextf(ctx, "清理字段%d的数据集%d关联关系成功", field.FieldID, datasetID)
		}
	}
	return nil
}
