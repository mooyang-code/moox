package collectmgr

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/server/internal/service/collectmgr/dao"

	"gorm.io/gorm"
)

// DataTypeConfigServiceImpl 数据类型配置服务实现
type DataTypeConfigServiceImpl struct {
	dataTypeConfigDAO dao.CollectorDataTypeConfigsDAO
	fieldConfigDAO    dao.CollectorFieldConfigsDAO
	db                *gorm.DB
}

// NewDataTypeConfigServiceImpl 创建数据类型配置服务实现
func NewDataTypeConfigServiceImpl(
	dataTypeConfigDAO dao.CollectorDataTypeConfigsDAO,
	fieldConfigDAO dao.CollectorFieldConfigsDAO,
	db *gorm.DB,
) DataTypeConfigService {
	return &DataTypeConfigServiceImpl{
		dataTypeConfigDAO: dataTypeConfigDAO,
		fieldConfigDAO:    fieldConfigDAO,
		db:                db,
	}
}

// GetDataTypeConfigs 获取所有数据类型配置
func (s *DataTypeConfigServiceImpl) GetDataTypeConfigs(ctx context.Context) ([]*DataTypeConfigDTO, error) {
	models, err := s.dataTypeConfigDAO.GetDataTypeConfigs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get data type configs: %w", err)
	}

	var dtos []*DataTypeConfigDTO
	for _, model := range models {
		dto := &DataTypeConfigDTO{
			ID:                 model.ID,
			DataType:           model.DataType,
			TypeName:           model.TypeName,
			TypeDesc:           model.TypeDesc,
			DataSourceOptions:  model.DataSourceOptions,
			SortOrder:          model.SortOrder,
			Version:            model.Version,
			CreateTime:         model.CreateTime,
			ModifyTime:         model.ModifyTime,
		}
		dtos = append(dtos, dto)
	}

	return dtos, nil
}

// GetDataTypeConfigWithFields 获取数据类型配置及字段信息
func (s *DataTypeConfigServiceImpl) GetDataTypeConfigWithFields(ctx context.Context, dataType string) (*DataTypeConfigDetailDTO, error) {
	// 获取数据类型配置
	configModel, err := s.dataTypeConfigDAO.GetDataTypeConfigByType(ctx, dataType)
	if err != nil {
		return nil, fmt.Errorf("failed to get data type config: %w", err)
	}
	if configModel == nil {
		return nil, fmt.Errorf("data type config not found: %s", dataType)
	}

	// 获取字段配置
	fieldModels, err := s.fieldConfigDAO.GetFieldConfigsByDataType(ctx, dataType)
	if err != nil {
		return nil, fmt.Errorf("failed to get field configs: %w", err)
	}

	// 转换为DTO
	configDTO := &DataTypeConfigDTO{
		ID:                 configModel.ID,
		DataType:           configModel.DataType,
		TypeName:           configModel.TypeName,
		TypeDesc:           configModel.TypeDesc,
		DataSourceOptions:  configModel.DataSourceOptions,
		SortOrder:          configModel.SortOrder,
		Version:            configModel.Version,
		CreateTime:         configModel.CreateTime,
		ModifyTime:         configModel.ModifyTime,
	}

	var fieldDTOs []*FieldConfigDTO
	for _, fieldModel := range fieldModels {
		fieldDTO := &FieldConfigDTO{
			ID:               fieldModel.ID,
			DataType:         fieldModel.DataType,
			FieldKey:         fieldModel.FieldKey,
			FieldName:        fieldModel.FieldName,
			FieldType:        fieldModel.FieldType,
			IsRequired:       fieldModel.IsRequired,
			DefaultValue:     fieldModel.DefaultValue,
			FieldOptions:     fieldModel.FieldOptions,
			DataSourceOptions: fieldModel.DataSourceOptions,
			SortOrder:        fieldModel.SortOrder,
			CreateTime:       fieldModel.CreateTime,
			ModifyTime:       fieldModel.ModifyTime,
		}
		fieldDTOs = append(fieldDTOs, fieldDTO)
	}

	return &DataTypeConfigDetailDTO{
		Config: configDTO,
		Fields: fieldDTOs,
	}, nil
}