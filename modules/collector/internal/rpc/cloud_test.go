package rpc

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/cloud"
	"github.com/mooyang-code/moox/modules/collector/internal/fetchplan"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/stretchr/testify/require"
)

// noCredentials 是不会被用到的密钥读取（这些用例不碰 COS 与腾讯云）。
type noCredentials struct{}

func (noCredentials) Resolve(context.Context, store.CloudAccount) (cloud.Credential, error) {
	return cloud.Credential{}, errors.New("测试里不读密钥")
}

func newCloudFixture(t *testing.T, inUse func(context.Context, string) error) (*Service, *store.Store) {
	t.Helper()
	service, db := newReregisterFixture(t)
	service.SetCloud(CloudDeps{Accounts: cloud.NewAccounts(db), Packages: cloud.NewPackages(db, noCredentials{}, "acct"), AccountInUse: inUse, Reconcilers: &fetchplan.Reconcilers{}})
	return service, db
}

func notInUse(context.Context, string) error { return nil }

// 没注入云端能力时，账号、代码包、云函数方法都报内部错误，不 panic。
func TestCloudMethodsReportMissingDependencies(t *testing.T) {
	service := &Service{}
	ctx := context.Background()
	accounts, err := service.ListCloudAccounts(ctx, &pb.ListCloudAccountsReq{})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, accounts.GetRetInfo().GetCode())
	created, err := service.CreateCloudAccount(ctx, &pb.CreateCloudAccountReq{Account: &pb.CloudAccount{AccountId: "a"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, created.GetRetInfo().GetCode())
	deleted, err := service.DeleteCloudAccount(ctx, &pb.DeleteCloudAccountReq{AccountId: "a"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, deleted.GetRetInfo().GetCode())
	packages, err := service.ListFunctionPackages(ctx, &pb.ListFunctionPackagesReq{})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, packages.GetRetInfo().GetCode())
	upload, err := service.InitPackageUpload(ctx, &pb.InitPackageUploadReq{})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, upload.GetRetInfo().GetCode())
	functions, err := service.ListCloudFunctions(ctx, &pb.ListCloudFunctionsReq{SpaceId: "crypto"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, functions.GetRetInfo().GetCode())
}

// 参数缺失返回 INVALID_PARAM。
func TestCloudMethodsValidateParameters(t *testing.T) {
	service, _ := newCloudFixture(t, notInUse)
	ctx := context.Background()
	created, err := service.CreateCloudAccount(ctx, &pb.CreateCloudAccountReq{})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, created.GetRetInfo().GetCode())
	deleted, err := service.DeleteCloudAccount(ctx, &pb.DeleteCloudAccountReq{})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, deleted.GetRetInfo().GetCode())
	upload, err := service.InitPackageUpload(ctx, &pb.InitPackageUploadReq{PackageName: "collector"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, upload.GetRetInfo().GetCode())
	complete, err := service.CompletePackageUpload(ctx, &pb.CompletePackageUploadReq{PackageId: "p"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, complete.GetRetInfo().GetCode())
	removed, err := service.DeleteFunctionPackage(ctx, &pb.DeleteFunctionPackageReq{})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, removed.GetRetInfo().GetCode())
	for name, code := range map[string]pb.ErrorCode{
		"list":   mustCode(service.ListCloudFunctions(ctx, &pb.ListCloudFunctionsReq{})),
		"delete": mustCode(service.DeleteCloudFunctions(ctx, &pb.DeleteCloudFunctionsReq{})),
		"probe":  mustCode(service.ProbeFunctionEgress(ctx, &pb.ProbeFunctionEgressReq{})),
	} {
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, code, name)
	}
}

type retInfoGetter interface{ GetRetInfo() *pb.RetInfo }

func mustCode[T retInfoGetter](rsp T, err error) pb.ErrorCode {
	if err != nil {
		panic(err)
	}
	return rsp.GetRetInfo().GetCode()
}

// 空间没有对账器（不在管理集合里）时，云函数三个方法都报 NOT_FOUND 并说明原因。
func TestCloudFunctionMethodsNeedTheSpaceReconciler(t *testing.T) {
	service, _ := newCloudFixture(t, notInUse)
	ctx := context.Background()
	list, err := service.ListCloudFunctions(ctx, &pb.ListCloudFunctionsReq{SpaceId: "crypto"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NOT_FOUND, list.GetRetInfo().GetCode())
	require.Contains(t, list.GetRetInfo().GetMsg(), "对账器")
	deleted, err := service.DeleteCloudFunctions(ctx, &pb.DeleteCloudFunctionsReq{SpaceId: "crypto", Names: []string{"f"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NOT_FOUND, deleted.GetRetInfo().GetCode())
	probe, err := service.ProbeFunctionEgress(ctx, &pb.ProbeFunctionEgressReq{SpaceId: "crypto"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NOT_FOUND, probe.GetRetInfo().GetCode())
}

// 云账号的增改查：新建同 ID 冲突，更新不存在的账号报 NOT_FOUND，只支持腾讯云、必须登记密钥 ID。
func TestCloudAccountCreateUpdateList(t *testing.T) {
	service, _ := newCloudFixture(t, notInUse)
	ctx := context.Background()
	account := &pb.CloudAccount{AccountId: "acct", AccountName: "主账号", CredentialSecretId: "secret-1", CosRegion: "ap-guangzhou", CosBucket: "bucket-1"}
	created, err := service.CreateCloudAccount(ctx, &pb.CreateCloudAccountReq{Account: account})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, created.GetRetInfo().GetCode(), created.GetRetInfo().GetMsg())
	require.Equal(t, "tencent", created.GetAccount().GetProvider())
	require.Positive(t, created.GetAccount().GetCreatedAt())
	again, err := service.CreateCloudAccount(ctx, &pb.CreateCloudAccountReq{Account: account})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_CONFLICT, again.GetRetInfo().GetCode())
	missing, err := service.UpdateCloudAccount(ctx, &pb.UpdateCloudAccountReq{Account: &pb.CloudAccount{AccountId: "other", CredentialSecretId: "s"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NOT_FOUND, missing.GetRetInfo().GetCode())
	wrong, err := service.UpdateCloudAccount(ctx, &pb.UpdateCloudAccountReq{Account: &pb.CloudAccount{AccountId: "acct", Provider: "aliyun", CredentialSecretId: "s"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, wrong.GetRetInfo().GetCode())
	updated, err := service.UpdateCloudAccount(ctx, &pb.UpdateCloudAccountReq{Account: &pb.CloudAccount{AccountId: "acct", AccountName: "改名", CredentialSecretId: "secret-2"}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, updated.GetRetInfo().GetCode(), updated.GetRetInfo().GetMsg())
	list, err := service.ListCloudAccounts(ctx, &pb.ListCloudAccountsReq{})
	require.NoError(t, err)
	require.Len(t, list.GetAccounts(), 1)
	require.Equal(t, "改名", list.GetAccounts()[0].GetAccountName())
	require.Equal(t, "secret-2", list.GetAccounts()[0].GetCredentialSecretId())
	regions, err := service.ListCloudRegions(ctx, &pb.ListCloudRegionsReq{})
	require.NoError(t, err)
	require.NotEmpty(t, regions.GetRegions())
}

// 删云账号：被 scf_plan 引用或云上还有它建的函数时拒绝（CONFLICT，msg 是原因）；还有代码包时同样拒绝；都没有才删。
func TestDeleteCloudAccountRefusesWhileTheAccountIsInUse(t *testing.T) {
	inUse := map[string]error{"referenced": fmt.Errorf("%w：scf_plan.cloud_account_id 引用了它", cloud.ErrAccountInUse)}
	service, db := newCloudFixture(t, func(_ context.Context, id string) error { return inUse[id] })
	ctx := context.Background()
	for _, id := range []string{"referenced", "packaged", "free"} {
		require.NoError(t, db.CloudAccounts().Upsert(ctx, store.CloudAccount{AccountID: id, Provider: "tencent", CredentialSecretID: "s"}))
	}
	require.NoError(t, db.FunctionPackages().Upsert(ctx, store.FunctionPackage{PackageID: "pkg-1", PackageName: "collector", CloudAccountID: "packaged", Status: store.FunctionPackageAvailable}))
	referenced, err := service.DeleteCloudAccount(ctx, &pb.DeleteCloudAccountReq{AccountId: "referenced"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_CONFLICT, referenced.GetRetInfo().GetCode())
	require.Contains(t, referenced.GetRetInfo().GetMsg(), "scf_plan")
	packaged, err := service.DeleteCloudAccount(ctx, &pb.DeleteCloudAccountReq{AccountId: "packaged"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_CONFLICT, packaged.GetRetInfo().GetCode())
	require.Contains(t, packaged.GetRetInfo().GetMsg(), "代码包")
	free, err := service.DeleteCloudAccount(ctx, &pb.DeleteCloudAccountReq{AccountId: "free"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, free.GetRetInfo().GetCode())
	for id, want := range map[string]bool{"referenced": true, "packaged": true, "free": false} {
		account, err := db.CloudAccounts().Get(ctx, id)
		require.NoError(t, err)
		require.Equal(t, want, account != nil, id)
	}
	// 读不了云上（AccountInUse 返回普通错误）时报内部错误，同样不删。
	failing, _ := newCloudFixture(t, func(context.Context, string) error { return errors.New("网络不通") })
	rsp, err := failing.DeleteCloudAccount(ctx, &pb.DeleteCloudAccountReq{AccountId: "free"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
}

// 代码包列表全局返回；删除不存在的包也成功。
func TestListAndDeleteFunctionPackages(t *testing.T) {
	service, db := newCloudFixture(t, notInUse)
	ctx := context.Background()
	require.NoError(t, db.FunctionPackages().Upsert(ctx, store.FunctionPackage{PackageID: "pkg-1", PackageName: "collector", Version: "v1", CloudAccountID: "acct", Status: store.FunctionPackageAvailable}))
	require.NoError(t, db.FunctionPackages().Upsert(ctx, store.FunctionPackage{PackageID: "pkg-2", PackageName: "collector", Version: "v2", CloudAccountID: "acct", Status: store.FunctionPackagePending}))
	all, err := service.ListFunctionPackages(ctx, &pb.ListFunctionPackagesReq{})
	require.NoError(t, err)
	require.Len(t, all.GetPackages(), 2)
	require.Equal(t, "pkg-2", all.GetPackages()[0].GetPackageId())
	require.Equal(t, store.FunctionPackagePending, all.GetPackages()[0].GetStatus())
	removed, err := service.DeleteFunctionPackage(ctx, &pb.DeleteFunctionPackageReq{PackageId: "absent"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, removed.GetRetInfo().GetCode())
}

// 运行状态里的“云上状态”：读回成功时填存在、定时器、代码包与读回时间；读回出错时只填读回时间；只读了代码包时定时器算不一致。
func TestApplyCloudReadbackFillsTheStatusColumns(t *testing.T) {
	readAt := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	timer := &cloud.TimerState{Timer: cloud.Timer{Cron: "0 * * * * * *", Enabled: true}, AvailableStatus: "Available", Qualifier: "$LATEST", Message: "market_fetch_timer_v1"}
	latest := func(id string) bool { return id == "pkg-2" }
	ok := &pb.CollectorFunctionStatus{Name: "f", Executor: "scf", Cron: "0 * * * * * *"}
	applyCloudReadback(ok, fetchplan.FunctionReadback{FunctionState: cloud.FunctionState{Exists: true, PackageID: "pkg-2", Timer: timer, ReadAt: readAt}}, latest)
	require.True(t, ok.GetCloudExists())
	require.True(t, ok.GetCloudTimerMatches())
	require.True(t, ok.GetCloudPackageLatest())
	require.Equal(t, readAt.UnixMilli(), ok.GetCloudReadAt())

	drift := &pb.CollectorFunctionStatus{Name: "f", Executor: "scf", Cron: "5 * * * * * *"}
	applyCloudReadback(drift, fetchplan.FunctionReadback{FunctionState: cloud.FunctionState{Exists: true, PackageID: "pkg-1", Timer: timer, ReadAt: readAt}}, latest)
	require.True(t, drift.GetCloudExists())
	require.False(t, drift.GetCloudTimerMatches(), "cron 与期望不同")
	require.False(t, drift.GetCloudPackageLatest(), "代码包落后")

	retired := &pb.CollectorFunctionStatus{Name: "f", Executor: "scf"}
	applyCloudReadback(retired, fetchplan.FunctionReadback{FunctionState: cloud.FunctionState{Exists: true, PackageID: "pkg-2", ReadAt: readAt}}, latest)
	require.True(t, retired.GetCloudTimerMatches(), "期望停用、云上没有定时器")

	codeOnly := &pb.CollectorFunctionStatus{Name: "f", Executor: "scf", Cron: "0 * * * * * *"}
	applyCloudReadback(codeOnly, fetchplan.FunctionReadback{FunctionState: cloud.FunctionState{Exists: true, PackageID: "pkg-2", ReadAt: readAt}, CodeOnly: true}, latest)
	require.False(t, codeOnly.GetCloudTimerMatches(), "没读定时器不能算一致")

	failed := &pb.CollectorFunctionStatus{Name: "f", Executor: "scf", Cron: "0 * * * * * *"}
	applyCloudReadback(failed, fetchplan.FunctionReadback{FunctionState: cloud.FunctionState{ReadAt: readAt}, Error: "限频"}, latest)
	require.False(t, failed.GetCloudExists())
	require.Equal(t, readAt.UnixMilli(), failed.GetCloudReadAt())
}

// 定时器一致的口径与对账器相同：可用状态、Qualifier、Message 任何一项不对都算不一致。
func TestCloudTimerMatchesChecksAvailabilityQualifierAndMessage(t *testing.T) {
	good := cloud.TimerState{Timer: cloud.Timer{Cron: "0 * * * * * *", Enabled: true}, AvailableStatus: "Available", Qualifier: "$LATEST", Message: "market_fetch_timer_v1"}
	require.True(t, fetchplan.TimerMatches(&good, "0 * * * * * *"))
	for name, change := range map[string]func(*cloud.TimerState){
		"停用":        func(t *cloud.TimerState) { t.Enabled = false },
		"不可用":       func(t *cloud.TimerState) { t.AvailableStatus = "Unavailable" },
		"Qualifier": func(t *cloud.TimerState) { t.Qualifier = "1" },
		"Message":   func(t *cloud.TimerState) { t.Message = "other" },
	} {
		timer := good
		change(&timer)
		require.False(t, fetchplan.TimerMatches(&timer, "0 * * * * * *"), name)
	}
	require.False(t, fetchplan.TimerMatches(nil, "0 * * * * * *"))
	require.True(t, fetchplan.TimerMatches(nil, ""))
	require.False(t, fetchplan.TimerMatches(&good, ""), "期望停用时云上启用算不一致")
}

// 没有对账器时运行状态不填云上状态（Collector 刚重启、空间不在管理集合里）。
func TestCollectorStatusLeavesCloudColumnsEmptyWithoutReadbacks(t *testing.T) {
	service, db := newCloudFixture(t, notInUse)
	applyPlan(t, db, "crypto", `{"datasets":{},"functions":{"moox-fetch-plan-crypto-spot-nanjing-000":{"name":"moox-fetch-plan-crypto-spot-nanjing-000","executor":"scf","region":"ap-nanjing","market":"spot",`+
		`"target":{"v":1,"executor":"scf","market":{"market_id":"crypto","instrument_type":"spot","provider":"binance","source_id":"binance","market_type":"spot"},"fetch":{},"datasets":[{"dataset_id":"ds","frequency":"1m","copies":[{"subjects":[{"id":"BTC-USDT","index":0}],"hash":"h","count":1}]}]}}}}`)
	rsp, err := service.GetCollectorStatus(context.Background(), &pb.GetCollectorStatusReq{SpaceId: "crypto"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.Len(t, rsp.GetFunctions(), 1)
	require.Equal(t, "scf", rsp.GetFunctions()[0].GetExecutor())
	require.False(t, rsp.GetFunctions()[0].GetCloudExists())
	require.Zero(t, rsp.GetFunctions()[0].GetCloudReadAt())
}
