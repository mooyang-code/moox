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
// 用记录式假腾讯云（baselineSCF）跑现有代码的五条路径：建定时采集函数、改运行配置（环境加定时器）、
// 升级代码、停用定时器、建补采函数。请求内容按 Collector 实际提交的形状构造
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

// assertBaselineOrder 检查一条路径的写请求顺序满足三条约束：
//  1. 换代码在先、包 ID 后写：UpdateFunctionCode 发出时环境里的包 ID 还不是 wantPackage，
//     之后才有一次改配置把它写成 wantPackage。
//  2. 定时器最后设：EnsureTimerTrigger 之后不再有别的写请求。
//  3. 代码包落后时不启用定时器：启用定时器的请求发出时，环境里的包 ID 必须等于 wantPackage。
func assertBaselineOrder(t *testing.T, writes []string, wantPackage string) {
	t.Helper()
	if code := baselineIndex(writes, "UpdateFunctionCode "); code >= 0 {
		require.NotContains(t, writes[code], "package_id="+wantPackage+" ", "换代码时环境里的包 ID 不能已经是新包")
		marked := -1
		for index := code + 1; index < len(writes); index++ {
			if strings.HasPrefix(writes[index], "UpdateFunctionConfiguration ") && strings.Contains(writes[index], "package_id="+wantPackage+" ") {
				marked = index
				break
			}
		}
		require.Greater(t, marked, code, "包 ID 必须在换代码之后写入")
	}
	for index, write := range writes {
		if !strings.HasPrefix(write, "EnsureTimerTrigger ") {
			continue
		}
		require.Equal(t, len(writes)-1, index, "定时器必须是最后一个写请求")
		if strings.Contains(write, "enabled=true") {
			require.Contains(t, write, "package_id="+wantPackage+" ", "代码包落后时不能启用定时器")
		}
	}
}

func TestSCFProviderPathsMatchBaseline(t *testing.T) {
	ctx := context.Background()
	var paths []baselinePath
	record := func(name string, fake *baselineSCF, wantPackage string) {
		writes, final := fake.snapshot()
		assertBaselineOrder(t, writes, wantPackage)
		paths = append(paths, baselinePath{Name: name, Writes: writes, Final: final})
		fake.resetWrites()
	}
	network := map[string]string{"vpc_id": "vpc-fake", "subnet_id": "subnet-fake", "public_net_status": "ENABLE"}

	// 路径一：建定时采集函数。云上没有这个函数；提交的环境不带 CLS 项，由 CLS 注入补齐。
	fake := newBaselineSCF()
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
	record("create_timer_function", fake, baselineOldPackage)

	// 路径二：改运行配置（环境加定时器）。云上的函数是日志服务开通之前建的（没有 CLS 项），
	// 还留着已废弃的受管键和一项人工加的键；下发新的计划、轮换后的调用方密钥并启用定时器。
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
	})
	patch := &pb.FunctionInstanceRuntimeConfigPatch{
		InstanceId: baselineTimerName, TimerEnabled: true, TimerCron: "5 * * * * * *",
		CollectorPublishLeaseId: "fake-lease", CollectorPublishFencingToken: 1,
		ManagedEnvironment: baselineManagedEnvironment(`{"v":1,"plan":"final"}`, "hash-final", "42", "scf-key-2"),
	}
	require.Nil(t, svc.preflightRuntimeConfig(ctx, baselineSpace, patch))
	_, err = svc.executeRuntimeConfigItem(ctx, baselineSpace, patch)
	require.NoError(t, err)
	record("update_runtime_config", fake, baselineOldPackage)

	// 路径三：升级代码。只带新代码包，不带环境与配置（Collector 的 SubmitDeployFunctionInstances 就是这样提交的）。
	_, err = svc.executeDeployInstanceItem(ctx, baselineSpace, &pb.FunctionInstanceDeployItem{InstanceId: baselineTimerName, PackageId: baselineNewPackage, OperationId: "baseline-deploy"})
	require.NoError(t, err)
	record("upgrade_code", fake, baselineNewPackage)

	// 路径四：停用定时器。不带受管环境，cron 用占位值（Collector 的 DisableAll 就是这样提交的）。
	disable := &pb.FunctionInstanceRuntimeConfigPatch{InstanceId: baselineTimerName, TimerEnabled: false, TimerCron: "0 * * * * * *", CollectorPublishLeaseId: "fake-lease", CollectorPublishFencingToken: 1}
	require.Nil(t, svc.preflightRuntimeConfig(ctx, baselineSpace, disable))
	_, err = svc.executeRuntimeConfigItem(ctx, baselineSpace, disable)
	require.NoError(t, err)
	record("disable_timer", fake, baselineNewPackage)

	// 路径五：建补采函数。没有定时器、没有采集计划，带预留并发（5 份内存）。
	backfillFake := newBaselineSCF()
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
	record("create_backfill_function", backfillFake, baselineOldPackage)

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
