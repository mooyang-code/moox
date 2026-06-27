package cloudnode

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/dao"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
)

// ========== 代码包管理 ==========
//
// 注：代码包上传已改为走 /asynctask/CreateAsyncJob 异步任务（UPLOAD_FILE_TO_COS），
// 故 PackageService 不再提供 UploadPackage；上传由 UploadPackageExecutor 直接操作 dao/COS。

// GetPackageList 获取代码包列表
func (s *ServiceImpl) GetPackageList(ctx context.Context, req *pb.GetPackageListReq) (*pb.GetPackageListRsp, error) {
	q := req.GetQuery()
	if q == nil {
		q = &pb.PackageListRequest{}
	}

	daoReq := &dao.ListRequest{
		Page:        int(q.GetPage()),
		PageSize:    int(q.GetPageSize()),
		PackageName: q.GetPackageName(),
		Runtime:     q.GetRuntime(),
		PackageType: q.GetPackageType(),
		BizType:     q.GetBizType(),
	}
	if q.Status != nil {
		statusVal := int(*q.Status)
		daoReq.Status = &statusVal
	}

	packages, total, err := s.packageDAO.List(ctx, daoReq)
	if err != nil {
		return nil, fmt.Errorf("查询代码包列表失败: %w", err)
	}

	items := make([]*pb.PackageListItem, len(packages))
	for i := range packages {
		items[i] = packageListItemModelToPB(&packages[i])
	}

	return &pb.GetPackageListRsp{
		Items: items,
		Total: total,
	}, nil
}

// GetPackageDetail 获取代码包详情
func (s *ServiceImpl) GetPackageDetail(ctx context.Context, packageID string) (*pb.PackageDetail, error) {
	pkg, err := s.packageDAO.GetByID(ctx, packageID)
	if err != nil {
		return nil, fmt.Errorf("查询代码包详情失败: %w", err)
	}
	return packageDetailModelToPB(pkg), nil
}

// DeletePackage 删除代码包（软删除，根据package_id字符串）
func (s *ServiceImpl) DeletePackage(ctx context.Context, packageID string) error {
	return s.packageDAO.Delete(ctx, packageID)
}

// ========== 代码包下载 ==========

// GetPackageDownloadURL 获取代码包下载URL（带JWT认证，根据package_id字符串）
func (s *ServiceImpl) GetPackageDownloadURL(ctx context.Context, packageID string) (*pb.PackageDownloadURL, error) {
	pkg, err := s.packageDAO.GetByID(ctx, packageID)
	if err != nil {
		return nil, fmt.Errorf("查询代码包失败: %w", err)
	}

	filename := s.generateDisplayFilename(pkg)
	localFilePath := s.determineLocalFilePath(pkg)

	downloadURL, err := s.ensureFileAvailable(ctx, pkg, localFilePath)
	if err != nil {
		return nil, err
	}

	return &pb.PackageDownloadURL{
		PackageId:   pkg.PackageID,
		PackageName: pkg.PackageName,
		Version:     pkg.Version,
		Filename:    filename,
		DownloadUrl: downloadURL,
		FileSize:    pkg.FileSize,
		FileMd5:     pkg.FileMD5,
	}, nil
}
