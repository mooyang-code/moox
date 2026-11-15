package cloud

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/cloud/tencentscf"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	tcerrors "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/errors"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	// OperationTimeout 是一次 Ensure、UpgradeCode、SetTimer、Delete 的总时限（含三次重试与等待 Active）。
	// 建一个带 VPC 的函数要几分钟；这个上限只在操作卡死时起作用。调用方的上下文取消时操作随之结束。
	OperationTimeout = 16 * time.Minute
	// DisabledTimerCron 是停用定时器且没有写 cron 时用的占位 cron。
	DisabledTimerCron = "0 * * * * * *"

	timerTriggerName      = "moox-market-fetch-timer"
	timerTriggerMessage   = "market_fetch_timer_v1"
	timerTriggerQualifier = "$LATEST"

	// 采集函数的固定配置：事件函数、Go1 运行时、并发 1、开公网出口、不让腾讯云重试异步调用（重试由 Collector 负责）。
	functionRuntime        = "Go1"
	functionHandler        = "main"
	functionType           = "Event"
	functionDescription    = "MooX cloud function instance "
	functionMaxConcurrency = 1
	functionPublicNet      = "ENABLE"
	asyncRetryNum          = 0
	// asyncMsgTTL 是腾讯云要求的异步调用最短保留期（秒）。
	asyncMsgTTL = 60

	// waitStepTimeout 是单次等待函数回到 Active（或删除完成）的上限。
	waitStepTimeout = 5 * time.Minute
	// createAttemptTimeout 是单次建函数请求的上限。
	createAttemptTimeout = 4 * time.Minute
	// providerAttempts 是遇到瞬时错误时一次操作最多从头做几遍。
	providerAttempts = 3
)

// 测试里替换：等待函数状态的轮询间隔、重试退避的起点。
var (
	activePollInterval = time.Second
	retryBaseDelay     = 500 * time.Millisecond
)

// UseFastPollingForTest 把等待函数状态的轮询间隔与重试退避调到 1 毫秒，给用假腾讯云的测试用（在测试的 init 里调一次）。
func UseFastPollingForTest() {
	activePollInterval = time.Millisecond
	retryBaseDelay = time.Millisecond
}

var (
	// ErrQuota 表示腾讯云的配额超限（函数个数、命名空间个数、账号的预留并发总额），对账按失败原因 quota 上报。
	ErrQuota = errors.New("腾讯云函数个数或命名空间配额超限")
	// ErrPackageBehind 表示函数还在旧代码包上，不能启用定时器。
	ErrPackageBehind = errors.New("函数的代码包落后")
	// ErrFunctionNotFound 表示云上没有这个函数。
	ErrFunctionNotFound = errors.New("云上没有这个函数")
)

// FunctionRef 定位一个云函数。名字由采集计划决定，命名空间由 PlanDeploy.NamespaceFor 算出。
type FunctionRef struct {
	AccountID, Region, Namespace, Name string
}

func (r FunctionRef) scf() tencentscf.FunctionRef {
	return tencentscf.FunctionRef{Region: r.Region, Namespace: r.Namespace, FunctionName: r.Name}
}

// FunctionSpec 是一个函数应有的全部内容。
type FunctionSpec struct {
	Ref FunctionRef
	// Package 是函数应跑的代码包（可用的那一份），它的 ID 写进环境的 MOOX_CODE_PACKAGE_ID。
	Package Package
	// Environment 是期望的环境：基础项与受管项。CLS 上报项由 Ensure 补齐，代码包 ID 由 Ensure 写入。
	Environment map[string]string
	MemoryMB    int64
	TimeoutS    int64
	VPCID       string
	SubnetID    string
	// ReservedMem 是预留并发内存（MB），0 表示不设预留并发（补采函数用它硬性限制并发实例数）。
	ReservedMem int64
	// Timer 为 nil 表示没有定时器（补采函数）。
	Timer *Timer
	// Pausing 在设定时器之前那一刻求值：返回 true（空间正在暂停）时，期望启用的定时器改成停用。
	Pausing func() bool
}

// Timer 是期望的定时器。
type Timer struct {
	Cron    string
	Enabled bool
}

// TimerState 是定时器的读回：除了 cron 与开关，还带云上的可用状态、Qualifier、Message，
// 用来判断定时器是不是真的在触发、参数是不是期望的那一份。
type TimerState struct {
	Timer
	AvailableStatus string
	Qualifier       string
	Message         string
}

// FunctionState 是从云上读回的实际状态，不落库。
type FunctionState struct {
	Exists bool
	// Status 是 Active、Creating、Updating、CreateFailed、UpdateFailed 等。
	Status string
	// PackageID 取自环境变量 MOOX_CODE_PACKAGE_ID。
	PackageID string
	// Environment 已经去掉密钥项（见 publicEnvironment）。
	Environment map[string]string
	// Timer 只有 Describe 带；DescribeCode 不读定时器。云上没有定时器时为 nil。
	Timer *TimerState
	// AsyncRetry 是异步重试配置的读回，只有 Describe 带；云上没有显式配置时为 nil。
	AsyncRetry *AsyncRetryState
	// ReservedMem 是预留并发内存（MB）的读回，只有 Describe 带；没有设置时为 0。
	ReservedMem int64
	ReadAt      time.Time
}

// AsyncRetryState 是异步重试配置的读回。期望是不重试（RetryNum 0）、消息保留 60 秒。
type AsyncRetryState struct {
	RetryNum int64
	MsgTTL   int64
}

// InvokeResult 是一次调用的结果。
type InvokeResult struct {
	RequestID    string
	Code         int32
	Message      string
	ReturnResult string
	Duration     int64
	BillDuration int64
	MemoryUsage  int64
}

// Functions 是 Collector 对云函数的全部操作。
type Functions interface {
	// Describe 完整读回：GetFunction 加定时器（ListTriggers，限额 20 次/秒）。函数不存在时 Exists 为 false、不报错。
	Describe(ctx context.Context, ref FunctionRef) (FunctionState, error)
	// DescribeCode 只调 GetFunction，给 2 分钟一次的升级检查用。
	DescribeCode(ctx context.Context, ref FunctionRef) (FunctionState, error)
	// Ensure 幂等：云上没有就建，有就按名字接管；按写死的顺序逐项比对，只改不同的。
	Ensure(ctx context.Context, spec FunctionSpec) error
	// UpgradeCode 只换代码：环境取云上读回的值，只把 MOOX_CODE_PACKAGE_ID 换成新包。不动定时器。
	UpgradeCode(ctx context.Context, ref FunctionRef, pkg Package) error
	// SetTimer 只改定时器。启用时先确认函数是 Active 且代码包等于 wantPackageID，否则返回 ErrPackageBehind、不启用。
	SetTimer(ctx context.Context, ref FunctionRef, timer Timer, wantPackageID string) error
	// Delete 删定时器、删函数，并等它从云上消失。函数已经不在时不报错。
	Delete(ctx context.Context, ref FunctionRef) error
	// Invoke 调用函数：sync 为 true 时同步等结果，否则异步投递。
	Invoke(ctx context.Context, ref FunctionRef, event any, sync bool) (InvokeResult, error)
	// List 列出一个地域里名字以 prefix 开头的函数，覆盖这个地域的全部命名空间（含分片命名空间）。
	List(ctx context.Context, accountID, region, prefix string) ([]FunctionRef, error)
}

// SCFClient 是 Functions 用到的腾讯云接口（*tencentscf.Client 实现）。
type SCFClient interface {
	EnsureNamespace(context.Context, string, string) error
	GetFunction(context.Context, tencentscf.FunctionRef) (*tencentscf.FunctionInfo, error)
	CreateFunction(context.Context, tencentscf.CreateFunctionRequest) (*tencentscf.CreateFunctionResponse, error)
	DeleteFunction(context.Context, tencentscf.FunctionRef) error
	UpdateFunctionCode(context.Context, tencentscf.UpdateFunctionCodeRequest) (*tencentscf.UpdateFunctionCodeResponse, error)
	UpdateFunctionConfiguration(context.Context, tencentscf.UpdateFunctionConfigurationRequest) (*tencentscf.UpdateFunctionConfigurationResponse, error)
	EnsureTimerTrigger(context.Context, tencentscf.TimerTriggerRequest) (*tencentscf.TimerTriggerInfo, error)
	GetTimerTrigger(context.Context, tencentscf.FunctionRef, string) (*tencentscf.TimerTriggerInfo, error)
	DeleteTimerTrigger(context.Context, tencentscf.TimerTriggerRequest) error
	InvokeFunction(context.Context, tencentscf.InvokeFunctionRequest) (*tencentscf.InvokeFunctionResponse, error)
	PutReservedConcurrencyConfig(context.Context, tencentscf.PutReservedConcurrencyConfigRequest) error
	UpdateFunctionEventInvokeConfig(context.Context, tencentscf.UpdateFunctionEventInvokeConfigRequest) (*tencentscf.UpdateFunctionEventInvokeConfigResponse, error)
	GetFunctionEventInvokeConfig(context.Context, tencentscf.FunctionRef) (*tencentscf.EventInvokeConfig, error)
	GetReservedConcurrencyConfig(context.Context, tencentscf.FunctionRef) (int64, error)
	ListFunctionInventory(context.Context, string) ([]tencentscf.DiscoveryFunction, error)
}

// CLSEnvironment 把 CLS 上报环境补进函数环境，返回是否有改动；实现保证失败时不阻断调用方（*CLSProvisioner 实现）。
type CLSEnvironment interface {
	Apply(ctx context.Context, accountID string, credential Credential, environment map[string]string) bool
}

// TencentFunctions 是 Functions 的腾讯云实现。
type TencentFunctions struct {
	accounts    *store.CloudAccountRepository
	credentials CredentialResolver
	cls         CLSEnvironment
	newClient   func(Credential) SCFClient
	now         func() time.Time
}

// NewFunctions 构造腾讯云函数操作。cls 为 nil 时不补 CLS 上报环境（这时定时采集函数的环境校验会因缺 CLS 项而拒绝）。
func NewFunctions(db *store.Store, credentials CredentialResolver, cls CLSEnvironment) *TencentFunctions {
	return &TencentFunctions{
		accounts: db.CloudAccounts(), credentials: credentials, cls: cls, now: time.Now,
		newClient: func(credential Credential) SCFClient {
			return tencentscf.New(credential.AccountID, credential.SecretID, credential.SecretKey)
		},
	}
}

// NewFunctionsWithClient 与 NewFunctions 相同，只是腾讯云客户端由 newClient 构造（测试里换成假腾讯云）。
func NewFunctionsWithClient(db *store.Store, credentials CredentialResolver, cls CLSEnvironment, newClient func(Credential) SCFClient) *TencentFunctions {
	functions := NewFunctions(db, credentials, cls)
	functions.newClient = newClient
	return functions
}

var _ Functions = (*TencentFunctions)(nil)

// session 是一次操作用的云账号、密钥与腾讯云客户端。
type session struct {
	account    store.CloudAccount
	credential Credential
	client     SCFClient
}

func (f *TencentFunctions) open(ctx context.Context, accountID string) (session, error) {
	account, err := f.accounts.Get(ctx, accountID)
	if err != nil {
		return session{}, err
	}
	if account == nil {
		return session{}, fmt.Errorf("云账号 %s 不存在", accountID)
	}
	if account.Provider != "tencent" {
		return session{}, fmt.Errorf("云账号 %s 不是腾讯云账号", accountID)
	}
	if f.credentials == nil {
		return session{}, fmt.Errorf("没有配置读取云账号密钥的方式")
	}
	credential, err := f.credentials.Resolve(ctx, *account)
	if err != nil {
		return session{}, err
	}
	return session{account: *account, credential: credential, client: f.newClient(credential)}, nil
}

// Describe 实现 Functions。
func (f *TencentFunctions) Describe(ctx context.Context, ref FunctionRef) (FunctionState, error) {
	return f.describe(ctx, ref, true)
}

// DescribeCode 实现 Functions。
func (f *TencentFunctions) DescribeCode(ctx context.Context, ref FunctionRef) (FunctionState, error) {
	return f.describe(ctx, ref, false)
}

func (f *TencentFunctions) describe(ctx context.Context, ref FunctionRef, withTimer bool) (FunctionState, error) {
	s, err := f.open(ctx, ref.AccountID)
	if err != nil {
		return FunctionState{}, err
	}
	info, err := s.client.GetFunction(ctx, ref.scf())
	if isFunctionNotFound(err) {
		return FunctionState{ReadAt: f.now()}, nil
	}
	if err != nil {
		return FunctionState{}, fmt.Errorf("读取云函数 %s 失败: %w", ref.Name, err)
	}
	state := FunctionState{Exists: true, Status: info.Status, PackageID: info.Environment[packageIDKey], Environment: publicEnvironment(info.Environment)}
	// CreateFailed 时腾讯云拒绝触发器操作，函数也不可能有定时器：只读函数本身，下一次 Ensure 会删掉重建。
	if withTimer && !strings.EqualFold(info.Status, "CreateFailed") {
		trigger, err := readTimer(ctx, s.client, ref.scf())
		if err != nil {
			return FunctionState{}, fmt.Errorf("读取云函数 %s 的定时器失败: %w", ref.Name, err)
		}
		if trigger != nil {
			state.Timer = &TimerState{Timer: Timer{Cron: trigger.Cron, Enabled: trigger.Enabled}, AvailableStatus: trigger.AvailableStatus, Qualifier: trigger.Qualifier, Message: trigger.Message}
		}
		// 异步重试与预留并发只用于展示（对账不比对这两项，Ensure 自己会读、会改）：读失败只记日志，不让函数算作没读到。
		async, err := s.client.GetFunctionEventInvokeConfig(ctx, ref.scf())
		if err != nil && !isConfigNotFound(err) {
			log.WarnContextf(ctx, "[Collector-Cloud] 读取云函数 %s 的异步重试配置失败: %v", ref.Name, err)
		}
		if err == nil && async != nil && async.Configured {
			state.AsyncRetry = &AsyncRetryState{RetryNum: async.RetryNum, MsgTTL: async.MsgTTL}
		}
		reserved, err := s.client.GetReservedConcurrencyConfig(ctx, ref.scf())
		if err != nil && !isConfigNotFound(err) {
			log.WarnContextf(ctx, "[Collector-Cloud] 读取云函数 %s 的预留并发失败: %v", ref.Name, err)
		}
		if err == nil {
			state.ReservedMem = reserved
		}
	}
	state.ReadAt = f.now()
	return state, nil
}

// Ensure 实现 Functions。内部顺序写死，不能调换：
//
//  1. 读函数。不存在：建命名空间、建函数（环境里就带期望的环境与包 ID）；CreateFailed：先删、等删完，再按不存在重建；
//     UpdateFailed：不删，往下重做更新。
//  2. 等到 Active。
//  3. 代码包与期望不同：换代码（不动环境），等到 Active。
//  4. 环境或配置（内存、超时、VPC、公网、并发）与期望不同：改，MOOX_CODE_PACKAGE_ID 跟这一步一起写（它只能在第 3 步成功之后写），
//     等到 Active；再读异步重试与预留并发，不同才改。
//  5. 读回校验代码包、环境、配置都对。
//  6. 最后才设定时器（cron、开关），设之前看一眼空间是否正在暂停。
//
// 环境比对的口径：期望的每一项在云上都有且值相等就算一致，云上多出来的项不算不一致；写入时以云上环境为底合并期望值，
// 并删掉创建预留号与废弃的 DNS 键。任何写请求之前先校验合并后的完整环境，不合规就一条都不发。
func (f *TencentFunctions) Ensure(ctx context.Context, spec FunctionSpec) error {
	if err := validateSpec(spec); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	s, err := f.open(ctx, spec.Ref.AccountID)
	if err != nil {
		return err
	}
	defer lockFunction(spec.Ref)()
	return withRetry(ctx, spec.Ref.Name, func() error { return f.ensureOnce(ctx, s, spec) })
}

func (f *TencentFunctions) ensureOnce(ctx context.Context, s session, spec FunctionSpec) error {
	ref, name := spec.Ref.scf(), spec.Ref.Name
	desired := make(map[string]string, len(spec.Environment)+1)
	for key, value := range spec.Environment {
		desired[key] = value
	}
	desired[packageIDKey] = spec.Package.PackageID

	// 第 1 步：读函数，并在任何写请求之前算出、校验要写的完整环境。
	info, err := s.client.GetFunction(ctx, ref)
	exists := err == nil
	if err != nil && !isFunctionNotFound(err) {
		return fmt.Errorf("读取云函数 %s 失败: %w", name, err)
	}
	status := ""
	var base map[string]string
	if exists {
		status = info.Status
		if !strings.EqualFold(status, "CreateFailed") {
			base = info.Environment
		}
	}
	environment := mergeEnvironment(base, desired)
	f.applyCLS(ctx, s, name, environment)
	if err := validateEnvironment(environment); err != nil {
		return fmt.Errorf("云函数 %s 的环境不合规: %w", name, err)
	}
	if strings.EqualFold(status, "CreateFailed") {
		log.WarnContextf(ctx, "[Collector-Cloud] 云函数 %s 建失败了，先删掉再重建", name)
		if err := s.client.DeleteFunction(ctx, ref); err != nil && !isFunctionNotFound(err) {
			return fmt.Errorf("删除建失败的云函数 %s 失败: %w", name, err)
		}
		if err := waitDeleted(ctx, s.client, ref); err != nil {
			return err
		}
		exists, info = false, nil
	}
	if !exists {
		if err := f.create(ctx, s, spec, environment); err != nil {
			return err
		}
		info = nil
	}

	// 第 2 步：等到 Active。UpdateFailed 不等（它不会自己回到 Active），直接往下重做更新。
	repairUpdate := exists && strings.EqualFold(status, "UpdateFailed")
	if !repairUpdate {
		if info, err = waitActive(ctx, s.client, ref, info); err != nil {
			return err
		}
	}

	// 第 3 步：换代码，不动环境。
	codeReplaced := false
	if strings.TrimSpace(info.Environment[packageIDKey]) != spec.Package.PackageID {
		if _, err := s.client.UpdateFunctionCode(ctx, codeRequest(ref, spec.Package)); err != nil {
			return fmt.Errorf("云函数 %s 换代码失败: %w", name, err)
		}
		codeReplaced = true
		if info, err = waitActive(ctx, s.client, ref, nil); err != nil {
			return err
		}
	}

	// 第 4 步：环境与配置。换过代码时必须写一次（包 ID 跟这一步一起写），UpdateFailed 也必须重做一次。
	// 环境单独变化时只带环境与超时；配置（内存、并发、公网、VPC）不一致时才一起带上。
	if fullConfig := configDiffers(info, spec); repairUpdate || codeReplaced || fullConfig || !environmentMatches(info.Environment, environment) {
		request := tencentscf.UpdateFunctionConfigurationRequest{
			FunctionRef: ref, Environment: environment, Timeout: spec.TimeoutS,
			// 只在紧跟换代码的这次清掉腾讯云自带的日志投递（Collector 用自己的 CLS 写入）。
			ClearNativeCLS: codeReplaced,
		}
		if fullConfig {
			request.MemorySize, request.MaxInstanceConcurrency, request.PublicNetStatus = spec.MemoryMB, functionMaxConcurrency, functionPublicNet
			request.VpcID, request.SubnetID = spec.VPCID, spec.SubnetID
		}
		if _, err := s.client.UpdateFunctionConfiguration(ctx, request); err != nil {
			return fmt.Errorf("云函数 %s 改环境与配置失败: %w", name, err)
		}
		if _, err := waitActive(ctx, s.client, ref, nil); err != nil {
			return err
		}
	}
	if err := ensureExtras(ctx, s.client, ref, spec.ReservedMem); err != nil {
		return err
	}

	// 第 5 步：读回校验。写异步重试、预留并发之后函数可能短暂进入 Updating，先等回到 Active。
	verified, err := waitActive(ctx, s.client, ref, nil)
	if err != nil {
		return err
	}
	if err := verifyFunction(verified, spec, environment); err != nil {
		return err
	}

	// 第 6 步：最后才设定时器。
	if spec.Timer == nil {
		return nil
	}
	timer := normalizeTimer(*spec.Timer)
	if timer.Enabled && spec.Pausing != nil && spec.Pausing() {
		log.InfoContextf(ctx, "[Collector-Cloud] 空间正在暂停，云函数 %s 的定时器设成停用", name)
		timer.Enabled = false
	}
	return applyTimer(ctx, s.client, ref, timer)
}

// create 建命名空间与函数。建函数的结果含糊时（返回错误但云上可能已经建出来）按名字读一次，读到了就接着往下比对。
func (f *TencentFunctions) create(ctx context.Context, s session, spec FunctionSpec, environment map[string]string) error {
	ref, name := spec.Ref.scf(), spec.Ref.Name
	if err := s.client.EnsureNamespace(ctx, ref.Region, ref.Namespace); err != nil {
		return quotaOr(fmt.Errorf("准备命名空间 %s（%s）失败: %w", ref.Namespace, ref.Region, err), err)
	}
	createCtx, cancel := context.WithTimeout(ctx, createAttemptTimeout)
	_, err := s.client.CreateFunction(createCtx, tencentscf.CreateFunctionRequest{
		FunctionRef: ref, Runtime: functionRuntime, Handler: functionHandler, Description: functionDescription + name,
		MemorySize: spec.MemoryMB, Timeout: spec.TimeoutS, MaxInstanceConcurrency: functionMaxConcurrency, Environment: environment,
		COSBucket: spec.Package.COSBucket, COSRegion: spec.Package.COSRegion, COSObject: strings.TrimPrefix(spec.Package.COSPath, "/"),
		Type: functionType, PublicNetStatus: functionPublicNet, VpcID: spec.VPCID, SubnetID: spec.SubnetID,
	})
	cancel()
	if err == nil {
		return nil
	}
	createErr := quotaOr(fmt.Errorf("建云函数 %s 失败: %w", name, err), err)
	if ctx.Err() != nil || errors.Is(createErr, ErrQuota) {
		return createErr
	}
	if _, readErr := s.client.GetFunction(ctx, ref); readErr != nil {
		return createErr
	}
	log.WarnContextf(ctx, "[Collector-Cloud] 建云函数 %s 返回错误但云上已经有它，接着按读回比对: %v", name, err)
	return nil
}

// UpgradeCode 实现 Functions。
func (f *TencentFunctions) UpgradeCode(ctx context.Context, ref FunctionRef, pkg Package) error {
	if err := validateRef(ref); err != nil {
		return err
	}
	if err := validatePackage(pkg); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	s, err := f.open(ctx, ref.AccountID)
	if err != nil {
		return err
	}
	defer lockFunction(ref)()
	return withRetry(ctx, ref.Name, func() error {
		scfRef := ref.scf()
		info, err := s.client.GetFunction(ctx, scfRef)
		if isFunctionNotFound(err) {
			return fmt.Errorf("%w：%s", ErrFunctionNotFound, ref.Name)
		}
		if err != nil {
			return fmt.Errorf("读取云函数 %s 失败: %w", ref.Name, err)
		}
		if info, err = waitActive(ctx, s.client, scfRef, info); err != nil {
			return err
		}
		if strings.TrimSpace(info.Environment[packageIDKey]) == pkg.PackageID {
			// 已经是新包：顺带修正异步重试（读回一致就不写），保留每次升级都自愈的老行为。
			return ensureExtras(ctx, s.client, scfRef, 0)
		}
		// 环境沿用云上读回的值（升级不会把正在过渡的环境改回去），只换包 ID；换代码之前先校验，环境不合规就一条都不发。
		environment := mergeEnvironment(info.Environment, map[string]string{packageIDKey: pkg.PackageID})
		if err := validateEnvironment(environment); err != nil {
			return fmt.Errorf("云函数 %s 的环境不合规: %w", ref.Name, err)
		}
		if _, err := s.client.UpdateFunctionCode(ctx, codeRequest(scfRef, pkg)); err != nil {
			return fmt.Errorf("云函数 %s 换代码失败: %w", ref.Name, err)
		}
		if _, err := waitActive(ctx, s.client, scfRef, nil); err != nil {
			return err
		}
		if _, err := s.client.UpdateFunctionConfiguration(ctx, tencentscf.UpdateFunctionConfigurationRequest{FunctionRef: scfRef, Environment: environment, ClearNativeCLS: true}); err != nil {
			return fmt.Errorf("云函数 %s 写入新代码包 ID 失败: %w", ref.Name, err)
		}
		if _, err := waitActive(ctx, s.client, scfRef, nil); err != nil {
			return err
		}
		if err := ensureExtras(ctx, s.client, scfRef, 0); err != nil {
			return err
		}
		verified, err := waitActive(ctx, s.client, scfRef, nil)
		if err != nil {
			return err
		}
		if !environmentCovers(verified.Environment, environment) {
			return fmt.Errorf("云函数 %s 升级后读回的环境与写入的不一致", ref.Name)
		}
		return nil
	})
}

// SetTimer 实现 Functions。
func (f *TencentFunctions) SetTimer(ctx context.Context, ref FunctionRef, timer Timer, wantPackageID string) error {
	if err := validateRef(ref); err != nil {
		return err
	}
	timer = normalizeTimer(timer)
	if !isSupportedTimerCron(timer.Cron) {
		return fmt.Errorf("定时器的 cron %q 不是采集计划支持的周期", timer.Cron)
	}
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	s, err := f.open(ctx, ref.AccountID)
	if err != nil {
		return err
	}
	defer lockFunction(ref)()
	return withRetry(ctx, ref.Name, func() error {
		scfRef := ref.scf()
		info, err := s.client.GetFunction(ctx, scfRef)
		if isFunctionNotFound(err) {
			return fmt.Errorf("%w：%s", ErrFunctionNotFound, ref.Name)
		}
		if err != nil {
			return fmt.Errorf("读取云函数 %s 失败: %w", ref.Name, err)
		}
		// 停用不看函数状态、不等 Active：函数在 Updating、UpdateFailed 时定时器也必须能停（暂停靠它，真机实测都接受）。
		// CreateFailed 时腾讯云拒绝一切触发器操作，而建定时器要求函数正常，所以这时不可能有定时器，直接算停用成功；
		// 其他状态下云上没有定时器也直接成功，不去建一个关着的定时器（没有定时器就不会触发）。
		if !timer.Enabled {
			if strings.EqualFold(info.Status, "CreateFailed") {
				return nil
			}
			current, err := readTimer(ctx, s.client, scfRef)
			if err != nil {
				return fmt.Errorf("读取云函数 %s 的定时器失败: %w", ref.Name, err)
			}
			if current == nil {
				return nil
			}
		}
		if timer.Enabled {
			if info, err = waitActive(ctx, s.client, scfRef, info); err != nil {
				return err
			}
			current := strings.TrimSpace(info.Environment[packageIDKey])
			if strings.TrimSpace(wantPackageID) == "" || current != strings.TrimSpace(wantPackageID) {
				return fmt.Errorf("%w：云函数 %s 的代码包是 %q，期望 %q，先升级再启用定时器", ErrPackageBehind, ref.Name, current, wantPackageID)
			}
		}
		return applyTimer(ctx, s.client, scfRef, timer)
	})
}

// Delete 实现 Functions。
func (f *TencentFunctions) Delete(ctx context.Context, ref FunctionRef) error {
	if err := validateRef(ref); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	s, err := f.open(ctx, ref.AccountID)
	if err != nil {
		return err
	}
	defer lockFunction(ref)()
	return withRetry(ctx, ref.Name, func() error {
		scfRef := ref.scf()
		info, err := s.client.GetFunction(ctx, scfRef)
		if isFunctionNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("读取云函数 %s 失败: %w", ref.Name, err)
		}
		// CreateFailed 时腾讯云拒绝触发器操作，函数也不可能有定时器：直接删函数。
		if !strings.EqualFold(info.Status, "CreateFailed") {
			if err := s.client.DeleteTimerTrigger(ctx, tencentscf.TimerTriggerRequest{FunctionRef: scfRef, Name: timerTriggerName, Qualifier: timerTriggerQualifier}); err != nil && !isTriggerNotFound(err) && !isFunctionNotFound(err) {
				return fmt.Errorf("删除云函数 %s 的定时器失败: %w", ref.Name, err)
			}
		}
		if err := s.client.DeleteFunction(ctx, scfRef); err != nil && !isFunctionNotFound(err) {
			return fmt.Errorf("删除云函数 %s 失败: %w", ref.Name, err)
		}
		return waitDeleted(ctx, s.client, scfRef)
	})
}

// Invoke 实现 Functions。
func (f *TencentFunctions) Invoke(ctx context.Context, ref FunctionRef, event any, sync bool) (InvokeResult, error) {
	s, err := f.open(ctx, ref.AccountID)
	if err != nil {
		return InvokeResult{}, err
	}
	invokeType := "Event"
	if sync {
		invokeType = "RequestResponse"
	}
	rsp, err := s.client.InvokeFunction(ctx, tencentscf.InvokeFunctionRequest{Region: ref.Region, FunctionName: ref.Name, Namespace: ref.Namespace, InvokeType: invokeType, EventData: event})
	if err != nil {
		return InvokeResult{}, err
	}
	return InvokeResult{RequestID: rsp.RequestID, Code: rsp.Code, Message: rsp.Message, ReturnResult: rsp.ReturnResult, Duration: rsp.Duration, BillDuration: rsp.BillDuration, MemoryUsage: rsp.MemoryUsage}, nil
}

// List 实现 Functions：扫这个地域的全部命名空间，按名字前缀过滤，结果按命名空间、函数名排序。
func (f *TencentFunctions) List(ctx context.Context, accountID, region, prefix string) ([]FunctionRef, error) {
	s, err := f.open(ctx, accountID)
	if err != nil {
		return nil, err
	}
	inventory, err := s.client.ListFunctionInventory(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("列出 %s 的云函数失败: %w", region, err)
	}
	var refs []FunctionRef
	for _, item := range inventory {
		if strings.HasPrefix(item.FunctionName, prefix) {
			refs = append(refs, FunctionRef{AccountID: accountID, Region: item.Region, Namespace: item.Namespace, Name: item.FunctionName})
		}
	}
	return refs, nil
}

// applyCLS 在环境里补齐 CLS 上报项。尽力而为：开通失败、补齐后超长都只记日志，环境保持原样。
func (f *TencentFunctions) applyCLS(ctx context.Context, s session, name string, environment map[string]string) {
	if f.cls == nil {
		return
	}
	candidate := make(map[string]string, len(environment))
	for key, value := range environment {
		candidate[key] = value
	}
	if !f.cls.Apply(ctx, s.account.AccountID, s.credential, candidate) {
		return
	}
	if err := tencent.ValidateSCFEnvironment(candidate); err != nil {
		log.WarnContextf(ctx, "[Collector-Cloud] 云函数 %s 补上 CLS 环境后超长，不补: %v", name, err)
		return
	}
	for key, value := range candidate {
		environment[key] = value
	}
}

func validateRef(ref FunctionRef) error {
	if strings.TrimSpace(ref.AccountID) == "" || strings.TrimSpace(ref.Region) == "" || strings.TrimSpace(ref.Namespace) == "" || strings.TrimSpace(ref.Name) == "" {
		return fmt.Errorf("云函数的账号、地域、命名空间、函数名都必须写明：%+v", ref)
	}
	return nil
}

func validatePackage(pkg Package) error {
	if strings.TrimSpace(pkg.PackageID) == "" {
		return fmt.Errorf("没有指定代码包")
	}
	if pkg.Status != store.FunctionPackageAvailable {
		return fmt.Errorf("代码包 %s 还不可用（状态 %s）", pkg.PackageID, pkg.Status)
	}
	if pkg.COSBucket == "" || pkg.COSRegion == "" || strings.TrimPrefix(pkg.COSPath, "/") == "" {
		return fmt.Errorf("代码包 %s 没有 COS 位置", pkg.PackageID)
	}
	return nil
}

// validateSpec 在碰云端之前校验期望内容：两种函数（带定时器的采集函数、不带定时器的补采函数）的固定约束。
func validateSpec(spec FunctionSpec) error {
	if err := validateRef(spec.Ref); err != nil {
		return err
	}
	if err := validatePackage(spec.Package); err != nil {
		return err
	}
	if spec.TimeoutS != tencent.CollectorTimerTimeoutSeconds || spec.Environment[timeoutKey] != fmt.Sprint(tencent.CollectorTimerTimeoutSeconds) {
		return fmt.Errorf("云函数 %s 的超时与环境里的 %s 都必须是 %d 秒", spec.Ref.Name, timeoutKey, tencent.CollectorTimerTimeoutSeconds)
	}
	if spec.MemoryMB <= 0 {
		return fmt.Errorf("云函数 %s 没有指定内存", spec.Ref.Name)
	}
	if strings.TrimSpace(spec.VPCID) == "" || strings.TrimSpace(spec.SubnetID) == "" {
		return fmt.Errorf("云函数 %s 必须指定 VPC 与子网", spec.Ref.Name)
	}
	if spec.ReservedMem < 0 {
		return fmt.Errorf("云函数 %s 的预留并发内存不能为负", spec.Ref.Name)
	}
	if spec.Timer == nil {
		if strings.TrimSpace(spec.Environment[modeKey]) != "backfill" {
			return fmt.Errorf("云函数 %s 没有定时器，只能是补采函数（%s=backfill）", spec.Ref.Name, modeKey)
		}
		return nil
	}
	if strings.TrimSpace(spec.Environment[planKey]) == "" {
		return fmt.Errorf("带定时器的云函数 %s 必须带采集计划 %s", spec.Ref.Name, planKey)
	}
	if timer := normalizeTimer(*spec.Timer); !isSupportedTimerCron(timer.Cron) {
		return fmt.Errorf("定时器的 cron %q 不是采集计划支持的周期", timer.Cron)
	}
	return nil
}

func normalizeTimer(timer Timer) Timer {
	timer.Cron = strings.TrimSpace(timer.Cron)
	if timer.Cron == "" && !timer.Enabled {
		timer.Cron = DisabledTimerCron
	}
	return timer
}

func codeRequest(ref tencentscf.FunctionRef, pkg Package) tencentscf.UpdateFunctionCodeRequest {
	return tencentscf.UpdateFunctionCodeRequest{FunctionRef: ref, Handler: functionHandler, COSBucket: pkg.COSBucket, COSRegion: pkg.COSRegion, COSObject: strings.TrimPrefix(pkg.COSPath, "/")}
}

func configDiffers(info *tencentscf.FunctionInfo, spec FunctionSpec) bool {
	return info.MemorySize != spec.MemoryMB || info.Timeout != spec.TimeoutS || info.MaxInstanceConcurrency != functionMaxConcurrency ||
		!strings.EqualFold(info.PublicNetStatus, functionPublicNet) || info.VpcID != spec.VPCID || info.SubnetID != spec.SubnetID
}

// verifyFunction 读回校验：代码包、环境（期望的每一项）、配置都与写入的一致。
func verifyFunction(info *tencentscf.FunctionInfo, spec FunctionSpec, environment map[string]string) error {
	name := spec.Ref.Name
	if info == nil {
		return fmt.Errorf("读回的云函数 %s 为空", name)
	}
	if !strings.EqualFold(info.Status, "Active") {
		return fmt.Errorf("云函数 %s 读回的状态是 %q，不是 Active", name, info.Status)
	}
	if got := info.Environment[packageIDKey]; got != spec.Package.PackageID {
		return fmt.Errorf("云函数 %s 读回的代码包是 %q，期望 %q", name, got, spec.Package.PackageID)
	}
	if !environmentCovers(info.Environment, environment) {
		return fmt.Errorf("云函数 %s 读回的环境与写入的不一致", name)
	}
	if configDiffers(info, spec) {
		return fmt.Errorf("云函数 %s 读回的配置与期望不一致：内存 %d、超时 %d、并发 %d、公网 %q、VPC %q、子网 %q", name,
			info.MemorySize, info.Timeout, info.MaxInstanceConcurrency, info.PublicNetStatus, info.VpcID, info.SubnetID)
	}
	return nil
}

// ensureExtras 比对并设置异步重试（腾讯云不重试，重试由 Collector 负责）与预留并发（reservedMem 为 0 时不管）：读回一致就不写。
func ensureExtras(ctx context.Context, client SCFClient, ref tencentscf.FunctionRef, reservedMem int64) error {
	// 没配置过时腾讯云可能直接回“不存在”：当作没有配置，照常去写。
	async, err := client.GetFunctionEventInvokeConfig(ctx, ref)
	if err != nil && !isConfigNotFound(err) {
		return fmt.Errorf("读取云函数 %s 的异步重试配置失败: %w", ref.FunctionName, err)
	}
	if async == nil || !async.Configured || async.RetryNum != asyncRetryNum || async.MsgTTL != asyncMsgTTL {
		if _, err := client.UpdateFunctionEventInvokeConfig(ctx, tencentscf.UpdateFunctionEventInvokeConfigRequest{FunctionRef: ref, RetryNum: asyncRetryNum, MsgTTL: asyncMsgTTL}); err != nil {
			return fmt.Errorf("设置云函数 %s 的异步重试失败: %w", ref.FunctionName, err)
		}
	}
	if reservedMem <= 0 {
		return nil
	}
	current, err := client.GetReservedConcurrencyConfig(ctx, ref)
	if err != nil && !isConfigNotFound(err) {
		return fmt.Errorf("读取云函数 %s 的预留并发失败: %w", ref.FunctionName, err)
	}
	if current == reservedMem {
		return nil
	}
	if err := client.PutReservedConcurrencyConfig(ctx, tencentscf.PutReservedConcurrencyConfigRequest{FunctionRef: ref, ReservedConcurrencyMem: reservedMem}); err != nil {
		return quotaOr(fmt.Errorf("设置云函数 %s 的预留并发失败: %w", ref.FunctionName, err), err)
	}
	return nil
}

// readTimer 读定时器；云上没有时返回 nil。
func readTimer(ctx context.Context, client SCFClient, ref tencentscf.FunctionRef) (*tencentscf.TimerTriggerInfo, error) {
	trigger, err := client.GetTimerTrigger(ctx, ref, timerTriggerName)
	if isTriggerNotFound(err) {
		return nil, nil
	}
	return trigger, err
}

// applyTimer 把定时器设成期望的样子：读回已经一致（且可用）就不写。
func applyTimer(ctx context.Context, client SCFClient, ref tencentscf.FunctionRef, timer Timer) error {
	current, err := readTimer(ctx, client, ref)
	if err != nil {
		return fmt.Errorf("读取云函数 %s 的定时器失败: %w", ref.FunctionName, err)
	}
	if current != nil && current.Cron == timer.Cron && current.Enabled == timer.Enabled && current.Qualifier == timerTriggerQualifier &&
		current.Message == timerTriggerMessage && strings.EqualFold(current.AvailableStatus, "Available") {
		return nil
	}
	if _, err := client.EnsureTimerTrigger(ctx, tencentscf.TimerTriggerRequest{FunctionRef: ref, Name: timerTriggerName, Cron: timer.Cron, Enabled: timer.Enabled, Qualifier: timerTriggerQualifier, Message: timerTriggerMessage}); err != nil {
		return fmt.Errorf("设置云函数 %s 的定时器失败: %w", ref.FunctionName, err)
	}
	return nil
}

// waitActive 等函数回到 Active。current 不为空时先看它；状态带 failed 时直接报错。
func waitActive(ctx context.Context, client SCFClient, ref tencentscf.FunctionRef, current *tencentscf.FunctionInfo) (*tencentscf.FunctionInfo, error) {
	waitCtx, cancel := context.WithTimeout(ctx, waitStepTimeout)
	defer cancel()
	for {
		if current == nil {
			info, err := client.GetFunction(waitCtx, ref)
			if err != nil && !isFunctionNotFound(err) && !isTransientError(err) {
				return nil, fmt.Errorf("读取云函数 %s 的状态失败: %w", ref.FunctionName, err)
			}
			current = info
		}
		if current != nil {
			status := strings.ToLower(strings.TrimSpace(current.Status))
			if status == "active" {
				return current, nil
			}
			if status == "" || strings.Contains(status, "failed") {
				return nil, fmt.Errorf("云函数 %s 进入了状态 %q", ref.FunctionName, current.Status)
			}
		}
		if err := sleepContext(waitCtx, activePollInterval); err != nil {
			return nil, fmt.Errorf("等云函数 %s 回到 Active 超时或被取消: %w", ref.FunctionName, err)
		}
		current = nil
	}
}

// waitDeleted 等函数从云上消失。
func waitDeleted(ctx context.Context, client SCFClient, ref tencentscf.FunctionRef) error {
	waitCtx, cancel := context.WithTimeout(ctx, waitStepTimeout)
	defer cancel()
	for {
		_, err := client.GetFunction(waitCtx, ref)
		if isFunctionNotFound(err) {
			return nil
		}
		if err != nil && !isTransientError(err) {
			return fmt.Errorf("删除后读取云函数 %s 失败: %w", ref.FunctionName, err)
		}
		if err := sleepContext(waitCtx, activePollInterval); err != nil {
			return fmt.Errorf("等云函数 %s 删除完成超时或被取消: %w", ref.FunctionName, err)
		}
	}
}

// withRetry 遇到瞬时错误时把整个操作从头再做一遍，最多 3 遍；调用方的上下文结束或配额超限时不重试。
func withRetry(ctx context.Context, name string, operation func() error) error {
	for attempt := 1; ; attempt++ {
		err := operation()
		if err == nil || ctx.Err() != nil || errors.Is(err, ErrQuota) || attempt >= providerAttempts || !isTransientError(err) {
			return err
		}
		delay := retryBaseDelay << (attempt - 1)
		log.WarnContextf(ctx, "[Collector-Cloud] 云函数 %s 的操作遇到瞬时错误，%s 后重试 attempt=%d/%d: %v", name, delay, attempt, providerAttempts, err)
		if sleepContext(ctx, delay) != nil {
			return err
		}
	}
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// quotaOr 在 cause 是配额超限时把 err 标成 ErrQuota。
func quotaOr(err, cause error) error {
	if isQuotaError(cause) {
		return fmt.Errorf("%w：%w", ErrQuota, err)
	}
	return err
}

// quotaErrorCodes 是腾讯云的配额超限错误码：函数个数（按文档，没有实测）、命名空间个数（实测）、账号的预留并发总额（实测，
// 账号上限 128000 MB）。LimitExceeded.Memory（实测）、.Timeout、.MsgTTL 这类是参数越界，限频是 RequestLimitExceeded，都不算配额。
var quotaErrorCodes = []string{"LimitExceeded.Function", "LimitExceeded.Namespace", "LimitExceeded.FunctionReservedConcurrencyMemory"}

// isQuotaError 判断腾讯云的错误是不是配额超限（见 quotaErrorCodes）。
func isQuotaError(err error) bool {
	var sdkErr *tcerrors.TencentCloudSDKError
	return errors.As(err, &sdkErr) && slices.Contains(quotaErrorCodes, sdkErr.GetCode())
}

// 资源不存在的错误码。只认这几个：例如密钥失效时的 AuthFailure.SecretIdNotFound 不能当成函数不存在。
var (
	functionNotFoundCodes = []string{"ResourceNotFound.Function", "ResourceNotFound.FunctionName", "ResourceNotFound.Namespace"}
	triggerNotFoundCodes  = []string{"ResourceNotFound.Trigger", "ResourceNotFound.Timer"}
)

// isFunctionNotFound 判断错误是不是“函数（或它所在的命名空间）不存在”。
func isFunctionNotFound(err error) bool { return hasErrorCode(err, functionNotFoundCodes) }

// isTriggerNotFound 判断错误是不是“定时器不存在”。
func isTriggerNotFound(err error) bool { return hasErrorCode(err, triggerNotFoundCodes) }

// isConfigNotFound 判断读异步重试、预留并发得到的是不是“没有配置”（任意 ResourceNotFound.*）：没配置过时照常去写。
func isConfigNotFound(err error) bool {
	var sdkErr *tcerrors.TencentCloudSDKError
	if errors.As(err, &sdkErr) {
		return strings.HasPrefix(sdkErr.GetCode(), "ResourceNotFound.")
	}
	return err != nil && strings.Contains(err.Error(), "ResourceNotFound.")
}

// hasErrorCode 判断腾讯云 SDK 错误的错误码是不是 codes 之一。不是 SDK 错误（测试里的假腾讯云）时按错误文本里的错误码匹配。
func hasErrorCode(err error, codes []string) bool {
	if err == nil {
		return false
	}
	var sdkErr *tcerrors.TencentCloudSDKError
	if errors.As(err, &sdkErr) {
		return slices.Contains(codes, sdkErr.GetCode())
	}
	message := err.Error()
	for _, code := range codes {
		if strings.Contains(message, code) {
			return true
		}
	}
	return false
}

// isTransientError 判断是不是值得重试的瞬时错误（网络、限频、超时、5xx）。参数、权限、不存在、配额错误不重试。
func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	// 等本地限流令牌的时间超过了截止时间：请求没有发出，下一次尝试有新的预算。
	if errors.Is(err, tencentscf.ErrRateLimitWaitExceedsDeadline) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"validation", "invalid param", "permission denied", "access denied", "not found", "quota", "limitexceeded.function"} {
		if strings.Contains(message, marker) {
			return false
		}
	}
	for _, marker := range []string{
		"clienterror.networkerror", "requestlimitexceeded", "context deadline exceeded", "client.timeout", "request canceled", "context canceled",
		"connection reset", "connection refused", "service unavailable", "too many requests",
		"status code: 429", "status code: 500", "status code: 502", "status code: 503", "status code: 504",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return strings.Contains(message, " i/o timeout") || strings.HasSuffix(message, "timeout")
}
