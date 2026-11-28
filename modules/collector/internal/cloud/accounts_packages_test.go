package cloud

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
	"github.com/tencentyun/cos-go-sdk-v5"
	"google.golang.org/protobuf/proto"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// fakeGateway 记下经网关调的方法，按预设返回响应。
type fakeGateway struct {
	calls    []string
	response *adminpb.GetCloudCredentialRsp
	err      error
}

func (g *fakeGateway) Invoke(_ context.Context, service, method string, req, rsp any) error {
	g.calls = append(g.calls, service+"/"+method+" "+req.(*adminpb.GetCloudCredentialReq).GetSecretId())
	if g.err != nil {
		return g.err
	}
	proto.Merge(rsp.(*adminpb.GetCloudCredentialRsp), g.response)
	return nil
}

// Collector 经网关调 SecretMgr 的专用方法 GetCloudCredential 读云账号密钥；被拒绝、不完整都报错。
func TestGatewayCredentialsUseDedicatedSecretMgrMethod(t *testing.T) {
	ctx := context.Background()
	account := store.CloudAccount{AccountID: "account-a", Provider: "tencent", CredentialSecretID: "cloud-secret"}
	gateway := &fakeGateway{response: &adminpb.GetCloudCredentialRsp{RetInfo: &adminpb.RetInfo{Code: adminpb.ErrorCode_SUCCESS}, Credential: &adminpb.CloudCredential{KeyId: "AKIDfake", SecretValue: "skey"}}}
	credential, err := (&GatewayCredentials{Gateway: gateway}).Resolve(ctx, account)
	require.NoError(t, err)
	require.Equal(t, Credential{AccountID: "account-a", SecretID: "AKIDfake", SecretKey: "skey"}, credential, "限流配额以云账号标识为准")
	require.Equal(t, []string{"trpc.moox.ops.SecretMgr/GetCloudCredential cloud-secret"}, gateway.calls)

	for name, response := range map[string]*adminpb.GetCloudCredentialRsp{
		"被拒绝":  {RetInfo: &adminpb.RetInfo{Code: adminpb.ErrorCode_INVALID_PARAM, Msg: "只能读取类别为 cloud 的秘钥"}},
		"不完整":  {RetInfo: &adminpb.RetInfo{Code: adminpb.ErrorCode_SUCCESS}, Credential: &adminpb.CloudCredential{KeyId: "AKIDfake"}},
		"没有内容": {RetInfo: &adminpb.RetInfo{Code: adminpb.ErrorCode_SUCCESS}},
	} {
		_, err := (&GatewayCredentials{Gateway: &fakeGateway{response: response}}).Resolve(ctx, account)
		require.Error(t, err, name)
	}
	_, err = (&GatewayCredentials{Gateway: &fakeGateway{err: errors.New("网关不通")}}).Resolve(ctx, account)
	require.ErrorContains(t, err, "网关不通")

	// 不是腾讯云账号或没登记密钥 ID：不去问 SecretMgr。
	unused := &fakeGateway{}
	for _, bad := range []store.CloudAccount{{AccountID: "a", Provider: "aliyun", CredentialSecretID: "s"}, {AccountID: "a", Provider: "tencent"}} {
		_, err := (&GatewayCredentials{Gateway: unused}).Resolve(ctx, bad)
		require.Error(t, err)
	}
	require.Empty(t, unused.calls)
}

// 云账号只支持腾讯云、必须登记密钥 ID；账号下还有代码包时不能删。
func TestAccountsValidateAndRefuseDeletingAccountWithPackages(t *testing.T) {
	ctx := context.Background()
	db := openTestStore(t)
	accounts := NewAccounts(db)
	require.ErrorContains(t, accounts.Save(ctx, store.CloudAccount{AccountID: "a", Provider: "aliyun", CredentialSecretID: "s"}), "腾讯云")
	require.ErrorContains(t, accounts.Save(ctx, store.CloudAccount{AccountID: "a"}), "密钥 ID")
	require.ErrorContains(t, accounts.Save(ctx, store.CloudAccount{CredentialSecretID: "s"}), "云账号 ID")
	require.NoError(t, accounts.Save(ctx, store.CloudAccount{AccountID: " a ", CredentialSecretID: "s"}))
	account, err := accounts.Get(ctx, "a")
	require.NoError(t, err)
	require.Equal(t, "tencent", account.Provider)

	require.NoError(t, db.FunctionPackages().Upsert(ctx, store.FunctionPackage{PackageID: "p1", PackageName: "collector", CloudAccountID: "a"}))
	require.ErrorIs(t, accounts.Delete(ctx, "a"), ErrAccountInUse)
	require.NoError(t, db.FunctionPackages().Delete(ctx, "p1"))
	require.NoError(t, accounts.Delete(ctx, "a"))
	listed, err := accounts.List(ctx)
	require.NoError(t, err)
	require.Empty(t, listed)
}

// fakeObjects 是内存里的 COS：记下对象，按预设让删除失败。
type fakeObjects struct {
	objects   map[string]bool
	deleted   []string
	failKey   string
	ensured   int
	verifyErr error
}

func (o *fakeObjects) EnsureBucket(context.Context) error { o.ensured++; return nil }
func (o *fakeObjects) PresignPut(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://fake-bucket.cos.example/" + key + "?sign=fake", nil
}
func (o *fakeObjects) Verify(_ context.Context, key string, _ int64, _ string) error {
	if o.verifyErr != nil {
		return o.verifyErr
	}
	if !o.objects[key] {
		return errors.New("COS 里没有找到上传的对象")
	}
	return nil
}
func (o *fakeObjects) Delete(_ context.Context, key string) error {
	if key == o.failKey {
		return errors.New("COS 暂时不可用")
	}
	o.deleted = append(o.deleted, key)
	delete(o.objects, key)
	return nil
}

func newTestPackages(t *testing.T) (*Packages, *store.Store, *fakeObjects) {
	t.Helper()
	db := openTestStore(t)
	require.NoError(t, db.CloudAccounts().Upsert(context.Background(), store.CloudAccount{AccountID: "account-a", Provider: "tencent", CredentialSecretID: "s", COSRegion: "ap-guangzhou", COSBucket: "fake-bucket"}))
	objects := &fakeObjects{objects: map[string]bool{}}
	packages := NewPackages(db, fakeCredentials{}, "account-a")
	packages.objects = func(bucket, region string, _ Credential) objectStore {
		require.Equal(t, "fake-bucket", bucket)
		require.Equal(t, "ap-guangzhou", region)
		return objects
	}
	return packages, db, objects
}

// 上传：发起时登记待上传的包并给出预签名地址；核对过 COS 对象才变成可用，成为最新包。
func TestPackageUploadRegistersPendingThenAvailable(t *testing.T) {
	ctx := context.Background()
	packages, db, objects := newTestPackages(t)
	noAccount := NewPackages(db, fakeCredentials{}, "")
	_, err := noAccount.InitUpload(ctx, UploadRequest{PackageName: "collector", Version: "v1", Runtime: "Go1"})
	require.ErrorContains(t, err, "云账号")
	upload, err := packages.InitUpload(ctx, UploadRequest{PackageName: "collector", Version: "v1", Runtime: "Go1"})
	require.NoError(t, err)
	require.Equal(t, 1, objects.ensured, "发起上传前保证桶与跨域规则就绪")
	require.Equal(t, store.FunctionPackagePending, upload.Package.Status)
	require.True(t, strings.HasPrefix(upload.Package.PackageID, "collector_v1_"))
	require.Contains(t, upload.UploadURL, upload.Package.COSPath)
	latest, err := packages.Latest(ctx, "collector")
	require.NoError(t, err)
	require.Nil(t, latest, "还没上传完的包不算")

	_, err = packages.CompleteUpload(ctx, upload.Package.PackageID, 10, "abc")
	require.ErrorContains(t, err, "没有找到")
	objects.objects[upload.Package.COSPath] = true
	done, err := packages.CompleteUpload(ctx, upload.Package.PackageID, 10, "ABC")
	require.NoError(t, err)
	require.Equal(t, store.FunctionPackageAvailable, done.Status)
	require.Equal(t, "abc", done.FileMD5)
	latest, err = packages.Latest(ctx, "collector")
	require.NoError(t, err)
	require.Equal(t, upload.Package.PackageID, latest.PackageID)
	_, err = packages.CompleteUpload(ctx, upload.Package.PackageID, 10, "abc")
	require.ErrorContains(t, err, "不在等待上传")
}

// 清理：每个包名只留最新 3 个可用包，更早的连同 COS 对象删除；上传超过 6 小时仍没完成的删除；
// COS 对象删不掉时记录保留，下次再删。
func TestPackageCleanupKeepsNewestThreePerNameAndDropsStalePending(t *testing.T) {
	ctx := context.Background()
	packages, db, objects := newTestPackages(t)
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	packages.now = func() time.Time { return now }
	add := func(id, name, status string, created time.Time) {
		key := "moox/cloud-packages/" + id + ".zip"
		objects.objects[key] = true
		require.NoError(t, db.FunctionPackages().Upsert(ctx, store.FunctionPackage{PackageID: id, PackageName: name, CloudAccountID: "account-a", COSRegion: "ap-guangzhou", COSBucket: "fake-bucket", COSPath: key, Status: status, CreateTime: created}))
	}
	recent := now.Add(-time.Hour)
	for _, id := range []string{"c1", "c2", "c3", "c4", "c5"} {
		add(id, "collector", store.FunctionPackageAvailable, recent)
	}
	add("other1", "other", store.FunctionPackageAvailable, recent)
	add("pending-old", "collector", store.FunctionPackagePending, now.Add(-7*time.Hour))
	add("pending-new", "collector", store.FunctionPackagePending, now.Add(-time.Hour))
	objects.failKey = "moox/cloud-packages/c1.zip"

	result, err := packages.Cleanup(ctx)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"c2", "pending-old"}, result.Deleted)
	require.Len(t, result.Failed, 1)
	require.True(t, strings.HasPrefix(result.Failed[0], "c1："))
	require.ElementsMatch(t, []string{"moox/cloud-packages/c2.zip", "moox/cloud-packages/pending-old.zip"}, objects.deleted)
	left, err := packages.List(ctx)
	require.NoError(t, err)
	var ids []string
	for _, pkg := range left {
		ids = append(ids, pkg.PackageID)
	}
	require.ElementsMatch(t, []string{"c1", "c3", "c4", "c5", "other1", "pending-new"}, ids)

	objects.failKey = ""
	result, err = packages.Cleanup(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"c1"}, result.Deleted)
	require.Empty(t, result.Failed)

	require.NoError(t, packages.Delete(ctx, "c3"))
	require.Contains(t, objects.deleted, "moox/cloud-packages/c3.zip")
	require.NoError(t, packages.Delete(ctx, "absent"))
}

type fakeCORS struct {
	rules   []cos.BucketCORSRule
	getErr  error
	putErrs []error
	puts    int
}

func (f *fakeCORS) GetCORS(context.Context) (*cos.BucketGetCORSResult, *cos.Response, error) {
	if f.getErr != nil {
		return nil, nil, f.getErr
	}
	return &cos.BucketGetCORSResult{Rules: f.rules}, nil, nil
}

func (f *fakeCORS) PutCORS(_ context.Context, opt *cos.BucketPutCORSOptions) (*cos.Response, error) {
	f.puts++
	if len(f.putErrs) > 0 {
		err := f.putErrs[0]
		f.putErrs = f.putErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	f.rules = opt.Rules
	return nil, nil
}

// 首次配置失败后重试（桶已存在）必须补上跨域规则；已有允许 PUT 的规则时不改动；读取失败不能当作已配置。
func TestEnsureBucketCORSRetriesAfterFailureAndKeepsExistingRules(t *testing.T) {
	notFound := &cos.ErrorResponse{Response: &http.Response{StatusCode: http.StatusNotFound}, Code: "NoSuchCORSConfiguration"}
	api := &fakeCORS{getErr: notFound, putErrs: []error{errors.New("network")}}
	require.Error(t, ensureBucketCORS(context.Background(), api, "bucket"), "首次配置失败要报错")
	require.NoError(t, ensureBucketCORS(context.Background(), api, "bucket"), "重试时补上规则")
	require.Equal(t, 2, api.puts)
	require.Contains(t, api.rules[0].AllowedMethods, "PUT")

	existing := &fakeCORS{rules: []cos.BucketCORSRule{{AllowedMethods: []string{"put", "GET"}, AllowedOrigins: []string{"https://console.example"}}}}
	require.NoError(t, ensureBucketCORS(context.Background(), existing, "bucket"))
	require.Zero(t, existing.puts, "已有允许 PUT 的规则不改动")

	denied := &fakeCORS{getErr: errors.New("access denied")}
	require.Error(t, ensureBucketCORS(context.Background(), denied, "bucket"))
}
