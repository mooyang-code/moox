package fetchplan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

// 批 2 的基线金样（Collector 这一层）。
//
// plan_environment.golden.json：对已有的几个对账场景（单市场、标的变更、退役、交接、统一数据集），把每份采集计划里每个函数的
// 过渡与最终内容落成文件：云函数记 functionEnvironment 的输出（其中 MOOX_MARKET_FETCH_PLAN 就是下发给函数的
// Plan JSON，函数严格解码，一个字节都不能变）与两个 cron；推送与本地执行项没有函数环境，记 Plan JSON。要重新生成时设 MOOX_UPDATE_GOLDEN=1。
//
// plan_dispatch.golden.json 是 T0 用旧实现（经 CloudFunction 的作业接口）录下的“对账器在什么时刻要哪个函数变成什么样”，
// 文件原样保留、不再重写。T5 之后对账器直接调 cloud 包（Ensure、SetTimer、UpgradeCode、Delete），请求的形状变了，
// 比对按“函数在每个时刻被要求成为的样子”换算（见 planPushesFromGolden 与 planPushesFromCalls）：
//   - 旧的“建函数 + 同一时刻的运行配置” = 新的一次 Ensure（建函数与下发环境、定时器合成一次）；
//   - 旧的只停定时器的运行配置（退役、暂停）= 新的 SetTimer(停用)；
//   - 旧的部署（只换代码）= 新的 UpgradeCode；补采函数的部署、外部入口变了之后的“删掉再建” = 新的一次 Ensure（就地改写）。
// 比的是受管环境（采集计划换成 SHA-256）、定时器、代码包；创建预留号、函数元数据这些旧实现的附带项不再存在，不比。

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

// planDispatch 是 T0 金样里对账器经旧作业接口提交的一批请求（当时的租约字段没有录：它们不进函数）。
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

// planPush 是“对账器在某一时刻要某个函数成为的样子”，下发金样的比对口径。
type planPush struct {
	At       string `json:"at"`
	Function string `json:"function"`
	// Kind 是 push（下发环境与定时器）、timer（只改定时器）、upgrade（只换代码）、delete。
	Kind string `json:"kind"`
	// Environment 是受管环境（去掉基础项，采集计划换成 SHA-256），只有 push 有。
	Environment map[string]string `json:"environment,omitempty"`
	// Timer 是定时器（cron 与开关）；补采函数没有定时器，为空。
	Timer string `json:"timer,omitempty"`
	// PackageID 是 upgrade 的目标包与补采函数 push 的代码包。
	PackageID string `json:"package_id,omitempty"`
}

// planBaseKeys 是函数环境里的基础项（不由计划决定），比对时去掉。
var planBaseKeys = []string{"MOOX_SPACE_ID"}

// planManagedEnvironment 去掉基础项，并把采集计划换成 SHA-256（对账器发出的环境用）。
func planManagedEnvironment(environment map[string]string) map[string]string {
	return stripBaseKeys(planDispatchEnvironment(environment))
}

// stripBaseKeys 去掉基础项（金样里的环境：采集计划已经是 SHA-256）。
func stripBaseKeys(environment map[string]string) map[string]string {
	out := make(map[string]string, len(environment))
	for key, value := range environment {
		out[key] = value
	}
	for _, key := range planBaseKeys {
		delete(out, key)
	}
	return out
}

func planTimer(enabled bool, cron string) string {
	if strings.TrimSpace(cron) == "" {
		cron = "0 * * * * * *"
	}
	if enabled {
		return "enabled " + cron
	}
	return "disabled " + cron
}

func sortPushes(pushes []planPush) []planPush {
	sort.SliceStable(pushes, func(i, j int) bool {
		if pushes[i].At != pushes[j].At {
			return pushes[i].At < pushes[j].At
		}
		if pushes[i].Function != pushes[j].Function {
			return pushes[i].Function < pushes[j].Function
		}
		return pushes[i].Kind < pushes[j].Kind
	})
	return pushes
}

// planPushesFromGolden 把旧实现提交的批次换算成每个函数的要求（换算规则见文件开头）。
func planPushesFromGolden(t *testing.T, dispatches []planDispatch) []planPush {
	t.Helper()
	var pushes []planPush
	last := map[string]map[string]string{}
	created := map[string]planDispatchCreate{}
	deleted := map[string]bool{}
	for index, dispatch := range dispatches {
		switch dispatch.Kind {
		case "create":
			for _, item := range dispatch.Creates {
				if item.TriggerType == "invoke" {
					environment := stripBaseKeys(item.Environment)
					pushes = append(pushes, planPush{At: dispatch.At, Function: item.Name, Kind: "push", Environment: environment, PackageID: item.PackageID})
					last[item.Name] = environment
					delete(deleted, item.Name)
					continue
				}
				// 定时函数：建出来的环境就是同一时刻紧跟的那批运行配置下发的过渡环境。
				created[item.Name] = item
				require.Less(t, index+1, len(dispatches), "建函数之后必须紧跟运行配置：%s", item.Name)
			}
		case "runtime_config":
			for _, patch := range dispatch.RuntimeConfigs {
				environment := stripBaseKeys(patch.ManagedEnvironment)
				if item, ok := created[patch.InstanceID]; ok {
					require.Equal(t, stripBaseKeys(item.Environment), environment, "建函数时的受管环境就是同一时刻下发的那份：%s", patch.InstanceID)
					delete(created, patch.InstanceID)
				}
				if !patch.TimerEnabled && (len(patch.ManagedEnvironment) == 0 || equalEnvironment(last[patch.InstanceID], environment)) {
					// 只停定时器（退役保持过渡环境不变；暂停不带环境）。
					pushes = append(pushes, planPush{At: dispatch.At, Function: patch.InstanceID, Kind: "timer", Timer: planTimer(false, patch.TimerCron)})
					continue
				}
				pushes = append(pushes, planPush{At: dispatch.At, Function: patch.InstanceID, Kind: "push", Environment: environment, Timer: planTimer(patch.TimerEnabled, patch.TimerCron)})
				last[patch.InstanceID] = environment
			}
		case "deploy":
			for _, name := range dispatch.Functions {
				if strings.Contains(name, "backfill") {
					// 补采函数的代码包变了：新实现就地 Ensure（环境不变，代码与包 ID 换新）。
					pushes = append(pushes, planPush{At: dispatch.At, Function: name, Kind: "push", Environment: last[name], PackageID: dispatch.PackageID})
					continue
				}
				pushes = append(pushes, planPush{At: dispatch.At, Function: name, Kind: "upgrade", PackageID: dispatch.PackageID})
			}
		case "delete":
			for _, name := range dispatch.Functions {
				// 补采函数外部入口变了时旧实现删掉再建；新实现就地 Ensure，删除那一步不再有（紧跟的建函数换算成 push）。
				require.Contains(t, name, "backfill", "金样里只有补采函数的删除")
				deleted[name] = true
			}
		default:
			t.Fatalf("unknown dispatch kind %q", dispatch.Kind)
		}
	}
	require.Empty(t, created, "每个建出来的定时函数都要有同一时刻的运行配置")
	return sortPushes(pushes)
}

func equalEnvironment(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

// planPushesFromCalls 把对账器发出的云端操作换算成同样的口径。
func planPushesFromCalls(calls []cloudCall) []planPush {
	var pushes []planPush
	for _, call := range calls {
		at := call.At.UTC().Format(time.RFC3339)
		switch call.Op {
		case "ensure":
			push := planPush{At: at, Function: call.Name, Kind: "push", Environment: planManagedEnvironment(call.Spec.Environment)}
			if call.Spec.Timer != nil {
				push.Timer = planTimer(call.Spec.Timer.Enabled, call.Spec.Timer.Cron)
			} else {
				push.PackageID = call.PackageID
			}
			pushes = append(pushes, push)
		case "timer":
			pushes = append(pushes, planPush{At: at, Function: call.Name, Kind: "timer", Timer: planTimer(call.Timer.Enabled, call.Timer.Cron)})
		case "upgrade":
			pushes = append(pushes, planPush{At: at, Function: call.Name, Kind: "upgrade", PackageID: call.PackageID})
		case "delete":
			pushes = append(pushes, planPush{At: at, Function: call.Name, Kind: "delete"})
		}
	}
	return sortPushes(pushes)
}

// loadDispatchGolden 读 T0 录下的下发金样（只读，不重写）。
func loadDispatchGolden(t *testing.T) map[string][]planDispatch {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "plan_dispatch.golden.json"))
	require.NoError(t, err)
	var scenarios []planDispatchScenario
	require.NoError(t, json.Unmarshal(raw, &scenarios))
	out := map[string][]planDispatch{}
	for _, scenario := range scenarios {
		out[scenario.Name] = scenario.Dispatches
	}
	return out
}

// planDispatchScenario 是一个场景里对账器先后提交给云端的全部请求（金样文件的结构）。
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
	dispatched := map[string][]cloudCall{}

	// 单市场：一个现货 1m 任务，120 个标的分成 3 个函数。之后标的减少一个（标的变更，过渡计划带新旧两份）。
	{
		h := newPlanHarness(t, 120)
		r := h.reconciler()
		planBaselineSettle(t, h, r)
		r.lastVerified = h.now
		h.setSubjects(119)
		planBaselineSettle(t, h, r)
		scenarios = append(scenarios, planBaselineScenario{Name: "single_market_then_subject_change", Plans: planBaselineRecords(t, r, h.store.records)})
		dispatched["single_market_then_subject_change"] = h.cloud.operations()
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
		dispatched["retire_functions"] = h.cloud.operations()
	}

	// 交接：1m 与 1h 两个数据集先由云函数采集，再切到推送，再切回云函数。
	{
		h := newIngestHarness(t, ingestSCF, "1m", "1h")
		h.settle(true)
		h.r.lastVerified = h.now
		h.setIngest("ds_spot_1m", ingestStream)
		h.setIngest("ds_spot_1h", ingestStream)
		h.settle(true)
		h.r.lastVerified = h.now
		h.setIngest("ds_spot_1m", ingestSCF)
		h.setIngest("ds_spot_1h", ingestSCF)
		h.settle(true)
		scenarios = append(scenarios, planBaselineScenario{Name: "handoff_scf_stream_scf", Plans: planBaselineRecords(t, h.r, h.store.records)})
		dispatched["handoff_scf_stream_scf"] = h.cloud.operations()
	}

	// 交接：1m 数据集先由云函数采集，再切到本地执行端（拉取方式不变，只换执行端）。
	{
		h := newIngestHarness(t, ingestSCF, "1m")
		h.settle(true)
		h.r.lastVerified = h.now
		h.setIngest("ds_spot_1m", ingestLocal)
		h.settle(true)
		require.Equal(t, marketfetch.PlanExecutorLocal, h.applied().Functions[localItem].Executor)
		scenarios = append(scenarios, planBaselineScenario{Name: "handoff_scf_local", Plans: planBaselineRecords(t, h.r, h.store.records)})
		dispatched["handoff_scf_local"] = h.cloud.operations()
	}

	// 统一数据集：同一个数据集挂现货、永续两个任务，两个市场的函数共用一份快照；之后标的减少一个。
	{
		h, r := newPlanBaselineUnified(t)
		planBaselineSettle(t, h, r)
		r.lastVerified = h.now
		h.setSubjects(59)
		planBaselineSettle(t, h, r)
		scenarios = append(scenarios, planBaselineScenario{Name: "unified_dataset_then_subject_change", Plans: planBaselineRecords(t, r, h.store.records)})
		dispatched["unified_dataset_then_subject_change"] = h.cloud.operations()
	}

	for _, scenario := range scenarios {
		require.NotEmpty(t, scenario.Plans, scenario.Name)
		for _, plan := range scenario.Plans {
			require.NotEmpty(t, plan, scenario.Name)
		}
	}
	// 暂停：计划生效后停用全部定时器（只停定时器，cron 用占位值）。
	{
		h := newPlanHarness(t, 60)
		r := h.reconciler()
		planBaselineSettle(t, h, r)
		h.cloud.resetOperations()
		done, err := r.DisableAll(context.Background())
		require.NoError(t, err)
		require.True(t, done)
		dispatched["disable_all"] = h.cloud.operations()
	}

	// 升级代码：计划生效后上传了新代码包，落后的函数只换代码（不带环境、不动定时器）。
	{
		h := newPlanHarness(t, 60)
		r := h.reconciler()
		planBaselineSettle(t, h, r)
		h.cloud.resetOperations()
		h.cloud.setPackages("pkg", "pkg-new")
		h.now = h.now.Add(2 * time.Hour)
		require.NoError(t, h.reconcile(r))
		h.waitOps(r)
		dispatched["upgrade_code"] = h.cloud.operations()
	}

	// 补采函数：先创建（不带定时器与采集计划，预留并发 5 份内存），换代码包后升级，调用方密钥轮换后按新身份改写。
	{
		h := newPlanHarness(t, 60)
		r := h.reconciler()
		_, err := r.EnsureBackfillFunction(context.Background(), "spot")
		require.NoError(t, err)
		h.cloud.setPackages("pkg", "pkg-new")
		h.now = h.now.Add(time.Minute)
		_, err = r.EnsureBackfillFunction(context.Background(), "spot")
		require.NoError(t, err)
		r.Entry = func(string) (map[string]string, error) {
			access, _ := testSCFEntryEnvironment("")
			access["MOOX_CALLER_KEY_ID"] = "assigned-scf-key-18"
			return access, nil
		}
		h.now = h.now.Add(time.Minute)
		_, err = r.EnsureBackfillFunction(context.Background(), "spot")
		require.NoError(t, err)
		dispatched["backfill_function"] = h.cloud.operations()
		spec := h.cloud.operations()[0].Spec
		require.Nil(t, spec.Timer)
		require.Equal(t, int64(DefaultBackfillReservedConcurrency*64), spec.ReservedMem)
		require.Equal(t, int64(320), h.mustFunction(BackfillFunctionName("crypto", "spot", "ap-nanjing")).ReservedMem)
	}

	planBaselineCompare(t, "plan_environment.golden.json", scenarios, "函数环境、过渡或最终计划、cron 与基线不一致")
	golden := loadDispatchGolden(t)
	require.Len(t, golden, len(dispatched))
	for name, calls := range dispatched {
		want := planPushesFromGolden(t, golden[name])
		got := planPushesFromCalls(calls)
		require.NotEmpty(t, got, name)
		require.Equal(t, want, got, "场景 %s：对账器在各时刻要各函数成为的样子与 T0 的下发金样不一致", name)
	}
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
