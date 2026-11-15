package rpc

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/cloud"
	"github.com/mooyang-code/moox/modules/collector/internal/fetchplan"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
)

// CloudDeps 是云账号、代码包、云函数相关方法用到的能力（Collector 直接调腾讯云，见 internal/cloud）。
type CloudDeps struct {
	Accounts *cloud.Accounts
	Packages *cloud.Packages
	// AccountInUse 判断云账号还在不在用（被 scf_plan 引用、云上还有它建的函数）；返回非空错误表示不能删。代码包由 Accounts.Delete 检查。
	AccountInUse func(ctx context.Context, accountID string) error
	// Reconcilers 按空间找到采集计划对账器：列出、删除、探测云函数都经它（删除经操作表）。
	Reconcilers *fetchplan.Reconcilers
}

// SetCloud 注入云账号、代码包与云函数相关方法用到的能力。
func (s *Service) SetCloud(deps CloudDeps) { s.cloud = deps }

const (
	errNoCloudAccounts = "Collector 没有配置云账号管理"
	errNoCloudPackages = "Collector 没有配置代码包管理"
	errNoReconcilers   = "Collector 没有配置采集计划对账器"
)

// ----- 云账号 -----

// ListCloudAccounts 返回全部云账号。
func (s *Service) ListCloudAccounts(ctx context.Context, _ *pb.ListCloudAccountsReq) (*pb.ListCloudAccountsRsp, error) {
	if s.cloud.Accounts == nil {
		return &pb.ListCloudAccountsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, errNoCloudAccounts)}, nil
	}
	accounts, err := s.cloud.Accounts.List(ctx)
	if err != nil {
		return &pb.ListCloudAccountsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	rsp := &pb.ListCloudAccountsRsp{RetInfo: retOK()}
	for _, account := range accounts {
		rsp.Accounts = append(rsp.Accounts, cloudAccountToPB(account))
	}
	return rsp, nil
}

// CreateCloudAccount 登记一个新的云账号；同 ID 的账号已存在时拒绝。
func (s *Service) CreateCloudAccount(ctx context.Context, req *pb.CreateCloudAccountReq) (*pb.CreateCloudAccountRsp, error) {
	account, code, msg := s.saveCloudAccount(ctx, req.GetAccount(), true)
	if code != pb.ErrorCode_SUCCESS {
		return &pb.CreateCloudAccountRsp{RetInfo: retErr(code, msg)}, nil
	}
	return &pb.CreateCloudAccountRsp{RetInfo: retOK(), Account: account}, nil
}

// UpdateCloudAccount 整行更新已有的云账号；账号不存在时返回 NOT_FOUND。
func (s *Service) UpdateCloudAccount(ctx context.Context, req *pb.UpdateCloudAccountReq) (*pb.UpdateCloudAccountRsp, error) {
	account, code, msg := s.saveCloudAccount(ctx, req.GetAccount(), false)
	if code != pb.ErrorCode_SUCCESS {
		return &pb.UpdateCloudAccountRsp{RetInfo: retErr(code, msg)}, nil
	}
	return &pb.UpdateCloudAccountRsp{RetInfo: retOK(), Account: account}, nil
}

func (s *Service) saveCloudAccount(ctx context.Context, input *pb.CloudAccount, create bool) (*pb.CloudAccount, pb.ErrorCode, string) {
	if s.cloud.Accounts == nil {
		return nil, pb.ErrorCode_INNER_ERR, errNoCloudAccounts
	}
	accountID := strings.TrimSpace(input.GetAccountId())
	if accountID == "" {
		return nil, pb.ErrorCode_INVALID_PARAM, "account.account_id 不能为空"
	}
	existing, err := s.cloud.Accounts.Get(ctx, accountID)
	if err != nil {
		return nil, pb.ErrorCode_INNER_ERR, err.Error()
	}
	switch {
	case create && existing != nil:
		return nil, pb.ErrorCode_CONFLICT, "云账号 " + accountID + " 已存在"
	case !create && existing == nil:
		return nil, pb.ErrorCode_NOT_FOUND, "云账号 " + accountID + " 不存在"
	}
	account := store.CloudAccount{
		AccountID: accountID, AccountName: strings.TrimSpace(input.GetAccountName()), Provider: strings.TrimSpace(input.GetProvider()),
		CredentialSecretID: input.GetCredentialSecretId(), AppID: strings.TrimSpace(input.GetAppId()),
		COSRegion: strings.TrimSpace(input.GetCosRegion()), COSBucket: strings.TrimSpace(input.GetCosBucket()),
	}
	if err := s.cloud.Accounts.Save(ctx, account); err != nil {
		return nil, pb.ErrorCode_INVALID_PARAM, err.Error()
	}
	saved, err := s.cloud.Accounts.Get(ctx, accountID)
	if err != nil || saved == nil {
		return cloudAccountToPB(account), pb.ErrorCode_SUCCESS, ""
	}
	return cloudAccountToPB(*saved), pb.ErrorCode_SUCCESS, ""
}

// DeleteCloudAccount 删除云账号。账号还被 scf_plan 引用、云上还有它建的函数或还有代码包时拒绝（CONFLICT），msg 是原因。
func (s *Service) DeleteCloudAccount(ctx context.Context, req *pb.DeleteCloudAccountReq) (*pb.DeleteCloudAccountRsp, error) {
	if s.cloud.Accounts == nil || s.cloud.AccountInUse == nil {
		return &pb.DeleteCloudAccountRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, errNoCloudAccounts)}, nil
	}
	accountID := strings.TrimSpace(req.GetAccountId())
	if accountID == "" {
		return &pb.DeleteCloudAccountRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "account_id 不能为空")}, nil
	}
	refuse := func(err error) *pb.DeleteCloudAccountRsp {
		if errors.Is(err, cloud.ErrAccountInUse) {
			return &pb.DeleteCloudAccountRsp{RetInfo: retErr(pb.ErrorCode_CONFLICT, err.Error())}
		}
		return &pb.DeleteCloudAccountRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}
	}
	if err := s.cloud.AccountInUse(ctx, accountID); err != nil {
		return refuse(err), nil
	}
	if err := s.cloud.Accounts.Delete(ctx, accountID); err != nil {
		return refuse(err), nil
	}
	return &pb.DeleteCloudAccountRsp{RetInfo: retOK()}, nil
}

// ListCloudRegions 返回腾讯云函数可用的地域。
func (s *Service) ListCloudRegions(context.Context, *pb.ListCloudRegionsReq) (*pb.ListCloudRegionsRsp, error) {
	regions := make([]*pb.CloudRegion, 0, len(tencent.SCFRegions()))
	for _, region := range tencent.SCFRegions() {
		regions = append(regions, &pb.CloudRegion{Code: region.Code, Name: region.Name, Tag: region.Tag, MaxInstances: 128, MaxNamespacesPerRegion: 32, MaxFunctionsPerNamespace: 1024})
	}
	return &pb.ListCloudRegionsRsp{RetInfo: retOK(), Regions: regions}, nil
}

func cloudAccountToPB(account store.CloudAccount) *pb.CloudAccount {
	return &pb.CloudAccount{
		AccountId: account.AccountID, AccountName: account.AccountName, Provider: account.Provider, CredentialSecretId: account.CredentialSecretID,
		AppId: account.AppID, CosRegion: account.COSRegion, CosBucket: account.COSBucket,
		CreateTime: formatCloudTime(account.CreateTime), ModifyTime: formatCloudTime(account.ModifyTime),
	}
}

func formatCloudTime(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}

// ----- 代码包 -----

// InitPackageUpload 登记一个待上传的代码包并返回 COS 预签名上传地址。
func (s *Service) InitPackageUpload(ctx context.Context, req *pb.InitPackageUploadReq) (*pb.InitPackageUploadRsp, error) {
	if s.cloud.Packages == nil {
		return &pb.InitPackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, errNoCloudPackages)}, nil
	}
	for name, value := range map[string]string{"space_id": req.GetSpaceId(), "package_name": req.GetPackageName(), "version": req.GetVersion(), "runtime": req.GetRuntime(), "cloud_account_id": req.GetCloudAccountId()} {
		if strings.TrimSpace(value) == "" {
			return &pb.InitPackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, name+" 不能为空")}, nil
		}
	}
	upload, err := s.cloud.Packages.InitUpload(ctx, cloud.UploadRequest{
		SpaceID: req.GetSpaceId(), PackageName: req.GetPackageName(), Version: req.GetVersion(), Runtime: req.GetRuntime(),
		CloudAccountID: req.GetCloudAccountId(), Filename: req.GetFilename(),
	})
	if err != nil {
		return &pb.InitPackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.InitPackageUploadRsp{RetInfo: retOK(), Package: functionPackageToPB(upload.Package), UploadUrl: upload.UploadURL, ExpiresAt: formatCloudTime(upload.ExpiresAt)}, nil
}

// CompletePackageUpload 核对 COS 里的对象（大小与 MD5）后把代码包标成可用。
func (s *Service) CompletePackageUpload(ctx context.Context, req *pb.CompletePackageUploadReq) (*pb.CompletePackageUploadRsp, error) {
	if s.cloud.Packages == nil {
		return &pb.CompletePackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, errNoCloudPackages)}, nil
	}
	if strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetPackageId()) == "" || req.GetFileSize() <= 0 || strings.TrimSpace(req.GetFileMd5()) == "" {
		return &pb.CompletePackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id、package_id、file_size、file_md5 都不能为空")}, nil
	}
	pkg, err := s.cloud.Packages.CompleteUpload(ctx, strings.TrimSpace(req.GetSpaceId()), strings.TrimSpace(req.GetPackageId()), req.GetFileSize(), req.GetFileMd5())
	if err != nil {
		return &pb.CompletePackageUploadRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.CompletePackageUploadRsp{RetInfo: retOK(), Package: functionPackageToPB(pkg)}, nil
}

// ListFunctionPackages 返回一个空间（space_id 为空时全部空间）的代码包。
func (s *Service) ListFunctionPackages(ctx context.Context, req *pb.ListFunctionPackagesReq) (*pb.ListFunctionPackagesRsp, error) {
	if s.cloud.Packages == nil {
		return &pb.ListFunctionPackagesRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, errNoCloudPackages)}, nil
	}
	packages, err := s.cloud.Packages.List(ctx, strings.TrimSpace(req.GetSpaceId()))
	if err != nil {
		return &pb.ListFunctionPackagesRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	rsp := &pb.ListFunctionPackagesRsp{RetInfo: retOK()}
	for _, pkg := range packages {
		rsp.Packages = append(rsp.Packages, functionPackageToPB(pkg))
	}
	return rsp, nil
}

// DeleteFunctionPackage 删除代码包：先删 COS 对象再删记录。
func (s *Service) DeleteFunctionPackage(ctx context.Context, req *pb.DeleteFunctionPackageReq) (*pb.DeleteFunctionPackageRsp, error) {
	if s.cloud.Packages == nil {
		return &pb.DeleteFunctionPackageRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, errNoCloudPackages)}, nil
	}
	if strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetPackageId()) == "" {
		return &pb.DeleteFunctionPackageRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id 与 package_id 不能为空")}, nil
	}
	if err := s.cloud.Packages.Delete(ctx, strings.TrimSpace(req.GetSpaceId()), strings.TrimSpace(req.GetPackageId())); err != nil {
		return &pb.DeleteFunctionPackageRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.DeleteFunctionPackageRsp{RetInfo: retOK()}, nil
}

func functionPackageToPB(pkg store.FunctionPackage) *pb.FunctionPackage {
	return &pb.FunctionPackage{
		SpaceId: pkg.SpaceID, PackageId: pkg.PackageID, PackageName: pkg.PackageName, Version: pkg.Version, Runtime: pkg.Runtime,
		FileSize: pkg.FileSize, FileMd5: pkg.FileMD5, CloudAccountId: pkg.CloudAccountID, CosRegion: pkg.COSRegion, CosBucket: pkg.COSBucket,
		CosPath: pkg.COSPath, Status: pkg.Status, CreateTime: formatCloudTime(pkg.CreateTime), ModifyTime: formatCloudTime(pkg.ModifyTime),
	}
}

// ----- 云函数 -----

// reconcilerFor 返回空间的对账器；没有时返回错误码与原因。
func (s *Service) reconcilerFor(spaceID string) (*fetchplan.PlanReconciler, pb.ErrorCode, string) {
	if strings.TrimSpace(spaceID) == "" {
		return nil, pb.ErrorCode_INVALID_PARAM, "space_id 不能为空"
	}
	if s.cloud.Reconcilers == nil {
		return nil, pb.ErrorCode_INNER_ERR, errNoReconcilers
	}
	reconciler, err := s.cloud.Reconcilers.Require(strings.TrimSpace(spaceID))
	if err != nil {
		return nil, pb.ErrorCode_NOT_FOUND, err.Error()
	}
	return reconciler, pb.ErrorCode_SUCCESS, ""
}

// ListCloudFunctions 读回空间计划里的函数与计划外的同前缀函数，返回期望与实际的对照。
func (s *Service) ListCloudFunctions(ctx context.Context, req *pb.ListCloudFunctionsReq) (*pb.ListCloudFunctionsRsp, error) {
	reconciler, code, msg := s.reconcilerFor(req.GetSpaceId())
	if reconciler == nil {
		return &pb.ListCloudFunctionsRsp{RetInfo: retErr(code, msg)}, nil
	}
	views, err := reconciler.ListCloudFunctions(ctx)
	if err != nil {
		return &pb.ListCloudFunctionsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	rsp := &pb.ListCloudFunctionsRsp{RetInfo: retOK()}
	for _, view := range views {
		rsp.Functions = append(rsp.Functions, cloudFunctionToPB(view))
	}
	return rsp, nil
}

func cloudFunctionToPB(view fetchplan.CloudFunctionView) *pb.CloudFunction {
	out := &pb.CloudFunction{
		Name: view.Name, Region: view.Region, Namespace: view.Namespace, Kind: view.Kind, Unplanned: view.Unplanned,
		ExpectedCron: view.ExpectedCron, ExpectedAssignmentHash: view.ExpectedAssignmentHash, ExpectedPackageId: view.ExpectedPackageID,
		Operation: view.Operation, TimerMatches: view.TimerMatches, PackageLatest: view.PackageLatest, EnvironmentMatches: view.EnvironmentMatches,
	}
	readback := view.Readback
	out.ReadAt, out.ReadError = formatCloudTime(readback.ReadAt), readback.Error
	if readback.Error == "" && readback.Exists {
		out.Exists, out.Status, out.PackageId = true, readback.Status, readback.PackageID
		out.AssignmentHash = readback.Environment["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"]
		if timer := readback.Timer; timer != nil {
			out.TimerCron, out.TimerEnabled, out.TimerAvailableStatus = timer.Cron, timer.Enabled, timer.AvailableStatus
		}
	}
	return out
}

// DeleteCloudFunctions 按名字或前缀删除空间的云函数（经操作表）。
func (s *Service) DeleteCloudFunctions(ctx context.Context, req *pb.DeleteCloudFunctionsReq) (*pb.DeleteCloudFunctionsRsp, error) {
	reconciler, code, msg := s.reconcilerFor(req.GetSpaceId())
	if reconciler == nil {
		return &pb.DeleteCloudFunctionsRsp{RetInfo: retErr(code, msg)}, nil
	}
	if len(req.GetNames()) == 0 && strings.TrimSpace(req.GetPrefix()) == "" {
		return &pb.DeleteCloudFunctionsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "names 与 prefix 至少给一个")}, nil
	}
	results, err := reconciler.DeleteCloudFunctions(ctx, req.GetNames(), req.GetPrefix())
	if err != nil {
		return &pb.DeleteCloudFunctionsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	rsp := &pb.DeleteCloudFunctionsRsp{RetInfo: retOK()}
	for _, result := range results {
		rsp.Results = append(rsp.Results, &pb.CloudFunctionDeletion{Name: result.Name, Region: result.Region, Done: result.Done, Error: result.Error})
	}
	return rsp, nil
}

// ProbeFunctionEgress 探测空间已生效计划里各采集函数此刻的公网出口 IP。
func (s *Service) ProbeFunctionEgress(ctx context.Context, req *pb.ProbeFunctionEgressReq) (*pb.ProbeFunctionEgressRsp, error) {
	reconciler, code, msg := s.reconcilerFor(req.GetSpaceId())
	if reconciler == nil {
		return &pb.ProbeFunctionEgressRsp{RetInfo: retErr(code, msg)}, nil
	}
	probes, err := reconciler.ProbeFunctionEgress(ctx, req.GetNames())
	if err != nil {
		return &pb.ProbeFunctionEgressRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	rsp := &pb.ProbeFunctionEgressRsp{RetInfo: retOK()}
	for _, probe := range probes {
		rsp.Probes = append(rsp.Probes, &pb.FunctionEgress{Name: probe.Name, Ip: probe.IP, Error: probe.Error})
	}
	return rsp, nil
}

// ----- 运行状态页的“云上状态” -----

// fillCloudReadbacks 给运行状态里的云函数填上内存里最近一次读回（不发起新的读回）。没有对账器或没有读回时不填。
// 代码包是否最新：与对账器最近一次解析出的最新可用包比较。
func (s *Service) fillCloudReadbacks(spaceID string, functions []*pb.CollectorFunctionStatus) {
	if s.cloud.Reconcilers == nil {
		return
	}
	reconciler := s.cloud.Reconcilers.Get(spaceID)
	if reconciler == nil {
		return
	}
	latest := strings.TrimSpace(reconciler.LatestPackageID())
	packageLatest := func(packageID string) bool { return latest != "" && packageID == latest }
	for _, function := range functions {
		if function.GetExecutor() != marketfetch.PlanExecutorSCF {
			continue
		}
		if readback, ok := reconciler.TimerReadback(function.GetName(), function.GetRegion()); ok {
			applyCloudReadback(function, readback, packageLatest)
		}
	}
}

// applyCloudReadback 把一次读回填进函数状态：读回出错或云上没有函数时只填读回时间；只读了代码包（没有定时器读回）时定时器算不一致。
func applyCloudReadback(function *pb.CollectorFunctionStatus, readback fetchplan.FunctionReadback, packageLatest func(string) bool) {
	function.CloudReadAt = formatCloudTime(readback.ReadAt)
	if readback.Error != "" || !readback.Exists {
		return
	}
	function.CloudExists = true
	function.CloudTimerMatches = !readback.CodeOnly && fetchplan.TimerMatches(readback.Timer, function.GetCron())
	function.CloudPackageLatest = packageLatest(readback.PackageID)
}
