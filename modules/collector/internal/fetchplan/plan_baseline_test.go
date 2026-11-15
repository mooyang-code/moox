package fetchplan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	cloudfunctionpb "github.com/mooyang-code/moox/modules/cloudfunction/proto/cloudfunctiongen"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

// 批 2 的基线金样（Collector 这一层）。
//
// 对已有的几个对账场景（单市场、标的变更、退役、交接、统一数据集），把每份采集计划里每个函数的
// 过渡与最终内容落成文件：云函数记 functionEnvironment 的输出（其中 MOOX_MARKET_FETCH_PLAN 就是下发给函数的
// Plan JSON，函数严格解码，一个字节都不能变）与两个 cron；推送与本地执行项没有函数环境，记 Plan JSON。
// 后续改造（拆包、采集方式收成一列、时间线拆表、改用 cloud 包）之后必须原样通过。
// 调用方密钥是固定的假值。要重新生成时设 MOOX_UPDATE_GOLDEN=1。

// planBaselineFunction 是一份计划里一个函数的记录。
type planBaselineFunction struct {
	Name     string `json:"name"`
	Region   string `json:"region,omitempty"`
	Executor string `json:"executor"`
	Push     bool   `json:"push"`
	Create   bool   `json:"create"`
	Retired  bool   `json:"retired"`
	// 云函数：过渡与最终阶段的 cron 与受管环境。退役的函数没有最终环境。
	TransitionalCron        string            `json:"transitional_cron,omitempty"`
	FinalCron               string            `json:"final_cron,omitempty"`
	TransitionalEnvironment map[string]string `json:"transitional_environment,omitempty"`
	FinalEnvironment        map[string]string `json:"final_environment,omitempty"`
	// 推送与本地执行项：过渡与最终阶段的 Plan JSON。
	TransitionalPlan string `json:"transitional_plan,omitempty"`
	FinalPlan        string `json:"final_plan,omitempty"`
}

// planBaselineScenario 是一个场景里先后产生的采集计划。
type planBaselineScenario struct {
	Name  string                   `json:"name"`
	Plans [][]planBaselineFunction `json:"plans"`
}

// planBaselineDump 导出一份计划内容里全部函数的过渡与最终内容。
func planBaselineDump(t *testing.T, r *PlanReconciler, content *FetchPlanContent) []planBaselineFunction {
	t.Helper()
	var out []planBaselineFunction
	for _, name := range content.SortedFunctionNames() {
		function := content.Functions[name]
		executor := function.Executor
		if function.IsSCF() {
			executor = marketfetch.PlanExecutorSCF
		}
		item := planBaselineFunction{Name: name, Region: function.Region, Executor: executor, Push: function.Push, Create: function.Create, Retired: function.New == nil}
		transitional, final := content.TransitionalPlan(function), content.FinalPlan(function)
		if function.IsSCF() {
			item.TransitionalCron, item.FinalCron = content.TransitionalCron(function), content.FinalCron(function)
			if transitional != nil {
				environment, err := r.functionEnvironment(function.Region, transitional)
				require.NoError(t, err, name)
				item.TransitionalEnvironment = environment
			}
			if final != nil {
				environment, err := r.functionEnvironment(function.Region, final)
				require.NoError(t, err, name)
				item.FinalEnvironment = environment
			}
		} else {
			if transitional != nil {
				encoded, err := marketfetch.EncodePlan(transitional)
				require.NoError(t, err, name)
				item.TransitionalPlan = encoded
			}
			if final != nil {
				encoded, err := marketfetch.EncodePlan(final)
				require.NoError(t, err, name)
				item.FinalPlan = encoded
			}
		}
		out = append(out, item)
	}
	return out
}

// planBaselineRecords 导出计划库里按创建顺序的全部计划。
func planBaselineRecords(t *testing.T, r *PlanReconciler, records []*store.FetchPlanRecord) [][]planBaselineFunction {
	t.Helper()
	var plans [][]planBaselineFunction
	for _, record := range records {
		content, err := decodeFetchPlanContent(record.Plan)
		require.NoError(t, err)
		plans = append(plans, planBaselineDump(t, r, content))
	}
	return plans
}

// planDispatch 是对账器实际提交给云端的一批请求（发布租约的两项不记：它们只是通行证，不进函数）。
type planDispatch struct {
	// At 是提交时刻（测试里的假时钟，UTC）。
	At   string `json:"at"`
	Kind string `json:"kind"` // create、runtime_config、deploy、delete
	// Creates 是创建项，RuntimeConfigs 是运行配置补丁，各自按函数名排序（同一批里的先后没有含义）。
	Creates        []planDispatchCreate `json:"creates,omitempty"`
	RuntimeConfigs []planDispatchPatch  `json:"runtime_configs,omitempty"`
	// Functions 与 PackageID 是升级代码、删除函数的对象。
	Functions []string `json:"functions,omitempty"`
	PackageID string   `json:"package_id,omitempty"`
}

type planDispatchCreate struct {
	Name                string            `json:"name"`
	CloudAccountID      string            `json:"cloud_account_id"`
	FunctionType        string            `json:"function_type"`
	TriggerType         string            `json:"trigger_type"`
	Runtime             string            `json:"runtime"`
	Handler             string            `json:"handler"`
	Namespace           string            `json:"namespace"`
	Region              string            `json:"region"`
	PackageID           string            `json:"package_id"`
	Environment         map[string]string `json:"environment"`
	Config              map[string]string `json:"config"`
	Metadata            map[string]any    `json:"metadata"`
	CreateReservationID string            `json:"create_reservation_id"`
}

type planDispatchPatch struct {
	InstanceID         string            `json:"instance_id"`
	ManagedEnvironment map[string]string `json:"managed_environment"`
	TimerEnabled       bool              `json:"timer_enabled"`
	TimerCron          string            `json:"timer_cron"`
}

// planDispatchEnvironment 拷贝一份环境，把采集计划换成它的 SHA-256：计划的原文在 plan_environment.golden.json 里
// 逐字节记着，这里只要能看出每个阶段下发的是哪一份。
func planDispatchEnvironment(environment map[string]string) map[string]string {
	out := make(map[string]string, len(environment))
	for key, value := range environment {
		if key == marketfetch.PlanEnvName {
			sum := sha256.Sum256([]byte(value))
			value = "sha256:" + hex.EncodeToString(sum[:])
		}
		out[key] = value
	}
	return out
}

func planDispatchCreates(items []*cloudfunctionpb.FunctionInstanceCreateItem) planDispatch {
	dispatch := planDispatch{Kind: "create"}
	for _, item := range items {
		metadata := item.GetMetadata().AsMap()
		name, _ := metadata["function_name"].(string)
		config := make(map[string]string, len(item.GetConfig()))
		for key, value := range item.GetConfig() {
			config[key] = value
		}
		dispatch.Creates = append(dispatch.Creates, planDispatchCreate{
			Name: name, CloudAccountID: item.GetCloudAccountId(), FunctionType: item.GetFunctionType(), TriggerType: item.GetTriggerType(),
			Runtime: item.GetRuntime(), Handler: item.GetHandler(), Namespace: item.GetNamespace(), Region: item.GetRegion(), PackageID: item.GetPackageId(),
			Environment: planDispatchEnvironment(item.GetEnvironment()), Config: config, Metadata: metadata, CreateReservationID: item.GetCreateReservationId(),
		})
	}
	sort.Slice(dispatch.Creates, func(i, j int) bool { return dispatch.Creates[i].Name < dispatch.Creates[j].Name })
	return dispatch
}

func planDispatchPatches(patches []*cloudfunctionpb.FunctionInstanceRuntimeConfigPatch) planDispatch {
	dispatch := planDispatch{Kind: "runtime_config"}
	for _, patch := range patches {
		dispatch.RuntimeConfigs = append(dispatch.RuntimeConfigs, planDispatchPatch{
			InstanceID: patch.GetInstanceId(), ManagedEnvironment: planDispatchEnvironment(patch.GetManagedEnvironment()),
			TimerEnabled: patch.GetTimerEnabled(), TimerCron: patch.GetTimerCron(),
		})
	}
	sort.Slice(dispatch.RuntimeConfigs, func(i, j int) bool {
		return dispatch.RuntimeConfigs[i].InstanceID < dispatch.RuntimeConfigs[j].InstanceID
	})
	return dispatch
}

// recordDispatch 记下一批提交。
func (c *fakePlanCloud) recordDispatch(dispatch planDispatch) {
	if c.clock != nil {
		dispatch.At = c.clock().UTC().Format(time.RFC3339)
	}
	sort.Strings(dispatch.Functions)
	c.patchMu.Lock()
	c.dispatches = append(c.dispatches, dispatch)
	c.patchMu.Unlock()
}

// planDispatchScenario 是一个场景里对账器先后提交给云端的全部请求。
type planDispatchScenario struct {
	Name       string         `json:"name"`
	Dispatches []planDispatch `json:"dispatches"`
}

// planBaselineCompare 把结果与金样文件比对；设了 MOOX_UPDATE_GOLDEN=1 时重写金样。
func planBaselineCompare(t *testing.T, file string, value any, message string) {
	t.Helper()
	got, err := json.MarshalIndent(value, "", " ")
	require.NoError(t, err)
	got = append(got, '\n')
	path := filepath.Join("testdata", file)
	if os.Getenv("MOOX_UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.WriteFile(path, got, 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, string(want), string(got), message)
}

// planBaselineSettle 推进时间直到没有未生效的计划。
func planBaselineSettle(t *testing.T, h *planHarness, r *PlanReconciler) {
	t.Helper()
	for i := 0; i < 400; i++ {
		require.NoError(t, h.reconcile(r))
		if h.open() == nil {
			return
		}
		h.now = h.now.Add(20 * time.Second)
	}
	t.Fatal("计划没有走到已生效")
}

func TestPlanEnvironmentsMatchBaseline(t *testing.T) {
	var scenarios []planBaselineScenario
	var dispatched []planDispatchScenario

	// 单市场：一个现货 1m 任务，120 个标的分成 3 个函数。之后标的减少一个（标的变更，过渡计划带新旧两份）。
	{
		h := newPlanHarness(t, 120)
		r := h.reconciler()
		planBaselineSettle(t, h, r)
		r.lastVerified = h.now
		h.setSubjects(119)
		planBaselineSettle(t, h, r)
		scenarios = append(scenarios, planBaselineScenario{Name: "single_market_then_subject_change", Plans: planBaselineRecords(t, r, h.store.records)})
		dispatched = append(dispatched, planDispatchScenario{Name: "single_market_then_subject_change", Dispatches: h.cloud.dispatches})
	}

	// 退役：标的从 120 个减到 40 个，后两个函数退役（保持过渡环境、只停定时器）。
	{
		h := newPlanHarness(t, 120)
		r := h.reconciler()
		planBaselineSettle(t, h, r)
		r.lastVerified = h.now
		h.setSubjects(40)
		planBaselineSettle(t, h, r)
		scenarios = append(scenarios, planBaselineScenario{Name: "retire_functions", Plans: planBaselineRecords(t, r, h.store.records)})
		dispatched = append(dispatched, planDispatchScenario{Name: "retire_functions", Dispatches: h.cloud.dispatches})
	}

	// 交接：1m 与 1h 两个数据集先由云函数采集，再切到推送，再切回云函数。
	{
		h := newIngestHarness(t, modePullSCF, "1m", "1h")
		h.settle(true)
		h.r.lastVerified = h.now
		h.modes.set("ds_spot_1m", IngestStreaming, marketfetch.PlanExecutorLocal, false)
		h.modes.set("ds_spot_1h", IngestStreaming, marketfetch.PlanExecutorLocal, false)
		h.settle(true)
		h.r.lastVerified = h.now
		h.modes.set("ds_spot_1m", IngestPull, marketfetch.PlanExecutorSCF, false)
		h.modes.set("ds_spot_1h", IngestPull, marketfetch.PlanExecutorSCF, false)
		h.settle(true)
		scenarios = append(scenarios, planBaselineScenario{Name: "handoff_scf_stream_scf", Plans: planBaselineRecords(t, h.r, h.store.records)})
		dispatched = append(dispatched, planDispatchScenario{Name: "handoff_scf_stream_scf", Dispatches: h.cloud.dispatches})
	}

	// 交接：1m 数据集先由云函数采集，再切到本地执行端（拉取方式不变，只换执行端）。
	{
		h := newIngestHarness(t, modePullSCF, "1m")
		h.settle(true)
		h.r.lastVerified = h.now
		h.modes.set("ds_spot_1m", IngestPull, marketfetch.PlanExecutorLocal, false)
		h.settle(true)
		require.Equal(t, marketfetch.PlanExecutorLocal, h.applied().Functions[localItem].Executor)
		scenarios = append(scenarios, planBaselineScenario{Name: "handoff_scf_local", Plans: planBaselineRecords(t, h.r, h.store.records)})
		dispatched = append(dispatched, planDispatchScenario{Name: "handoff_scf_local", Dispatches: h.cloud.dispatches})
	}

	// 统一数据集：同一个数据集挂现货、永续两个任务，两个市场的函数共用一份快照；之后标的减少一个。
	{
		h, r := newPlanBaselineUnified(t)
		planBaselineSettle(t, h, r)
		r.lastVerified = h.now
		h.setSubjects(59)
		planBaselineSettle(t, h, r)
		scenarios = append(scenarios, planBaselineScenario{Name: "unified_dataset_then_subject_change", Plans: planBaselineRecords(t, r, h.store.records)})
		dispatched = append(dispatched, planDispatchScenario{Name: "unified_dataset_then_subject_change", Dispatches: h.cloud.dispatches})
	}

	for _, scenario := range scenarios {
		require.NotEmpty(t, scenario.Plans, scenario.Name)
		for _, plan := range scenario.Plans {
			require.NotEmpty(t, plan, scenario.Name)
		}
	}
	// 暂停：计划生效后停用全部定时器（DisableAll 的补丁不带受管环境，cron 用占位值）。
	{
		h := newPlanHarness(t, 60)
		r := h.reconciler()
		planBaselineSettle(t, h, r)
		h.cloud.dispatches = nil
		done, err := r.DisableAll(context.Background())
		require.NoError(t, err)
		require.True(t, done)
		dispatched = append(dispatched, planDispatchScenario{Name: "disable_all", Dispatches: h.cloud.dispatches})
	}

	// 升级代码：计划生效后换了代码包，落后的函数只提交换代码（不带环境、不动定时器）。
	{
		h := newPlanHarness(t, 60)
		r := h.reconciler()
		planBaselineSettle(t, h, r)
		for name, patch := range h.cloud.patches {
			h.cloud.nodes[name] = scfinvoker.FunctionInstance{InstanceID: name, PackageID: "pkg-old", Metadata: map[string]any{
				"assignment_hash": patch.GetManagedEnvironment()["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"], "timer_actual_enabled": true,
				"access_address": patch.GetManagedEnvironment()["MOOX_ACCESS_ADDRESS"], "access_id": patch.GetManagedEnvironment()["MOOX_ACCESS_ID"], "access_key_id": patch.GetManagedEnvironment()["MOOX_CALLER_KEY_ID"],
				"timer_available_status": "Available", "timer_actual_cron": patch.GetTimerCron(), "timer_actual_qualifier": timerTriggerQualifier, "timer_actual_message": timerTriggerMessage,
			}}
		}
		h.cloud.dispatches = nil
		r.Deploy.PackageID = "pkg-new"
		h.now = h.now.Add(2 * time.Hour)
		require.NoError(t, h.reconcile(r))
		dispatched = append(dispatched, planDispatchScenario{Name: "upgrade_code", Dispatches: h.cloud.dispatches})
	}

	// 补采函数：先创建（不带定时器与采集计划，预留并发 5 份内存），换代码包后升级，调用方密钥轮换后删掉重建。
	{
		h := newPlanHarness(t, 60)
		r := h.reconciler()
		_, err := r.EnsureBackfillFunction(context.Background(), "spot")
		require.NoError(t, err)
		r.Deploy.PackageID = "pkg-new"
		h.now = h.now.Add(time.Minute)
		_, err = r.EnsureBackfillFunction(context.Background(), "spot")
		require.NoError(t, err)
		r.Access = func(string) (map[string]string, error) {
			access, _ := testSCFAccessEnvironment("")
			access["MOOX_CALLER_KEY_ID"] = "assigned-scf-key-18"
			return access, nil
		}
		h.now = h.now.Add(time.Minute)
		_, err = r.EnsureBackfillFunction(context.Background(), "spot")
		require.NoError(t, err)
		dispatched = append(dispatched, planDispatchScenario{Name: "backfill_function", Dispatches: h.cloud.dispatches})
	}

	for _, scenario := range scenarios {
		require.NotEmpty(t, scenario.Plans, scenario.Name)
		for _, plan := range scenario.Plans {
			require.NotEmpty(t, plan, scenario.Name)
		}
	}
	for _, scenario := range dispatched {
		require.NotEmpty(t, scenario.Dispatches, scenario.Name)
	}
	planBaselineCompare(t, "plan_environment.golden.json", scenarios, "函数环境、过渡或最终计划、cron 与基线不一致")
	planBaselineCompare(t, "plan_dispatch.golden.json", dispatched, "对账器实际提交给云端的请求（创建项、各阶段的运行配置、停用、升级、补采函数）与基线不一致")
}

// planBaselineSymbols 让永续标签落在永续市场上（共用夹具 planSymbolsStub 的标签一律是现货）。
type planBaselineSymbols struct{ *planSymbolsStub }

func (s planBaselineSymbols) GetTag(_ context.Context, _, tagID string) (*storagepb.Tag, error) {
	marketType := "spot"
	if tagID == "binance_swap" {
		marketType = "swap"
	}
	return &storagepb.Tag{TagId: tagID, Attributes: map[string]string{"sync.source": "binance", "market_type": marketType}}, nil
}

// newPlanBaselineUnified 构造统一数据集的夹具：同一个数据集挂现货、永续两个任务。
func newPlanBaselineUnified(t *testing.T) (*planHarness, *PlanReconciler) {
	h := newPlanHarness(t, 60)
	h.tasks.tasks = []domain.CollectionTask{
		{SpaceID: "crypto", TaskID: "unified-spot", Enabled: true, DataType: "kline", TagIDs: []string{"binance_spot"},
			CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"ds_unified","frequency":"1m"}`},
		{SpaceID: "crypto", TaskID: "unified-swap", Enabled: true, DataType: "kline", TagIDs: []string{"binance_swap"},
			CollectParams: `{"provider":"binance","market_type":"swap","subject_tags":["binance_swap"],"target_dataset_id":"ds_unified","frequency":"1m"}`},
	}
	r := h.reconciler()
	r.Groups.Symbols = planBaselineSymbols{h.symbols}
	r.Deploy.Networks["ap-hongkong"] = PlanNetwork{VPCID: "vpc-2", SubnetID: "subnet-2"}
	return h, r
}

// 基线场景里统一数据集确实走了两个市场：防止夹具变化后场景悄悄退化成单市场。
func TestPlanBaselineUnifiedScenarioCoversBothMarkets(t *testing.T) {
	h, r := newPlanBaselineUnified(t)
	require.NoError(t, h.reconcile(r))
	record, err := h.store.Open(context.Background(), "crypto")
	require.NoError(t, err)
	require.NotNil(t, record)
	regions := map[string]bool{}
	for _, function := range h.content(record).Functions {
		regions[function.Region] = true
	}
	require.Equal(t, map[string]bool{"ap-nanjing": true, "ap-hongkong": true}, regions)
}
