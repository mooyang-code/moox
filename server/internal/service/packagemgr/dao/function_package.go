package dao

import (
	"context"
	"time"

	cloudAccountModel "github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/server/internal/service/packagemgr/model"

	"gorm.io/gorm"
)

// FunctionPackageDAO 代码包数据访问接口
type FunctionPackageDAO interface {
	// 基础CRUD操作
	Create(ctx context.Context, pkg *model.FunctionPackage) error
	GetByID(ctx context.Context, id int64) (*model.FunctionPackage, error)
	Update(ctx context.Context, id int64, updates map[string]interface{}) error
	Delete(ctx context.Context, id int64) error

	// 查询操作
	List(ctx context.Context, req *ListRequest) ([]*model.FunctionPackage, int64, error)
	GetByNameAndVersion(ctx context.Context, packageName, version string) (*model.FunctionPackage, error)
	CheckVersionExists(ctx context.Context, packageName, version string) (bool, error)
	GetOptions(ctx context.Context, packageType string) ([]*model.FunctionPackage, error)

	// 云账户相关
	GetCloudAccount(ctx context.Context, accountID string) (*cloudAccountModel.CloudAccount, error)
}

// ListRequest 列表查询请求
type ListRequest struct {
	Page        int    `json:"page"`
	PageSize    int    `json:"page_size"`
	PackageName string `json:"package_name"`
	Runtime     string `json:"runtime"`
	PackageType string `json:"package_type"`
	Status      *int   `json:"status"`
}

// FunctionPackageDAOImpl 代码包数据访问实现
type FunctionPackageDAOImpl struct {
	db *gorm.DB
}

// NewFunctionPackageDAO 创建代码包数据访问对象
func NewFunctionPackageDAO(db *gorm.DB) FunctionPackageDAO {
	return &FunctionPackageDAOImpl{
		db: db,
	}
}

// Create 创建代码包
func (d *FunctionPackageDAOImpl) Create(ctx context.Context, pkg *model.FunctionPackage) error {
	return d.db.WithContext(ctx).Create(pkg).Error
}

// GetByID 根据ID获取代码包
func (d *FunctionPackageDAOImpl) GetByID(ctx context.Context, id int64) (*model.FunctionPackage, error) {
	var pkg model.FunctionPackage
	err := d.db.WithContext(ctx).Where("c_id = ? AND c_invalid = 0", id).First(&pkg).Error
	if err != nil {
		return nil, err
	}
	return &pkg, nil
}

// Update 更新代码包
func (d *FunctionPackageDAOImpl) Update(ctx context.Context, id int64, updates map[string]interface{}) error {
	return d.db.WithContext(ctx).Model(&model.FunctionPackage{}).
		Where("c_id = ?", id).
		Updates(updates).Error
}

// Delete 软删除代码包
func (d *FunctionPackageDAOImpl) Delete(ctx context.Context, id int64) error {
	return d.db.WithContext(ctx).Model(&model.FunctionPackage{}).
		Where("c_id = ?", id).
		Updates(map[string]interface{}{
			"c_invalid": 1,
			"c_status":  model.PackageStatusDeleted,
			"c_mtime":   time.Now(),
		}).Error
}

// List 获取代码包列表
func (d *FunctionPackageDAOImpl) List(ctx context.Context, req *ListRequest) ([]*model.FunctionPackage, int64, error) {
	var packages []*model.FunctionPackage
	var total int64

	query := d.db.WithContext(ctx).Model(&model.FunctionPackage{}).Where("c_invalid = 0")

	// 添加查询条件
	if req.PackageName != "" {
		query = query.Where("c_package_name LIKE ?", "%"+req.PackageName+"%")
	}
	if req.Runtime != "" {
		query = query.Where("c_runtime = ?", req.Runtime)
	}
	if req.PackageType != "" {
		query = query.Where("c_package_type = ?", req.PackageType)
	}
	if req.Status != nil {
		query = query.Where("c_status = ?", *req.Status)
	}

	// 获取总数
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 分页查询
	page := req.Page
	if page <= 0 {
		page = 1
	}
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}

	offset := (page - 1) * pageSize
	if err := query.Order("c_ctime DESC").Offset(offset).Limit(pageSize).Find(&packages).Error; err != nil {
		return nil, 0, err
	}
	return packages, total, nil
}

// GetByNameAndVersion 根据包名和版本获取代码包
func (d *FunctionPackageDAOImpl) GetByNameAndVersion(ctx context.Context, packageName, version string) (*model.FunctionPackage, error) {
	var pkg model.FunctionPackage
	err := d.db.WithContext(ctx).Where("c_package_name = ? AND c_version = ? AND c_invalid = 0",
		packageName, version).First(&pkg).Error
	if err != nil {
		return nil, err
	}
	return &pkg, nil
}

// CheckVersionExists 检查版本是否存在
func (d *FunctionPackageDAOImpl) CheckVersionExists(ctx context.Context, packageName, version string) (bool, error) {
	var count int64
	err := d.db.WithContext(ctx).Model(&model.FunctionPackage{}).
		Where("c_package_name = ? AND c_version = ? AND c_invalid = 0", packageName, version).
		Count(&count).Error
	return count > 0, err
}

// GetOptions 获取代码包选项
func (d *FunctionPackageDAOImpl) GetOptions(ctx context.Context, packageType string) ([]*model.FunctionPackage, error) {
	var packages []*model.FunctionPackage
	query := d.db.WithContext(ctx).Where("c_status = ? AND c_invalid = 0", model.PackageStatusAvailable)

	if packageType != "" {
		query = query.Where("c_package_type = ?", packageType)
	}

	err := query.Order("c_package_name ASC, c_version DESC").Find(&packages).Error
	return packages, err
}

// GetCloudAccount 获取云账户信息
func (d *FunctionPackageDAOImpl) GetCloudAccount(ctx context.Context, accountID string) (*cloudAccountModel.CloudAccount, error) {
	var account cloudAccountModel.CloudAccount
	err := d.db.WithContext(ctx).Where("c_account_id = ? AND c_invalid = 0", accountID).First(&account).Error
	if err != nil {
		return nil, err
	}
	return &account, nil
}
