package sqlite

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/services/common/constants"
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/config"
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao/model"
	"trpc.group/trpc-go/trpc-go/log"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// dataDBImpl metadata数据层实现
type dataDBImpl struct {
	db           *gorm.DB
	databaseName string
	cfg          *config.Config
}

// InitSQLiteImp 初始化metadata数据层（SQLite实现）
func InitSQLiteImp() (*dataDBImpl, error) {
	// 从配置文件加载所有配置
	metaCfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("LoadConfig err[%v]", err)
	}

	// 从配置中获取存储设备信息
	storageDevice := metaCfg.MetadataDatabase.StorageDevice
	if storageDevice == "" {
		return nil, fmt.Errorf("配置中未指定存储设备")
	}

	// 解析存储设备信息 (例如："sqlite:/path/to/database.db")
	parts := strings.SplitN(storageDevice, ":", 2)
	if len(parts) != 2 {
		log.Errorf("存储设备配置格式错误: %s", storageDevice)
		return nil, fmt.Errorf("存储设备配置格式错误: %s", storageDevice)
	}

	// 获取数据库类型和路径
	dbType := strings.ToLower(parts[0])
	dbPath := parts[1]

	if dbType != "sqllite" && dbType != "sqlite" {
		log.Errorf("不支持的数据库类型: %s", dbType)
		return nil, fmt.Errorf("不支持的数据库类型: %s", dbType)
	}

	// 确保目录存在
	dataDir := filepath.Dir(dbPath)
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		err = os.MkdirAll(dataDir, 0755)
		if err != nil {
			return nil, err
		}
	}

	// 连接数据库
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		return nil, err
	}

	// 从路径中提取数据库名称
	baseName := filepath.Base(dbPath)
	dbName := strings.TrimSuffix(baseName, filepath.Ext(baseName))

	return &dataDBImpl{
		db:           db,
		databaseName: dbName,
		cfg:          metaCfg,
	}, nil
}

func (imp *dataDBImpl) InitMetadata() error {
	return nil
}

// BeginTx 开始事务
func (d *dataDBImpl) BeginTx() (*gorm.DB, error) {
	return d.db.Begin(), nil
}

// CommitTx 提交事务
func (d *dataDBImpl) CommitTx(tx *gorm.DB) error {
	if err := tx.Commit().Error; err != nil {
		log.Errorf("CommitTx err[%v]", err)
		return err
	}
	return nil
}

// RollbackTx 回滚事务
func (d *dataDBImpl) RollbackTx(tx *gorm.DB) error {
	if err := tx.Rollback().Error; err != nil {
		log.Errorf("RollbackTx err[%v]", err)
		return err
	}
	return nil
}

// AddProjectWithTx 在事务中添加项目
func (d *dataDBImpl) AddProjectWithTx(tx *gorm.DB, projID int, projName string, remark string) error {
	project := &model.Project{
		ProjID:     projID,
		ProjName:   projName,
		Remark:     remark,
		Enabled:    constants.EnabledValue,
		CreateTime: time.Now(),
		ModifyTime: time.Now(),
	}
	result := tx.Create(project)
	if result.Error != nil {
		log.Errorf("AddProjectWithTx err[%v]", result.Error)
		return result.Error
	}
	return nil
}

// AddDatasetWithTx 在事务中添加数据集
func (d *dataDBImpl) AddDatasetWithTx(tx *gorm.DB, dataset *model.Dataset) error {
	if dataset.Enabled == "" {
		dataset.Enabled = constants.EnabledValue
	}
	result := tx.Create(dataset)
	if result.Error != nil {
		log.Errorf("AddDatasetWithTx err[%v]", result.Error)
		return result.Error
	}
	return nil
}

// AddFieldWithTx 在事务中添加字段
func (d *dataDBImpl) AddFieldWithTx(tx *gorm.DB, field *model.Field) error {
	if field.Enabled == "" {
		field.Enabled = constants.EnabledValue
	}
	result := tx.Create(field)
	if result.Error != nil {
		log.Errorf("AddFieldWithTx err[%v]", result.Error)
		return result.Error
	}
	return nil
}
