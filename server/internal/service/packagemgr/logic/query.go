package logic

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/packagemgr/dao"
	"github.com/mooyang-code/moox/server/internal/service/packagemgr/model"
)

// GetPackageList 获取代码包列表
func (s *FunctionPackageService) GetPackageList(ctx context.Context, req *PackageListRequest) (*PackageListResponse, error) {
	// 转换为dao请求
	daoReq := &dao.ListRequest{
		Page:        req.Page,
		PageSize:    req.PageSize,
		PackageName: req.PackageName,
		Runtime:     req.Runtime,
		PackageType: req.PackageType,
		Status:      req.Status,
	}

	// 调用dao层获取数据
	packages, total, err := s.dao.List(ctx, daoReq)
	if err != nil {
		return nil, fmt.Errorf("查询代码包列表失败: %w", err)
	}

	// 转换为VO
	items := make([]*PackageListItem, len(packages))
	for i, pkg := range packages {
		items[i] = &PackageListItem{
			ID:               pkg.ID,
			PackageName:      pkg.PackageName,
			Version:          pkg.Version,
			Description:      pkg.Description,
			Runtime:          pkg.Runtime,
			PackageType:      pkg.PackageType,
			PackageTypeLabel: model.GetPackageTypeDisplayName(pkg.PackageType),
			FileSize:         pkg.FileSize,
			FileMD5:          pkg.FileMD5,
			CloudAccountID:   pkg.CloudAccountID,
			COSRegion:        pkg.COSRegion,
			Status:           pkg.Status,
			StatusLabel:      model.GetStatusDisplayName(pkg.Status),
			LastDeployTime:   pkg.LastDeployTime,
			CreatedBy:        pkg.CreatedBy,
			CreatedAt:        pkg.CTime,
		}
	}

	return &PackageListResponse{
		Total: total,
		Items: items,
	}, nil
}

// GetPackageDetail 获取代码包详情
func (s *FunctionPackageService) GetPackageDetail(ctx context.Context, id int64) (*PackageDetail, error) {
	pkg, err := s.dao.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("查询代码包详情失败: %w", err)
	}

	// 转换为详情，包含所有字段和显示标签
	detail := &PackageDetail{
		ID:               pkg.ID,
		PackageName:      pkg.PackageName,
		Version:          pkg.Version,
		Description:      pkg.Description,
		Runtime:          pkg.Runtime,
		PackageType:      pkg.PackageType,
		PackageTypeLabel: model.GetPackageTypeDisplayName(pkg.PackageType),

		// 文件信息
		OriginalFilename: pkg.OriginalFilename,
		FileSize:         pkg.FileSize,
		FileMD5:          pkg.FileMD5,

		// COS存储信息
		CloudAccountID: pkg.CloudAccountID,
		COSRegion:      pkg.COSRegion,
		COSBucket:      pkg.COSBucket,
		COSPath:        pkg.COSPath,
		COSURL:         pkg.COSURL,

		// 状态管理
		Status:         pkg.Status,
		StatusLabel:    model.GetStatusDisplayName(pkg.Status),
		UploadProgress: pkg.UploadProgress,
		ErrorMessage:   pkg.ErrorMessage,

		// 使用统计
		LastDeployTime: pkg.LastDeployTime,

		// 审计字段
		CreatedBy: pkg.CreatedBy,
		Invalid:   pkg.Invalid,
		CreatedAt: pkg.CTime,
		UpdatedAt: pkg.MTime,
	}
	return detail, nil
}

// GetPackageDetailModel 获取代码包详情（原始模型，用于内部调用）
func (s *FunctionPackageService) GetPackageDetailModel(ctx context.Context, id int64) (*model.FunctionPackage, error) {
	return s.dao.GetByID(ctx, id)
}

// DeletePackage 删除代码包（软删除）
func (s *FunctionPackageService) DeletePackage(ctx context.Context, id int64) error {
	return s.dao.Delete(ctx, id)
}

// checkVersionExists 检查版本是否已存在
func (s *FunctionPackageService) checkVersionExists(ctx context.Context, packageName, version string) (bool, error) {
	return s.dao.CheckVersionExists(ctx, packageName, version)
}

// updatePackageStatus 更新代码包状态
func (s *FunctionPackageService) updatePackageStatus(ctx context.Context, id int64, status, progress int, errorMsg string) error {
	updates := map[string]interface{}{
		"c_status":          status,
		"c_upload_progress": progress,
		"c_mtime":           time.Now(),
	}

	if errorMsg != "" {
		updates["c_error_message"] = errorMsg
	}
	return s.dao.Update(ctx, id, updates)
}
