package fetchplan

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/cloud"
	"github.com/mooyang-code/moox/modules/collector/internal/cloud/cloudtest"
	"github.com/mooyang-code/moox/modules/collector/internal/cloud/tencentscf"
	"github.com/stretchr/testify/require"
)

// 用 T0 的记录式假腾讯云跑对账器的五条路径（建函数、改配置、升级、停用、建补采函数），每条路径结束时云上的最终状态
// 与腾讯云客户端这一层的金样（cloud/testdata/scf_paths.golden.json，原件在 CloudFunction 模块）比对，并断言三条顺序约束。
//
// 金样是直接调 CloudFunction 录的，环境里的采集计划是手写的占位（{"v":1,"plan":"transition"} 之类）；对账器下发的是真实计划。
// 所以比对前把由计划决定的三项（采集计划、分配哈希、标的数）换成占位，描述里的函数名、定时器的 cron 也换成占位，
// 其余（基础项、Access、CLS、代码包、配置、异步重试、预留并发、定时器的开关与 Qualifier、Message、命名空间）逐项相等。
// 允许的差别只有两处：环境里没有 MOOX_CREATE_RESERVATION_ID（预留机制已删除）；建函数这条路径结束时定时器是启用的——
// 旧对账器在同一轮里紧跟建函数下发运行配置启用定时器（见 plan_dispatch.golden.json 里同一时刻的 create 与 runtime_config），
// 新实现把两步合成一次 Ensure。

// cloudGoldenPath 是金样文件里的一条路径。
type cloudGoldenPath struct {
	Name   string          `json:"name"`
	Writes []string        `json:"writes"`
	Final  cloudtest.State `json:"final"`
}

func loadCloudGolden(t *testing.T) map[string]cloudGoldenPath {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "cloud", "testdata", "scf_paths.golden.json"))
	require.NoError(t, err)
	var paths []cloudGoldenPath
	require.NoError(t, json.Unmarshal(raw, &paths))
	out := map[string]cloudGoldenPath{}
	for _, path := range paths {
		out[path.Name] = path
	}
	return out
}

// normalizeCloudFunction 把由计划决定的值与函数名换成占位，去掉创建预留号。
func normalizeCloudFunction(function cloudtest.FunctionSnapshot) cloudtest.FunctionSnapshot {
	environment := map[string]string{}
	for key, value := range function.Environment {
		environment[key] = value
	}
	delete(environment, "MOOX_CREATE_RESERVATION_ID")
	for _, key := range []string{"MOOX_MARKET_FETCH_PLAN", "MOOX_MARKET_FETCH_ASSIGNMENT_HASH", "MOOX_MARKET_FETCH_SUBJECT_COUNT"} {
		if _, ok := environment[key]; ok {
			environment[key] = "<由计划决定>"
		}
	}
	function.Environment = environment
	function.Description = "MooX cloud function instance <函数名>"
	if function.Timer != nil {
		timer := *function.Timer
		timer.Cron = "<cron>"
		function.Timer = &timer
	}
	return function
}

// onlyFunction 返回状态里唯一的那个函数。
func onlyFunction(t *testing.T, state cloudtest.State) cloudtest.FunctionSnapshot {
	t.Helper()
	require.Len(t, state.Functions, 1)
	for _, function := range state.Functions {
		return function
	}
	return cloudtest.FunctionSnapshot{}
}

// assertCloudOrder 检查属于 function 的写请求满足三条顺序约束（与 cloud 包金样测试的定义相同）：
// 换代码在先、包 ID 后写；定时器是这个函数的最后一个写请求；启用定时器时包 ID 已是 wantPackage。
func assertCloudOrder(t *testing.T, writes []string, function, wantPackage string) {
	t.Helper()
	var own []string
	for _, write := range writes {
		if fields := strings.Fields(write); len(fields) >= 2 && fields[1] == function {
			own = append(own, write)
		}
	}
	require.NotEmpty(t, own, "函数 %s 没有写请求", function)
	code := -1
	for index, write := range own {
		if strings.HasPrefix(write, "UpdateFunctionCode ") {
			code = index
			break
		}
	}
	if code >= 0 {
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

// newGoldenHarness 按金样的环境配置对账器：新加坡地域、命名空间 moox-collector、VPC vpc-fake、Access 与基础项取金样里的值，
// 代码包 moox-collector_v1（COS 对象 packages/collector-v1.zip）。
func newGoldenHarness(t *testing.T) (*planHarness, *PlanReconciler, *string) {
	h := newPlanHarness(t, 40)
	h.scf.PendingReads = 1
	h.cloud.packages = []cloud.Package{goldenPackage("v1")}
	keyID := "scf-key-1"
	r := h.reconciler()
	r.Options.Markets = map[string][]string{"spot": {"ap-singapore"}}
	r.Deploy.Namespace = "moox-collector"
	r.Deploy.Networks = map[string]PlanNetwork{"ap-singapore": {VPCID: "vpc-fake", SubnetID: "subnet-fake"}}
	r.Deploy.BaseEnvironment = map[string]string{"MOOX_SPACE_ID": "crypto", "MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON": `{"moox-collector":"` + strings.Repeat("a", 64) + `"}`}
	r.Access = func(string) (map[string]string, error) {
		return map[string]string{
			"MOOX_ACCESS_ADDRESS": "access.example:11004", "MOOX_ACCESS_ID": "access@control-a", "MOOX_CALLER": "scf-collector",
			"MOOX_CALLER_KEY_ID": keyID, "MOOX_CALLER_KEY": strings.Repeat("b", 64),
		}, nil
	}
	return h, r, &keyID
}

// scfRefIn 返回函数在假腾讯云里的位置（指定地域与命名空间）。
func scfRefIn(name, region, namespace string) tencentscf.FunctionRef {
	return tencentscf.FunctionRef{Region: region, Namespace: namespace, FunctionName: name}
}

func goldenPackage(version string) cloud.Package {
	pkg := testPackage("moox-collector_" + version)
	pkg.COSPath = "/packages/collector-" + version + ".zip"
	return pkg
}

func TestReconcilerPathsMatchTheCloudBaseline(t *testing.T) {
	golden := loadCloudGolden(t)
	compare := func(name string, h *planHarness, functionKey, wantPackage string, adjust func(*cloudtest.FunctionSnapshot)) {
		t.Helper()
		writes, final := h.scf.Snapshot()
		t.Logf("路径 %s 的写请求：\n%s", name, strings.Join(writes, "\n"))
		assertCloudOrder(t, writes, functionKey, wantPackage)
		require.Empty(t, h.scf.RejectedWrites(), "路径 %s 在函数不是 Active 时发了写请求", name)
		want := normalizeCloudFunction(onlyFunction(t, golden[name].Final))
		if adjust != nil {
			adjust(&want)
		}
		require.Equal(t, golden[name].Final.Namespaces, final.Namespaces, name)
		require.Equal(t, want, normalizeCloudFunction(onlyFunction(t, final)), "路径 %s 结束时云上的最终状态与金样不一致", name)
		h.scf.ResetWrites()
	}

	h, r, keyID := newGoldenHarness(t)
	name := PlanFunctionName("crypto", "spot", "ap-singapore", 0)
	key := "ap-singapore/moox-collector/" + name

	// 路径一：建定时采集函数（过渡计划）。建函数与下发环境、定时器合成一次 Ensure，所以定时器结束时是启用的。
	require.NoError(t, h.reconcile(r))
	compare("create_timer_function", h, key, "moox-collector_v1", func(want *cloudtest.FunctionSnapshot) { want.Timer.Enabled = true })
	change := h.content(h.open()).Datasets["ds_spot|1m"]
	h.now = time.Unix(change.P, 0).Add(time.Minute)
	require.NoError(t, h.reconcile(r))
	require.Nil(t, h.open())
	h.scf.ResetWrites()

	// 路径二：改环境与配置。云上的函数是日志服务开通之前建的（没有 CLS 项），还留着已废弃的受管键和一项人工加的键，
	// 超时被改成了 30 秒；调用方密钥轮换后，下一次读回发现 Access 不一致，按已生效计划重推。
	h.scf.Mutate(scfRefIn(name, "ap-singapore", "moox-collector"), func(function *cloudtest.Function) {
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
	*keyID = "scf-key-2"
	h.now = h.now.Add(2 * time.Hour)
	require.NoError(t, h.reconcile(r))
	h.waitOps(r)
	compare("update_runtime_config", h, key, "moox-collector_v1", nil)

	// 路径三：升级代码。只换代码，环境取云上读回的值，不动定时器。
	h.cloud.setPackagesTo(goldenPackage("v1"), goldenPackage("v2"))
	h.now = h.now.Add(planUpgradeCheckInterval)
	r.lastVerified = h.now
	require.NoError(t, h.reconcile(r))
	h.waitOps(r)
	compare("upgrade_code", h, key, "moox-collector_v2", nil)

	// 路径四：停用定时器（暂停）。云上的函数又没有 CLS 项了：停用只改定时器，不碰环境。
	h.scf.Mutate(scfRefIn(name, "ap-singapore", "moox-collector"), func(function *cloudtest.Function) {
		for key := range function.Info.Environment {
			if strings.HasPrefix(key, "MOOX_CLS_") {
				delete(function.Info.Environment, key)
			}
		}
	})
	done, err := r.DisableAll(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	compare("disable_timer", h, key, "moox-collector_v2", nil)

	// 路径五：建补采函数。没有定时器、没有采集计划，带预留并发（5 份内存）。
	backfillHarness, backfillReconciler, _ := newGoldenHarness(t)
	_, err = backfillReconciler.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)
	compare("create_backfill_function", backfillHarness, "ap-singapore/moox-collector/"+BackfillFunctionName("crypto", "spot", "ap-singapore"), "moox-collector_v1", nil)
}
