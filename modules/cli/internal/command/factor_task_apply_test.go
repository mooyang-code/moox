package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/packages/factorname"
	"github.com/mooyang-code/moox/packages/storagepolicy"
	"github.com/stretchr/testify/require"
)

// fakeFactorMgr 是 FactorMgr 的有状态替身：任务、定义和成员都记在内存里，按服务端同一套规则推导成员键与列名
// （固定成员参数全为默认值时就是因子 ID，否则 因子ID__值1_值2；可变成员 因子ID__var；单输出因子的列名就是成员键）。
type fakeFactorMgr struct {
	calls []string
	// definitions 是服务端已有的定义；tasks 是已有的任务（键是任务 ID），taskStatus 是它们的状态。
	definitions map[string]setupFactorDefinition
	tasks       map[string]setupFactorTask
	taskStatus  map[string]string
	members     map[string][]factorAPIMember
	// paramDecls 是各定义声明的参数（名称与默认值，按声明顺序）。
	paramDecls map[string][]factorAPIParam
	// definitionCategory 是已有定义的分类。
	definitionCategory map[string]string
	categories         []string
	movedCategories    []string
	createdFactors     []map[string]any
	createdTasks       []map[string]any
	// existingCategories 是线上已有的分类（ID → 名称）。
	existingCategories map[string]string
}

func newFakeFactorMgr() *fakeFactorMgr {
	return &fakeFactorMgr{
		definitions: map[string]setupFactorDefinition{}, tasks: map[string]setupFactorTask{}, taskStatus: map[string]string{},
		members: map[string][]factorAPIMember{}, paramDecls: map[string][]factorAPIParam{}, definitionCategory: map[string]string{},
		existingCategories: map[string]string{},
	}
}

// fakeDefinitionHash 模拟 FactorMgr 编译后的 definition_hash：忽略首尾空白。
func fakeDefinitionHash(spec string) string { return "dh:" + strings.TrimSpace(spec) }

// memberKey 与服务端一样用 factorname 推导成员键，避免替身认可服务端不会生成的键。
func (f *fakeFactorMgr) memberKey(factorID, kind string, given map[string]float64) string {
	if kind == "var" {
		key, _ := factorname.VarKey(factorID)
		return key
	}
	decls := make([]factorname.Param, 0, len(f.paramDecls[factorID]))
	for _, decl := range f.paramDecls[factorID] {
		decls = append(decls, factorname.Param{Name: decl.Name, Default: decl.DefaultValue})
	}
	key, _ := factorname.FixedKey(factorID, decls, given)
	return key
}

func (f *fakeFactorMgr) CallGatewayJSON(_ context.Context, service string, method string, body, response any) error {
	f.calls = append(f.calls, method)
	var request map[string]any
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return err
	}
	result := response.(*factorAPIResponse)
	result.RetInfo = &factorAPIRetInfo{Code: 0}
	notFound := func() { result.RetInfo.Code, result.RetInfo.Msg = 9, "not found" }
	switch method {
	case "GetFactorTask":
		task, ok := f.tasks[request["task_id"].(string)]
		if !ok {
			notFound()
			return nil
		}
		result.FactorTask.TaskID, result.FactorTask.Name = task.TaskID, task.Name
		result.FactorTask.SpaceID, result.FactorTask.SourceDatasetID, result.FactorTask.Freq = task.SpaceID, task.SourceDatasetID, task.Freq
		result.FactorTask.Status = f.taskStatus[task.TaskID]
		if result.FactorTask.Status == "" {
			result.FactorTask.Status = "enabled"
		}
		result.Members = append([]factorAPIMember(nil), f.members[task.TaskID]...)
	case "CreateFactorTask":
		create := request["factor_task"].(map[string]any)
		f.createdTasks = append(f.createdTasks, create)
		id := create["task_id"].(string)
		f.tasks[id] = setupFactorTask{TaskID: id, Name: create["name"].(string), SpaceID: create["space_id"].(string),
			SourceDatasetID: create["source_dataset_id"].(string), Freq: create["freq"].(string)}
		f.taskStatus[id] = "enabled"
	case "GetFactor":
		def, ok := f.definitions[request["factor_id"].(string)]
		if !ok {
			notFound()
			return nil
		}
		result.Factor.FactorID, result.Factor.Name = def.FactorID, def.Name
		result.Factor.CategoryID = f.definitionCategory[def.FactorID]
		result.Factor.DefinitionHash = fakeDefinitionHash(def.Spec)
		result.Factor.Params = f.paramDecls[def.FactorID]
	case "ValidateFactorSpec":
		result.Valid, result.DefinitionHash = true, fakeDefinitionHash(request["spec"].(string))
	case "CreateFactor":
		factor := request["factor"].(map[string]any)
		f.createdFactors = append(f.createdFactors, factor)
		id := factor["factor_id"].(string)
		f.definitions[id] = setupFactorDefinition{FactorID: id, Name: factor["name"].(string), Spec: factor["spec"].(string)}
		f.definitionCategory[id], _ = factor["category_id"].(string)
		result.Factor.Params = f.paramDecls[id]
	case "AddTaskMember":
		taskID, factorID := request["task_id"].(string), request["factor_id"].(string)
		kind, _ := request["kind"].(string)
		given := map[string]float64{}
		if params, ok := request["params"].(map[string]any); ok {
			for name, value := range params {
				given[name] = value.(float64)
			}
		}
		key := f.memberKey(factorID, kind, given)
		full := map[string]float64{}
		for _, decl := range f.paramDecls[factorID] {
			full[decl.Name] = decl.DefaultValue
			if value, ok := given[decl.Name]; ok {
				full[decl.Name] = value
			}
		}
		member := factorAPIMember{TaskID: taskID, FactorID: factorID, MemberKey: key, Kind: kind, Status: "disabled", Params: full, OutputColumns: []string{key}}
		f.members[taskID] = append(f.members[taskID], member)
		result.Member = member
	case "SetTaskMemberStatus":
		taskID, key := request["task_id"].(string), request["member_key"].(string)
		for index := range f.members[taskID] {
			if f.members[taskID][index].MemberKey == key {
				f.members[taskID][index].Status = request["status"].(string)
				result.Member = f.members[taskID][index]
			}
		}
	case "ListFactorCategories":
		for id, name := range f.existingCategories {
			result.Categories = append(result.Categories, factorAPICategory{CategoryID: id, Name: name})
		}
	case "UpsertFactorCategory":
		category := request["category"].(map[string]any)
		f.categories = append(f.categories, category["category_id"].(string)+"="+category["name"].(string))
		f.existingCategories[category["category_id"].(string)] = category["name"].(string)
	case "SetFactorCategory":
		f.movedCategories = append(f.movedCategories, request["factor_id"].(string)+"="+request["category_id"].(string))
	}
	return nil
}

// writeCalls 是写方法的调用次数之和；“有差异就不写入”看的就是它。
func (f *fakeFactorMgr) writeCalls() int {
	total := 0
	for _, method := range []string{"UpsertFactorCategory", "CreateFactor", "SetFactorCategory", "CreateFactorTask", "AddTaskMember", "SetTaskMemberStatus", "RemoveTaskMember", "DeleteFactor"} {
		total += f.callsOf(method)
	}
	return total
}

func (f *fakeFactorMgr) callsOf(method string) int {
	count := 0
	for _, call := range f.calls {
		if call == method {
			count++
		}
	}
	return count
}

const testLibrary = `- factor_id: bias
  name: bias 偏离
  spec: |
    params:
      window: {type: int, min: 2, max: 60, default: 5}
    outputs:
      bias: close / ts_mean(close, window)
- factor_id: cci
  name: CCI
  spec: |
    outputs:
      cci: close
`

func writeLibrary(t *testing.T, dir, name string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(testLibrary), 0o600))
}

// writeSeed 在 dir 下写一份因子任务文件，因子库放在它旁边的 factors/lib.yaml。
func writeSeed(t *testing.T, dir, body string) string {
	t.Helper()
	writeLibrary(t, filepath.Join(dir, "factors"), "lib.yaml")
	path := filepath.Join(dir, "my-factors.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

const testSeed = `source_dir: factors
tasks:
  - id: ftask_prices_1m
    name: 行情因子
    space_id: crypto
    source_dataset_id: dataset_prices
    freq: 1m
    members:
      - {factor: bias}
      - {factor: bias, params: {window: 10}, status: disabled}
      - {factor: bias, var: true}
  - id: ftask_cn_5m
    name: A股因子
    space_id: stockcn
    source_dataset_id: dataset_cn
    freq: 5m
    members:
      - {factor: cci}
definitions:
  - {factor_id: bias, file: lib.yaml}
  - {factor_id: cci, file: lib.yaml}
`

func TestLoadFactorPlanReadsTheSeedAndItsLibrary(t *testing.T) {
	path := writeSeed(t, t.TempDir(), testSeed)
	plan, err := loadFactorPlan(path, storagepolicy.Default(), "")
	require.NoError(t, err)
	require.Len(t, plan.Definitions, 2)
	require.Equal(t, "bias 偏离", plan.Definitions[0].Name)
	require.Contains(t, plan.Definitions[0].Spec, "ts_mean(close, window)")
	require.Len(t, plan.Tasks, 2)
	task := plan.Tasks[1]
	require.Equal(t, "ftask_prices_1m", task.TaskID)
	require.Equal(t, []setupFactorMember{
		{FactorID: "bias", Kind: "fixed", Status: "enabled"},
		{FactorID: "bias", Kind: "fixed", Params: map[string]float64{"window": 10}, Status: "disabled"},
		{FactorID: "bias", Kind: "var", Status: "enabled"},
	}, task.Members)
	require.Equal(t, 4, plan.memberCount())

	// 按空间过滤只留任务，定义全部带上。
	plan, err = loadFactorPlan(path, storagepolicy.Default(), "stockcn")
	require.NoError(t, err)
	require.Len(t, plan.Tasks, 1)
	require.Equal(t, "ftask_cn_5m", plan.Tasks[0].TaskID)
	require.Len(t, plan.Definitions, 2)
}

func TestLoadFactorPlanRejectsBadSeeds(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"旧的 enabled 字段": {"enabled: true\n" + testSeed, "field enabled not found"},
		"库里没有的因子":       {strings.Replace(testSeed, "{factor_id: cci, file: lib.yaml}", "{factor_id: missing, file: lib.yaml}", 1), "references unknown factor"},
		"因子 ID 不合规":     {strings.Replace(testSeed, "factor_id: cci", "factor_id: Cci", 1), "factor_id is invalid"},
		"定义文件跑出因子库目录":   {strings.Replace(testSeed, "{factor_id: cci, file: lib.yaml}", "{factor_id: cci, file: ../lib.yaml}", 1), "must stay under source_dir"},
		"成员引用没声明的定义":    {strings.Replace(testSeed, "- {factor: cci}", "- {factor: rsi}", 1), "references unknown factor"},
		"任务名没有汉字":       {strings.Replace(testSeed, "name: A股因子", "name: cn", 1), "must contain Chinese"},
		"同一成员写了两遍":      {strings.Replace(testSeed, "- {factor: bias, var: true}", "- {factor: bias}", 1), "repeats factor"},
		"没有 source_dir": {strings.Replace(testSeed, "source_dir: factors\n", "", 1), "source_dir"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadFactorPlan(writeSeed(t, t.TempDir(), tc.body), storagepolicy.Default(), "")
			require.ErrorContains(t, err, tc.want)
		})
	}
	dir := t.TempDir()
	path := writeSeed(t, dir, testSeed)
	bad := "- factor_id: bias\n  name: X\n  spec: x\n  factor_type: timeseries\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "factors", "lib.yaml"), []byte(bad), 0o600))
	_, err := loadFactorPlan(path, storagepolicy.Default(), "")
	require.ErrorContains(t, err, "factor_type", "因子库里的未知字段同样报错")
}

func TestLoadFactorPlanRejectsSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	path := writeSeed(t, dir, testSeed)
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	require.NoError(t, os.WriteFile(outside, []byte(testLibrary), 0o600))
	require.NoError(t, os.Remove(filepath.Join(dir, "factors", "lib.yaml")))
	if err := os.Symlink(outside, filepath.Join(dir, "factors", "lib.yaml")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := loadFactorPlan(path, storagepolicy.Default(), "")
	require.ErrorContains(t, err, "must stay under source_dir")
}

// 仓库里的默认种子：能读出来，按空间拆开后 crypto 两个任务、A 股一个，默认保留期装得下全部回看。
func TestRepositoryFactorSeedLoads(t *testing.T) {
	path := filepath.Join(repoRoot(t), filepath.FromSlash(defaultFactorSeed))
	plan, err := loadFactorPlan(path, storagepolicy.Default(), "")
	require.NoError(t, err)
	require.Len(t, plan.Definitions, 14)
	var ids []string
	for _, task := range plan.Tasks {
		ids = append(ids, task.TaskID)
	}
	require.Equal(t, []string{"ftask_cn_5m", "ftask_spot_1m", "ftask_swap_1m"}, ids)
	crypto, err := loadFactorPlan(path, storagepolicy.Default(), "crypto")
	require.NoError(t, err)
	require.Len(t, crypto.Tasks, 2)
	for space, example := range spaceExamples {
		scoped, err := loadFactorPlan(path, storagepolicy.Default(), space)
		require.NoError(t, err)
		require.True(t, slices.ContainsFunc(scoped.Tasks, func(task setupFactorTask) bool { return task.TaskID == example.FactorTask }),
			"默认示例策略 %s 读的因子任务 %s 要在种子里", example.StrategyID, example.FactorTask)
	}
}

func testSetupPlan() setupFactorPlan {
	return setupFactorPlan{
		Tasks: []setupFactorTask{{
			TaskID: "ftask_prices_1m", Name: "行情因子", SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m",
			Members: []setupFactorMember{{FactorID: "bias", Kind: "fixed", Status: "enabled"}},
		}},
		Definitions: []setupFactorDefinition{{FactorID: "bias", Name: "bias", Spec: "output: close / ts_mean(close, 5)\n"}},
	}
}

func newApplier(f *fakeFactorMgr) (*remoteSetupFactor, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return &remoteSetupFactor{client: f, out: out}, out
}

// 新建：先只读核对，再按分类、定义、任务、成员的顺序新建；逐项结果带成员键与全部列名。
func TestFactorTaskApplyCreatesWhatIsMissing(t *testing.T) {
	fake := newFakeFactorMgr()
	fake.paramDecls["bias"] = []factorAPIParam{{Name: "window", DefaultValue: 5}}
	applier, out := newApplier(fake)
	result, err := applier.Apply(context.Background(), testSetupPlan())
	require.NoError(t, err)
	require.Equal(t, 1, result.DefinitionsCreated)
	require.Equal(t, 1, result.TasksCreated)
	require.Equal(t, 1, result.MembersCreated)
	require.Zero(t, result.Conflicts)
	require.Equal(t, []string{"ValidateFactorSpec", "GetFactor", "GetFactorTask", "CreateFactor", "CreateFactorTask", "AddTaskMember", "SetTaskMemberStatus"}, fake.calls)
	require.Equal(t, "行情因子", fake.createdTasks[0]["name"])
	require.Equal(t, "pending", fake.createdTasks[0]["status"])
	require.Equal(t, []setupFactorItem{
		{Kind: "definition", ID: "bias", Result: itemCreated},
		{Kind: "task", ID: "ftask_prices_1m", Result: itemCreated, Detail: "行情因子"},
		{Kind: "member", ID: "ftask_prices_1m/bias", Result: itemCreated, MemberKey: "bias", Columns: []string{"bias"}},
	}, result.Items)
	require.Equal(t, "enabled", fake.members["ftask_prices_1m"][0].Status)
	require.Contains(t, out.String(), "因子成员 ftask_prices_1m/bias：新建，成员键 bias，列 bias")
}

// 已存在相同：再导入一次全部是“已存在”，一个写调用都不发。
func TestFactorTaskApplyTreatsIdenticalContentAsExisting(t *testing.T) {
	fake := newFakeFactorMgr()
	fake.paramDecls["bias"] = []factorAPIParam{{Name: "window", DefaultValue: 5}}
	applier, _ := newApplier(fake)
	_, err := applier.Apply(context.Background(), testSetupPlan())
	require.NoError(t, err)
	fake.calls = nil

	again, _ := newApplier(fake)
	result, err := again.Apply(context.Background(), testSetupPlan())
	require.NoError(t, err)
	require.Equal(t, 1, result.DefinitionsExist)
	require.Equal(t, 1, result.TasksExist)
	require.Equal(t, 1, result.MembersExist)
	require.Zero(t, result.DefinitionsCreated+result.TasksCreated+result.MembersCreated+result.Conflicts)
	for _, item := range result.Items {
		require.Equal(t, itemExists, item.Result, "%+v", item)
	}
	require.Zero(t, fake.writeCalls())
}

// 已存在不同：逐项列出差异，一项都不写（文件里别的新任务也不建），以错误结束。
func TestFactorTaskApplyRefusesDifferencesAndWritesNothing(t *testing.T) {
	plan := testSetupPlan()
	plan.Definitions = append(plan.Definitions, setupFactorDefinition{FactorID: "cci", Name: "CCI", Spec: "output: close\n", CategoryID: "momentum", CategoryName: "动量类"})
	plan.Tasks = append(plan.Tasks, setupFactorTask{TaskID: "ftask_new", Name: "新任务", SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "5m",
		Members: []setupFactorMember{{FactorID: "cci", Kind: "fixed", Status: "enabled"}}})
	fake := newFakeFactorMgr()
	changed := plan.Definitions[0]
	changed.Spec = "output: close / ts_mean(close, 10)\n"
	fake.definitions["bias"] = changed
	fake.tasks["ftask_prices_1m"] = setupFactorTask{TaskID: "ftask_prices_1m", Name: "旧名字", SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m"}
	applier, out := newApplier(fake)
	result, err := applier.Apply(context.Background(), plan)
	require.ErrorContains(t, err, "一项都没有写入")
	require.Equal(t, 2, result.Conflicts, "定义的表达式不同一项，任务的名称不同一项")
	require.Zero(t, fake.writeCalls(), "有差异时不发任何写调用")
	conflicts := map[string]string{}
	notWritten := 0
	for _, item := range result.Items {
		switch item.Result {
		case itemConflict:
			conflicts[item.Kind+":"+item.ID] = item.Detail
		case itemNotWritten:
			notWritten++
		}
	}
	require.Contains(t, conflicts["definition:bias"], "表达式或参数不同")
	require.Contains(t, conflicts["task:ftask_prices_1m"], `名称：线上 "旧名字"，文件 "行情因子"`)
	require.Equal(t, 4, notWritten, "新分类、新定义、新任务和它的成员都没有写")
	require.Contains(t, out.String(), "因子任务 ftask_prices_1m：与线上不同")
	require.Contains(t, out.String(), "因子任务 ftask_new：线上没有（因有冲突未写入）")
}

// 成员的状态不同、可变成员的默认参数不同、分类名称不同都算差异；线上多出来的成员只报告，不算差异。
func TestFactorTaskApplyComparesMembersAndCategories(t *testing.T) {
	plan := testSetupPlan()
	plan.Definitions[0].CategoryID, plan.Definitions[0].CategoryName = "momentum", "动量类"
	plan.Tasks[0].Members = []setupFactorMember{
		{FactorID: "bias", Kind: "fixed", Status: "enabled"},
		{FactorID: "bias", Kind: "var", Params: map[string]float64{"window": 20}, Status: "enabled"},
	}
	existing := func() *fakeFactorMgr {
		fake := newFakeFactorMgr()
		fake.definitions["bias"] = plan.Definitions[0]
		fake.definitionCategory["bias"] = "momentum"
		fake.existingCategories["momentum"] = "动量类"
		fake.paramDecls["bias"] = []factorAPIParam{{Name: "window", DefaultValue: 5}}
		fake.tasks["ftask_prices_1m"] = setupFactorTask{TaskID: "ftask_prices_1m", Name: "行情因子", SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m"}
		fake.members["ftask_prices_1m"] = []factorAPIMember{
			{TaskID: "ftask_prices_1m", FactorID: "bias", MemberKey: "bias", Kind: "fixed", Status: "disabled", Params: map[string]float64{"window": 5}},
			{TaskID: "ftask_prices_1m", FactorID: "bias", MemberKey: "bias__var", Kind: "var", Status: "enabled", Params: map[string]float64{"window": 5}},
			{TaskID: "ftask_prices_1m", FactorID: "cci", MemberKey: "cci", Kind: "fixed", Status: "enabled"},
		}
		return fake
	}
	fake := existing()
	applier, _ := newApplier(fake)
	result, err := applier.Apply(context.Background(), plan)
	require.Error(t, err)
	require.Equal(t, 2, result.Conflicts)
	details := map[string]string{}
	for _, item := range result.Items {
		details[item.Result+":"+item.MemberKey] = item.Detail
	}
	require.Contains(t, details[itemConflict+":bias"], "状态：线上 disabled，文件 enabled")
	require.Contains(t, details[itemConflict+":bias__var"], "默认参数不同")
	require.Contains(t, details[itemOrphan+":cci"], "只报告，不移除")
	require.Zero(t, fake.writeCalls())

	fake = existing()
	fake.existingCategories["momentum"] = "趋势类"
	plan.Tasks[0].Members = plan.Tasks[0].Members[:1]
	fake.members["ftask_prices_1m"][0].Status = "enabled"
	applier, _ = newApplier(fake)
	result, err = applier.Apply(context.Background(), plan)
	require.Error(t, err)
	require.Equal(t, 1, result.Conflicts)
	require.Equal(t, itemConflict, result.Items[0].Result)
	require.Contains(t, result.Items[0].Detail, `名称：线上 "趋势类"，文件 "动量类"`)
}

// 线上任务停在 pending（上次创建被中断）：算已存在，接着把它建完再加成员。
func TestFactorTaskApplyResumesAPendingTask(t *testing.T) {
	plan := testSetupPlan()
	fake := newFakeFactorMgr()
	fake.definitions["bias"] = plan.Definitions[0]
	fake.tasks["ftask_prices_1m"] = setupFactorTask{TaskID: "ftask_prices_1m", Name: "行情因子", SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m"}
	fake.taskStatus["ftask_prices_1m"] = "pending"
	applier, _ := newApplier(fake)
	result, err := applier.Apply(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, 1, fake.callsOf("CreateFactorTask"))
	require.Zero(t, result.TasksCreated)
	require.Equal(t, 1, result.MembersCreated)
}

// 命令本身：有差异时退出码非 0，摘要的 status 是 conflict。
func TestFactorTaskApplyCommandFailsOnConflict(t *testing.T) {
	dir := t.TempDir()
	seed := writeSeed(t, dir, testSeed)
	manifest := filepath.Join(dir, "moox.toml")
	require.NoError(t, os.WriteFile(manifest, []byte("[hosts.control]\naddress = \"192.0.2.10\"\nssh = { username = \"ubuntu\" }\n\n[deployments]\ncontrol = [\"admin\", \"eventbus\"]\n"), 0o644))
	fake := newFakeFactorMgr()
	fake.tasks["ftask_cn_5m"] = setupFactorTask{TaskID: "ftask_cn_5m", Name: "别的名字", SpaceID: "stockcn", SourceDatasetID: "dataset_cn", Freq: "5m"}
	previous := openCommandGateway
	openCommandGateway = func(context.Context, string, *topology.Config) (commandGateway, error) {
		return factorFakeGateway{fake}, nil
	}
	t.Cleanup(func() { openCommandGateway = previous })
	cmd := newFactorTaskApplyCommand()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--file", seed, "--config", manifest})
	require.Error(t, cmd.Execute())
	require.Contains(t, stdout.String(), `"status":"conflict"`)
	require.Contains(t, stderr.String(), "因子任务 ftask_cn_5m：与线上不同")
	require.Zero(t, fake.writeCalls())
}

// factorFakeGateway 把网关的 JSON 转发交给假的 FactorMgr。
type factorFakeGateway struct{ fake *fakeFactorMgr }

func (g factorFakeGateway) Forward(ctx context.Context, service, method string, _ int, body []byte) ([]byte, error) {
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, err
	}
	response := &factorAPIResponse{}
	if err := g.fake.CallGatewayJSON(ctx, service, method, request, response); err != nil {
		return nil, err
	}
	return json.Marshal(response)
}

func (factorFakeGateway) Invoke(context.Context, string, string, any, any) error {
	return errors.New("not used")
}

func (factorFakeGateway) Close() error { return nil }

// CLI 自己解析 JSON、不引入 factorgen：把读到的字段钉在 protobuf JSON 的形状上。
func TestFactorAPIResponseMatchesFactorProtoJSON(t *testing.T) {
	var get factorAPIResponse
	require.NoError(t, json.Unmarshal([]byte(`{"ret_info":{"code":0},"factor":{"factor_id":"bias","factor_type":"timeseries","name":"bias","spec":"outputs: {}","spec_hash":"sha256:x","input_columns":["close"],"outputs":["bias"],"lookback_periods":20,"definition_hash":"sha256:d","params":[{"name":"window","default_value":5}]}}`), &get))
	require.Equal(t, "bias", get.Factor.FactorID)
	require.Equal(t, "sha256:d", get.Factor.DefinitionHash)
	require.Equal(t, map[string]float64{"window": 5}, paramDefaults(get.Factor))

	var validated factorAPIResponse
	require.NoError(t, json.Unmarshal([]byte(`{"ret_info":{"code":0},"valid":false,"diagnostic":{"path":"outputs.bias","line":1,"column":3,"message":"未知函数"}}`), &validated))
	require.False(t, validated.Valid)
	require.Equal(t, "未知函数", validated.Diagnostic.Message)

	var task factorAPIResponse
	require.NoError(t, json.Unmarshal([]byte(`{"ret_info":{"code":0},"factor_task":{"task_id":"ftask_a","name":"行情因子","space_id":"crypto","source_dataset_id":"ds","freq":"1m","status":"enabled","bound_instance_id":"i1"},"members":[{"task_id":"ftask_a","factor_id":"bias","member_key":"bias__var","kind":"var","status":"enabled","params":{"window":5},"output_columns":["bias__var"]}]}`), &task))
	require.Equal(t, "行情因子", task.FactorTask.Name)
	require.Equal(t, []factorAPIMember{{TaskID: "ftask_a", FactorID: "bias", MemberKey: "bias__var", Kind: "var", Status: "enabled", Params: map[string]float64{"window": 5}, OutputColumns: []string{"bias__var"}}}, task.Members)

	var categories factorAPIResponse
	require.NoError(t, json.Unmarshal([]byte(`{"ret_info":{"code":0},"categories":[{"category_id":"trend","name":"趋势类","sort_order":1}]}`), &categories))
	require.Equal(t, []factorAPICategory{{CategoryID: "trend", Name: "趋势类"}}, categories.Categories)
}
