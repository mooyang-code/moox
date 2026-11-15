package cloud

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/cloud/tencentscf"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/stretchr/testify/require"
	tcerrors "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/errors"
)

func transitionSpec(timer Timer) FunctionSpec {
	return timerSpec(timerEnvironment(`{"v":1,"plan":"transition"}`, "hash-transition", "40", "scf-key-1"), "v1", timer)
}

func finalSpec(version string) FunctionSpec {
	return timerSpec(timerEnvironment(`{"v":1,"plan":"final"}`, "hash-final", "42", "scf-key-2"), version, Timer{Cron: "5 * * * * * *", Enabled: true})
}

// seedTimerFunction 在假腾讯云里直接放一个已经建好的定时采集函数（不记写请求）。
func seedTimerFunction(fake *baselineSCF, status, packageID string, environment map[string]string, timer *tencentscf.TimerTriggerInfo) {
	env := make(map[string]string, len(environment)+7)
	for key, value := range environment {
		env[key] = value
	}
	for key, value := range map[string]string{
		"MOOX_CLS_ENABLED": "true", "MOOX_CLS_ENDPOINT": "ap-guangzhou.cls.tencentcs.com", "MOOX_CLS_TOPIC_ID": "fake-topic",
		"MOOX_CLS_TIMEOUT_MS": "3000", "MOOX_CLS_SECRET_ID": "fake-cls-secret-id", "MOOX_CLS_SECRET_KEY": "fake-cls-secret-key",
	} {
		env[key] = value
	}
	env[packageIDKey] = packageID
	fake.seed(baselineRef, &baselineFunction{
		info: tencentscf.FunctionInfo{
			Status: status, Runtime: "Go1", Type: "Event", MemorySize: 64, Timeout: 60, MaxInstanceConcurrency: 1,
			PublicNetStatus: "ENABLE", VpcID: "vpc-fake", SubnetID: "subnet-fake", Environment: env,
		},
		handler: "main", cosBucket: "fake-bucket", cosRegion: "ap-guangzhou", cosObject: "packages/collector-" + strings.TrimPrefix(packageID, "moox-collector_") + ".zip",
		timer: timer, asyncRetry: &baselineAsyncRetry{RetryNum: 0, MsgTTL: 60},
	})
}

func enabledTimer(cron string) *tencentscf.TimerTriggerInfo {
	return &tencentscf.TimerTriggerInfo{Name: timerTriggerName, Type: "timer", Cron: cron, Enabled: true, AvailableStatus: "Available", Qualifier: timerTriggerQualifier, Message: timerTriggerMessage}
}

func functionState(t *testing.T, fake *baselineSCF, ref tencentscf.FunctionRef) baselineFunctionState {
	t.Helper()
	_, state := fake.snapshot()
	function, ok := state.Functions[baselineKey(ref)]
	require.True(t, ok, "云上没有函数 %s", baselineKey(ref))
	return function
}

// 云上没有：建出来；重复调用第二次没有任何写请求。
func TestEnsureCreatesMissingFunctionAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	fake := newBaselineSCF()
	fake.pendingReads = 2
	functions := newTestFunctions(t, fake)
	spec := finalSpec("v1")
	require.NoError(t, functions.Ensure(ctx, spec))
	function := functionState(t, fake, baselineRef)
	require.Equal(t, "Active", function.Status)
	require.Equal(t, baselineOldPackage, function.Environment[packageIDKey])
	require.True(t, function.Timer.Enabled)
	require.Empty(t, fake.rejectedWrites())

	fake.resetWrites()
	require.NoError(t, functions.Ensure(ctx, spec))
	writes, _ := fake.snapshot()
	require.Empty(t, writes, "云上已经和期望一致，第二次调用不能有写请求")
}

// 接管同名旧函数：代码包、环境、超时、VPC 都和期望不同，还带着旧部署的创建预留号。
// 先换代码、后写包 ID，环境以云上为底合并（人工加的键保留，预留号与废弃键删掉），定时器最后设。
func TestEnsureTakesOverSameNameFunctionWithDifferentPackageAndEnvironment(t *testing.T) {
	ctx := context.Background()
	fake := newBaselineSCF()
	fake.pendingReads = 1
	old := timerEnvironment(`{"v":1,"plan":"old-deploy"}`, "hash-old", "7", "scf-key-0")
	old["MOOX_CREATE_RESERVATION_ID"] = "old-reservation"
	old["MOOX_MARKET_FETCH_DNS_HASH"] = "retired"
	old["OPERATOR_EXTRA"] = "kept"
	seedTimerFunction(fake, "Active", "moox-collector_v0", old, &tencentscf.TimerTriggerInfo{Name: timerTriggerName, Type: "timer", Cron: "0 * * * * * *", Enabled: true, AvailableStatus: "Available", Qualifier: timerTriggerQualifier, Message: timerTriggerMessage})
	fake.mutate(baselineRef, func(function *baselineFunction) {
		function.info.Timeout, function.info.VpcID, function.info.SubnetID = 30, "vpc-old", "subnet-old"
		function.info.MemorySize, function.info.MaxInstanceConcurrency, function.info.PublicNetStatus = 128, 0, "DISABLE"
		// 旧函数的异步重试按腾讯云默认（没有显式配置）。
		function.asyncRetry = nil
	})
	functions := newTestFunctions(t, fake)
	require.NoError(t, functions.Ensure(ctx, finalSpec("v2")))

	writes, _ := fake.snapshot()
	assertBaselineOrder(t, writes, baselineKey(baselineRef), baselineNewPackage)
	require.Equal(t, -1, baselineIndex(writes, "DeleteFunction "), "接管不删函数")
	require.Equal(t, -1, baselineIndex(writes, "CreateFunction "), "接管不重建函数")
	function := functionState(t, fake, baselineRef)
	require.Equal(t, baselineNewPackage, function.Environment[packageIDKey])
	require.Equal(t, "packages/collector-v2.zip", function.Code.COSObject)
	require.Equal(t, "hash-final", function.Environment["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"])
	require.Equal(t, "kept", function.Environment["OPERATOR_EXTRA"])
	require.NotContains(t, function.Environment, "MOOX_CREATE_RESERVATION_ID")
	require.NotContains(t, function.Environment, "MOOX_MARKET_FETCH_DNS_HASH")
	require.Equal(t, int64(60), function.Timeout)
	require.Equal(t, int64(64), function.MemorySize)
	require.Equal(t, int64(1), function.MaxInstanceConcurrency)
	require.Equal(t, "ENABLE", function.PublicNetStatus)
	require.Equal(t, "vpc-fake", function.VpcID)
	require.Equal(t, "subnet-fake", function.SubnetID)
	require.True(t, function.NativeCLSCleared, "紧跟换代码的那次改配置清掉腾讯云自带的日志投递")
	require.Equal(t, "5 * * * * * *", function.Timer.Cron)
	require.Equal(t, &baselineAsyncRetry{RetryNum: 0, MsgTTL: 60}, function.AsyncRetry)
	require.Empty(t, fake.rejectedWrites())
}

// 定时器被人停掉或删掉：Ensure 把它改回期望的样子，环境与代码不动。
func TestEnsureRestoresStoppedOrDeletedTimer(t *testing.T) {
	ctx := context.Background()
	for name, timer := range map[string]*tencentscf.TimerTriggerInfo{
		"被停掉": {Name: timerTriggerName, Type: "timer", Cron: "5 * * * * * *", Enabled: false, AvailableStatus: "Available", Qualifier: timerTriggerQualifier, Message: timerTriggerMessage},
		"被删掉": nil,
	} {
		t.Run(name, func(t *testing.T) {
			fake := newBaselineSCF()
			seedTimerFunction(fake, "Active", baselineOldPackage, timerEnvironment(`{"v":1,"plan":"final"}`, "hash-final", "42", "scf-key-2"), timer)
			require.NoError(t, newTestFunctions(t, fake).Ensure(ctx, finalSpec("v1")))
			writes, _ := fake.snapshot()
			require.Len(t, writes, 1, "只改定时器")
			require.True(t, strings.HasPrefix(writes[0], "EnsureTimerTrigger "))
			require.True(t, functionState(t, fake, baselineRef).Timer.Enabled)
		})
	}
}

// 状态是 CreateFailed：先删、等删完、再建成（H20）。
func TestEnsureDeletesCreateFailedFunctionThenRecreatesIt(t *testing.T) {
	ctx := context.Background()
	fake := newBaselineSCF()
	fake.pendingReads = 1
	seedTimerFunction(fake, "CreateFailed", baselineOldPackage, timerEnvironment(`{"v":1,"plan":"broken"}`, "hash-broken", "1", "scf-key-0"), nil)
	require.NoError(t, newTestFunctions(t, fake).Ensure(ctx, finalSpec("v1")))
	writes, _ := fake.snapshot()
	deleteAt, createAt := baselineIndex(writes, "DeleteFunction "), baselineIndex(writes, "CreateFunction ")
	require.GreaterOrEqual(t, deleteAt, 0)
	require.Greater(t, createAt, deleteAt, "先删再建")
	function := functionState(t, fake, baselineRef)
	require.Equal(t, "Active", function.Status)
	require.Equal(t, "hash-final", function.Environment["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"])
	require.True(t, function.Timer.Enabled)
	require.Empty(t, fake.rejectedWrites())
}

// 状态是 UpdateFailed：不删，直接重做更新（即使环境看起来已经对了），成功后回到 Active。
// 假设腾讯云接受对 UpdateFailed 的函数重做更新（scriptedSCF.acceptUpdateFailed 模拟这一点，没有在真实腾讯云上验证）。
func TestEnsureRedoesUpdateForUpdateFailedFunction(t *testing.T) {
	ctx := context.Background()
	fake := newBaselineSCF()
	fake.pendingReads = 1
	seedTimerFunction(fake, "UpdateFailed", baselineOldPackage, timerEnvironment(`{"v":1,"plan":"final"}`, "hash-final", "42", "scf-key-2"), enabledTimer("5 * * * * * *"))
	client := newScriptedSCF(fake)
	client.acceptUpdateFailed = true
	require.NoError(t, newTestFunctions(t, client).Ensure(ctx, finalSpec("v1")))
	require.Zero(t, client.count("DeleteFunction"), "UpdateFailed 不删函数")
	require.Zero(t, client.count("CreateFunction"))
	require.Equal(t, 1, client.count("UpdateFunctionConfiguration"), "重做一次更新")
	require.Equal(t, "Active", functionState(t, fake, baselineRef).Status)
	require.Empty(t, fake.rejectedWrites())
}

// 换代码失败：环境里的包 ID 仍是旧的，定时器没被动过。
func TestEnsureCodeUpdateFailureLeavesPackageIDAndTimerUntouched(t *testing.T) {
	ctx := context.Background()
	fake := newBaselineSCF()
	seedTimerFunction(fake, "Active", baselineOldPackage, timerEnvironment(`{"v":1,"plan":"final"}`, "hash-final", "42", "scf-key-2"), &tencentscf.TimerTriggerInfo{Name: timerTriggerName, Type: "timer", Cron: "5 * * * * * *", Enabled: false, AvailableStatus: "Available", Qualifier: timerTriggerQualifier, Message: timerTriggerMessage})
	client := newScriptedSCF(fake)
	client.codeErrs = []error{tcerrors.NewTencentCloudSDKError("InvalidParameterValue.Code", "代码包不可用", "req-1")}
	err := newTestFunctions(t, client).Ensure(ctx, finalSpec("v2"))
	require.ErrorContains(t, err, "换代码失败")
	function := functionState(t, fake, baselineRef)
	require.Equal(t, baselineOldPackage, function.Environment[packageIDKey])
	require.False(t, function.Timer.Enabled, "定时器没被动过")
	require.Zero(t, client.count("UpdateFunctionConfiguration"))
	require.Zero(t, client.count("EnsureTimerTrigger"))
	require.Equal(t, 1, client.count("UpdateFunctionCode"), "参数错误不重试")
}

// 建到一半被取消（建函数请求已发出、函数还在 Creating）：再调一次能续上，函数只建一次。
func TestEnsureResumesAfterCancellationMidCreate(t *testing.T) {
	fake := newBaselineSCF()
	fake.pendingReads = 3
	client := newScriptedSCF(fake)
	ctx, cancel := context.WithCancel(context.Background())
	client.afterCreate = cancel
	functions := newTestFunctions(t, client)
	err := functions.Ensure(ctx, finalSpec("v1"))
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)

	client.afterCreate = nil
	require.NoError(t, functions.Ensure(context.Background(), finalSpec("v1")))
	require.Equal(t, 1, client.count("CreateFunction"), "第二次接着做，不重复建")
	function := functionState(t, fake, baselineRef)
	require.Equal(t, "Active", function.Status)
	require.True(t, function.Timer.Enabled)
	require.Equal(t, &baselineAsyncRetry{RetryNum: 0, MsgTTL: 60}, function.AsyncRetry, "第一次没来得及设的异步重试在第二次补上")
	require.Empty(t, fake.rejectedWrites())
}

// 设定时器那一步看到空间正在暂停：期望启用的定时器设成停用；暂停信号在第 4 步期间才翻过来也算。
func TestEnsureDisablesTimerWhenSpaceIsPausing(t *testing.T) {
	ctx := context.Background()
	fake := newBaselineSCF()
	fake.pendingReads = 1
	seedTimerFunction(fake, "Active", baselineOldPackage, timerEnvironment(`{"v":1,"plan":"transition"}`, "hash-transition", "40", "scf-key-1"), &tencentscf.TimerTriggerInfo{Name: timerTriggerName, Type: "timer", Cron: "0 * * * * * *", Enabled: false, AvailableStatus: "Available", Qualifier: timerTriggerQualifier, Message: timerTriggerMessage})
	client := newScriptedSCF(fake)
	var pausing atomic.Bool
	client.beforeConfig = func() { pausing.Store(true) }
	spec := finalSpec("v1")
	spec.Pausing = pausing.Load
	require.NoError(t, newTestFunctions(t, client).Ensure(ctx, spec))
	require.Equal(t, 1, client.count("UpdateFunctionConfiguration"), "第 4 步照常改环境")
	function := functionState(t, fake, baselineRef)
	require.Equal(t, "hash-final", function.Environment["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"])
	require.False(t, function.Timer.Enabled, "空间正在暂停，定时器不能被打开")
	require.Equal(t, "5 * * * * * *", function.Timer.Cron)
}

// SetTimer 启用时代码包落后：返回 ErrPackageBehind，定时器保持原样；停用不看代码包。
func TestSetTimerRefusesToEnableFunctionOnOutdatedPackage(t *testing.T) {
	ctx := context.Background()
	fake := newBaselineSCF()
	disabled := &tencentscf.TimerTriggerInfo{Name: timerTriggerName, Type: "timer", Cron: "5 * * * * * *", Enabled: false, AvailableStatus: "Available", Qualifier: timerTriggerQualifier, Message: timerTriggerMessage}
	seedTimerFunction(fake, "Active", baselineOldPackage, timerEnvironment(`{"v":1,"plan":"final"}`, "hash-final", "42", "scf-key-2"), disabled)
	functions := newTestFunctions(t, fake)
	for _, want := range []string{baselineNewPackage, ""} {
		err := functions.SetTimer(ctx, timerRef, Timer{Cron: "5 * * * * * *", Enabled: true}, want)
		require.ErrorIs(t, err, ErrPackageBehind)
	}
	writes, _ := fake.snapshot()
	require.Empty(t, writes)
	require.False(t, functionState(t, fake, baselineRef).Timer.Enabled)

	require.NoError(t, functions.SetTimer(ctx, timerRef, Timer{Cron: "5 * * * * * *", Enabled: true}, baselineOldPackage))
	require.True(t, functionState(t, fake, baselineRef).Timer.Enabled)
	require.NoError(t, functions.SetTimer(ctx, timerRef, Timer{}, ""), "停用不看代码包，cron 为空时用占位 cron")
	timer := functionState(t, fake, baselineRef).Timer
	require.False(t, timer.Enabled)
	require.Equal(t, DisabledTimerCron, timer.Cron)
	require.ErrorIs(t, functions.SetTimer(ctx, FunctionRef{AccountID: baselineAccount, Region: "ap-singapore", Namespace: "moox-collector", Name: "absent"}, Timer{}, ""), ErrFunctionNotFound)
}

// 配额超限单独成为 ErrQuota，不重试；限频（RequestLimitExceeded）不算配额。
func TestEnsureReportsQuotaExceededSeparately(t *testing.T) {
	ctx := context.Background()
	for _, code := range []string{"LimitExceeded.Function", "LimitExceeded.Namespace"} {
		client := newScriptedSCF(newBaselineSCF())
		client.createErrs = []error{tcerrors.NewTencentCloudSDKError(code, "超过上限", "req-1")}
		err := newTestFunctions(t, client).Ensure(ctx, finalSpec("v1"))
		require.ErrorIs(t, err, ErrQuota, code)
		require.Equal(t, 1, client.count("CreateFunction"), "配额超限不重试")
	}
	require.False(t, isQuotaError(tcerrors.NewTencentCloudSDKError("RequestLimitExceeded", "限频", "req-2")))
	require.True(t, isTransientError(tcerrors.NewTencentCloudSDKError("RequestLimitExceeded", "限频", "req-2")))
	require.False(t, isQuotaError(errors.New("LimitExceeded.Function 不是 SDK 错误")))
}

// 遇到瞬时错误（网络）把整个操作从头再做一遍；不是瞬时错误的不重试。
func TestEnsureRetriesTransientErrorsOnly(t *testing.T) {
	ctx := context.Background()
	client := newScriptedSCF(newBaselineSCF())
	client.getErrs = []error{errors.New("ClientError.NetworkError: connection reset by peer")}
	require.NoError(t, newTestFunctions(t, client).Ensure(ctx, finalSpec("v1")))
	require.Equal(t, 1, client.count("CreateFunction"))

	client = newScriptedSCF(newBaselineSCF())
	client.getErrs = []error{errors.New("AuthFailure.SignatureFailure: permission denied")}
	require.ErrorContains(t, newTestFunctions(t, client).Ensure(ctx, finalSpec("v1")), "permission denied")
	require.Equal(t, 1, client.count("GetFunction"))
	require.Zero(t, client.count("CreateFunction"))
}

// 碰云端之前就被拒绝，一条写请求都不发：环境带 EventBus 项（采集函数不连 EventBus）、超时不是 60、环境超长、
// 缺 Storage 应用密钥或 Access 三项、定时函数不带采集计划、cron 不支持、代码包不可用。
func TestEnsureRejectsInvalidSpecsBeforeAnyWrite(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name   string
		mutate func(*FunctionSpec)
		want   string
	}{
		{"环境带 EventBus 项", func(spec *FunctionSpec) { spec.Environment["MOOX_EVENTBUS_NATS_URL"] = "tls://eventbus.example:4222" }, "MOOX_EVENTBUS_NATS_URL"},
		{"环境带 EventBus 的 CA PEM", func(spec *FunctionSpec) { spec.Environment["MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64"] = "ZmFrZQ==" }, "MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64"},
		{"外层超时不是 60", func(spec *FunctionSpec) { spec.TimeoutS = 30 }, "60"},
		{"环境里的超时不是 60", func(spec *FunctionSpec) { spec.Environment["MOOX_FETCH_TIMEOUT_SECONDS"] = "30" }, "60"},
		{"环境超长", func(spec *FunctionSpec) { spec.Environment["OPERATOR_EXTRA"] = strings.Repeat("x", 4096) }, "4096"},
		{"缺 Storage 应用密钥", func(spec *FunctionSpec) { delete(spec.Environment, "MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON") }, "MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON"},
		{"缺 Access 地址", func(spec *FunctionSpec) { delete(spec.Environment, "MOOX_ACCESS_ADDRESS") }, "MOOX_ACCESS_ADDRESS"},
		{"缺 Access 身份", func(spec *FunctionSpec) { delete(spec.Environment, "MOOX_ACCESS_ID") }, "MOOX_ACCESS_ID"},
		{"缺调用方密钥", func(spec *FunctionSpec) { delete(spec.Environment, "MOOX_CALLER_KEY") }, "MOOX_CALLER_KEY"},
		{"调用方不是 scf-collector", func(spec *FunctionSpec) { spec.Environment["MOOX_CALLER"] = "strategy" }, "scf-collector"},
		{"定时函数不带采集计划", func(spec *FunctionSpec) { delete(spec.Environment, "MOOX_MARKET_FETCH_PLAN") }, "MOOX_MARKET_FETCH_PLAN"},
		{"cron 不支持", func(spec *FunctionSpec) { spec.Timer = &Timer{Cron: "0 */7 * * * * *", Enabled: true} }, "cron"},
		{"代码包不可用", func(spec *FunctionSpec) { spec.Package.Status = "pending" }, "不可用"},
		{"没有 VPC", func(spec *FunctionSpec) { spec.VPCID = "" }, "VPC"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := newBaselineSCF()
			spec := finalSpec("v1")
			test.mutate(&spec)
			require.ErrorContains(t, newTestFunctions(t, fake).Ensure(ctx, spec), test.want)
			writes, state := fake.snapshot()
			require.Empty(t, writes)
			require.Empty(t, state.Functions)
		})
	}

	// 补采函数不能不带 backfill 标记；CLS 补不上（没有 CLS）时定时采集函数的环境校验拒绝。
	fake := newBaselineSCF()
	spec := backfillSpec()
	delete(spec.Environment, "MOOX_MARKET_FETCH_MODE")
	require.ErrorContains(t, newTestFunctions(t, fake).Ensure(ctx, spec), "补采函数")
	functions := newTestFunctions(t, fake)
	functions.cls = nil
	require.ErrorContains(t, functions.Ensure(ctx, finalSpec("v1")), "MOOX_CLS_")
	writes, _ := fake.snapshot()
	require.Empty(t, writes)
}

// 云上的环境带 EventBus 项、或合并后超长：Ensure 与 UpgradeCode 都拒绝，环境、代码与定时器都不动。
func TestEnsureAndUpgradeRejectInvalidMergedEnvironmentWithoutWrites(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name  string
		drift func(*baselineFunction)
		want  string
	}{
		{"云上环境带 EventBus 项", func(function *baselineFunction) {
			function.info.Environment["MOOX_EVENTBUS_NATS_URL"] = "tls://eventbus.example:4222"
		}, "MOOX_EVENTBUS_NATS_URL"},
		{"合并后环境超长", func(function *baselineFunction) {
			function.info.Environment["OPERATOR_EXTRA"] = strings.Repeat("x", 4096)
		}, "4096"},
		{"云上缺调用方密钥", func(function *baselineFunction) { delete(function.info.Environment, "MOOX_CALLER_KEY") }, "MOOX_CALLER_KEY"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := newBaselineSCF()
			seedTimerFunction(fake, "Active", baselineOldPackage, timerEnvironment(`{"v":1,"plan":"transition"}`, "hash-transition", "40", "scf-key-1"), enabledTimer("0 * * * * * *"))
			fake.mutate(baselineRef, test.drift)
			_, before := fake.snapshot()
			functions := newTestFunctions(t, fake)
			if test.name != "云上缺调用方密钥" {
				require.ErrorContains(t, functions.Ensure(ctx, finalSpec("v1")), test.want)
			}
			require.ErrorContains(t, functions.UpgradeCode(ctx, timerRef, baselinePackage("v2")), test.want)
			writes, after := fake.snapshot()
			require.Empty(t, writes)
			require.Equal(t, before, after)
		})
	}
}

// 升级只换代码：环境沿用云上读回的值（含调用方密钥与 Storage 应用密钥，读回给包外的结果虽然去掉了它们），定时器不动；
// 已经是新包时不发任何写请求。
func TestUpgradeCodeKeepsRemoteEnvironmentIncludingSecrets(t *testing.T) {
	ctx := context.Background()
	fake := newBaselineSCF()
	fake.pendingReads = 1
	transition := timerEnvironment(`{"v":1,"plan":"transition"}`, "hash-transition", "40", "scf-key-1")
	seedTimerFunction(fake, "Active", baselineOldPackage, transition, enabledTimer("0 * * * * * *"))
	functions := newTestFunctions(t, fake)
	require.NoError(t, functions.UpgradeCode(ctx, timerRef, baselinePackage("v2")))
	writes, _ := fake.snapshot()
	assertBaselineOrder(t, writes, baselineKey(baselineRef), baselineNewPackage)
	require.Equal(t, -1, baselineIndex(writes, "EnsureTimerTrigger "), "升级不动定时器")
	function := functionState(t, fake, baselineRef)
	require.Equal(t, baselineNewPackage, function.Environment[packageIDKey])
	for key, value := range transition {
		require.Equal(t, value, function.Environment[key], "升级保留过渡环境里的 %s", key)
	}
	require.Equal(t, strings.Repeat("b", 64), function.Environment["MOOX_CALLER_KEY"])

	fake.resetWrites()
	require.NoError(t, functions.UpgradeCode(ctx, timerRef, baselinePackage("v2")))
	writes, _ = fake.snapshot()
	require.Empty(t, writes)
}

// 读回不带出调用方密钥与 Storage 应用密钥；Describe 带定时器的可用状态、Qualifier、Message，DescribeCode 不读定时器。
func TestDescribeStripsSecretsAndReadsTimer(t *testing.T) {
	ctx := context.Background()
	fake := newBaselineSCF()
	seedTimerFunction(fake, "Active", baselineOldPackage, timerEnvironment(`{"v":1,"plan":"final"}`, "hash-final", "42", "scf-key-2"), enabledTimer("5 * * * * * *"))
	client := newScriptedSCF(fake)
	functions := newTestFunctions(t, client)
	state, err := functions.Describe(ctx, timerRef)
	require.NoError(t, err)
	require.True(t, state.Exists)
	require.Equal(t, "Active", state.Status)
	require.Equal(t, baselineOldPackage, state.PackageID)
	require.Equal(t, "hash-final", state.Environment["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"])
	require.Equal(t, "scf-key-2", state.Environment["MOOX_CALLER_KEY_ID"])
	for _, key := range []string{"MOOX_CALLER_KEY", "MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON", "MOOX_CLS_SECRET_KEY"} {
		require.NotContains(t, state.Environment, key)
	}
	require.Equal(t, &TimerState{Timer: Timer{Cron: "5 * * * * * *", Enabled: true}, AvailableStatus: "Available", Qualifier: "$LATEST", Message: timerTriggerMessage}, state.Timer)
	require.False(t, state.ReadAt.IsZero())

	reads := client.count("GetTimerTrigger")
	code, err := functions.DescribeCode(ctx, timerRef)
	require.NoError(t, err)
	require.Equal(t, baselineOldPackage, code.PackageID)
	require.Nil(t, code.Timer)
	require.Equal(t, reads, client.count("GetTimerTrigger"), "DescribeCode 只调 GetFunction")

	missing, err := functions.Describe(ctx, FunctionRef{AccountID: baselineAccount, Region: "ap-singapore", Namespace: "moox-collector", Name: "absent"})
	require.NoError(t, err)
	require.False(t, missing.Exists)
}

// 列举覆盖这个地域的全部命名空间（含第二片命名空间），只按前缀过滤；计划外的函数只停定时器、不删除。
func TestListCoversShardNamespacesAndUnplannedFunctionsCanBeStoppedWithoutDeletion(t *testing.T) {
	ctx := context.Background()
	fake := newBaselineSCF()
	environment := timerEnvironment(`{"v":1,"plan":"old"}`, "hash-old", "3", "scf-key-0")
	for _, ref := range []tencentscf.FunctionRef{
		{Region: "ap-singapore", Namespace: "moox-collector", FunctionName: "moox-fetch-plan-crypto-crypto-singapore-007"},
		{Region: "ap-singapore", Namespace: "moox-collector-2", FunctionName: "moox-fetch-plan-crypto-crypto-singapore-130"},
		{Region: "ap-singapore", Namespace: "other", FunctionName: "someone-else"},
		{Region: "ap-tokyo", Namespace: "moox-collector", FunctionName: "moox-fetch-plan-crypto-crypto-tokyo-000"},
	} {
		env := make(map[string]string, len(environment)+1)
		for key, value := range environment {
			env[key] = value
		}
		env[packageIDKey] = baselineOldPackage
		fake.seed(ref, &baselineFunction{info: tencentscf.FunctionInfo{Status: "Active", Environment: env}, timer: enabledTimer("0 * * * * * *")})
	}
	functions := newTestFunctions(t, fake)
	refs, err := functions.List(ctx, baselineAccount, "ap-singapore", "moox-fetch-plan-crypto-")
	require.NoError(t, err)
	require.Equal(t, []FunctionRef{
		{AccountID: baselineAccount, Region: "ap-singapore", Namespace: "moox-collector", Name: "moox-fetch-plan-crypto-crypto-singapore-007"},
		{AccountID: baselineAccount, Region: "ap-singapore", Namespace: "moox-collector-2", Name: "moox-fetch-plan-crypto-crypto-singapore-130"},
	}, refs)
	for _, ref := range refs {
		require.NoError(t, functions.SetTimer(ctx, ref, Timer{}, ""))
	}
	_, state := fake.snapshot()
	require.Len(t, state.Functions, 4, "只停定时器，不删函数")
	for _, ref := range refs {
		require.False(t, state.Functions[ref.Region+"/"+ref.Namespace+"/"+ref.Name].Timer.Enabled)
	}
}

// 同一个函数的写操作不并发（H12）：并发的 Ensure 与 Delete 排队执行。
func TestConcurrentEnsureAndDeleteOnOneFunctionDoNotOverlap(t *testing.T) {
	ctx := context.Background()
	fake := newBaselineSCF()
	fake.pendingReads = 1
	client := newScriptedSCF(fake)
	client.slowWrites = 5 * time.Millisecond
	functions := newTestFunctions(t, client)
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%3 == 2 {
				errs[i] = functions.Delete(ctx, timerRef)
				return
			}
			errs[i] = functions.Ensure(ctx, finalSpec("v1"))
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Empty(t, client.overlapping(), "同一个函数的写请求并发了")
	require.Empty(t, fake.rejectedWrites())
}

// 补采函数建到一半（预留并发还没设）就中断：下一次 Ensure 读回预留并发，补上；已经对的不再写。
func TestEnsureRepairsMissingBackfillReservedConcurrency(t *testing.T) {
	ctx := context.Background()
	fake := newBaselineSCF()
	functions := newTestFunctions(t, fake)
	require.NoError(t, functions.Ensure(ctx, backfillSpec()))
	fake.mutate(baselineBackfillRef, func(function *baselineFunction) { function.reservedMem = 0 })
	fake.resetWrites()
	require.NoError(t, functions.Ensure(ctx, backfillSpec()))
	writes, _ := fake.snapshot()
	require.Equal(t, []string{"PutReservedConcurrencyConfig ap-singapore/moox-collector/" + baselineBackfill + " mem=320"}, writes)
	fake.resetWrites()
	require.NoError(t, functions.Ensure(ctx, backfillSpec()))
	writes, _ = fake.snapshot()
	require.Empty(t, writes)
}

// 删除：函数已经不在时不报错；补采函数没有定时器也能删。
func TestDeleteIsIdempotent(t *testing.T) {
	ctx := context.Background()
	fake := newBaselineSCF()
	functions := newTestFunctions(t, fake)
	require.NoError(t, functions.Ensure(ctx, backfillSpec()))
	require.NoError(t, functions.Delete(ctx, backfillRef))
	require.NoError(t, functions.Delete(ctx, backfillRef))
	_, state := fake.snapshot()
	require.Empty(t, state.Functions)
}

// 调用按 sync 选同步或异步。
func TestInvokeChoosesInvocationType(t *testing.T) {
	ctx := context.Background()
	client := newScriptedSCF(newBaselineSCF())
	functions := newTestFunctions(t, client)
	_, err := functions.Invoke(ctx, backfillRef, map[string]any{"probe": true}, true)
	require.NoError(t, err)
	_, err = functions.Invoke(ctx, backfillRef, map[string]any{"probe": true}, false)
	require.NoError(t, err)
	require.Equal(t, 1, client.count("Invoke:RequestResponse"))
	require.Equal(t, 1, client.count("Invoke:Event"))
}

// 调用方取消上下文时操作随之结束，不把剩下的步骤做完。
func TestEnsureStopsWhenCallerCancels(t *testing.T) {
	fake := newBaselineSCF()
	fake.pendingReads = 1 << 20
	client := newScriptedSCF(fake)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	begin := time.Now()
	require.ErrorIs(t, newTestFunctions(t, client).Ensure(ctx, finalSpec("v1")), context.DeadlineExceeded)
	require.Less(t, time.Since(begin), 5*time.Second)
	require.Zero(t, client.count("EnsureTimerTrigger"))
}

// recordingCLS 记下补 CLS 时拿到的密钥，并补上与真实实现同量级的七个变量（约 300 字节）。
type recordingCLS struct {
	credential Credential
	calls      int
}

func (r *recordingCLS) Apply(_ context.Context, _ string, credential Credential, environment map[string]string) bool {
	r.calls++
	r.credential = credential
	for _, key := range []string{"MOOX_CLS_ENABLED", "MOOX_CLS_ENDPOINT", "MOOX_CLS_LOGSET_ID", "MOOX_CLS_TOPIC_ID", "MOOX_CLS_SECRET_ID", "MOOX_CLS_SECRET_KEY", "MOOX_CLS_TIMEOUT_MS"} {
		environment[key] = strings.Repeat("v", 40)
	}
	return true
}

// 补 CLS 用的是云账号的密钥；补上之后会超过 4096 字节时放弃补齐，原环境不变；没有配置 CLS 时是空操作。
func TestApplyCLSSkipsInjectionThatWouldOverflowTheEnvironmentLimit(t *testing.T) {
	ctx := context.Background()
	recorder := &recordingCLS{}
	functions := &TencentFunctions{cls: recorder}
	s := session{account: store.CloudAccount{AccountID: baselineAccount}, credential: Credential{AccountID: baselineAccount, SecretID: "id", SecretKey: "key"}}
	environment := map[string]string{"MOOX_SPACE_ID": "crypto"}
	functions.applyCLS(ctx, s, "fn", environment)
	require.Equal(t, strings.Repeat("v", 40), environment["MOOX_CLS_ENABLED"])
	require.Equal(t, s.credential, recorder.credential)

	full := map[string]string{"MOOX_SPACE_ID": "crypto", "FILLER": strings.Repeat("x", tencent.SCFEnvironmentLimitBytes-150)}
	functions.applyCLS(ctx, s, "fn", full)
	require.Len(t, full, 2)
	require.NotContains(t, full, "MOOX_CLS_ENABLED")
	require.NoError(t, tencent.ValidateSCFEnvironment(full), "原环境保持合法")

	untouched := map[string]string{}
	(&TencentFunctions{}).applyCLS(ctx, s, "fn", untouched)
	require.Empty(t, untouched)
}

// notConfiguredSCF 模拟腾讯云对没配置过异步重试、预留并发的函数直接回“不存在”。
type notConfiguredSCF struct{ *baselineSCF }

func (notConfiguredSCF) GetFunctionEventInvokeConfig(context.Context, tencentscf.FunctionRef) (*tencentscf.EventInvokeConfig, error) {
	return nil, errors.New("ResourceNotFound.AsyncTriggerConfig")
}

func (notConfiguredSCF) GetReservedConcurrencyConfig(context.Context, tencentscf.FunctionRef) (int64, error) {
	return 0, errors.New("ResourceNotFound.ReservedConcurrency")
}

// 读回两项配置得到“不存在”时当作没有配置：照常写上，不让建函数失败。
func TestEnsureTreatsNotFoundExtrasAsUnconfigured(t *testing.T) {
	fake := newBaselineSCF()
	require.NoError(t, newTestFunctions(t, notConfiguredSCF{fake}).Ensure(context.Background(), backfillSpec()))
	function := functionState(t, fake, baselineBackfillRef)
	require.Equal(t, &baselineAsyncRetry{RetryNum: 0, MsgTTL: 60}, function.AsyncRetry)
	require.Equal(t, int64(320), function.ReservedConcurrencyMem)
}
