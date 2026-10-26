package command

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/packages/factorname"
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
}

func newFakeFactorMgr() *fakeFactorMgr {
	return &fakeFactorMgr{
		definitions: map[string]setupFactorDefinition{}, tasks: map[string]setupFactorTask{}, taskStatus: map[string]string{},
		members: map[string][]factorAPIMember{}, paramDecls: map[string][]factorAPIParam{}, definitionCategory: map[string]string{},
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
		result.FactorTask.Status = defaultString(f.taskStatus[task.TaskID], "enabled")
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
	case "UpsertFactorCategory":
		category := request["category"].(map[string]any)
		f.categories = append(f.categories, category["category_id"].(string)+"="+category["name"].(string))
	case "SetFactorCategory":
		f.movedCategories = append(f.movedCategories, request["factor_id"].(string)+"="+request["category_id"].(string))
	}
	return nil
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

func configTask(members ...setupconfig.FactorSetupMember) setupconfig.FactorSetupTask {
	return setupconfig.FactorSetupTask{ID: "ftask_prices_1m", Name: "行情因子", SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m", Members: members}
}

func TestLoadSetupFactorsReadsYAMLLibraryAndTasks(t *testing.T) {
	root := t.TempDir()
	writeLibrary(t, filepath.Join(root, "examples", "factors"), "lib.yaml")
	plan, err := loadSetupFactors(setupconfig.Manifest{Factors: setupconfig.FactorSetup{
		Enabled: true, SourceDir: "./examples/factors",
		Tasks: []setupconfig.FactorSetupTask{configTask(
			setupconfig.FactorSetupMember{Factor: "bias", Status: "enabled"},
			setupconfig.FactorSetupMember{Factor: "bias", Params: map[string]float64{"window": 10}, Status: "disabled"},
			setupconfig.FactorSetupMember{Factor: "bias", Var: true, Status: "enabled"},
		)},
		Definitions: []setupconfig.FactorSetupDefinition{{FactorID: "bias", File: "lib.yaml"}},
	}}, root)
	require.NoError(t, err)
	require.Len(t, plan.Definitions, 1)
	require.Equal(t, "bias", plan.Definitions[0].FactorID)
	require.Equal(t, "bias 偏离", plan.Definitions[0].Name)
	require.Contains(t, plan.Definitions[0].Spec, "ts_mean(close, window)")
	require.Len(t, plan.Tasks, 1)
	task := plan.Tasks[0]
	require.Equal(t, setupFactorTask{TaskID: "ftask_prices_1m", Name: "行情因子", SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m", Members: task.Members}, task)
	require.Equal(t, []setupFactorMember{
		{FactorID: "bias", Kind: "fixed", Status: "enabled"},
		{FactorID: "bias", Kind: "fixed", Params: map[string]float64{"window": 10}, Status: "disabled"},
		{FactorID: "bias", Kind: "var", Status: "enabled"},
	}, task.Members)
	require.Equal(t, 3, plan.memberCount())
}

func TestLoadSetupFactorsRejectsUnknownFactorAndBadLibrary(t *testing.T) {
	root := t.TempDir()
	writeLibrary(t, filepath.Join(root, "factors"), "lib.yaml")
	manifest := setupconfig.Manifest{Factors: setupconfig.FactorSetup{
		Enabled: true, SourceDir: "factors",
		Tasks:       []setupconfig.FactorSetupTask{configTask()},
		Definitions: []setupconfig.FactorSetupDefinition{{FactorID: "missing", File: "lib.yaml"}},
	}}
	_, err := loadSetupFactors(manifest, root)
	require.ErrorContains(t, err, `has no factor "missing"`)
	bad := "- factor_id: x\n  name: X\n  spec: x\n  factor_type: timeseries\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "factors", "bad.yaml"), []byte(bad), 0o600))
	manifest.Factors.Definitions = []setupconfig.FactorSetupDefinition{{FactorID: "x", File: "bad.yaml"}}
	_, err = loadSetupFactors(manifest, root)
	require.ErrorContains(t, err, "factor_type", "unknown library fields are rejected")
	manifest.Factors.Definitions = []setupconfig.FactorSetupDefinition{{FactorID: "Bias", File: "lib.yaml"}}
	_, err = loadSetupFactors(manifest, root)
	require.ErrorContains(t, err, "factor_id is invalid", "factor IDs follow the factorname rules")
}

func TestLoadSetupFactorsRequiresExplicitSetup(t *testing.T) {
	_, err := loadSetupFactors(setupconfig.Manifest{Factors: setupconfig.FactorSetup{
		Enabled: true, SourceDir: "modules/factor/factors",
	}}, t.TempDir())
	require.ErrorContains(t, err, "factors.enabled requires")
}

func TestLoadSetupFactorsRejectsSourcePathEscape(t *testing.T) {
	_, err := loadSetupFactors(setupconfig.Manifest{Factors: setupconfig.FactorSetup{
		Enabled: true, SourceDir: "./examples/factors",
		Tasks:       []setupconfig.FactorSetupTask{configTask()},
		Definitions: []setupconfig.FactorSetupDefinition{{FactorID: "bias", File: "../bias.yaml"}},
	}}, t.TempDir())
	require.Error(t, err)
}

func TestLoadSetupFactorsRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	factorsDir := filepath.Join(root, "examples", "factors")
	require.NoError(t, os.MkdirAll(factorsDir, 0o755))
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	require.NoError(t, os.WriteFile(outside, []byte(testLibrary), 0o600))
	link := filepath.Join(factorsDir, "outside.yaml")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := loadSetupFactors(setupconfig.Manifest{Factors: setupconfig.FactorSetup{
		Enabled: true, SourceDir: "./examples/factors",
		Tasks:       []setupconfig.FactorSetupTask{configTask()},
		Definitions: []setupconfig.FactorSetupDefinition{{FactorID: "bias", File: "outside.yaml"}},
	}}, root)
	require.Error(t, err)
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

// 先定义、再任务、最后成员；逐项结果带成员键与全部列名。
func TestApplyCreatesDefinitionsThenTasksThenMembers(t *testing.T) {
	fake := newFakeFactorMgr()
	fake.paramDecls["bias"] = []factorAPIParam{{Name: "window", DefaultValue: 5}}
	applier, out := newApplier(fake)
	result, err := applier.Apply(context.Background(), testSetupPlan())
	require.NoError(t, err)
	require.Equal(t, 1, result.TasksCreated)
	require.Equal(t, 1, result.Imported)
	require.Equal(t, 1, result.MembersAdded)
	require.Equal(t, 1, result.MembersEnabled)
	require.Zero(t, result.Conflicts)
	require.Equal(t, []string{"GetFactor", "CreateFactor", "GetFactorTask", "CreateFactorTask", "AddTaskMember", "SetTaskMemberStatus"}, fake.calls)
	require.Equal(t, "行情因子", fake.createdTasks[0]["name"])
	require.Equal(t, "pending", fake.createdTasks[0]["status"])
	require.NotContains(t, fake.createdFactors[0], "task_id")
	require.Equal(t, []setupFactorItem{
		{Kind: "definition", ID: "bias", Result: itemCreated},
		{Kind: "task", ID: "ftask_prices_1m", Result: itemCreated, Detail: "行情因子"},
		{Kind: "member", ID: "ftask_prices_1m/bias", Result: itemCreated, MemberKey: "bias", Columns: []string{"bias"}, Detail: "状态改为 enabled"},
	}, result.Items)
	require.Contains(t, out.String(), "因子成员 ftask_prices_1m/bias：新建，成员键 bias，列 bias")
	require.Contains(t, out.String(), "因子任务 ftask_prices_1m：新建")
}

func TestApplyIsIdempotent(t *testing.T) {
	fake := newFakeFactorMgr()
	fake.paramDecls["bias"] = []factorAPIParam{{Name: "window", DefaultValue: 5}}
	applier, _ := newApplier(fake)
	_, err := applier.Apply(context.Background(), testSetupPlan())
	require.NoError(t, err)
	fake.calls = nil

	again, _ := newApplier(fake)
	result, err := again.Apply(context.Background(), testSetupPlan())
	require.NoError(t, err)
	require.Equal(t, 1, result.Unchanged)
	require.Equal(t, 1, result.MembersUnchanged)
	require.Zero(t, result.TasksCreated+result.Imported+result.MembersAdded+result.MembersEnabled+result.Conflicts)
	for _, item := range result.Items {
		require.Equal(t, itemExists, item.Result, "%+v", item)
	}
	for _, mutation := range []string{"CreateFactor", "CreateFactorTask", "AddTaskMember", "SetTaskMemberStatus"} {
		require.Zero(t, fake.callsOf(mutation), mutation)
	}
}

func TestApplyResumesPendingFactorTask(t *testing.T) {
	plan := testSetupPlan()
	fake := newFakeFactorMgr()
	fake.definitions["bias"] = plan.Definitions[0]
	fake.tasks["ftask_prices_1m"] = setupFactorTask{TaskID: "ftask_prices_1m", Name: "行情因子", SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m"}
	fake.taskStatus["ftask_prices_1m"] = "pending"
	applier, _ := newApplier(fake)
	result, err := applier.Apply(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, 1, fake.callsOf("CreateFactorTask"), "创建被中断的任务要先接着建好，再加成员")
	require.Zero(t, result.TasksCreated)
}

// 任务的身份（空间、源数据集、频率、名称）创建后不能改：与已有任务不同时记冲突，其他项照常处理，最后以错误结束。
func TestApplyReportsTaskIdentityConflictAndContinues(t *testing.T) {
	plan := testSetupPlan()
	plan.Tasks = append(plan.Tasks, setupFactorTask{TaskID: "ftask_other", Name: "另一个", SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "5m",
		Members: []setupFactorMember{{FactorID: "bias", Kind: "fixed", Status: "enabled"}}})
	fake := newFakeFactorMgr()
	fake.tasks["ftask_prices_1m"] = setupFactorTask{TaskID: "ftask_prices_1m", Name: "旧名字", SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m"}
	applier, out := newApplier(fake)
	result, err := applier.Apply(context.Background(), plan)
	require.ErrorContains(t, err, "1 项")
	require.Equal(t, 1, result.Conflicts)
	require.Equal(t, 1, result.MembersAdded, "没有冲突的任务照常处理")
	require.Contains(t, out.String(), "因子任务 ftask_prices_1m：冲突")
	var conflict *setupFactorItem
	for index := range result.Items {
		if result.Items[index].Result == itemConflict {
			conflict = &result.Items[index]
		}
	}
	require.NotNil(t, conflict)
	require.Contains(t, conflict.Detail, "旧名字")
}

func TestApplySkipsIdenticalDefinitionAndComparesHashAndName(t *testing.T) {
	plan := testSetupPlan()
	fake := newFakeFactorMgr()
	fake.definitions["bias"] = plan.Definitions[0]
	applier, _ := newApplier(fake)
	result, err := applier.Apply(context.Background(), plan)
	require.NoError(t, err)
	require.Zero(t, result.Imported)
	require.Equal(t, 1, result.Unchanged)
	require.Empty(t, fake.createdFactors)

	// 规格不同：冲突，引用它的成员本次不处理。
	changed := plan.Definitions[0]
	changed.Spec = "output: close / ts_mean(close, 10)\n"
	fake = newFakeFactorMgr()
	fake.definitions["bias"] = changed
	applier, _ = newApplier(fake)
	result, err = applier.Apply(context.Background(), plan)
	require.Error(t, err)
	require.Equal(t, 2, result.Conflicts, "定义冲突一项，引用它的成员跳过一项")
	require.Zero(t, result.MembersAdded)
	// 名称不同：同样冲突。
	renamed := plan.Definitions[0]
	renamed.Name = "Other"
	fake = newFakeFactorMgr()
	fake.definitions["bias"] = renamed
	applier, _ = newApplier(fake)
	_, err = applier.Apply(context.Background(), plan)
	require.Error(t, err)
	// 只差格式（编译后 definition_hash 相同）不算不同。
	formatted := plan.Definitions[0]
	formatted.Spec = "\n" + plan.Definitions[0].Spec + "\n\n"
	fake = newFakeFactorMgr()
	fake.definitions["bias"] = formatted
	applier, _ = newApplier(fake)
	_, err = applier.Apply(context.Background(), plan)
	require.NoError(t, err)
}

// 因子库里的分类先建立（同一分类只调一次），新定义带上分类；已有定义的分类不同时改成因子库里的（分类不进 definition_hash）。
func TestApplySyncsCategories(t *testing.T) {
	plan := testSetupPlan()
	plan.Definitions[0].CategoryID, plan.Definitions[0].CategoryName = "momentum", "动量类"
	plan.Definitions = append(plan.Definitions, setupFactorDefinition{FactorID: "cci", Name: "CCI", Spec: "output: close\n", CategoryID: "momentum", CategoryName: "动量类"})
	fake := newFakeFactorMgr()
	applier, _ := newApplier(fake)
	_, err := applier.Apply(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, []string{"momentum=动量类"}, fake.categories)
	require.Equal(t, "momentum", fake.createdFactors[0]["category_id"])

	fake = newFakeFactorMgr()
	fake.definitions["bias"] = plan.Definitions[0]
	fake.definitionCategory["bias"] = "uncategorized"
	plan.Definitions = plan.Definitions[:1]
	applier, _ = newApplier(fake)
	_, err = applier.Apply(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, []string{"bias=momentum"}, fake.movedCategories)
}

// 固定参数与可变成员：没给的参数用定义的默认值补全；成员键由服务端推导；列名随结果带回。
func TestApplyAddsFixedAndVariableMembers(t *testing.T) {
	plan := testSetupPlan()
	plan.Tasks[0].Members = []setupFactorMember{
		{FactorID: "bias", Kind: "fixed", Status: "enabled"},
		{FactorID: "bias", Kind: "fixed", Params: map[string]float64{"window": 10}, Status: "enabled"},
		{FactorID: "bias", Kind: "var", Status: "disabled"},
	}
	fake := newFakeFactorMgr()
	fake.paramDecls["bias"] = []factorAPIParam{{Name: "window", DefaultValue: 5}}
	applier, _ := newApplier(fake)
	result, err := applier.Apply(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, 3, result.MembersAdded)
	var keys []string
	for _, item := range result.Items {
		if item.Kind == "member" {
			keys = append(keys, item.MemberKey)
		}
	}
	require.Equal(t, []string{"bias", "bias__10", "bias__var"}, keys)
	statuses := map[string]string{}
	for _, member := range fake.members["ftask_prices_1m"] {
		statuses[member.MemberKey] = member.Status
	}
	require.Equal(t, map[string]string{"bias": "enabled", "bias__10": "enabled", "bias__var": "disabled"}, statuses, "配置要求停用的成员保持新增时的停用")
}

// 再次执行不重复添加：按（因子、类型、补全后的完整参数）与现有成员比较；状态不同时只改状态。
func TestApplyMatchesExistingMembersByFactorKindAndParams(t *testing.T) {
	plan := testSetupPlan()
	plan.Tasks[0].Members = []setupFactorMember{
		{FactorID: "bias", Kind: "fixed", Status: "enabled"},
		{FactorID: "bias", Kind: "fixed", Params: map[string]float64{"window": 10}, Status: "enabled"},
		{FactorID: "bias", Kind: "var", Status: "enabled"},
	}
	fake := newFakeFactorMgr()
	fake.definitions["bias"] = plan.Definitions[0]
	fake.paramDecls["bias"] = []factorAPIParam{{Name: "window", DefaultValue: 5}}
	fake.tasks["ftask_prices_1m"] = setupFactorTask{TaskID: "ftask_prices_1m", Name: "行情因子", SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m"}
	fake.members["ftask_prices_1m"] = []factorAPIMember{
		{TaskID: "ftask_prices_1m", FactorID: "bias", MemberKey: "bias", Kind: "fixed", Status: "enabled", Params: map[string]float64{"window": 5}, OutputColumns: []string{"bias"}},
		{TaskID: "ftask_prices_1m", FactorID: "bias", MemberKey: "bias__10", Kind: "fixed", Status: "enabled", Params: map[string]float64{"window": 10}, OutputColumns: []string{"bias__10"}},
		{TaskID: "ftask_prices_1m", FactorID: "bias", MemberKey: "bias__var", Kind: "var", Status: "disabled", Params: map[string]float64{"window": 5}, OutputColumns: []string{"bias__var"}},
	}
	applier, _ := newApplier(fake)
	result, err := applier.Apply(context.Background(), plan)
	require.NoError(t, err)
	require.Zero(t, result.MembersAdded)
	require.Equal(t, 2, result.MembersUnchanged)
	require.Equal(t, 1, result.MembersEnabled)
	require.Equal(t, 1, fake.callsOf("SetTaskMemberStatus"))
}

// 可变成员的默认参数加成员后不能改：配置里的默认参数与现有不同时记冲突，要换参数先移除成员再重新加入。
func TestApplyReportsVariableMemberDefaultsConflict(t *testing.T) {
	plan := testSetupPlan()
	plan.Tasks[0].Members = []setupFactorMember{{FactorID: "bias", Kind: "var", Params: map[string]float64{"window": 20}, Status: "enabled"}}
	fake := newFakeFactorMgr()
	fake.definitions["bias"] = plan.Definitions[0]
	fake.paramDecls["bias"] = []factorAPIParam{{Name: "window", DefaultValue: 5}}
	fake.tasks["ftask_prices_1m"] = setupFactorTask{TaskID: "ftask_prices_1m", Name: "行情因子", SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m"}
	fake.members["ftask_prices_1m"] = []factorAPIMember{{TaskID: "ftask_prices_1m", FactorID: "bias", MemberKey: "bias__var", Kind: "var", Status: "enabled", Params: map[string]float64{"window": 5}}}
	applier, _ := newApplier(fake)
	result, err := applier.Apply(context.Background(), plan)
	require.Error(t, err)
	require.Equal(t, 1, result.Conflicts)
	require.Zero(t, fake.callsOf("AddTaskMember"))
}

// 只增不减：任务里有、配置里已去掉的成员只报告，不移除；配置没有的定义也不删。
func TestApplyReportsOrphanMembersWithoutRemoving(t *testing.T) {
	plan := testSetupPlan()
	fake := newFakeFactorMgr()
	fake.definitions["bias"] = plan.Definitions[0]
	fake.tasks["ftask_prices_1m"] = setupFactorTask{TaskID: "ftask_prices_1m", Name: "行情因子", SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m"}
	fake.members["ftask_prices_1m"] = []factorAPIMember{
		{TaskID: "ftask_prices_1m", FactorID: "bias", MemberKey: "bias", Kind: "fixed", Status: "enabled"},
		{TaskID: "ftask_prices_1m", FactorID: "cci", MemberKey: "cci", Kind: "fixed", Status: "enabled"},
	}
	applier, out := newApplier(fake)
	result, err := applier.Apply(context.Background(), plan)
	require.NoError(t, err)
	require.Zero(t, fake.callsOf("RemoveTaskMember")+fake.callsOf("DeleteFactor")+fake.callsOf("SetTaskMemberStatus"))
	var orphans []string
	for _, item := range result.Items {
		if item.Result == itemOrphan {
			orphans = append(orphans, item.MemberKey)
		}
	}
	require.Equal(t, []string{"cci"}, orphans)
	require.Contains(t, out.String(), "配置已去掉")
}

// The CLI parses raw JSON instead of importing factorgen, so pin the shapes it
// reads against what protobuf JSON emits for FactorMgr.
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
	require.Equal(t, "i1", task.FactorTask.BoundInstanceID)
	require.Equal(t, []factorAPIMember{{TaskID: "ftask_a", FactorID: "bias", MemberKey: "bias__var", Kind: "var", Status: "enabled", Params: map[string]float64{"window": 5}, OutputColumns: []string{"bias__var"}}}, task.Members)
}
