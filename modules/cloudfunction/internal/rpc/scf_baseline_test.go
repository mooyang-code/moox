package rpc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cloudfunction/internal/cloudcredential"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/clsprovision"
	tencentscf "github.com/mooyang-code/moox/modules/cloudfunction/internal/providers/tencentscf"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudfunction/proto/cloudfunctiongen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// 批 2 的基线金样（腾讯云客户端这一层）。
//
// 用记录式假腾讯云（baselineSCF）跑现有代码的六条路径：建定时采集函数、改运行配置（环境加定时器）、
// 升级代码、停用定时器、删函数、建补采函数。请求内容按 Collector 实际提交的形状构造
// （marketfetch 的 createItem、pushFunctions、upgradeCodeOf、DisableAll、EnsureBackfillFunction）。
// 每条路径落两样东西：结束时假腾讯云里的最终状态，以及写请求的先后顺序。
//
// 后续改造（批 2 的 T4、T5）比对的是最终状态加三条顺序约束（见 assertBaselineOrder），
// 请求次数的增减不算差别；允许的唯一差别是环境里不再有 MOOX_CREATE_RESERVATION_ID。
// 密钥类的值都是固定的假值。要重新生成时设 MOOX_UPDATE_GOLDEN=1。

const (
	baselineSpace      = "crypto"
	baselineOldPackage = "moox-collector_v1"
	baselineNewPackage = "moox-collector_v2"
	baselineTimerName  = "moox-fetch-plan-crypto-crypto-singapore-000"
	baselineBackfill   = "moox-fetch-plan-backfill-crypto-crypto-singapore"
)

var baselineRef = tencentscf.FunctionRef{Region: "ap-singapore", Namespace: "moox-collector", FunctionName: baselineTimerName}

var baselineBackfillRef = tencentscf.FunctionRef{Region: "ap-singapore", Namespace: "moox-collector", FunctionName: baselineBackfill}

// baselinePath 是金样文件里一条路径的记录。
type baselinePath struct {
	Name string `json:"name"`
	// Writes 是这条路径发给腾讯云的写请求，按先后顺序。
	Writes []string `json:"writes"`
	// Final 是这条路径结束时假腾讯云里的全部状态。
	Final baselineState `json:"final"`
}

// baselineCLSEnvironment 补齐日志服务（CLS）的六个环境项，值是固定的假值。
type baselineCLSEnvironment struct{}

func (baselineCLSEnvironment) Apply(_ context.Context, _ string, _ clsprovision.Credential, environment map[string]string) bool {
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

// baselineBaseEnvironment 是 Collector 的非受管环境（PlanDeploy.BaseEnvironment）。
func baselineBaseEnvironment() map[string]string {
	return map[string]string{
		"MOOX_SPACE_ID": baselineSpace,
		"MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON": `{"moox-collector":"` + strings.Repeat("a", 64) + `"}`,
	}
}

// baselineAccessEnvironment 是 Access 入口与调用方身份的五项，keyID 用来模拟密钥轮换。
func baselineAccessEnvironment(keyID string) map[string]string {
	return map[string]string{
		"MOOX_ACCESS_ADDRESS": "access.example:11004", "MOOX_ACCESS_ID": "access@control-a", "MOOX_CALLER": "scf-collector",
		"MOOX_CALLER_KEY_ID": keyID, "MOOX_CALLER_KEY": strings.Repeat("b", 64),
	}
}

// baselineManagedEnvironment 是 Collector 的受管环境（functionEnvironment 的输出形状）。
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

func baselineMetadata(t *testing.T, values map[string]any) *structpb.Struct {
	t.Helper()
	metadata, err := structpb.NewStruct(values)
	require.NoError(t, err)
	return metadata
}

func newBaselineService(t *testing.T, fake *baselineSCF) *Service {
	t.Helper()
	catalog := store.NewCatalogRepository(newInstanceSCFTestDB(t))
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	require.NoError(t, catalog.UpsertAccount(context.Background(), store.CloudAccount{AccountID: "account-a", Provider: "tencent", CredentialSecretID: "fake-secret", COSRegion: "ap-guangzhou", COSBucket: "fake-bucket", CreateTime: now}))
	for _, version := range []string{"v1", "v2"} {
		require.NoError(t, catalog.UpsertPackage(context.Background(), store.FunctionPackage{
			SpaceID: baselineSpace, PackageID: "moox-collector_" + version, PackageName: "collector", Version: version, Runtime: "CustomRuntime",
			PackageType: "collector", WorkloadType: "market_fetcher", CloudAccountID: "account-a", COSRegion: "ap-guangzhou", COSBucket: "fake-bucket",
			COSPath: "/packages/collector-" + version + ".zip", Status: "available", CreateTime: now,
		}))
	}
	svc := &Service{
		catalog:            catalog,
		credentialResolver: fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "fake-secret-id", SecretKey: "fake-secret-key"}},
		scfClientFactory:   func(cloudcredential.TencentCredential) scfProvisioner { return fake },
	}
	WithCLSEnvironment(baselineCLSEnvironment{})(svc)
	return svc
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

// assertBaselineOrder 检查写请求里属于 function（地域/命名空间/函数名）的那些满足三条顺序约束；
// 别的函数的请求穿插在中间不影响判断。
//  1. 换代码在先、包 ID 后写：UpdateFunctionCode 发出时环境里的包 ID 还不是 wantPackage，
//     之后才有一次改配置把它写成 wantPackage。
//  2. 定时器最后设：这个函数的 EnsureTimerTrigger 之后不再有它的别的写请求。
//  3. 代码包落后时不启用定时器：启用定时器的请求发出时，环境里的包 ID 必须等于 wantPackage。
//     现有代码（CloudFunction 这一层）没有这条保护：它只按请求设定时器，“落后的先升级”由 Collector 在上一层保证，
//     所以在这份基线的路径上这一条恒成立，是空检。它要到批 2 的 cloud 内部包（SetTimer 自己拒绝给落后的函数开定时器）
//     才有意义，留在这里是为了那时直接复用。
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

func TestSCFProviderPathsMatchBaseline(t *testing.T) {
	ctx := context.Background()
	var paths []baselinePath
	record := func(name string, fake *baselineSCF, function tencentscf.FunctionRef, wantPackage string) {
		writes, final := fake.snapshot()
		assertBaselineOrder(t, writes, baselineKey(function), wantPackage)
		paths = append(paths, baselinePath{Name: name, Writes: writes, Final: final})
		fake.resetWrites()
	}
	network := map[string]string{"vpc_id": "vpc-fake", "subnet_id": "subnet-fake", "public_net_status": "ENABLE"}

	// 路径一：建定时采集函数。云上没有这个函数；提交的环境不带 CLS 项，由 CLS 注入补齐。
	fake := newBaselineSCF()
	// 每次建函数、换代码、改配置之后，函数要被读一次才回到 Active：现有代码在这期间等待，不会在过渡状态下发写请求。
	fake.pendingReads = 1
	svc := newBaselineService(t, fake)
	createEnvironment := baselineManagedEnvironment(`{"v":1,"plan":"transition"}`, "hash-transition", "40", "scf-key-1")
	for key, value := range baselineBaseEnvironment() {
		createEnvironment[key] = value
	}
	createConfig := map[string]string{"timeout": "60", "memory_size": "64", "max_instance_concurrency": "1"}
	for key, value := range network {
		createConfig[key] = value
	}
	_, err := svc.executeCreateInstanceItem(ctx, baselineSpace, &pb.FunctionInstanceCreateItem{
		CloudAccountId: "account-a", FunctionType: "scf-event", TriggerType: "timer", Runtime: "Go1", Handler: "main",
		Namespace: baselineRef.Namespace, Region: baselineRef.Region, PackageId: baselineOldPackage, Environment: createEnvironment, Config: createConfig,
		Metadata: baselineMetadata(t, map[string]any{
			"function_name": baselineTimerName, "instance_id": baselineTimerName, "workload_type": "market_fetcher",
			"function_mode": "kline", "memory_size": 64, "timeout_seconds": 60,
		}),
		CreateReservationId: "0123456789abcdef0123456789abcdef",
	}, 0)
	require.NoError(t, err)
	record("create_timer_function", fake, baselineRef, baselineOldPackage)

	// 路径二：改运行配置（环境加定时器）。云上的函数是日志服务开通之前建的（没有 CLS 项），
	// 还留着已废弃的受管键和一项人工加的键，超时被改成了 30 秒；下发新的计划、轮换后的调用方密钥并启用定时器。
	fake.mutate(baselineRef, func(function *baselineFunction) {
		for key := range function.info.Environment {
			if strings.HasPrefix(key, "MOOX_CLS_") {
				delete(function.info.Environment, key)
			}
		}
		function.info.Environment["MOOX_MARKET_FETCH_DNS_ROUTES_JSON"] = `{"retired":true}`
		function.info.Environment["MOOX_MARKET_FETCH_DNS_HASH"] = "retired-hash"
		function.info.Environment["MOOX_MARKET_FETCH_DNS_UPDATED_AT"] = "2026-01-01T00:00:00Z"
		function.info.Environment["OPERATOR_EXTRA"] = "kept"
		// 超时被人改成了 30 秒：改运行配置时要改回 60 秒。
		function.info.Timeout = 30
	})
	patch := &pb.FunctionInstanceRuntimeConfigPatch{
		InstanceId: baselineTimerName, TimerEnabled: true, TimerCron: "5 * * * * * *",
		CollectorPublishLeaseId: "fake-lease", CollectorPublishFencingToken: 1,
		ManagedEnvironment: baselineManagedEnvironment(`{"v":1,"plan":"final"}`, "hash-final", "42", "scf-key-2"),
	}
	require.Nil(t, svc.preflightRuntimeConfig(ctx, baselineSpace, patch))
	_, err = svc.executeRuntimeConfigItem(ctx, baselineSpace, patch)
	require.NoError(t, err)
	record("update_runtime_config", fake, baselineRef, baselineOldPackage)

	// 路径三：升级代码。只带新代码包，不带环境与配置（Collector 的 SubmitDeployFunctionInstances 就是这样提交的）。
	_, err = svc.executeDeployInstanceItem(ctx, baselineSpace, &pb.FunctionInstanceDeployItem{InstanceId: baselineTimerName, PackageId: baselineNewPackage, OperationId: "baseline-deploy"})
	require.NoError(t, err)
	record("upgrade_code", fake, baselineRef, baselineNewPackage)

	// 路径四：停用定时器。不带受管环境，cron 用占位值（Collector 的 DisableAll 就是这样提交的）。
	// 云上的函数又没有 CLS 项了：停用必须在任何情况下都能完成，不顺带开通日志服务，环境保持原样。
	fake.mutate(baselineRef, func(function *baselineFunction) {
		for key := range function.info.Environment {
			if strings.HasPrefix(key, "MOOX_CLS_") {
				delete(function.info.Environment, key)
			}
		}
	})
	disable := &pb.FunctionInstanceRuntimeConfigPatch{InstanceId: baselineTimerName, TimerEnabled: false, TimerCron: "0 * * * * * *", CollectorPublishLeaseId: "fake-lease", CollectorPublishFencingToken: 1}
	require.Nil(t, svc.preflightRuntimeConfig(ctx, baselineSpace, disable))
	_, err = svc.executeRuntimeConfigItem(ctx, baselineSpace, disable)
	require.NoError(t, err)
	record("disable_timer", fake, baselineRef, baselineNewPackage)

	// 路径六（接在停用之后）：删函数。先删定时器，再删函数，并等它从云上消失。
	lifecycleID, err := svc.catalog.InstanceLifecycleID(ctx, baselineSpace, baselineTimerName)
	require.NoError(t, err)
	_, err = svc.executeDeleteInstanceItem(ctx, baselineSpace, &pb.FunctionInstanceDeleteItem{InstanceId: baselineTimerName, LifecycleId: lifecycleID, OperationId: "baseline-delete"})
	require.NoError(t, err)
	record("delete_function", fake, baselineRef, baselineNewPackage)

	// 路径五：建补采函数。没有定时器、没有采集计划，带预留并发（5 份内存）。
	backfillFake := newBaselineSCF()
	backfillFake.pendingReads = 1
	backfillSvc := newBaselineService(t, backfillFake)
	backfillEnvironment := map[string]string{"MOOX_MARKET_FETCH_MODE": "backfill", "MOOX_FETCH_TIMEOUT_SECONDS": "60"}
	for key, value := range baselineAccessEnvironment("scf-key-1") {
		backfillEnvironment[key] = value
	}
	for key, value := range baselineBaseEnvironment() {
		backfillEnvironment[key] = value
	}
	backfillConfig := map[string]string{"timeout": "60", "memory_size": "64", "max_instance_concurrency": "1", "reserved_concurrency_mem": "320"}
	for key, value := range network {
		backfillConfig[key] = value
	}
	_, err = backfillSvc.executeCreateInstanceItem(ctx, baselineSpace, &pb.FunctionInstanceCreateItem{
		CloudAccountId: "account-a", FunctionType: "scf-event", TriggerType: "invoke", Runtime: "Go1", Handler: "main",
		Namespace: baselineRef.Namespace, Region: baselineRef.Region, PackageId: baselineOldPackage, Environment: backfillEnvironment, Config: backfillConfig,
		Metadata: baselineMetadata(t, map[string]any{
			"function_name": baselineBackfill, "instance_id": baselineBackfill, "workload_type": "market_fetcher",
			"function_mode": "kline", "memory_size": 64, "timeout_seconds": 60, "access_fingerprint": "fake-fingerprint",
		}),
		CreateReservationId: "fedcba9876543210fedcba9876543210",
	}, 0)
	require.NoError(t, err)
	record("create_backfill_function", backfillFake, baselineBackfillRef, baselineOldPackage)

	require.Empty(t, fake.rejectedWrites(), "现有代码不会在函数不是 Active 时发写请求")
	require.Empty(t, backfillFake.rejectedWrites(), "现有代码不会在函数不是 Active 时发写请求")

	got, err := json.MarshalIndent(paths, "", "  ")
	require.NoError(t, err)
	got = append(got, '\n')
	path := filepath.Join("testdata", "scf_paths.golden.json")
	if os.Getenv("MOOX_UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, got, 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, string(want), string(got), "发给腾讯云的写请求或云上的最终状态与基线不一致")
}

// 假腾讯云在函数不是 Active 时拒绝写请求，并记下被拒的那一条。
func TestBaselineSCFRejectsWritesWhileTheFunctionIsNotActive(t *testing.T) {
	ctx := context.Background()
	fake := newBaselineSCF()
	fake.pendingReads = 1
	_, err := fake.CreateFunction(ctx, tencentscf.CreateFunctionRequest{FunctionRef: baselineRef, Environment: map[string]string{}})
	require.NoError(t, err)
	_, err = fake.UpdateFunctionConfiguration(ctx, tencentscf.UpdateFunctionConfigurationRequest{FunctionRef: baselineRef})
	require.ErrorContains(t, err, "Creating")
	_, err = fake.EnsureTimerTrigger(ctx, tencentscf.TimerTriggerRequest{FunctionRef: baselineRef, Name: timerTriggerName, Cron: "0 * * * * * *"})
	require.ErrorContains(t, err, "Creating")
	require.Len(t, fake.rejectedWrites(), 2)
	info, err := fake.GetFunction(ctx, baselineRef)
	require.NoError(t, err)
	require.Equal(t, "Creating", info.Status)
	_, err = fake.UpdateFunctionConfiguration(ctx, tencentscf.UpdateFunctionConfigurationRequest{FunctionRef: baselineRef, ClearNativeCLS: true})
	require.NoError(t, err)
	info, err = fake.GetFunction(ctx, baselineRef)
	require.NoError(t, err)
	require.Equal(t, "Updating", info.Status)
	// 清过一次自带的日志投递之后，不带清除标记的改配置不会把它恢复。
	_, err = fake.UpdateFunctionConfiguration(ctx, tencentscf.UpdateFunctionConfigurationRequest{FunctionRef: baselineRef})
	require.NoError(t, err)
	_, state := fake.snapshot()
	require.True(t, state.Functions[baselineKey(baselineRef)].NativeCLSCleared)
}

// 反例：这些请求在碰云端之前就被拒绝，一条写请求都不发。
//   - 带采集计划的函数环境里出现 EventBus 项（packages/cloudprovider/tencent 的 validateCollectorPlanEnvironment）：
//     采集函数不连 EventBus，这是真正起保护作用的校验。升级代码路径里“有 CA 文件就删 CA PEM”那一段，
//     对带计划的函数走不到（EventBus 项在这之前已经被拒）。
//   - 定时采集函数的超时不是 60 秒。
//   - 环境超过 4096 字节。
func TestSCFProviderRejectsInvalidRequestsBeforeAnyWrite(t *testing.T) {
	ctx := context.Background()
	validEnvironment := func() map[string]string {
		environment := baselineManagedEnvironment(`{"v":1,"plan":"transition"}`, "hash-transition", "40", "scf-key-1")
		for key, value := range baselineBaseEnvironment() {
			environment[key] = value
		}
		return environment
	}
	createItem := func(environment, config map[string]string) *pb.FunctionInstanceCreateItem {
		return &pb.FunctionInstanceCreateItem{
			CloudAccountId: "account-a", FunctionType: "scf-event", TriggerType: "timer", Runtime: "Go1", Handler: "main",
			Namespace: baselineRef.Namespace, Region: baselineRef.Region, PackageId: baselineOldPackage, Environment: environment, Config: config,
			Metadata: baselineMetadata(t, map[string]any{
				"function_name": baselineTimerName, "instance_id": baselineTimerName, "workload_type": "market_fetcher",
				"function_mode": "kline", "memory_size": 64, "timeout_seconds": 60,
			}),
			CreateReservationId: "0123456789abcdef0123456789abcdef",
		}
	}
	validConfig := func() map[string]string {
		return map[string]string{"timeout": "60", "memory_size": "64", "max_instance_concurrency": "1", "vpc_id": "vpc-fake", "subnet_id": "subnet-fake", "public_net_status": "ENABLE"}
	}
	for _, test := range []struct {
		name   string
		mutate func(environment, config map[string]string)
		want   string
	}{
		{"环境带 EventBus 项", func(environment, _ map[string]string) {
			environment["MOOX_EVENTBUS_NATS_URL"] = "tls://eventbus.example:4222"
		}, "MOOX_EVENTBUS_NATS_URL"},
		{"环境带 EventBus 的 CA PEM", func(environment, _ map[string]string) { environment["MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64"] = "ZmFrZQ==" }, "MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64"},
		{"外层超时不是 60", func(_, config map[string]string) { config["timeout"] = "30" }, "60"},
		{"环境里的超时不是 60", func(environment, _ map[string]string) { environment["MOOX_FETCH_TIMEOUT_SECONDS"] = "30" }, "60"},
		{"环境超长", func(environment, _ map[string]string) { environment["OPERATOR_EXTRA"] = strings.Repeat("x", 4096) }, "4096"},
	} {
		t.Run("创建/"+test.name, func(t *testing.T) {
			fake := newBaselineSCF()
			svc := newBaselineService(t, fake)
			environment, config := validEnvironment(), validConfig()
			test.mutate(environment, config)
			_, err := svc.executeCreateInstanceItem(ctx, baselineSpace, createItem(environment, config), 0)
			require.ErrorContains(t, err, test.want)
			writes, state := fake.snapshot()
			require.Empty(t, writes)
			require.Empty(t, state.Functions)
		})
	}

	// 改运行配置：云上的环境里有 EventBus 项、或合并后超长时，启用定时器的更新被拒绝，环境与定时器都不动。
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
	} {
		t.Run("改运行配置/"+test.name, func(t *testing.T) {
			fake := newBaselineSCF()
			svc := newBaselineService(t, fake)
			_, err := svc.executeCreateInstanceItem(ctx, baselineSpace, createItem(validEnvironment(), validConfig()), 0)
			require.NoError(t, err)
			fake.mutate(baselineRef, test.drift)
			fake.resetWrites()
			_, before := fake.snapshot()
			patch := &pb.FunctionInstanceRuntimeConfigPatch{
				InstanceId: baselineTimerName, TimerEnabled: true, TimerCron: "5 * * * * * *",
				CollectorPublishLeaseId: "fake-lease", CollectorPublishFencingToken: 1,
				ManagedEnvironment: baselineManagedEnvironment(`{"v":1,"plan":"final"}`, "hash-final", "42", "scf-key-2"),
			}
			require.Nil(t, svc.preflightRuntimeConfig(ctx, baselineSpace, patch))
			_, err = svc.executeRuntimeConfigItem(ctx, baselineSpace, patch)
			require.ErrorContains(t, err, test.want)
			writes, after := fake.snapshot()
			require.Empty(t, writes)
			require.Equal(t, before, after)
		})
	}

	// 改运行配置的预检：超时不是 60、受管环境里出现不归 Collector 管的键，提交时就被拒绝。
	svc := newBaselineService(t, newBaselineSCF())
	_, err := svc.executeCreateInstanceItem(ctx, baselineSpace, createItem(validEnvironment(), validConfig()), 0)
	require.NoError(t, err)
	shortTimeout := baselineManagedEnvironment(`{"v":1,"plan":"final"}`, "hash-final", "42", "scf-key-2")
	shortTimeout["MOOX_FETCH_TIMEOUT_SECONDS"] = "30"
	unmanaged := baselineManagedEnvironment(`{"v":1,"plan":"final"}`, "hash-final", "42", "scf-key-2")
	unmanaged["MOOX_EVENTBUS_NATS_URL"] = "tls://eventbus.example:4222"
	for name, managed := range map[string]map[string]string{"超时不是 60": shortTimeout, "不归 Collector 管的键": unmanaged} {
		ret := svc.preflightRuntimeConfig(ctx, baselineSpace, &pb.FunctionInstanceRuntimeConfigPatch{
			InstanceId: baselineTimerName, TimerEnabled: true, TimerCron: "5 * * * * * *",
			CollectorPublishLeaseId: "fake-lease", CollectorPublishFencingToken: 1, ManagedEnvironment: managed,
		})
		require.NotNil(t, ret, name)
	}
}
