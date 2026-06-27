package collectmgr

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/admin/internal/service/collectmgr/dao"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"

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
func (s *DataTypeConfigServiceImpl) GetDataTypeConfigs(ctx context.Context) ([]*pb.DataTypeConfig, error) {
	models, err := s.dataTypeConfigDAO.GetDataTypeConfigs(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get data type configs: %w", err)
	}
	result := make([]*pb.DataTypeConfig, 0, len(models))
	for _, m := range models {
		result = append(result, dataTypeConfigModelToPB(m))
	}
	return result, nil
}

// GetDataTypeConfigWithFields 获取数据类型配置及字段信息
func (s *DataTypeConfigServiceImpl) GetDataTypeConfigWithFields(ctx context.Context, dataType string) (*pb.DataTypeConfigDetail, error) {
	configModel, err := s.dataTypeConfigDAO.GetDataTypeConfigByType(ctx, dataType)
	if err != nil {
		return nil, fmt.Errorf("failed to get data type config: %w", err)
	}
	if configModel == nil {
		return nil, fmt.Errorf("data type config not found: %s", dataType)
	}

	fieldModels, err := s.fieldConfigDAO.GetFieldConfigsByDataType(ctx, dataType)
	if err != nil {
		return nil, fmt.Errorf("failed to get field configs: %w", err)
	}

	fields := make([]*pb.DataTypeFieldConfig, 0, len(fieldModels))
	for _, fm := range fieldModels {
		fields = append(fields, fieldConfigModelToPB(fm))
	}

	return &pb.DataTypeConfigDetail{
		Config: dataTypeConfigModelToPB(configModel),
		Fields: fields,
	}, nil
}
