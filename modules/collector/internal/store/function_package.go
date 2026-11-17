package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 代码包的两种状态：已发上传地址、COS 对象已核对。
const (
	FunctionPackagePending   = "pending"
	FunctionPackageAvailable = "available"
)

// FunctionPackage 是一次上传的云函数代码包。
type FunctionPackage struct {
	ID             int64     `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID        string    `gorm:"column:c_space_id"`
	PackageID      string    `gorm:"column:c_package_id"`
	PackageName    string    `gorm:"column:c_package_name"`
	Version        string    `gorm:"column:c_version"`
	Runtime        string    `gorm:"column:c_runtime"`
	FileSize       int64     `gorm:"column:c_file_size"`
	FileMD5        string    `gorm:"column:c_file_md5"`
	CloudAccountID string    `gorm:"column:c_cloud_account_id"`
	COSRegion      string    `gorm:"column:c_cos_region"`
	COSBucket      string    `gorm:"column:c_cos_bucket"`
	COSPath        string    `gorm:"column:c_cos_path"`
	Status         string    `gorm:"column:c_status"`
	CreateTime     time.Time `gorm:"column:c_ctime"`
	ModifyTime     time.Time `gorm:"column:c_mtime"`
}

func (FunctionPackage) TableName() string { return "t_collector_function_packages" }

// FunctionPackageRepository 保存云函数代码包。
type FunctionPackageRepository struct{ db *gorm.DB }

func NewFunctionPackageRepository(db *gorm.DB) *FunctionPackageRepository {
	return &FunctionPackageRepository{db: db}
}

func (r *FunctionPackageRepository) ready() error {
	if r == nil || r.db == nil {
		return fmt.Errorf("代码包仓库未初始化")
	}
	return nil
}

// Get 返回一个代码包；不存在时返回 nil。
func (r *FunctionPackageRepository) Get(ctx context.Context, spaceID, packageID string) (*FunctionPackage, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	var pkg FunctionPackage
	err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_package_id = ?", strings.TrimSpace(spaceID), strings.TrimSpace(packageID)).First(&pkg).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &pkg, nil
}

// Latest 返回该空间某个包名最新的可用代码包；没有时返回 nil。
func (r *FunctionPackageRepository) Latest(ctx context.Context, spaceID, packageName string) (*FunctionPackage, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	var pkg FunctionPackage
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_package_name = ? AND c_status = ?", strings.TrimSpace(spaceID), strings.TrimSpace(packageName), FunctionPackageAvailable).
		Order("c_id DESC").First(&pkg).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &pkg, nil
}

// List 按上传先后倒序返回代码包；spaceID 为空时返回全部空间的。
func (r *FunctionPackageRepository) List(ctx context.Context, spaceID string) ([]FunctionPackage, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	query := r.db.WithContext(ctx).Model(&FunctionPackage{})
	if spaceID = strings.TrimSpace(spaceID); spaceID != "" {
		query = query.Where("c_space_id = ?", spaceID)
	}
	var packages []FunctionPackage
	err := query.Order("c_id DESC").Find(&packages).Error
	return packages, err
}

// CountByAccount 返回登记在某个云账号下的代码包个数。
func (r *FunctionPackageRepository) CountByAccount(ctx context.Context, accountID string) (int64, error) {
	if err := r.ready(); err != nil {
		return 0, err
	}
	var count int64
	err := r.db.WithContext(ctx).Model(&FunctionPackage{}).Where("c_cloud_account_id = ?", strings.TrimSpace(accountID)).Count(&count).Error
	return count, err
}

// Upsert 按（空间、包 ID）新建或更新一个代码包。
func (r *FunctionPackageRepository) Upsert(ctx context.Context, pkg FunctionPackage) error {
	if err := r.ready(); err != nil {
		return err
	}
	now := time.Now().UTC()
	if pkg.CreateTime.IsZero() {
		pkg.CreateTime = now
	}
	pkg.ModifyTime = now
	if pkg.Status == "" {
		pkg.Status = FunctionPackagePending
	}
	pkg.ID = 0
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "c_space_id"}, {Name: "c_package_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"c_package_name", "c_version", "c_runtime", "c_file_size", "c_file_md5", "c_cloud_account_id",
			"c_cos_region", "c_cos_bucket", "c_cos_path", "c_status", "c_mtime",
		}),
	}).Create(&pkg).Error
}

// Delete 删除一个代码包记录；记录不存在时不报错。
func (r *FunctionPackageRepository) Delete(ctx context.Context, spaceID, packageID string) error {
	if err := r.ready(); err != nil {
		return err
	}
	return r.db.WithContext(ctx).Where("c_space_id = ? AND c_package_id = ?", strings.TrimSpace(spaceID), strings.TrimSpace(packageID)).Delete(&FunctionPackage{}).Error
}
