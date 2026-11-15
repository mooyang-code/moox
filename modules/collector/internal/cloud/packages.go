package cloud

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/tencentyun/cos-go-sdk-v5"
)

// Package 是一个云函数代码包（Collector 库里的一行）。
type Package = store.FunctionPackage

const (
	// keepPackagesPerName 是每个（空间、包名）保留的可用包个数：函数总是滚动到最新包，更早的连同 COS 对象删除。
	keepPackagesPerName = 3
	// pendingPackageTTL 是发出上传地址后多久还没完成上传就删除。
	pendingPackageTTL = 6 * time.Hour
	// uploadURLTTL 是预签名上传地址的有效期。
	uploadURLTTL = time.Hour
)

// objectStore 是代码包用到的 COS 能力：桶、预签名上传、核对与删除对象。
type objectStore interface {
	EnsureBucket(ctx context.Context) error
	PresignPut(ctx context.Context, key string, expires time.Duration) (string, error)
	Verify(ctx context.Context, key string, size int64, md5 string) error
	Delete(ctx context.Context, key string) error
}

// Packages 管理云函数代码包：上传（COS 预签名）、登记、取最新、删除与清理。
type Packages struct {
	packages    *store.FunctionPackageRepository
	accounts    *store.CloudAccountRepository
	credentials CredentialResolver
	// objects 按桶、地域与密钥打开 COS；测试里替换。
	objects func(bucket, region string, credential Credential) objectStore
	now     func() time.Time
}

// NewPackages 用 Collector 库里的代码包与云账号表构造代码包管理。
func NewPackages(db *store.Store, credentials CredentialResolver) *Packages {
	return &Packages{packages: db.FunctionPackages(), accounts: db.CloudAccounts(), credentials: credentials, objects: newCOSObjects, now: time.Now}
}

// UploadRequest 是发起一次代码包上传的请求。
type UploadRequest struct {
	SpaceID        string
	PackageName    string
	Version        string
	Runtime        string
	CloudAccountID string
	// Filename 是上传的文件名，为空时用“包名-版本.zip”。
	Filename string
}

// Upload 是发起上传的结果：登记好的待上传代码包与预签名上传地址。
type Upload struct {
	Package   Package
	UploadURL string
	ExpiresAt time.Time
}

// InitUpload 登记一个待上传的代码包并返回 COS 预签名上传地址；桶不存在时先建桶并配好跨域规则。
func (p *Packages) InitUpload(ctx context.Context, req UploadRequest) (Upload, error) {
	for name, value := range map[string]string{"空间": req.SpaceID, "包名": req.PackageName, "版本": req.Version, "运行时": req.Runtime, "云账号": req.CloudAccountID} {
		if strings.TrimSpace(value) == "" {
			return Upload{}, fmt.Errorf("上传代码包必须写明%s", name)
		}
	}
	account, err := p.accounts.Get(ctx, req.CloudAccountID)
	if err != nil {
		return Upload{}, err
	}
	if account == nil {
		return Upload{}, fmt.Errorf("云账号 %s 不存在", req.CloudAccountID)
	}
	if account.COSRegion == "" || account.COSBucket == "" {
		return Upload{}, fmt.Errorf("云账号 %s 没有配置 COS 地域与桶", account.AccountID)
	}
	credential, err := p.credentials.Resolve(ctx, *account)
	if err != nil {
		return Upload{}, err
	}
	objects := p.objects(account.COSBucket, account.COSRegion, credential)
	if err := objects.EnsureBucket(ctx); err != nil {
		return Upload{}, fmt.Errorf("准备 COS 桶失败: %w", err)
	}
	filename := sanitizePackageFileName(firstNonEmpty(req.Filename, req.PackageName+"-"+req.Version+".zip"))
	packageID := buildPackageID(req.PackageName, req.Version, uuid.NewString())
	now := p.now().UTC()
	pkg := Package{
		SpaceID: strings.TrimSpace(req.SpaceID), PackageID: packageID, PackageName: strings.TrimSpace(req.PackageName),
		Version: strings.TrimSpace(req.Version), Runtime: strings.TrimSpace(req.Runtime), CloudAccountID: account.AccountID,
		COSRegion: account.COSRegion, COSBucket: account.COSBucket, COSPath: buildPackageCOSPath(now, req.PackageName, req.Version, packageID, filename),
		Status: store.FunctionPackagePending, CreateTime: now,
	}
	uploadURL, err := objects.PresignPut(ctx, pkg.COSPath, uploadURLTTL)
	if err != nil {
		return Upload{}, fmt.Errorf("生成上传地址失败: %w", err)
	}
	if err := p.packages.Upsert(ctx, pkg); err != nil {
		return Upload{}, err
	}
	return Upload{Package: pkg, UploadURL: uploadURL, ExpiresAt: now.Add(uploadURLTTL)}, nil
}

// CompleteUpload 核对 COS 里的对象（大小与 MD5）后把代码包标成可用。
func (p *Packages) CompleteUpload(ctx context.Context, spaceID, packageID string, size int64, md5 string) (Package, error) {
	if size <= 0 || strings.TrimSpace(md5) == "" {
		return Package{}, fmt.Errorf("完成上传必须带文件大小与 MD5")
	}
	pkg, err := p.packages.Get(ctx, spaceID, packageID)
	if err != nil {
		return Package{}, err
	}
	if pkg == nil {
		return Package{}, fmt.Errorf("代码包 %s 不存在", packageID)
	}
	if pkg.Status != store.FunctionPackagePending {
		return Package{}, fmt.Errorf("代码包 %s 不在等待上传的状态", packageID)
	}
	objects, err := p.objectsFor(ctx, *pkg)
	if err != nil {
		return Package{}, err
	}
	if err := objects.Verify(ctx, pkg.COSPath, size, md5); err != nil {
		return Package{}, err
	}
	pkg.FileSize, pkg.FileMD5, pkg.Status = size, strings.ToLower(strings.TrimSpace(md5)), store.FunctionPackageAvailable
	if err := p.packages.Upsert(ctx, *pkg); err != nil {
		return Package{}, err
	}
	return *pkg, nil
}

// List 返回一个空间的代码包；spaceID 为空时返回全部空间的。
func (p *Packages) List(ctx context.Context, spaceID string) ([]Package, error) {
	return p.packages.List(ctx, spaceID)
}

// Latest 返回该空间某个包名最新的可用代码包；没有时返回 nil。
func (p *Packages) Latest(ctx context.Context, spaceID, packageName string) (*Package, error) {
	return p.packages.Latest(ctx, spaceID, packageName)
}

// Delete 删除一个代码包：先删 COS 对象，再删记录；对象删不掉时记录保留，下次再删。
func (p *Packages) Delete(ctx context.Context, spaceID, packageID string) error {
	pkg, err := p.packages.Get(ctx, spaceID, packageID)
	if err != nil || pkg == nil {
		return err
	}
	return p.remove(ctx, *pkg)
}

// CleanupResult 是一次代码包清理的结果。
type CleanupResult struct {
	// Deleted 是删掉的代码包 ID。
	Deleted []string
	// Failed 是没删掉的代码包及原因（“包 ID：原因”），下次清理再试。
	Failed []string
}

// Cleanup 清理代码包：每个（空间、包名）只留最新 3 个可用包，更早的连同 COS 对象删除；上传超过 6 小时仍没完成的删除。
func (p *Packages) Cleanup(ctx context.Context) (CleanupResult, error) {
	packages, err := p.packages.List(ctx, "")
	if err != nil {
		return CleanupResult{}, err
	}
	// List 按上传先后倒序：同一包名里先出现的更新。
	kept := map[string]int{}
	cutoff := p.now().Add(-pendingPackageTTL)
	var result CleanupResult
	for _, pkg := range packages {
		remove := false
		switch pkg.Status {
		case store.FunctionPackageAvailable:
			key := pkg.SpaceID + "\x00" + pkg.PackageName
			kept[key]++
			remove = kept[key] > keepPackagesPerName
		default:
			remove = pkg.CreateTime.Before(cutoff)
		}
		if !remove {
			continue
		}
		if err := p.remove(ctx, pkg); err != nil {
			result.Failed = append(result.Failed, pkg.PackageID+"："+err.Error())
			continue
		}
		result.Deleted = append(result.Deleted, pkg.PackageID)
	}
	return result, nil
}

func (p *Packages) remove(ctx context.Context, pkg Package) error {
	if strings.TrimSpace(pkg.COSPath) != "" {
		objects, err := p.objectsFor(ctx, pkg)
		if err != nil {
			return err
		}
		if err := objects.Delete(ctx, pkg.COSPath); err != nil {
			return fmt.Errorf("删除 COS 对象失败: %w", err)
		}
	}
	return p.packages.Delete(ctx, pkg.SpaceID, pkg.PackageID)
}

// objectsFor 用代码包记录里的桶与地域（账号的 COS 配置后来可能改过）、账号的密钥打开 COS。
func (p *Packages) objectsFor(ctx context.Context, pkg Package) (objectStore, error) {
	account, err := p.accounts.Get(ctx, pkg.CloudAccountID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, fmt.Errorf("代码包 %s 的云账号 %s 不存在", pkg.PackageID, pkg.CloudAccountID)
	}
	credential, err := p.credentials.Resolve(ctx, *account)
	if err != nil {
		return nil, err
	}
	return p.objects(pkg.COSBucket, pkg.COSRegion, credential), nil
}

func buildPackageID(packageName, version, uploadID string) string {
	return sanitizePackagePathSegment(packageName) + "_" + sanitizePackagePathSegment(version) + "_" + sanitizePackagePathSegment(uploadID)
}

func buildPackageCOSPath(now time.Time, packageName, version, packageID, filename string) string {
	return path.Join("moox", "cloud-packages", now.UTC().Format("2006-01-02"),
		sanitizePackagePathSegment(packageName), sanitizePackagePathSegment(version), sanitizePackagePathSegment(packageID), filename)
}

func sanitizePackageFileName(value string) string {
	if cleaned := sanitizePackagePathSegment(value); cleaned != "" {
		return cleaned
	}
	return "package.zip"
}

func sanitizePackagePathSegment(value string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(value) {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	return strings.Trim(b.String(), "_.-")
}

// cosObjects 是 objectStore 的腾讯云 COS 实现。
type cosObjects struct {
	client     *cos.Client
	bucket     string
	credential Credential
}

func newCOSObjects(bucket, region string, credential Credential) objectStore {
	bucketURL, _ := url.Parse(fmt.Sprintf("https://%s.cos.%s.myqcloud.com", bucket, region))
	client := cos.NewClient(&cos.BaseURL{BucketURL: bucketURL}, &http.Client{Transport: &cos.AuthorizationTransport{SecretID: credential.SecretID, SecretKey: credential.SecretKey}})
	return &cosObjects{client: client, bucket: bucket, credential: credential}
}

// EnsureBucket 让发布自给自足：桶不存在就建一个私有桶，再保证跨域规则允许管理台直传。
func (c *cosObjects) EnsureBucket(ctx context.Context) error {
	if _, err := c.client.Bucket.Head(ctx); err != nil {
		if !cos.IsNotFoundError(err) {
			return fmt.Errorf("检查桶 %s 失败: %w", c.bucket, err)
		}
		if _, err := c.client.Bucket.Put(ctx, &cos.BucketPutOptions{XCosACL: "private"}); err != nil {
			// 并发的另一次上传可能刚建好它。
			if _, verifyErr := c.client.Bucket.Head(ctx); verifyErr != nil {
				return fmt.Errorf("创建桶 %s 失败: %w", c.bucket, err)
			}
		} else if _, err := c.client.Bucket.Head(ctx); err != nil {
			return fmt.Errorf("核对桶 %s 失败: %w", c.bucket, err)
		}
	}
	// 桶存在与跨域就绪分开处理：首次配置跨域失败后，重试时桶已存在，也要补上规则。
	return ensureBucketCORS(ctx, c.client.Bucket, c.bucket)
}

// PresignPut 生成带签名的限时上传地址：私有桶里的对象必须带签名才能访问。
func (c *cosObjects) PresignPut(ctx context.Context, key string, expires time.Duration) (string, error) {
	u, err := c.client.Object.GetPresignedURL(ctx, http.MethodPut, strings.TrimPrefix(key, "/"), c.credential.SecretID, c.credential.SecretKey, expires, nil)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// Verify 核对对象的大小与 MD5（COS 的 ETag）。
func (c *cosObjects) Verify(ctx context.Context, key string, size int64, md5 string) error {
	resp, err := c.client.Object.Head(ctx, strings.TrimPrefix(key, "/"), nil)
	if err != nil {
		return fmt.Errorf("COS 里没有找到上传的对象: %w", err)
	}
	if resp == nil || resp.Header == nil {
		return fmt.Errorf("COS 读回的对象信息为空")
	}
	contentLength := resp.ContentLength
	if contentLength == 0 {
		if raw := strings.TrimSpace(resp.Header.Get("Content-Length")); raw != "" {
			if _, err := fmt.Sscanf(raw, "%d", &contentLength); err != nil {
				return fmt.Errorf("COS 返回的 Content-Length 无效: %s", raw)
			}
		}
	}
	if contentLength != size {
		return fmt.Errorf("对象大小不一致：COS 是 %d，期望 %d", contentLength, size)
	}
	etag := strings.Trim(strings.ToLower(resp.Header.Get("ETag")), `"`)
	if etag == "" {
		return fmt.Errorf("COS 返回的 ETag 为空")
	}
	if want := strings.ToLower(strings.TrimSpace(md5)); etag != want {
		return fmt.Errorf("对象 MD5 不一致：COS 是 %s，期望 %s", etag, want)
	}
	return nil
}

// Delete 删除对象；对象已经不在时不报错。
func (c *cosObjects) Delete(ctx context.Context, key string) error {
	if _, err := c.client.Object.Delete(ctx, strings.TrimPrefix(key, "/")); err != nil && !cos.IsNotFoundError(err) {
		return err
	}
	return nil
}

// corsAPI 是 ensureBucketCORS 需要的桶跨域规则读写能力（*cos.BucketService 提供）。
type corsAPI interface {
	GetCORS(ctx context.Context) (*cos.BucketGetCORSResult, *cos.Response, error)
	PutCORS(ctx context.Context, opt *cos.BucketPutCORSOptions) (*cos.Response, error)
}

// ensureBucketCORS 保证桶允许浏览器跨域 PUT/GET 预签名地址：管理台直接上传到 COS，预检需要桶的跨域规则。
// 桶仍是私有的，所有访问都要带签名，所以放开来源。已经有包含 PUT 的规则时不改动。
func ensureBucketCORS(ctx context.Context, api corsAPI, bucket string) error {
	result, _, err := api.GetCORS(ctx)
	if err == nil && result != nil {
		for _, rule := range result.Rules {
			for _, method := range rule.AllowedMethods {
				if strings.EqualFold(method, "PUT") {
					return nil
				}
			}
		}
	} else if err != nil && !cos.IsNotFoundError(err) {
		return fmt.Errorf("读取桶 %s 的跨域规则失败: %w", bucket, err)
	}
	if _, err := api.PutCORS(ctx, &cos.BucketPutCORSOptions{Rules: []cos.BucketCORSRule{{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"PUT", "GET", "HEAD"},
		AllowedHeaders: []string{"*"},
		ExposeHeaders:  []string{"ETag"},
		MaxAgeSeconds:  3600,
	}}}); err != nil {
		return fmt.Errorf("配置桶 %s 的跨域规则失败: %w", bucket, err)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
