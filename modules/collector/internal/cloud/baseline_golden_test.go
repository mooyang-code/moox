package cloud

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/cloud/tencentscf"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

// 批 2 的金样比对（腾讯云客户端这一层）。
//
// testdata/scf_paths.golden.json 是 T0 用原 CloudFunction 模块的旧实现跑出来的（该模块已在批 2 删除），
// 这里用同一个假腾讯云（scf_baseline_fake_test.go）驱动 Ensure、UpgradeCode、SetTimer、Delete 跑同样的六条路径。
// 比对的是每条路径结束时假腾讯云里的最终状态（环境、配置、定时器），只允许一处不同：环境里没有 MOOX_CREATE_RESERVATION_ID。
// 另外按函数名过滤断言三条顺序约束（assertBaselineOrder）。请求次数多了少了不算差别。

const (
	baselineSpace      = "crypto"
	baselineOldPackage = "moox-collector_v1"
	baselineNewPackage = "moox-collector_v2"
	baselineTimerName  = "moox-fetch-plan-crypto-crypto-singapore-000"
	baselineBackfill   = "moox-fetch-plan-backfill-crypto-crypto-singapore"
	baselineAccount    = "account-a"
)

var (
	baselineRef         = tencentscf.FunctionRef{Region: "ap-singapore", Namespace: "moox-collector", FunctionName: baselineTimerName}
	baselineBackfillRef = tencentscf.FunctionRef{Region: "ap-singapore", Namespace: "moox-collector", FunctionName: baselineBackfill}
	timerRef            = FunctionRef{AccountID: baselineAccount, Region: baselineRef.Region, Namespace: baselineRef.Namespace, Name: baselineTimerName}
	backfillRef         = FunctionRef{AccountID: baselineAccount, Region: baselineRef.Region, Namespace: baselineRef.Namespace, Name: baselineBackfill}
)

// baselinePath 是金样文件里一条路径的记录。
type baselinePath struct {
	Name   string        `json:"name"`
	Writes []string      `json:"writes"`
	Final  baselineState `json:"final"`
}

// baselineCLSEnvironment 补齐日志服务（CLS）的六个环境项，值是固定的假值（与 T0 录金样时的一致）。
type baselineCLSEnvironment struct{}

func (baselineCLSEnvironment) Apply(_ context.Context, _ string, _ Credential, environment map[string]string) bool {
	changed := false
	for key, value := range map[string]string{
		"MOOX_CLS_ENABLED": "true", "MOOX_CLS_ENDPOINT": "ap-guangzhou.cls.tencentcs.com", "MOOX_CLS_TOPIC_ID": "fake-topic",
		"MOOX_CLS_TIMEOUT_MS": "3000", "MOOX_CLS_SECRET_ID": "fake-cls-secret-id", "MOOX_CLS_SECRET_KEY": "fake-cls-secret-key",
	} {
		if environment[key] != value {
			environment[key] = value
			changed = true
		}
	}
	return changed
}

func baselineBaseEnvironment() map[string]string {
	return map[string]string{
		"MOOX_SPACE_ID": baselineSpace,
		"MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON": `{"moox-collector":"` + strings.Repeat("a", 64) + `"}`,
	}
}

func baselineAccessEnvironment(keyID string) map[string]string {
	return map[string]string{
		"MOOX_ACCESS_ADDRESS": "access.example:11004", "MOOX_ACCESS_ID": "access@control-a", "MOOX_CALLER": "scf-collector",
		"MOOX_CALLER_KEY_ID": keyID, "MOOX_CALLER_KEY": strings.Repeat("b", 64),
	}
}

func baselineManagedEnvironment(plan, hash, count, keyID string) map[string]string {
	environment := map[string]string{
		"MOOX_MARKET_FETCH_PLAN": plan, "MOOX_MARKET_FETCH_ASSIGNMENT_HASH": hash,
		"MOOX_MARKET_FETCH_SUBJECT_COUNT": count, "MOOX_FETCH_TIMEOUT_SECONDS": "60",
	}
	for key, value := range baselineAccessEnvironment(keyID) {
		environment[key] = value
	}
	return environment
}

// timerEnvironment 是定时采集函数的完整期望环境（基础项加受管项），对应 Collector 的 functionEnvironment 加 BaseEnvironment。
func timerEnvironment(plan, hash, count, keyID string) map[string]string {
	environment := baselineManagedEnvironment(plan, hash, count, keyID)
	for key, value := range baselineBaseEnvironment() {
		environment[key] = value
	}
	return environment
}

func backfillEnvironment() map[string]string {
	environment := map[string]string{"MOOX_MARKET_FETCH_MODE": "backfill", "MOOX_FETCH_TIMEOUT_SECONDS": "60"}
	for key, value := range baselineAccessEnvironment("scf-key-1") {
		environment[key] = value
	}
	for key, value := range baselineBaseEnvironment() {
		environment[key] = value
	}
	return environment
}

func baselinePackage(version string) Package {
	return Package{
		SpaceID: baselineSpace, PackageID: "moox-collector_" + version, PackageName: "collector", Version: version, Runtime: "CustomRuntime",
		CloudAccountID: baselineAccount, COSRegion: "ap-guangzhou", COSBucket: "fake-bucket", COSPath: "/packages/collector-" + version + ".zip",
		Status: store.FunctionPackageAvailable,
	}
}

func timerSpec(environment map[string]string, version string, timer Timer) FunctionSpec {
	return FunctionSpec{
		Ref: timerRef, Package: baselinePackage(version), Environment: environment, MemoryMB: 64, TimeoutS: 60,
		VPCID: "vpc-fake", SubnetID: "subnet-fake", Timer: &timer,
	}
}

func backfillSpec() FunctionSpec {
	return FunctionSpec{
		Ref: backfillRef, Package: baselinePackage("v1"), Environment: backfillEnvironment(), MemoryMB: 64, TimeoutS: 60,
		VPCID: "vpc-fake", SubnetID: "subnet-fake", ReservedMem: 320,
	}
}

// newTestFunctions 用临时库（登记一个云账号）、假密钥、假 CLS 与给定的腾讯云客户端构造 TencentFunctions。
func newTestFunctions(t *testing.T, client SCFClient) *TencentFunctions {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.CloudAccounts().Upsert(context.Background(), store.CloudAccount{
		AccountID: baselineAccount, Provider: "tencent", CredentialSecretID: "fake-secret", COSRegion: "ap-guangzhou", COSBucket: "fake-bucket",
	}))
	functions := NewFunctions(db, fakeCredentials{}, baselineCLSEnvironment{})
	functions.newClient = func(Credential) SCFClient { return client }
	return functions
}

// baselineIndex 返回第一条以 prefix 开头的写请求的位置，没有返回 -1。
func baselineIndex(writes []string, prefix string) int {
	for index, write := range writes {
		if strings.HasPrefix(write, prefix) {
			return index
		}
	}
	return -1
}

// assertBaselineOrder 检查写请求里属于 function（地域/命名空间/函数名）的那些满足三条顺序约束（与 T0 的定义相同）；
// 别的函数的请求穿插在中间不影响判断。
//  1. 换代码在先、包 ID 后写：UpdateFunctionCode 发出时环境里的包 ID 还不是 wantPackage，之后才有一次改配置把它写成 wantPackage。
//  2. 定时器最后设：这个函数的 EnsureTimerTrigger 之后不再有它的别的写请求。
//  3. 代码包落后时不启用定时器：启用定时器的请求发出时，环境里的包 ID 必须等于 wantPackage。
func assertBaselineOrder(t *testing.T, writes []string, function, wantPackage string) {
	t.Helper()
	var own []string
	for _, write := range writes {
		if fields := strings.Fields(write); len(fields) >= 2 && fields[1] == function {
			own = append(own, write)
		}
	}
	if code := baselineIndex(own, "UpdateFunctionCode "); code >= 0 {
		require.NotContains(t, own[code], "package_id="+wantPackage+" ", "换代码时环境里的包 ID 不能已经是新包")
		marked := -1
		for index := code + 1; index < len(own); index++ {
			if strings.HasPrefix(own[index], "UpdateFunctionConfiguration ") && strings.Contains(own[index], "package_id="+wantPackage+" ") {
				marked = index
				break
			}
		}
		require.Greater(t, marked, code, "包 ID 必须在换代码之后写入")
	}
	for index, write := range own {
		if !strings.HasPrefix(write, "EnsureTimerTrigger ") {
			continue
		}
		require.Equal(t, len(own)-1, index, "定时器必须是这个函数的最后一个写请求")
		if strings.Contains(write, "enabled=true") {
			require.Contains(t, write, "package_id="+wantPackage+" ", "代码包落后时不能启用定时器")
		}
	}
}

// 顺序约束按函数过滤：两个函数的请求交错时不误报，单个函数违反时照样报。
func TestBaselineOrderIsCheckedPerFunction(t *testing.T) {
	interleaved := []string{
		"UpdateFunctionCode r/n/a package_id=v1 code=v2.zip",
		"EnsureTimerTrigger r/n/b cron=\"0 * * * * * *\" enabled=true package_id=v2 code=v2.zip",
		"UpdateFunctionConfiguration r/n/a package_id=v2 code=v2.zip",
		"EnsureTimerTrigger r/n/a cron=\"0 * * * * * *\" enabled=true package_id=v2 code=v2.zip",
		"UpdateFunctionEventInvokeConfig r/n/b retry=0 msg_ttl=60",
	}
	assertBaselineOrder(t, interleaved, "r/n/a", "v2")
	probe := &testing.T{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		assertBaselineOrder(probe, interleaved, "r/n/b", "v2")
	}()
	<-done
	require.True(t, probe.Failed(), "函数 b 的定时器之后还有它自己的写请求，应当报错")
}

// 假腾讯云在函数不是 Active 时拒绝写请求，并记下被拒的那一条。
func TestBaselineSCFRejectsWritesWhileTheFunctionIsNotActive(t *testing.T) {
	ctx := context.Background()
	fake := newBaselineSCF()
	fake.PendingReads = 1
	_, err := fake.CreateFunction(ctx, tencentscf.CreateFunctionRequest{FunctionRef: baselineRef, Environment: map[string]string{}})
	require.NoError(t, err)
	_, err = fake.UpdateFunctionConfiguration(ctx, tencentscf.UpdateFunctionConfigurationRequest{FunctionRef: baselineRef})
	require.ErrorContains(t, err, "Creating")
	_, err = fake.EnsureTimerTrigger(ctx, tencentscf.TimerTriggerRequest{FunctionRef: baselineRef, Name: timerTriggerName, Cron: "0 * * * * * *"})
	require.ErrorContains(t, err, "Creating")
	require.Len(t, fake.RejectedWrites(), 2)
	info, err := fake.GetFunction(ctx, baselineRef)
	require.NoError(t, err)
	require.Equal(t, "Creating", info.Status)
}

func loadBaselineGolden(t *testing.T) map[string]baselinePath {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "scf_paths.golden.json"))
	require.NoError(t, err)
	var paths []baselinePath
	require.NoError(t, json.Unmarshal(raw, &paths))
	out := make(map[string]baselinePath, len(paths))
	for _, path := range paths {
		// 唯一允许的差别：新实现不再写创建预留号。
		for key, function := range path.Final.Functions {
			delete(function.Environment, "MOOX_CREATE_RESERVATION_ID")
			path.Final.Functions[key] = function
		}
		out[path.Name] = path
	}
	require.Len(t, out, 6)
	return out
}

func TestFunctionPathsMatchBaselineFinalState(t *testing.T) {
	ctx := context.Background()
	golden := loadBaselineGolden(t)
	check := func(name string, fake *baselineSCF, function tencentscf.FunctionRef, wantPackage string) {
		t.Helper()
		writes, final := fake.Snapshot()
		t.Logf("路径 %s 的写请求：\n%s", name, strings.Join(writes, "\n"))
		assertBaselineOrder(t, writes, baselineKey(function), wantPackage)
		require.Equal(t, golden[name].Final, final, "路径 %s 结束时云上的最终状态与基线不一致", name)
		require.Empty(t, fake.RejectedWrites(), "路径 %s 在函数不是 Active 时发了写请求", name)
		fake.ResetWrites()
	}

	// 路径一：建定时采集函数（过渡计划、初始停用的定时器）。提交的环境不带 CLS 项，由 CLS 补齐。
	fake := newBaselineSCF()
	fake.PendingReads = 1
	functions := newTestFunctions(t, fake)
	require.NoError(t, functions.Ensure(ctx, timerSpec(timerEnvironment(`{"v":1,"plan":"transition"}`, "hash-transition", "40", "scf-key-1"), "v1", Timer{Cron: "0 * * * * * *"})))
	check("create_timer_function", fake, baselineRef, baselineOldPackage)

	// 路径二：改环境与定时器。云上的函数是日志服务开通之前建的（没有 CLS 项），还留着已废弃的受管键和一项人工加的键，
	// 超时被改成了 30 秒；下发最终计划、轮换后的调用方密钥并启用定时器。
	fake.Mutate(baselineRef, func(function *baselineFunction) {
		for key := range function.Info.Environment {
			if strings.HasPrefix(key, "MOOX_CLS_") {
				delete(function.Info.Environment, key)
			}
		}
		function.Info.Environment["MOOX_MARKET_FETCH_DNS_ROUTES_JSON"] = `{"retired":true}`
		function.Info.Environment["MOOX_MARKET_FETCH_DNS_HASH"] = "retired-hash"
		function.Info.Environment["MOOX_MARKET_FETCH_DNS_UPDATED_AT"] = "2026-01-01T00:00:00Z"
		function.Info.Environment["OPERATOR_EXTRA"] = "kept"
		function.Info.Timeout = 30
	})
	require.NoError(t, functions.Ensure(ctx, timerSpec(timerEnvironment(`{"v":1,"plan":"final"}`, "hash-final", "42", "scf-key-2"), "v1", Timer{Cron: "5 * * * * * *", Enabled: true})))
	check("update_runtime_config", fake, baselineRef, baselineOldPackage)

	// 路径三：升级代码。只换代码，环境取云上读回的值。
	require.NoError(t, functions.UpgradeCode(ctx, timerRef, baselinePackage("v2")))
	check("upgrade_code", fake, baselineRef, baselineNewPackage)

	// 路径四：停用定时器。云上的函数又没有 CLS 项了：停用只改定时器，不碰环境。
	fake.Mutate(baselineRef, func(function *baselineFunction) {
		for key := range function.Info.Environment {
			if strings.HasPrefix(key, "MOOX_CLS_") {
				delete(function.Info.Environment, key)
			}
		}
	})
	require.NoError(t, functions.SetTimer(ctx, timerRef, Timer{Cron: "0 * * * * * *"}, baselineNewPackage))
	check("disable_timer", fake, baselineRef, baselineNewPackage)

	// 路径六（接在停用之后）：删函数。先删定时器，再删函数，并等它从云上消失。
	require.NoError(t, functions.Delete(ctx, timerRef))
	check("delete_function", fake, baselineRef, baselineNewPackage)

	// 路径五：建补采函数。没有定时器、没有采集计划，带预留并发（5 份内存）。
	backfillFake := newBaselineSCF()
	backfillFake.PendingReads = 1
	require.NoError(t, newTestFunctions(t, backfillFake).Ensure(ctx, backfillSpec()))
	check("create_backfill_function", backfillFake, baselineBackfillRef, baselineOldPackage)
}
