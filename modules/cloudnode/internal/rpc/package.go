package rpc

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/cloudcredential"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/spacecontext"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/tencentyun/cos-go-sdk-v5"
)

func (s *Service) GetPackageList(ctx context.Context, req *pb.GetPackageListReq) (*pb.GetPackageListRsp, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.GetPackageListRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	packages, total, err := s.catalog.ListPackages(ctx, spaceID, req)
	if err != nil {
		return &pb.GetPackageListRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	out := make([]*pb.PackageListItem, 0, len(packages))
	for _, pkg := range packages {
		out = append(out, toPBPackageListItem(pkg))
	}
	page, size := pageFromCommon(req.GetPage())
	return &pb.GetPackageListRsp{RetInfo: retOK(), Items: out, Page: pageResult(page, size, total)}, nil
}

func (s *Service) GetPackageDetail(ctx context.Context, req *pb.GetPackageDetailReq) (*pb.GetPackageDetailRsp, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.GetPackageDetailRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	pkg, err := s.catalog.GetPackage(ctx, spaceID, req.GetPackageId())
	if err != nil {
		return &pb.GetPackageDetailRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	if pkg == nil {
		return &pb.GetPackageDetailRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "package not found")}, nil
	}
	return &pb.GetPackageDetailRsp{RetInfo: retOK(), Detail: toPBPackageDetail(*pkg)}, nil
}

func (s *Service) DeletePackage(ctx context.Context, req *pb.DeletePackageReq) (*pb.DeletePackageRsp, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.DeletePackageRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if req.GetPackageId() == "" {
		return &pb.DeletePackageRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "package_id is required")}, nil
	}
	if err := s.catalog.DeletePackage(ctx, spaceID, req.GetPackageId()); err != nil {
		return &pb.DeletePackageRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.DeletePackageRsp{RetInfo: retOK()}, nil
}

func (s *Service) GetPackageDownloadURL(ctx context.Context, req *pb.GetPackageDownloadURLReq) (*pb.GetPackageDownloadURLRsp, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.GetPackageDownloadURLRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	pkg, err := s.catalog.GetPackage(ctx, spaceID, req.GetPackageId())
	if err != nil {
		return &pb.GetPackageDownloadURLRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	if pkg == nil {
		return &pb.GetPackageDownloadURLRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "package not found")}, nil
	}
	return &pb.GetPackageDownloadURLRsp{RetInfo: retOK(), Url: &pb.PackageDownloadURL{
		PackageId:   pkg.PackageID,
		PackageName: pkg.PackageName,
		Version:     pkg.Version,
		Filename:    pkg.COSPath,
		DownloadUrl: cosURL(*pkg),
		FileSize:    pkg.FileSize,
		FileMd5:     pkg.FileMD5,
	}}, nil
}

func (s *Service) InitPackageUpload(ctx context.Context, req *pb.InitPackageUploadReq) (*pb.InitPackageUploadRsp, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.InitPackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if strings.TrimSpace(req.GetPackageName()) == "" {
		return &pb.InitPackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "package_name is required")}, nil
	}
	if strings.TrimSpace(req.GetVersion()) == "" {
		return &pb.InitPackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "version is required")}, nil
	}
	if strings.TrimSpace(req.GetRuntime()) == "" {
		return &pb.InitPackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "runtime is required")}, nil
	}
	if req.GetPackageType() == pb.PackageType_PACKAGE_TYPE_UNSPECIFIED {
		return &pb.InitPackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "package_type is required")}, nil
	}
	if strings.TrimSpace(req.GetCloudAccountId()) == "" {
		return &pb.InitPackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "cloud_account_id is required")}, nil
	}
	account, err := s.catalog.GetAccount(ctx, req.GetCloudAccountId())
	if err != nil {
		return &pb.InitPackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	if account == nil {
		return &pb.InitPackageUploadRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "cloud account not found")}, nil
	}
	if account.COSRegion == "" || account.COSBucket == "" {
		return &pb.InitPackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "cloud account cos_region/cos_bucket is required")}, nil
	}
	credential, err := s.resolveCloudCredential(ctx, *account)
	if err != nil {
		return &pb.InitPackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if err := ensureCOSBucket(ctx, *account, credential); err != nil {
		return &pb.InitPackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "ensure COS bucket failed: "+err.Error())}, nil
	}
	packageType := packageTypeToDB(req.GetPackageType())
	filename := sanitizePackageFileName(firstString(req.GetOriginalFilename(), req.GetPackageName()+"-"+req.GetVersion()+".zip"))
	packageID := buildPackageID(req.GetPackageName(), req.GetVersion(), uuid.NewString())
	cosPath := buildPackageCOSPath(packageType, req.GetPackageName(), req.GetVersion(), packageID, filename)
	expires := time.Now().UTC().Add(time.Hour)
	uploadURL, err := presignCOSPut(ctx, *account, credential, cosPath, expires)
	if err != nil {
		return &pb.InitPackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "presign upload url failed: "+err.Error())}, nil
	}
	pkg := store.FunctionPackage{
		SpaceID:        spaceID,
		PackageID:      packageID,
		PackageName:    req.GetPackageName(),
		Version:        req.GetVersion(),
		Description:    req.GetDescription(),
		Runtime:        req.GetRuntime(),
		PackageType:    packageType,
		WorkloadType:   req.GetBizType(),
		OriginalName:   filename,
		CloudAccountID: account.AccountID,
		COSRegion:      account.COSRegion,
		COSBucket:      account.COSBucket,
		COSPath:        cosPath,
		Status:         "pending",
		IsDeleted:      false,
	}
	if err := s.catalog.UpsertPackage(ctx, pkg); err != nil {
		return &pb.InitPackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.InitPackageUploadRsp{
		RetInfo:   retOK(),
		PackageId: packageID,
		UploadUrl: uploadURL,
		CosPath:   cosPath,
		ExpiresAt: expires.Unix(),
	}, nil
}

func (s *Service) CompletePackageUpload(ctx context.Context, req *pb.CompletePackageUploadReq) (*pb.CompletePackageUploadRsp, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.CompletePackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if req.GetPackageId() == "" {
		return &pb.CompletePackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "package_id is required")}, nil
	}
	if req.GetFileSize() <= 0 {
		return &pb.CompletePackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "file_size is required")}, nil
	}
	if strings.TrimSpace(req.GetFileMd5()) == "" {
		return &pb.CompletePackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "file_md5 is required")}, nil
	}
	pkg, err := s.catalog.GetPackage(ctx, spaceID, req.GetPackageId())
	if err != nil {
		return &pb.CompletePackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	if pkg == nil {
		return &pb.CompletePackageUploadRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "package not found")}, nil
	}
	if pkg.Status != "pending" {
		return &pb.CompletePackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "package is not pending upload")}, nil
	}
	account, err := s.catalog.GetAccount(ctx, pkg.CloudAccountID)
	if err != nil {
		return &pb.CompletePackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	if account == nil {
		return &pb.CompletePackageUploadRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "cloud account not found")}, nil
	}
	credential, err := s.resolveCloudCredential(ctx, *account)
	if err != nil {
		return &pb.CompletePackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if err := verifyCOSObject(ctx, *account, credential, pkg.COSPath, req.GetFileSize(), req.GetFileMd5()); err != nil {
		return &pb.CompletePackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	pkg.FileSize = req.GetFileSize()
	pkg.FileMD5 = strings.TrimSpace(strings.ToLower(req.GetFileMd5()))
	pkg.Status = "available"
	if err := s.catalog.UpsertPackage(ctx, *pkg); err != nil {
		return &pb.CompletePackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.CompletePackageUploadRsp{RetInfo: retOK(), Detail: toPBPackageDetail(*pkg)}, nil
}

func toPBPackageListItem(pkg store.FunctionPackage) *pb.PackageListItem {
	return &pb.PackageListItem{
		PackageId:      pkg.PackageID,
		PackageName:    pkg.PackageName,
		Version:        pkg.Version,
		Description:    pkg.Description,
		Runtime:        pkg.Runtime,
		PackageType:    packageTypeToPB(pkg.PackageType),
		BizType:        pkg.WorkloadType,
		FileSize:       pkg.FileSize,
		FileMd5:        pkg.FileMD5,
		CloudAccountId: pkg.CloudAccountID,
		CosRegion:      pkg.COSRegion,
		Status:         packageStatusToPB(pkg.Status),
		CreatedTime:    formatTime(pkg.CreateTime),
	}
}

func toPBPackageDetail(pkg store.FunctionPackage) *pb.PackageDetail {
	return &pb.PackageDetail{
		Id:               int64(pkg.ID),
		SpaceId:          pkg.SpaceID,
		PackageId:        pkg.PackageID,
		PackageName:      pkg.PackageName,
		Version:          pkg.Version,
		Description:      pkg.Description,
		Runtime:          pkg.Runtime,
		PackageType:      packageTypeToPB(pkg.PackageType),
		OriginalFilename: pkg.OriginalName,
		FileSize:         pkg.FileSize,
		FileMd5:          pkg.FileMD5,
		CloudAccountId:   pkg.CloudAccountID,
		CosRegion:        pkg.COSRegion,
		CosBucket:        pkg.COSBucket,
		CosPath:          pkg.COSPath,
		CosUrl:           cosURL(pkg),
		Status:           packageStatusToPB(pkg.Status),
		ErrorMessage:     pkg.ErrorMessage,
		IsDeleted:        pkg.IsDeleted,
		CreatedTime:      formatTime(pkg.CreateTime),
		UpdatedTime:      formatTime(pkg.ModifyTime),
	}
}

func cosURL(pkg store.FunctionPackage) string {
	if pkg.COSBucket == "" || pkg.COSRegion == "" || pkg.COSPath == "" {
		return ""
	}
	return fmt.Sprintf("https://%s.cos.%s.myqcloud.com/%s", pkg.COSBucket, pkg.COSRegion, strings.TrimPrefix(pkg.COSPath, "/"))
}

func presignCOSPut(ctx context.Context, account store.CloudAccount, credential cloudcredential.TencentCredential, objectPath string, expires time.Time) (string, error) {
	bucketURL, err := url.Parse(fmt.Sprintf("https://%s.cos.%s.myqcloud.com", account.COSBucket, account.COSRegion))
	if err != nil {
		return "", err
	}
	client := cos.NewClient(&cos.BaseURL{BucketURL: bucketURL}, &http.Client{
		Transport: &cos.AuthorizationTransport{
			SecretID:  credential.SecretID,
			SecretKey: credential.SecretKey,
		},
	})
	u, err := client.Object.GetPresignedURL(ctx, http.MethodPut, objectPath, credential.SecretID, credential.SecretKey, time.Until(expires), nil)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func newCOSClient(account store.CloudAccount, credential cloudcredential.TencentCredential) *cos.Client {
	bucketURL, _ := url.Parse(fmt.Sprintf("https://%s.cos.%s.myqcloud.com", account.COSBucket, account.COSRegion))
	return cos.NewClient(&cos.BaseURL{BucketURL: bucketURL}, &http.Client{
		Transport: &cos.AuthorizationTransport{
			SecretID:  credential.SecretID,
			SecretKey: credential.SecretKey,
		},
	})
}

// ensureCOSBucket makes package publishing self-contained. moox-cli starts an
// upload through InitPackageUpload, so a configured-but-not-yet-created bucket
// is created on demand instead of requiring a separate console operation.
func ensureCOSBucket(ctx context.Context, account store.CloudAccount, credential cloudcredential.TencentCredential) error {
	client := newCOSClient(account, credential)
	if client == nil {
		return fmt.Errorf("create COS client")
	}
	_, err := client.Bucket.Head(ctx)
	if err == nil {
		return nil
	}
	if !cos.IsNotFoundError(err) {
		return fmt.Errorf("check bucket %s: %w", account.COSBucket, err)
	}
	if _, err := client.Bucket.Put(ctx, &cos.BucketPutOptions{XCosACL: "private"}); err != nil {
		// A concurrent CLI invocation may have created it after our HEAD.
		if _, verifyErr := client.Bucket.Head(ctx); verifyErr != nil {
			return fmt.Errorf("create bucket %s: %w", account.COSBucket, err)
		}
		return nil
	}
	if _, err := client.Bucket.Head(ctx); err != nil {
		return fmt.Errorf("verify bucket %s: %w", account.COSBucket, err)
	}
	return nil
}

func verifyCOSObject(ctx context.Context, account store.CloudAccount, credential cloudcredential.TencentCredential, objectPath string, expectedSize int64, expectedMD5 string) error {
	client := newCOSClient(account, credential)
	key := strings.TrimPrefix(objectPath, "/")
	resp, err := client.Object.Head(ctx, key, nil)
	if err != nil {
		return fmt.Errorf("cos object not found: %w", err)
	}
	if resp == nil || resp.Header == nil {
		return fmt.Errorf("cos head returned empty response")
	}
	contentLength := resp.ContentLength
	if contentLength == 0 {
		if cl := strings.TrimSpace(resp.Header.Get("Content-Length")); cl != "" {
			if _, err := fmt.Sscanf(cl, "%d", &contentLength); err != nil {
				return fmt.Errorf("invalid cos content-length: %s", cl)
			}
		}
	}
	if contentLength != expectedSize {
		return fmt.Errorf("cos size mismatch: got %d want %d", contentLength, expectedSize)
	}
	etag := strings.Trim(strings.ToLower(resp.Header.Get("ETag")), `"`)
	md5Hex := strings.ToLower(strings.TrimSpace(expectedMD5))
	if etag == "" {
		return fmt.Errorf("cos etag is empty")
	}
	if etag != md5Hex {
		return fmt.Errorf("cos md5 mismatch: got %s want %s", etag, md5Hex)
	}
	return nil
}

func buildPackageID(packageName string, version string, uploadID string) string {
	return sanitizePackagePathSegment(packageName) + "_" +
		sanitizePackagePathSegment(version) + "_" +
		sanitizePackagePathSegment(uploadID)
}

func buildPackageCOSPath(packageType string, packageName string, version string, packageID string, filename string) string {
	return buildPackageCOSPathAt(time.Now().UTC(), packageType, packageName, version, packageID, filename)
}

func buildPackageCOSPathAt(now time.Time, packageType string, packageName string, version string, packageID string, filename string) string {
	return path.Join(
		"moox",
		"cloud-packages",
		now.UTC().Format("2006-01-02"),
		sanitizePackagePathSegment(packageType),
		sanitizePackagePathSegment(packageName),
		sanitizePackagePathSegment(version),
		sanitizePackagePathSegment(packageID),
		filename,
	)
}

func sanitizePackageFileName(value string) string {
	cleaned := sanitizePackagePathSegment(value)
	if cleaned == "" {
		return "package.zip"
	}
	return cleaned
}

func sanitizePackagePathSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	return strings.Trim(b.String(), "_.-")
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
		return "custom"
	}
}

func packageTypeToPB(raw string) pb.PackageType {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "collector":
		return pb.PackageType_PACKAGE_TYPE_COLLECTOR
	case "factor":
		return pb.PackageType_PACKAGE_TYPE_FACTOR
	case "custom":
		return pb.PackageType_PACKAGE_TYPE_CUSTOM
	default:
		return pb.PackageType_PACKAGE_TYPE_UNSPECIFIED
	}
}

func packageStatusToPB(status string) pb.PackageStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pending":
		return pb.PackageStatus_PACKAGE_STATUS_PENDING
	case "available", "uploaded", "created":
		return pb.PackageStatus_PACKAGE_STATUS_AVAILABLE
	case "failed":
		return pb.PackageStatus_PACKAGE_STATUS_FAILED
	case "deleted":
		return pb.PackageStatus_PACKAGE_STATUS_DELETED
	default:
		return pb.PackageStatus_PACKAGE_STATUS_UNSPECIFIED
	}
}
