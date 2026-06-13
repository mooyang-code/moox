package logic

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
	"github.com/mooyang-code/moox/modules/storage/internal/services/common/errors"
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao"
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao/model"
	"github.com/mooyang-code/moox/modules/storage/internal/toolkit/utils"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

// validateError 验证错误
type validateError struct {
	Code pb.EnumErrorCode
	Msg  string
}

func (e *validateError) Error() string {
	return e.Msg
}

// CreateProject 创建项目
func (x *MetaServicerImpl) CreateProject(ctx context.Context, req *pb.CreateProjectReq) (*pb.CreateProjectRsp, error) {
	log.InfoContextf(ctx, "CreateProject enter:%+v", req)

	// 初始化响应 - 使用RetInfo结构
	rsp := &pb.CreateProjectRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS, // 0表示成功
			Msg:  "项目创建成功",
		},
	}

	// 使用 defer 处理错误返回
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "CreateProject failed: %s", rsp.RetInfo.Msg)
		}
	}()

	// 1. 参数校验
	if err := validateCreateProjectReq(req); err != nil {
		rsp.RetInfo.Code = err.Code
		rsp.RetInfo.Msg = err.Msg
		return rsp, nil
	}

	// 2. 分配项目ID
	maxProjID, err := x.dbDAO.GetMaxProjectID()
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_UPDATE
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err)
		return rsp, nil
	}
	projID := maxProjID + 1

	// 检查项目ID是否超出uint32范围
	if projID < 0 || projID > int(^uint32(0)) {
		rsp.RetInfo.Code = pb.EnumErrorCode_INNER_ERR
		rsp.RetInfo.Msg = "项目ID超出范围"
		return rsp, nil
	}

	// 3. 创建数据集信息
	dataset, validateErr := createDataset(x.dbDAO, req, projID)
	if validateErr != nil {
		rsp.RetInfo.Code = validateErr.Code
		rsp.RetInfo.Msg = validateErr.Msg
		return rsp, nil
	}

	// 4. 创建主键字段信息
	fields, validateErr := createFields(x.dbDAO, req, projID, dataset.DatasetID)
	if validateErr != nil {
		rsp.RetInfo.Code = validateErr.Code
		rsp.RetInfo.Msg = validateErr.Msg
		return rsp, nil
	}

	// 5. 保存到数据库（项目、数据集、字段信息写入DB）
	if err := saveToDatabase(x.dbDAO, projID, req.GetProjName(), req.GetRemark(), dataset, fields); err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_UPDATE
		rsp.RetInfo.Msg = err.Msg
		return rsp, nil
	}

	// 6. 设置返回的项目ID
	rsp.ProjId = uint32(projID)
	log.InfoContextf(ctx, "CreateProject response: %+v", rsp)
	return rsp, nil
}

// validateCreateProjectReq 验证创建项目请求
func validateCreateProjectReq(req *pb.CreateProjectReq) *validateError {
	if req.GetProjName() == "" {
		return &validateError{
			Code: pb.EnumErrorCode_INVALID_PARAM,
			Msg:  errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("项目名称不能为空")),
		}
	}
	return nil
}

// createDataset 创建数据集
func createDataset(dbDAO dao.DataInterfacer, req *pb.CreateProjectReq, projID int) (*model.Dataset, *validateError) {
	// 计算数据集ID范围
	minDatasetID := projID * 100
	maxDatasetID := (projID+1)*100 - 1

	// 验证数据集信息
	if req.GetDataset() == nil {
		return nil, &validateError{
			Code: pb.EnumErrorCode_INVALID_PARAM,
			Msg:  errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("数据集信息不能为空")),
		}
	}

	if req.GetDataset().GetDatasetName() == "" {
		return nil, &validateError{
			Code: pb.EnumErrorCode_INVALID_PARAM,
			Msg:  errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("数据集名称不能为空")),
		}
	}

	// 获取当前范围内的最大数据集ID
	maxID, err := dbDAO.GetMaxDatasetIDInRange(minDatasetID, maxDatasetID)
	if err != nil {
		return nil, &validateError{
			Code: pb.EnumErrorCode_FAILED_UPDATE,
			Msg:  errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err),
		}
	}

	// 如果范围内没有数据集，使用最小值
	datasetID := minDatasetID
	if maxID >= minDatasetID {
		datasetID = maxID + 1
	}

	// 转换数据类型
	dataType := 0
	switch req.GetDataset().GetDataType() {
	case pb.DataSetType_STATIC:
		dataType = 1
	case pb.DataSetType_TIME_SERIES:
		dataType = 2
	}
	objectTableID := utils.GenObjectTableID(int32(datasetID))
	dataTableID := utils.GenDataTableID(int32(datasetID), "", "") // 基础数据表ID，不包含对象ID和频率

	dataset := &model.Dataset{
		DatasetID:     datasetID,
		DatasetName:   req.GetDataset().GetDatasetName(),
		ObjectTableID: objectTableID,
		DataTableID:   dataTableID,
		ProjID:        projID,
		DataType:      dataType,
		Freqs:         req.GetDataset().GetTimeSeriesPeriod(),
		CheckRules:    req.GetDataset().GetValidationRule(),
		Comment:       req.GetDataset().GetRemark(),
		Enabled:       constants.EnabledValue,
		CreateTime:    time.Now().Format("2006-01-02 15:04:05"),
		ModifyTime:    time.Now().Format("2006-01-02 15:04:05"),
	}
	return dataset, nil
}

// createFields 创建字段
func createFields(dbDAO dao.DataInterfacer, req *pb.CreateProjectReq, projID int, datasetID int) ([]*model.Field, *validateError) {
	var fields []*model.Field

	// 计算字段ID范围
	minFieldID := projID * 1000
	maxFieldID := (projID+1)*1000 - 1

	for _, field := range req.GetFields() {
		if field.GetFieldName() == "" {
			return nil, &validateError{
				Code: pb.EnumErrorCode_INVALID_PARAM,
				Msg:  errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("字段名称不能为空")),
			}
		}
		if field.GetInterfaceName() == "" {
			return nil, &validateError{
				Code: pb.EnumErrorCode_INVALID_PARAM,
				Msg:  errors.GetErrMsg(pb.EnumErrorCode_INVALID_PARAM, fmt.Errorf("字段接口名不能为空")),
			}
		}

		// 获取当前范围内的最大字段ID
		maxID, err := dbDAO.GetMaxFieldIDInRange(minFieldID, maxFieldID)
		if err != nil {
			return nil, &validateError{
				Code: pb.EnumErrorCode_FAILED_UPDATE,
				Msg:  errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err),
			}
		}

		// 如果范围内没有字段，使用最小值
		fieldID := minFieldID
		if maxID >= minFieldID {
			fieldID = maxID + 1
		}

		required := field.GetRequired()
		if required == "" {
			required = constants.DisabledValue
		}
		unique := field.GetUnique()
		if unique == "" {
			unique = constants.DisabledValue
		}

		fieldModel := &model.Field{
			FieldID:              fieldID,
			ProjID:               projID,
			DatasetIDs:           fmt.Sprintf("%d", datasetID),
			FieldName:            field.GetFieldName(),
			InterfaceName:        field.GetInterfaceName(),
			Desc:                 field.GetDesc(),
			Required:             required,
			Unique:               unique,
			TableType:            int(field.GetTableType()),
			FieldPrimaryFormat:   int(field.GetFieldPrimaryFormat()),
			FieldSecondaryFormat: int(field.GetFieldSecondaryFormat()),
			ValidationRule:       field.GetValidationRule(),
			WriteExample:         field.GetWriteExample(),
			Remark:               field.GetRemark(),
			Enabled:              constants.EnabledValue,
			CreateTime:           time.Now(),
			ModifyTime:           time.Now(),
		}
		fields = append(fields, fieldModel)
	}
	return fields, nil
}

// saveToDatabase 保存到数据库（使用事务确保数据一致性）
func saveToDatabase(dbDAO dao.DataInterfacer, projID int, projName, remark string, dataset *model.Dataset, fields []*model.Field) *validateError {
	// 开始事务
	tx, err := dbDAO.BeginTx()
	if err != nil {
		return &validateError{
			Code: pb.EnumErrorCode_FAILED_UPDATE,
			Msg:  errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err),
		}
	}

	// 使用 defer 确保事务在出现 panic 时回滚
	var panicErr *validateError
	defer func() {
		if r := recover(); r != nil {
			// 记录 panic 信息
			log.ErrorContextf(context.Background(), "saveToDatabase panic: %v", r)
			// 尝试回滚事务
			if rollbackErr := dbDAO.RollbackTx(tx); rollbackErr != nil {
				log.ErrorContextf(context.Background(), "RollbackTx failed after panic: %v", rollbackErr)
			}
			// 将 panic 转换为错误
			panicErr = &validateError{
				Code: pb.EnumErrorCode_FAILED_UPDATE,
				Msg:  errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, fmt.Errorf("数据库操作发生异常: %v", r)),
			}
		}
	}()

	// 1. 添加项目
	if err := dbDAO.AddProjectWithTx(tx, projID, projName, remark); err != nil {
		dbDAO.RollbackTx(tx)
		return &validateError{
			Code: pb.EnumErrorCode_FAILED_UPDATE,
			Msg:  errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err),
		}
	}

	// 2. 添加数据集
	if err := dbDAO.AddDatasetWithTx(tx, dataset); err != nil {
		dbDAO.RollbackTx(tx)
		return &validateError{
			Code: pb.EnumErrorCode_FAILED_UPDATE,
			Msg:  errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err),
		}
	}

	// 3. 添加字段
	for _, field := range fields {
		if err := dbDAO.AddFieldWithTx(tx, field); err != nil {
			dbDAO.RollbackTx(tx)
			return &validateError{
				Code: pb.EnumErrorCode_FAILED_UPDATE,
				Msg:  errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err),
			}
		}
	}

	// 提交事务
	if err := dbDAO.CommitTx(tx); err != nil {
		return &validateError{
			Code: pb.EnumErrorCode_FAILED_UPDATE,
			Msg:  errors.GetErrMsg(pb.EnumErrorCode_FAILED_UPDATE, err),
		}
	}

	// 检查是否有 panic 错误
	if panicErr != nil {
		return panicErr
	}
	return nil
}

// boolToInt 将bool转换为int
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// UpdateProject 更新项目信息
func (x *MetaServicerImpl) UpdateProject(ctx context.Context, req *pb.UpdateProjectReq) (*pb.UpdateProjectRsp, error) {
	log.InfoContextf(ctx, "UpdateProject enter:%+v", req)

	// 初始化响应 - 使用RetInfo结构
	rsp := &pb.UpdateProjectRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS, // 0表示成功
			Msg:  "项目更新成功",
		},
	}

	// 使用 defer 处理错误返回
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "UpdateProject failed: %s", rsp.RetInfo.Msg)
		}
	}()

	// 1. 参数校验
	if req.GetProjId() <= 0 {
		rsp.RetInfo.Code = pb.EnumErrorCode_INVALID_PARAM
		rsp.RetInfo.Msg = "项目ID必须大于0"
		return rsp, nil
	}

	// 2. 更新项目信息
	if err := x.dbDAO.UpdateProject(int(req.GetProjId()), "", req.GetRemark()); err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_UPDATE
		rsp.RetInfo.Msg = err.Error()
		return rsp, nil
	}

	log.InfoContextf(ctx, "UpdateProject suc, projID: %d", req.GetProjId())
	log.InfoContextf(ctx, "UpdateProject response: %+v", rsp)
	return rsp, nil
}

// ListProjects 拉取项目信息列表
func (x *MetaServicerImpl) ListProjects(ctx context.Context, req *pb.ListProjectsReq) (*pb.ListProjectsRsp, error) {
	log.InfoContextf(ctx, "ListProjects enter:%+v", req)

	// 初始化响应 - 使用RetInfo结构
	rsp := &pb.ListProjectsRsp{
		RetInfo: &pb.RetInfo{
			Code: pb.EnumErrorCode_SUCCESS, // 0表示成功
			Msg:  "项目列表获取成功",
		},
	}

	// 使用 defer 处理错误返回
	defer func() {
		if rsp.RetInfo.Code != pb.EnumErrorCode_SUCCESS {
			log.ErrorContextf(ctx, "ListProjects failed: %s", rsp.RetInfo.Msg)
		}
	}()

	// 1. 从数据库获取项目列表
	projects, err := x.dbDAO.GetProjectList()
	if err != nil {
		rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_SELECT
		rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_SELECT, err)
		return rsp, nil
	}

	// 2. 转换为 proto 格式
	protoProjects := make([]*pb.Project, 0, len(projects))
	for _, proj := range projects {
		// 获取项目下的数据集列表
		datasets, err := x.dbDAO.GetDatasetByProjID(proj.ProjID)
		if err != nil {
			rsp.RetInfo.Code = pb.EnumErrorCode_FAILED_SELECT
			rsp.RetInfo.Msg = errors.GetErrMsg(pb.EnumErrorCode_FAILED_SELECT, err)
			return rsp, nil
		}

		// 转换为 proto 格式的数据集列表
		protoDatasets := make([]*pb.DataSetMetaInfo, 0, len(datasets))
		for _, ds := range datasets {
			// 转换数据类型
			dataType := pb.DataSetType_STATIC
			if ds.DataType == 2 {
				dataType = pb.DataSetType_TIME_SERIES
			}

			protoDataset := &pb.DataSetMetaInfo{
				DatasetId:        uint32(ds.DatasetID),
				DatasetName:      ds.DatasetName,
				DataType:         dataType,
				TimeSeriesPeriod: ds.Freqs,
				ValidationRule:   ds.CheckRules,
				Remark:           ds.Comment,
			}
			protoDatasets = append(protoDatasets, protoDataset)
		}

		// 转换创建时间为字符串格式
		createTime := proj.CreateTime.Format("2006-01-02 15:04:05")
		protoProject := &pb.Project{
			Id:         uint32(proj.ProjID),
			Name:       proj.ProjName,
			Remark:     proj.Remark,
			CreateTime: createTime,
			Datasets:   protoDatasets,
		}
		protoProjects = append(protoProjects, protoProject)
	}

	rsp.Projects = protoProjects
	log.InfoContextf(ctx, "ListProjects suc, count: %d", len(protoProjects))
	log.InfoContextf(ctx, "ListProjects response: %+v", rsp)
	return rsp, nil
}
