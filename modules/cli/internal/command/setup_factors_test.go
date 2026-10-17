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
	updatedSetIDs []string
	addedMembers  []string
	statuses      []string
	createdBodies []map[string]any
	existing      *setupFactorDefinition
	usages        []factorAPIUsage
	existingSet   *setupFactorSet
	setStatus     string
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
			result.FactorSet.SubjectMode = f.existingSet.SubjectMode
			result.FactorSet.Subjects = append([]string(nil), f.existingSet.Subjects...)
			result.FactorSet.Status = f.setStatus
		}
	case "trpc.moox.factor.FactorMgr/CreateFactorSet":
		set := request["factor_set"].(map[string]any)
		f.createdSetIDs = append(f.createdSetIDs, set["set_id"].(string))
	case "trpc.moox.factor.FactorMgr/UpdateFactorSet":
		f.updatedSetIDs = append(f.updatedSetIDs, request["set_id"].(string))
	case "trpc.moox.factor.FactorMgr/GetFactor":
		if f.existing == nil {
			result.RetInfo.Code, result.RetInfo.Msg = 9, "not found"
		} else {
			result.Factor.FactorID = f.existing.FactorID
			result.Factor.Name = f.existing.Name
			result.Factor.DefinitionHash = fakeDefinitionHash(f.existing.Spec)
			result.Usages = append([]factorAPIUsage(nil), f.usages...)
		}
	case "trpc.moox.factor.FactorMgr/ValidateFactorSpec":
		result.Valid, result.DefinitionHash = true, fakeDefinitionHash(request["spec"].(string))
	case "trpc.moox.factor.FactorMgr/CreateFactor":
		factor := request["factor"].(map[string]any)
		f.createdBodies = append(f.createdBodies, factor)
		f.createdIDs = append(f.createdIDs, factor["factor_id"].(string))
	case "trpc.moox.factor.FactorMgr/AddFactorToSet":
		f.addedMembers = append(f.addedMembers, request["set_id"].(string)+"/"+request["factor_id"].(string))
	case "trpc.moox.factor.FactorMgr/SetFactorMemberStatus":
		f.statuses = append(f.statuses, request["status"].(string))
	}
	return nil
}

const testLibrary = `- factor_id: bias
  name: Bias 偏离
  spec: |
    params: {window: 5}
    outputs:
      bias_{window}: close / ts_mean(close, window)
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
		Sets:        []setupFactorSet{{SetID: "fset_prices_1m", SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m", SubjectMode: "all"}},
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
	require.Equal(t, []string{"enabled"}, client.statuses)
	require.Equal(t, []string{"fset_prices_1m/Bias"}, client.addedMembers)
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
	require.Equal(t, []string{"fset_prices_1m/Bias"}, client.addedMembers)
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

func TestApplyUpdatesExistingSetSubjectScope(t *testing.T) {
	plan := testSetupPlan()
	plan.Sets[0].SubjectMode, plan.Sets[0].Subjects = "include", []string{"BTC"}
	got := plan.Sets[0]
	got.SubjectMode, got.Subjects = "all", nil
	client := &fakeFactorJSONClient{existingSet: &got}
	_, err := (&remoteSetupFactor{client: client}).Apply(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, []string{"fset_prices_1m"}, client.updatedSetIDs)
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
