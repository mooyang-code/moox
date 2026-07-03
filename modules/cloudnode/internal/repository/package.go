package repository

import (
	"context"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *CatalogRepository) ListPackages(ctx context.Context, spaceID string, req *pb.GetPackageListReq) ([]FunctionPackage, int64, error) {
	q := r.db.WithContext(ctx).Model(&FunctionPackage{}).Where("c_is_deleted = ?", false)
	if spaceID != "" {
		q = q.Where("c_space_id = ?", spaceID)
	}
	if req.GetPackageName() != "" {
		q = q.Where("c_package_name LIKE ?", "%"+req.GetPackageName()+"%")
	}
	if req.GetRuntime() != "" {
		q = q.Where("c_runtime = ?", req.GetRuntime())
	}
	if req.GetPackageType() != pb.PackageType_PACKAGE_TYPE_UNSPECIFIED {
		q = q.Where("c_package_type = ?", packageTypeToDB(req.GetPackageType()))
	}
	if req.GetBizType() != "" {
		q = q.Where("c_workload_type LIKE ?", "%"+req.GetBizType()+"%")
	}
	if req.Status != nil {
		q = q.Where("c_status = ?", packageStatusToDB(req.GetStatus()))
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, size := pageFromCommon(req.GetPage())
	var packages []FunctionPackage
	err := q.Order("c_id DESC").Limit(size).Offset((page - 1) * size).Find(&packages).Error
	return packages, total, err
}

func (r *CatalogRepository) GetPackage(ctx context.Context, spaceID, packageID string) (*FunctionPackage, error) {
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(packageID) == "" {
		return nil, nil
	}
	var pkg FunctionPackage
	if err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_package_id = ? AND c_is_deleted = ?", spaceID, packageID, false).First(&pkg).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &pkg, nil
}

func (r *CatalogRepository) UpsertPackage(ctx context.Context, pkg FunctionPackage) error {
	now := time.Now().UTC()
	if pkg.CreateTime.IsZero() {
		pkg.CreateTime = now
	}
	pkg.ModifyTime = now
	if pkg.Status == "" {
		pkg.Status = "pending"
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "c_space_id"}, {Name: "c_package_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"c_package_name", "c_version", "c_description", "c_runtime", "c_package_type",
			"c_workload_type", "c_original_filename", "c_file_size", "c_file_md5",
			"c_cloud_account_id", "c_cos_region", "c_cos_bucket", "c_cos_path",
			"c_status", "c_error_message", "c_is_deleted", "c_mtime",
		}),
	}).Create(&pkg).Error
}

func (r *CatalogRepository) DeletePackage(ctx context.Context, spaceID, packageID string) error {
	return r.db.WithContext(ctx).Model(&FunctionPackage{}).Where("c_space_id = ? AND c_package_id = ?", spaceID, packageID).Updates(map[string]any{
		"c_is_deleted": true,
		"c_status":     "deleted",
		"c_mtime":      time.Now().UTC(),
	}).Error
}

func packageTypeToDB(t pb.PackageType) string {
	switch t {
	case pb.PackageType_PACKAGE_TYPE_COLLECTOR:
		return "collector"
	case pb.PackageType_PACKAGE_TYPE_FACTOR:
		return "factor"
	case pb.PackageType_PACKAGE_TYPE_CUSTOM:
		return "custom"
	default:
		return ""
	}
}

func packageStatusToDB(s pb.PackageStatus) string {
	switch s {
	case pb.PackageStatus_PACKAGE_STATUS_PENDING:
		return "pending"
	case pb.PackageStatus_PACKAGE_STATUS_AVAILABLE:
		return "available"
	case pb.PackageStatus_PACKAGE_STATUS_FAILED:
		return "failed"
	case pb.PackageStatus_PACKAGE_STATUS_DELETED:
		return "deleted"
	default:
		return ""
	}
}
