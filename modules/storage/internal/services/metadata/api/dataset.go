package api

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao"
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao/model"
	"trpc.group/trpc-go/trpc-go/log"
)

// Dataset 数据集表结构
type Dataset struct {
	model.Dataset
	dbDAO dao.DataInterfacer
}

var NewDataset = func() SchemaHandler {
	var imp Dataset
	var err error
	imp.dbDAO, err = dao.NewDataInterfacer()
	if err != nil {
		log.Fatalf("NewDataset NewDataInterfacer:%+v", err)
	}
	return &imp
}

// RegisterDatasetHandler 注册数据集处理器到API入口
func RegisterDatasetHandler() {
	// 注册数据集表处理器
	GetAPIHandleInstance().Register(NewDataset())
}

// SchemaID 实现接口TBItem
func (Dataset) SchemaID() string {
	return model.DatasetTableName
}

// GetHandle 数据集表的读接口入口
func (s Dataset) GetHandle(ctx context.Context, params map[string]string) (*APIRsp, error) {
	log.DebugContextf(ctx, "HTTP-GetHandle:%s, params:%+v", s.SchemaID(), params)

	// 如果提供了项目ID，则获取特定项目的数据集
	if projIDStr, ok := params["proj_id"]; ok && projIDStr != "" {
		var projID int
		_, err := fmt.Sscanf(projIDStr, "%d", &projID)
		if err == nil && projID > 0 {
			datasets, err := s.dbDAO.GetDatasetByProjID(projID)
			if err != nil {
				log.ErrorContextf(ctx, "GetDatasetByProjID失败: %v", err)
				return &APIRsp{
					Code: 500,
					Data: []any{},
				}, err
			}

			// 将数据集列表转换为接口切片
			dataList := make([]any, 0, len(datasets))
			for _, dataset := range datasets {
				dataList = append(dataList, dataset)
			}

			return &APIRsp{
				Code: 200,
				Data: dataList,
			}, nil
		}
	}

	// 如果提供了数据类型，则获取特定类型的数据集
	if dataTypeStr, ok := params["data_type"]; ok && dataTypeStr != "" {
		var dataType int
		_, err := fmt.Sscanf(dataTypeStr, "%d", &dataType)
		if err == nil {
			datasets, err := s.dbDAO.GetDatasetByDataType(dataType)
			if err != nil {
				log.ErrorContextf(ctx, "GetDatasetByDataType失败: %v", err)
				return &APIRsp{
					Code: 500,
					Data: []any{},
				}, err
			}

			// 将数据集列表转换为接口切片
			dataList := make([]any, 0, len(datasets))
			for _, dataset := range datasets {
				dataList = append(dataList, dataset)
			}

			return &APIRsp{
				Code: 200,
				Data: dataList,
			}, nil
		}
	}

	// 获取数据集列表
	datasets, err := s.dbDAO.GetDatasetList()
	if err != nil {
		log.ErrorContextf(ctx, "GetDatasetList失败: %v", err)
		return &APIRsp{
			Code: 500,
			Data: []any{},
		}, err
	}

	// 将数据集列表转换为接口切片
	dataList := make([]any, 0, len(datasets))
	for _, dataset := range datasets {
		dataList = append(dataList, dataset)
	}

	// 返回成功响应
	return &APIRsp{
		Code: 200,
		Data: dataList,
	}, nil
}

// PostHandle 数据集表的写接口入口
func (s Dataset) PostHandle(ctx context.Context, params map[string]string) (*APIRsp, error) {
	log.DebugContextf(ctx, "HTTP-PostHandle, params:%+v", params)
	// TODO: 实现添加/更新数据集的逻辑
	return &APIRsp{Code: 200, Data: []any{}}, nil
}
