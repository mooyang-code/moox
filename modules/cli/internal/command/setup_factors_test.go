package command

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/stretchr/testify/require"
)

type fakeFactorJSONClient struct {
	calls         []string
	createdIDs    []string
	createdSetIDs []string
	addedMembers  []string
	statuses      []string
	createdBodies []map[string]any
	existing      *setupFactorDefinition
	usages        []factorAPIUsage
	existingSet   *setupFactorSet
	setStatus     string
	// paramDefaults 是假服务端上定义声明的参数（名称与默认值）。
	paramDefaults []factorAPIParam
	// existingCategory 是已有定义的分类；categories、movedCategories 记录分类的建立与改动。
	existingCategory string
	categories       []string
	movedCategories  []string
}

func (f *fakeFactorJSONClient) defaultParams() []factorAPIParam {
	return append([]factorAPIParam(nil), f.paramDefaults...)
}

// fakeDefinitionHash 模拟 FactorMgr 编译后的 definition_hash：忽略首尾空白。
func fakeDefinitionHash(spec string) string { return "dh:" + strings.TrimSpace(spec) }

func (f *fakeFactorJSONClient) CallGatewayJSON(_ context.Context, service string, method string, body, response any) error {
	path := service + "/" + method
	f.calls = append(f.calls, path)
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
	switch path {
	case "trpc.moox.factor.FactorMgr/GetFactorSet":
		if f.existingSet == nil {
			result.RetInfo.Code, result.RetInfo.Msg = 9, "not found"
		} else {
			result.FactorSet.SetID = f.existingSet.SetID
			result.FactorSet.SpaceID = f.existingSet.SpaceID
			result.FactorSet.SourceDatasetID = f.existingSet.SourceDatasetID
			result.FactorSet.Freq = f.existingSet.Freq
			result.FactorSet.Status = f.setStatus
		}
	case "trpc.moox.factor.FactorMgr/CreateFactorSet":
		set := request["factor_set"].(map[string]any)
		f.createdSetIDs = append(f.createdSetIDs, set["set_id"].(string))
	case "trpc.moox.factor.FactorMgr/GetFactor":
		if f.existing == nil {
			result.RetInfo.Code, result.RetInfo.Msg = 9, "not found"
		} else {
			result.Factor.FactorID = f.existing.FactorID
			result.Factor.Name = f.existing.Name
			result.Factor.CategoryID = f.existingCategory
			result.Factor.DefinitionHash = fakeDefinitionHash(f.existing.Spec)
			result.Factor.Params = f.defaultParams()
			result.Usages = append([]factorAPIUsage(nil), f.usages...)
		}
	case "trpc.moox.factor.FactorMgr/ValidateFactorSpec":
		result.Valid, result.DefinitionHash = true, fakeDefinitionHash(request["spec"].(string))
	case "trpc.moox.factor.FactorMgr/CreateFactor":
		factor := request["factor"].(map[string]any)
		f.createdBodies = append(f.createdBodies, factor)
		f.createdIDs = append(f.createdIDs, factor["factor_id"].(string))
		result.Factor.Params = f.defaultParams()
	case "trpc.moox.factor.FactorMgr/AddFactorToSet":
		alias, _ := request["member_alias"].(string)
		if alias == "" {
			alias = request["factor_id"].(string) // 模拟服务端：不给别名时按参数生成，全默认时就是因子 ID
			if params, ok := request["params"].(map[string]any); ok && len(params) > 0 {
				alias += "_p"
			}
		}
		result.Member.MemberAlias = alias
		f.addedMembers = append(f.addedMembers, request["set_id"].(string)+"/"+alias+"="+request["factor_id"].(string))
	case "trpc.moox.factor.FactorMgr/SetFactorMemberStatus":
		f.statuses = append(f.statuses, request["member_alias"].(string)+"="+request["status"].(string))
	case "trpc.moox.factor.FactorMgr/UpsertFactorCategory":
		category := request["category"].(map[string]any)
		f.categories = append(f.categories, category["category_id"].(string)+"="+category["name"].(string))
	case "trpc.moox.factor.FactorMgr/SetFactorCategory":
		f.movedCategories = append(f.movedCategories, request["factor_id"].(string)+"="+request["category_id"].(string))
	}
	return nil
}

const testLibrary = `- factor_id: bias
  name: Bias 偏离
  spec: |
    params:
      window: {type: int, min: 2, max: 60, default: 5}
    outputs:
      bias: close / ts_mean(close, window)
- factor_id: Cci
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

func TestLoadSetupFactorsReadsYAMLLibrary(t *testing.T) {
	root := t.TempDir()
	writeLibrary(t, filepath.Join(root, "examples", "factors"), "lib.yaml")
	plan, err := loadSetupFactors(setupconfig.Manifest{Factors: setupconfig.FactorSetup{
		Enabled: true, SourceDir: "./examples/factors",
		Sets:        []setupconfig.FactorSetupSet{{SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m"}},
		Definitions: []setupconfig.FactorSetupDefinition{{FactorID: "bias", File: "lib.yaml"}},
		Members:     []setupconfig.FactorSetupMember{{SourceDatasetID: "dataset_prices", Freq: "1m", FactorID: "bias"}},
	}}, root)
	require.NoError(t, err)
	require.Len(t, plan.Definitions, 1)
	require.Equal(t, "bias", plan.Definitions[0].FactorID)
	require.Equal(t, "Bias 偏离", plan.Definitions[0].Name)
	require.Contains(t, plan.Definitions[0].Spec, "ts_mean(close, window)")
	require.Len(t, plan.Sets, 1)
	require.Equal(t, "crypto", plan.Sets[0].SpaceID)
	require.Equal(t, "fset_prices_1m", plan.Sets[0].SetID)
	require.Equal(t, []setupFactorMember{{SetID: "fset_prices_1m", FactorID: "bias", Status: "enabled"}}, plan.Members)
}

func TestLoadSetupFactorsRejectsUnknownFactorAndBadLibrary(t *testing.T) {
	root := t.TempDir()
	writeLibrary(t, filepath.Join(root, "factors"), "lib.yaml")
	manifest := setupconfig.Manifest{Factors: setupconfig.FactorSetup{
		Enabled: true, SourceDir: "factors",
		Sets:        []setupconfig.FactorSetupSet{{SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m"}},
		Definitions: []setupconfig.FactorSetupDefinition{{FactorID: "Missing", File: "lib.yaml"}},
	}}
	_, err := loadSetupFactors(manifest, root)
	require.ErrorContains(t, err, `has no factor "Missing"`)
	bad := "- factor_id: X\n  name: X\n  spec: x\n  factor_type: timeseries\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "factors", "bad.yaml"), []byte(bad), 0o600))
	manifest.Factors.Definitions = []setupconfig.FactorSetupDefinition{{FactorID: "X", File: "bad.yaml"}}
	_, err = loadSetupFactors(manifest, root)
	require.ErrorContains(t, err, "factor_type", "unknown library fields are rejected")
}

func TestLoadSetupFactorsRequiresMatchingSet(t *testing.T) {
	root := t.TempDir()
	writeLibrary(t, filepath.Join(root, "factors"), "lib.yaml")
	_, err := loadSetupFactors(setupconfig.Manifest{Factors: setupconfig.FactorSetup{
		Enabled: true, SourceDir: "factors",
		Definitions: []setupconfig.FactorSetupDefinition{{FactorID: "Cci", File: "lib.yaml"}},
		Members:     []setupconfig.FactorSetupMember{{SourceDatasetID: "dataset_prices", Freq: "1m", FactorID: "Cci"}},
	}}, root)
	require.ErrorContains(t, err, "no matching set")
}

func TestLoadSetupFactorsMatchesFrequencyExactly(t *testing.T) {
	root := t.TempDir()
	writeLibrary(t, filepath.Join(root, "factors"), "lib.yaml")
	_, err := loadSetupFactors(setupconfig.Manifest{Factors: setupconfig.FactorSetup{
		Enabled: true, SourceDir: "factors",
		Sets:        []setupconfig.FactorSetupSet{{SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1mo"}},
		Definitions: []setupconfig.FactorSetupDefinition{{FactorID: "Cci", File: "lib.yaml"}},
		Members:     []setupconfig.FactorSetupMember{{SourceDatasetID: "dataset_prices", Freq: "1m", FactorID: "Cci"}},
	}}, root)
	require.ErrorContains(t, err, "no matching set")
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
		Sets:        []setupconfig.FactorSetupSet{{SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m"}},
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
		Sets:        []setupconfig.FactorSetupSet{{SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m"}},
		Definitions: []setupconfig.FactorSetupDefinition{{FactorID: "bias", File: "outside.yaml"}},
	}}, root)
	require.Error(t, err)
}

func testSetupPlan() setupFactorPlan {
	return setupFactorPlan{
		Sets:        []setupFactorSet{{SetID: "fset_prices_1m", SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m"}},
		Definitions: []setupFactorDefinition{{FactorID: "Bias", Name: "Bias", Spec: "outputs:\n  bias: close / ts_mean(close, 5)\n"}},
		Members:     []setupFactorMember{{SetID: "fset_prices_1m", FactorID: "Bias", Status: "enabled"}},
	}
}

func TestApplyCreatesSetsThenDefinitionsThenMembers(t *testing.T) {
	client := &fakeFactorJSONClient{}
	plan := testSetupPlan()
	result, err := (&remoteSetupFactor{client: client}).Apply(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, setupFactorSummary{Enabled: true, Definitions: 1, Members: 1, SetsCreated: 1, Imported: 1, MembersAdded: 1, MembersEnabled: 1}, result)
	require.Equal(t, []string{
		"trpc.moox.factor.FactorMgr/GetFactorSet",
		"trpc.moox.factor.FactorMgr/CreateFactorSet",
		"trpc.moox.factor.FactorMgr/GetFactor",
		"trpc.moox.factor.FactorMgr/CreateFactor",
		"trpc.moox.factor.FactorMgr/AddFactorToSet",
		"trpc.moox.factor.FactorMgr/SetFactorMemberStatus",
	}, client.calls)
	require.Equal(t, []string{"Bias=enabled"}, client.statuses)
	require.Equal(t, []string{"fset_prices_1m/Bias=Bias"}, client.addedMembers, "初始化的成员别名就是 factor_id")
	require.Equal(t, []string{"fset_prices_1m"}, client.createdSetIDs)
	require.Len(t, client.createdBodies, 1)
	require.NotContains(t, client.createdBodies[0], "set_id")
	require.NotContains(t, client.createdBodies[0], "status")
	require.NotContains(t, client.createdBodies[0], "source_code")
	require.Equal(t, plan.Definitions[0].Spec, client.createdBodies[0]["spec"])
}

func TestApplyIsIdempotentWhenNothingChanged(t *testing.T) {
	plan := testSetupPlan()
	client := &fakeFactorJSONClient{
		existingSet: &plan.Sets[0], existing: &plan.Definitions[0],
		usages: []factorAPIUsage{{SetID: "fset_prices_1m", Status: "enabled"}},
	}
	result, err := (&remoteSetupFactor{client: client}).Apply(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, setupFactorSummary{Enabled: true, Definitions: 1, Members: 1, Unchanged: 1, MembersUnchanged: 1}, result)
	require.Equal(t, []string{"trpc.moox.factor.FactorMgr/GetFactorSet", "trpc.moox.factor.FactorMgr/GetFactor",
		"trpc.moox.factor.FactorMgr/ValidateFactorSpec"}, client.calls)
}

func TestApplyResumesPendingFactorSet(t *testing.T) {
	plan := testSetupPlan()
	client := &fakeFactorJSONClient{
		existingSet: &plan.Sets[0], setStatus: "pending", existing: &plan.Definitions[0],
		usages: []factorAPIUsage{{SetID: "fset_prices_1m", Status: "enabled"}},
	}
	_, err := (&remoteSetupFactor{client: client}).Apply(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, []string{"trpc.moox.factor.FactorMgr/GetFactorSet", "trpc.moox.factor.FactorMgr/CreateFactorSet",
		"trpc.moox.factor.FactorMgr/GetFactor", "trpc.moox.factor.FactorMgr/ValidateFactorSpec"}, client.calls,
		"a set left pending by an interrupted creation must be resumed before members are added")
}

func TestApplySkipsIdenticalDefinition(t *testing.T) {
	plan := testSetupPlan()
	client := &fakeFactorJSONClient{existingSet: &plan.Sets[0], existing: &plan.Definitions[0]}
	result, err := (&remoteSetupFactor{client: client}).Apply(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, 0, result.Imported)
	require.Equal(t, 1, result.Unchanged)
	require.Empty(t, client.createdIDs)
	require.Equal(t, []string{"fset_prices_1m/Bias=Bias"}, client.addedMembers)
}

// 因子库里的分类先建立（同一分类只调一次），新定义带上分类；已有定义的分类不同时改成因子库里的（分类不进 definition_hash）。
func TestApplySyncsCategoriesAndIgnoresOtherAliases(t *testing.T) {
	plan := testSetupPlan()
	plan.Definitions[0].CategoryID, plan.Definitions[0].CategoryName = "momentum", "动量类"
	plan.Definitions = append(plan.Definitions, setupFactorDefinition{FactorID: "Cci", Name: "CCI", Spec: "outputs:\n  cci: close\n",
		CategoryID: "momentum", CategoryName: "动量类"})
	client := &fakeFactorJSONClient{}
	_, err := (&remoteSetupFactor{client: client}).Apply(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, []string{"momentum=动量类"}, client.categories)
	require.Equal(t, "momentum", client.createdBodies[0]["category_id"])

	existing := plan.Definitions[0]
	client = &fakeFactorJSONClient{existingSet: &plan.Sets[0], existing: &existing, existingCategory: "uncategorized",
		usages: []factorAPIUsage{{SetID: "fset_prices_1m", MemberAlias: "bias_fast", Status: "enabled"}}}
	plan.Definitions = plan.Definitions[:1]
	_, err = (&remoteSetupFactor{client: client}).Apply(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, []string{"Bias=momentum"}, client.movedCategories)
	require.Empty(t, client.addedMembers, "没有参数的因子在同一任务里只能有一个成员：别名 bias_fast 的成员就是它，服务端也会拒绝重复")
}

func TestApplyComparesDefinitionHashAndName(t *testing.T) {
	plan := testSetupPlan()
	got := plan.Definitions[0]
	got.Spec = "outputs:\n  bias: close / ts_mean(close, 10)\n"
	client := &fakeFactorJSONClient{existingSet: &plan.Sets[0], existing: &got}
	_, err := (&remoteSetupFactor{client: client}).Apply(context.Background(), plan)
	require.ErrorContains(t, err, "different definition")
	got = plan.Definitions[0]
	got.Name = "Other"
	_, err = (&remoteSetupFactor{client: client}).Apply(context.Background(), plan)
	require.ErrorContains(t, err, "different definition")
	// 只差格式（编译后 definition_hash 相同）不算不同。
	got = plan.Definitions[0]
	got.Spec = "\n" + plan.Definitions[0].Spec + "\n\n"
	_, err = (&remoteSetupFactor{client: client}).Apply(context.Background(), plan)
	require.NoError(t, err)
}

func TestApplyDoesNotPruneExtraMembers(t *testing.T) {
	plan := testSetupPlan()
	client := &fakeFactorJSONClient{
		existingSet: &plan.Sets[0], existing: &plan.Definitions[0],
		usages: []factorAPIUsage{{SetID: "fset_prices_1m", Status: "enabled"}, {SetID: "fset_manual_5m", Status: "enabled"}},
	}
	_, err := (&remoteSetupFactor{client: client}).Apply(context.Background(), plan)
	require.NoError(t, err)
	for _, call := range client.calls {
		require.NotContains(t, call, "RemoveFactorFromSet")
		require.NotContains(t, call, "DeleteFactor")
	}
	require.Empty(t, client.statuses)
}

func paramPlan(members ...setupFactorMember) setupFactorPlan {
	plan := testSetupPlan()
	plan.Members = members
	return plan
}

// 同一个因子带不同固定参数多次加入同一个任务：没给的参数用定义的默认值补全；别名缺省时由服务端生成。
func TestApplyAddsMembersWithFixedParams(t *testing.T) {
	plan := paramPlan(
		setupFactorMember{SetID: "fset_prices_1m", FactorID: "Bias", Status: "enabled"},
		setupFactorMember{SetID: "fset_prices_1m", FactorID: "Bias", Params: map[string]float64{"window": 10}, Status: "enabled"},
		setupFactorMember{SetID: "fset_prices_1m", FactorID: "Bias", Alias: "bias_slow", Params: map[string]float64{"window": 30}, Status: "disabled"},
	)
	client := &fakeFactorJSONClient{paramDefaults: []factorAPIParam{{Name: "window", DefaultValue: 5}}}
	result, err := (&remoteSetupFactor{client: client}).Apply(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, 3, result.MembersAdded)
	require.Equal(t, []string{"fset_prices_1m/Bias=Bias", "fset_prices_1m/Bias_p=Bias", "fset_prices_1m/bias_slow=Bias"}, client.addedMembers)
	require.Equal(t, []string{"Bias=enabled", "Bias_p=enabled"}, client.statuses, "按服务端返回的别名设置状态；配置要求停用的成员保持新增时的停用")
}

// 再次执行不重复添加：按完整参数（用定义的默认值补全）与现有成员比较，别名相同的成员按别名认。
func TestApplyMatchesExistingMembersByAliasAndParams(t *testing.T) {
	plan := paramPlan(
		setupFactorMember{SetID: "fset_prices_1m", FactorID: "Bias", Status: "enabled"},
		setupFactorMember{SetID: "fset_prices_1m", FactorID: "Bias", Params: map[string]float64{"window": 10}, Status: "enabled"},
		setupFactorMember{SetID: "fset_prices_1m", FactorID: "Bias", Alias: "bias_slow", Params: map[string]float64{"window": 30}, Status: "enabled"},
	)
	client := &fakeFactorJSONClient{
		existingSet: &plan.Sets[0], existing: &plan.Definitions[0],
		paramDefaults: []factorAPIParam{{Name: "window", DefaultValue: 5}},
		usages: []factorAPIUsage{
			{SetID: "fset_prices_1m", MemberAlias: "Bias", Status: "enabled", Params: map[string]float64{"window": 5}},
			{SetID: "fset_prices_1m", MemberAlias: "Bias_10", Status: "enabled", Params: map[string]float64{"window": 10}},
			{SetID: "fset_prices_1m", MemberAlias: "bias_slow", Status: "disabled", Params: map[string]float64{"window": 30}},
			{SetID: "fset_other_5m", MemberAlias: "Bias", Status: "enabled", Params: map[string]float64{"window": 5}},
		},
	}
	result, err := (&remoteSetupFactor{client: client}).Apply(context.Background(), plan)
	require.NoError(t, err)
	require.Empty(t, client.addedMembers)
	require.Equal(t, 2, result.MembersUnchanged)
	require.Equal(t, 1, result.MembersEnabled)
	require.Equal(t, []string{"bias_slow=enabled"}, client.statuses)
}

// 固定参数加成员后不再改：别名已存在但参数不同时报错，要换参数请换别名。
func TestApplyRejectsAliasWithDifferentParams(t *testing.T) {
	plan := paramPlan(setupFactorMember{SetID: "fset_prices_1m", FactorID: "Bias", Alias: "bias_slow", Params: map[string]float64{"window": 31}, Status: "enabled"})
	client := &fakeFactorJSONClient{
		existingSet: &plan.Sets[0], existing: &plan.Definitions[0],
		paramDefaults: []factorAPIParam{{Name: "window", DefaultValue: 5}},
		usages:        []factorAPIUsage{{SetID: "fset_prices_1m", MemberAlias: "bias_slow", Status: "enabled", Params: map[string]float64{"window": 30}}},
	}
	_, err := (&remoteSetupFactor{client: client}).Apply(context.Background(), plan)
	require.ErrorContains(t, err, "different params")
}

// The CLI parses raw JSON instead of importing factorgen, so pin the shapes it
// reads against what protobuf JSON emits for FactorMgr.
func TestFactorAPIResponseMatchesFactorProtoJSON(t *testing.T) {
	var get factorAPIResponse
	require.NoError(t, json.Unmarshal([]byte(`{"ret_info":{"code":0},"factor":{"factor_id":"Bias","factor_type":"timeseries","name":"Bias","spec":"outputs: {}","spec_hash":"sha256:x","input_columns":["close"],"outputs":["bias"],"lookback_periods":20,"definition_hash":"sha256:d"},"usages":[{"set_id":"fset_a_1m","status":"enabled"}]}`), &get))
	require.Equal(t, "Bias", get.Factor.FactorID)
	require.Equal(t, "sha256:d", get.Factor.DefinitionHash)
	require.Equal(t, []factorAPIUsage{{SetID: "fset_a_1m", Status: "enabled"}}, get.Usages)

	var validated factorAPIResponse
	require.NoError(t, json.Unmarshal([]byte(`{"ret_info":{"code":0},"valid":false,"diagnostic":{"path":"outputs.bias","line":1,"column":3,"message":"未知函数"}}`), &validated))
	require.False(t, validated.Valid)
	require.Equal(t, "未知函数", validated.Diagnostic.Message)

	var list factorAPIResponse
	require.NoError(t, json.Unmarshal([]byte(`{"ret_info":{"code":0},"factors":[{"factor":{"factor_id":"Cci"},"usages":[{"set_id":"fset_a_1m","status":"disabled"}]}]}`), &list))
	require.Len(t, list.Factors, 1)
	require.Equal(t, "Cci", list.Factors[0].Factor.FactorID)
	require.Equal(t, "disabled", list.Factors[0].Usages[0].Status)
}
