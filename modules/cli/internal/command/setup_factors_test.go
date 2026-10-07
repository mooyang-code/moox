package command

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/stretchr/testify/require"
)

type fakeFactorJSONClient struct {
	calls         []string
	createdTypes  []string
	createdSetIDs []string
	updatedSetIDs []string
	addedMembers  []string
	statuses      []string
	createdBodies []map[string]any
	existing      *setupFactorDefinition
	usages        []factorAPIUsage
	existingSet   *setupFactorSet
}

func (f *fakeFactorJSONClient) CallJSON(_ context.Context, _ string, path string, body, response any) error {
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
	result.RetInfo.Code = 0
	switch path {
	case "/api/admin/factormgr/GetFactorSet":
		if f.existingSet == nil {
			result.RetInfo.Code, result.RetInfo.Msg = 9, "not found"
		} else {
			result.FactorSet.SetID = f.existingSet.SetID
			result.FactorSet.SpaceID = f.existingSet.SpaceID
			result.FactorSet.SourceDatasetID = f.existingSet.SourceDatasetID
			result.FactorSet.Freq = f.existingSet.Freq
			result.FactorSet.SubjectMode = f.existingSet.SubjectMode
			result.FactorSet.Subjects = append([]string(nil), f.existingSet.Subjects...)
		}
	case "/api/admin/factormgr/CreateFactorSet":
		set := request["factor_set"].(map[string]any)
		f.createdSetIDs = append(f.createdSetIDs, set["set_id"].(string))
	case "/api/admin/factormgr/UpdateFactorSet":
		f.updatedSetIDs = append(f.updatedSetIDs, request["set_id"].(string))
	case "/api/admin/factormgr/GetFactor":
		if f.existing == nil {
			result.RetInfo.Code, result.RetInfo.Msg = 9, "not found"
		} else {
			result.Factor.FactorType = f.existing.FactorType
			result.Factor.FactorID = f.existing.FactorID
			result.Factor.Name = f.existing.Name
			result.Factor.SourceHash = f.existing.SourceHash
			result.Factor.InputColumns = append([]string(nil), f.existing.InputColumns...)
			result.Factor.Outputs = append([]string(nil), f.existing.Outputs...)
			result.Factor.ParamsJSON = f.existing.ParamsJSON
			result.Factor.LookbackPeriods = f.existing.LookbackPeriods
			result.Usages = append([]factorAPIUsage(nil), f.usages...)
		}
	case "/api/admin/factormgr/CreateFactor":
		factor := request["factor"].(map[string]any)
		f.createdBodies = append(f.createdBodies, factor)
		f.createdTypes = append(f.createdTypes, factor["factor_type"].(string))
	case "/api/admin/factormgr/AddFactorToSet":
		f.addedMembers = append(f.addedMembers, request["set_id"].(string)+"/"+request["factor_id"].(string))
	case "/api/admin/factormgr/SetFactorMemberStatus":
		f.statuses = append(f.statuses, request["status"].(string))
	}
	return nil
}

func TestLoadSetupFactorsReadsConfiguredPythonSources(t *testing.T) {
	root := t.TempDir()
	factorsDir := filepath.Join(root, "examples", "factors")
	require.NoError(t, os.MkdirAll(filepath.Join(factorsDir, "timeseries"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(factorsDir, "timeseries", "bias.py"), []byte("def compute(df, params):\n    return df\n"), 0o600))
	plan, err := loadSetupFactors(setupconfig.Manifest{Factors: setupconfig.FactorSetup{
		Enabled: true, SourceDir: "./examples/factors",
		Sets: []setupconfig.FactorSetupSet{{SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m"}},
		Definitions: []setupconfig.FactorSetupDefinition{{
			FactorType: "timeseries", FactorID: "bias", File: "timeseries/bias.py", InputColumns: []string{"close"}, Outputs: []string{"bias_5"},
			ParamsJSON: `{"windows":[5]}`,
		}},
		Members: []setupconfig.FactorSetupMember{{SourceDatasetID: "dataset_prices", Freq: "1m", FactorID: "bias"}},
	}}, root)
	require.NoError(t, err)
	require.Len(t, plan.Definitions, 1)
	require.Equal(t, "bias", plan.Definitions[0].FactorID)
	require.Equal(t, "timeseries", plan.Definitions[0].FactorType)
	require.Equal(t, "bias", plan.Definitions[0].Name)
	require.NotEmpty(t, plan.Definitions[0].SourceHash)
	require.Len(t, plan.Sets, 1)
	require.Equal(t, "crypto", plan.Sets[0].SpaceID)
	require.Equal(t, "fset_prices_1m", plan.Sets[0].SetID)
	require.Equal(t, []setupFactorMember{{SetID: "fset_prices_1m", FactorID: "bias", Status: "enabled"}}, plan.Members)
}

func TestLoadSetupFactorsRequiresMatchingSet(t *testing.T) {
	root := t.TempDir()
	factorsDir := filepath.Join(root, "factors")
	require.NoError(t, os.MkdirAll(factorsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(factorsDir, "Bias.py"), []byte("def compute(df, params):\n    return df\n"), 0o600))
	_, err := loadSetupFactors(setupconfig.Manifest{Factors: setupconfig.FactorSetup{
		Enabled: true, SourceDir: "factors",
		Definitions: []setupconfig.FactorSetupDefinition{{
			FactorType: "timeseries", FactorID: "Bias", File: "Bias.py", InputColumns: []string{"close"}, Outputs: []string{"bias"},
		}},
		Members: []setupconfig.FactorSetupMember{{SourceDatasetID: "dataset_prices", Freq: "1m", FactorID: "Bias"}},
	}}, root)
	require.ErrorContains(t, err, "no matching set")
}

func TestLoadSetupFactorsMatchesFrequencyExactly(t *testing.T) {
	root := t.TempDir()
	factorsDir := filepath.Join(root, "factors")
	require.NoError(t, os.MkdirAll(factorsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(factorsDir, "Bias.py"), []byte("def compute(df, params):\n    return df\n"), 0o600))
	_, err := loadSetupFactors(setupconfig.Manifest{Factors: setupconfig.FactorSetup{
		Enabled: true, SourceDir: "factors",
		Sets: []setupconfig.FactorSetupSet{{SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1M"}},
		Definitions: []setupconfig.FactorSetupDefinition{{
			FactorType: "timeseries", FactorID: "Bias", File: "Bias.py", InputColumns: []string{"close"}, Outputs: []string{"bias"},
		}},
		Members: []setupconfig.FactorSetupMember{{SourceDatasetID: "dataset_prices", Freq: "1m", FactorID: "Bias"}},
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
		Sets: []setupconfig.FactorSetupSet{{SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m"}},
		Definitions: []setupconfig.FactorSetupDefinition{{
			FactorType: "timeseries", FactorID: "bias", File: "../bias.py", InputColumns: []string{"close"}, Outputs: []string{"bias"},
		}},
	}}, t.TempDir())
	require.Error(t, err)
}

func TestLoadSetupFactorsRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	factorsDir := filepath.Join(root, "examples", "factors")
	require.NoError(t, os.MkdirAll(factorsDir, 0o755))
	outside := filepath.Join(t.TempDir(), "outside.py")
	require.NoError(t, os.WriteFile(outside, []byte("print('outside')"), 0o600))
	link := filepath.Join(factorsDir, "outside.py")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := loadSetupFactors(setupconfig.Manifest{Factors: setupconfig.FactorSetup{
		Enabled: true, SourceDir: "./examples/factors",
		Sets: []setupconfig.FactorSetupSet{{SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m"}},
		Definitions: []setupconfig.FactorSetupDefinition{{
			FactorType: "timeseries", FactorID: "outside", File: "outside.py", InputColumns: []string{"close"}, Outputs: []string{"value"},
		}},
	}}, root)
	require.Error(t, err)
}

func TestLoadSetupFactorsRejectsMissingOrUnknownType(t *testing.T) {
	for _, factorType := range []string{"", "unknown"} {
		_, err := loadSetupFactors(setupconfig.Manifest{Factors: setupconfig.FactorSetup{
			Enabled: true, SourceDir: "factors", Definitions: []setupconfig.FactorSetupDefinition{{FactorID: "Bias", FactorType: factorType}},
		}}, t.TempDir())
		require.ErrorContains(t, err, "factor_type")
	}
}

func TestSameFactorContractIgnoresSetAndStatus(t *testing.T) {
	var response factorAPIResponse
	response.Factor.FactorType = "timeseries"
	want := setupFactorDefinition{FactorType: "timeseries"}
	require.True(t, sameFactorContract(response.Factor, want))
	response.Usages = []factorAPIUsage{{SetID: "fset_other_1m", Status: "enabled"}}
	require.True(t, sameFactorContract(response.Factor, want))
	want.FactorType = "cross_section"
	require.False(t, sameFactorContract(response.Factor, want))
}

func testSetupPlan() setupFactorPlan {
	return setupFactorPlan{
		Sets: []setupFactorSet{{SetID: "fset_prices_1m", SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m", SubjectMode: "all"}},
		Definitions: []setupFactorDefinition{{
			FactorType: "timeseries", FactorID: "Bias", Name: "Bias", SourceCode: "def compute(df, params):\n    return df\n", SourceHash: "hash",
			InputColumns: []string{"close"}, Outputs: []string{"bias_5"}, ParamsJSON: "{}", LookbackPeriods: 1,
		}},
		Members: []setupFactorMember{{SetID: "fset_prices_1m", FactorID: "Bias", Status: "enabled"}},
	}
}

func TestApplyCreatesSetsThenDefinitionsThenMembers(t *testing.T) {
	client := &fakeFactorJSONClient{}
	result, err := (&remoteSetupFactor{client: client}).Apply(context.Background(), testSetupPlan())
	require.NoError(t, err)
	require.Equal(t, setupFactorSummary{Enabled: true, Definitions: 1, Members: 1, SetsCreated: 1, Imported: 1, MembersAdded: 1, MembersEnabled: 1}, result)
	require.Equal(t, []string{
		"/api/admin/factormgr/GetFactorSet",
		"/api/admin/factormgr/CreateFactorSet",
		"/api/admin/factormgr/GetFactor",
		"/api/admin/factormgr/CreateFactor",
		"/api/admin/factormgr/AddFactorToSet",
		"/api/admin/factormgr/SetFactorMemberStatus",
	}, client.calls)
	require.Equal(t, []string{"enabled"}, client.statuses)
	require.Equal(t, []string{"fset_prices_1m/Bias"}, client.addedMembers)
	require.Equal(t, []string{"fset_prices_1m"}, client.createdSetIDs)
	require.Len(t, client.createdBodies, 1)
	require.NotContains(t, client.createdBodies[0], "set_id")
	require.NotContains(t, client.createdBodies[0], "status")
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
	require.Equal(t, []string{"/api/admin/factormgr/GetFactorSet", "/api/admin/factormgr/GetFactor"}, client.calls)
}

func TestApplySkipsIdenticalDefinition(t *testing.T) {
	plan := testSetupPlan()
	client := &fakeFactorJSONClient{existingSet: &plan.Sets[0], existing: &plan.Definitions[0]}
	result, err := (&remoteSetupFactor{client: client}).Apply(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, 0, result.Imported)
	require.Equal(t, 1, result.Unchanged)
	require.Empty(t, client.createdTypes)
	require.Equal(t, []string{"fset_prices_1m/Bias"}, client.addedMembers)
}

func TestApplyRejectsDifferentExistingDefinition(t *testing.T) {
	plan := testSetupPlan()
	got := plan.Definitions[0]
	got.SourceHash = "other"
	client := &fakeFactorJSONClient{existingSet: &plan.Sets[0], existing: &got}
	_, err := (&remoteSetupFactor{client: client}).Apply(context.Background(), plan)
	require.ErrorContains(t, err, "different definition")
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
	require.NoError(t, json.Unmarshal([]byte(`{"ret_info":{"code":0},"factor":{"factor_id":"Bias","factor_type":"timeseries","name":"Bias","source_hash":"sha256:x","input_columns":["close"],"outputs":["bias"],"params_json":"{}","lookback_periods":20},"usages":[{"set_id":"fset_a_1m","status":"enabled"}]}`), &get))
	require.Equal(t, "Bias", get.Factor.FactorID)
	require.Equal(t, 20, get.Factor.LookbackPeriods)
	require.Equal(t, []factorAPIUsage{{SetID: "fset_a_1m", Status: "enabled"}}, get.Usages)

	var list factorAPIResponse
	require.NoError(t, json.Unmarshal([]byte(`{"ret_info":{"code":0},"factors":[{"factor":{"factor_id":"Cci"},"usages":[{"set_id":"fset_a_1m","status":"disabled"}]}]}`), &list))
	require.Len(t, list.Factors, 1)
	require.Equal(t, "Cci", list.Factors[0].Factor.FactorID)
	require.Equal(t, "disabled", list.Factors[0].Usages[0].Status)
}
