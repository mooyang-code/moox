package command

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/stretchr/testify/require"
)

type fakeFactorJSONClient struct {
	createdTypes     []string
	calls            []string
	bindingStatuses  []string
	existing         *setupFactorItem
	existingBindings []factorAPIBinding
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
	if path == "/api/admin/factormgr/CreateFactor" {
		factor := request["factor"].(map[string]any)
		typeName, _ := factor["factor_type"].(string)
		f.createdTypes = append(f.createdTypes, typeName)
	}
	if path == "/api/admin/factormgr/GetFactor" {
		if f.existing == nil {
			result.RetInfo.Code = 9
			result.RetInfo.Msg = "not found"
		} else {
			result.Factor.FactorType = f.existing.FactorType
			result.Factor.FactorID = f.existing.FactorID
			result.Factor.Name = f.existing.Name
			result.Factor.SourceHash = f.existing.SourceHash
			result.Factor.InputColumns = append([]string(nil), f.existing.InputColumns...)
			result.Factor.Outputs = append([]string(nil), f.existing.Outputs...)
			result.Factor.ParamsJSON = f.existing.ParamsJSON
			result.Factor.LookbackPeriods = f.existing.LookbackPeriods
		}
	}
	if path == "/api/admin/factormgr/UpsertBinding" {
		if binding, ok := request["binding"].(map[string]any); ok {
			if status, ok := binding["status"].(string); ok {
				f.bindingStatuses = append(f.bindingStatuses, status)
			}
		}
		result.Binding.Status = "enabled"
	}
	if path == "/api/admin/factormgr/ListBindings" {
		result.Bindings = append([]factorAPIBinding(nil), f.existingBindings...)
	}
	return nil
}

func TestLoadSetupFactorsReadsConfiguredPythonSources(t *testing.T) {
	root := t.TempDir()
	factorsDir := filepath.Join(root, "examples", "factors")
	require.NoError(t, os.MkdirAll(filepath.Join(factorsDir, "timeseries"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(factorsDir, "timeseries", "bias.py"), []byte("def compute(df, params):\n    return df\n"), 0o600))
	items, err := loadSetupFactors(setupconfig.Manifest{Factors: setupconfig.FactorSetup{
		Enabled: true, SourceDir: "./examples/factors", Items: []setupconfig.FactorSetupItem{{
			FactorType: "timeseries", FactorID: "bias", File: "timeseries/bias.py", InputColumns: []string{"close"}, Outputs: []string{"bias_5"},
			ParamsJSON: `{"windows":[5]}`, SpaceID: "crypto", SourceViewID: "view_crypto_spot_kline_1m", Freq: "1m",
		}},
	}}, root)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "bias", items[0].FactorID)
	require.Equal(t, "timeseries", items[0].FactorType)
	require.Equal(t, "bias", items[0].Name)
	require.NotEmpty(t, items[0].SourceHash)
	require.Equal(t, "crypto", items[0].SpaceID)
}

func TestLoadSetupFactorsUsesRepositoryDefaultsWhenItemsAreOmitted(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "../../../.."))
	items, err := loadSetupFactors(setupconfig.Manifest{Factors: setupconfig.FactorSetup{
		Enabled: true, SourceDir: "modules/factor/factors",
	}}, root)
	require.NoError(t, err)
	require.Len(t, items, 4)
	require.Equal(t, "Bias", items[0].FactorID)
	require.Equal(t, "Cci", items[1].FactorID)
	require.Equal(t, "MinMax", items[2].FactorID)
	require.Equal(t, "QuoteVolumeMean", items[3].FactorID)
}

func TestDefaultSetupFactorItemsMatchFactorFiles(t *testing.T) {
	items := defaultSetupFactorItems()
	byID := make(map[string]setupconfig.FactorSetupItem, len(items))
	for _, item := range items {
		byID[item.FactorID] = item
	}

	ma, ok := byID["MinMax"]
	require.True(t, ok)
	require.Equal(t, "MinMax.py", ma.File)
	require.Equal(t, []string{"minmax_20"}, ma.Outputs)
	require.Equal(t, 20, ma.LookbackPeriods)

	sma, ok := byID["QuoteVolumeMean"]
	require.True(t, ok)
	require.Equal(t, "QuoteVolumeMean.py", sma.File)
	require.Equal(t, []string{"quote_volume_mean_20"}, sma.Outputs)
	require.JSONEq(t, `{"window":20}`, sma.ParamsJSON)
	require.Equal(t, 20, sma.LookbackPeriods)
}

func TestLoadSetupFactorsRejectsSourcePathEscape(t *testing.T) {
	_, err := loadSetupFactors(setupconfig.Manifest{Factors: setupconfig.FactorSetup{
		Enabled: true, SourceDir: "./examples/factors", Items: []setupconfig.FactorSetupItem{{
			FactorType: "timeseries", FactorID: "bias", File: "../bias.py", InputColumns: []string{"close"}, Outputs: []string{"bias"},
			SpaceID: "crypto", SourceViewID: "view", Freq: "1m",
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
		Enabled: true, SourceDir: "./examples/factors", Items: []setupconfig.FactorSetupItem{{
			FactorType: "timeseries", FactorID: "outside", File: "outside.py", InputColumns: []string{"close"}, Outputs: []string{"value"},
			SpaceID: "crypto", SourceViewID: "view", Freq: "1m",
		}},
	}}, root)
	require.Error(t, err)
}

func TestLoadSetupFactorsRejectsMissingOrUnknownType(t *testing.T) {
	for _, factorType := range []string{"", "unknown"} {
		_, err := loadSetupFactors(setupconfig.Manifest{Factors: setupconfig.FactorSetup{
			Enabled: true, SourceDir: "factors", Items: []setupconfig.FactorSetupItem{{
				FactorID: "Bias", FactorType: factorType,
			}},
		}}, t.TempDir())
		require.ErrorContains(t, err, "factor_type")
	}
}

func TestSameFactorContractIncludesExecutionType(t *testing.T) {
	var response factorAPIResponse
	response.Factor.FactorType = "timeseries"
	require.False(t, sameFactorContract(response.Factor, setupFactorItem{FactorType: "cross_section"}))
	require.True(t, sameFactorContract(response.Factor, setupFactorItem{FactorType: "timeseries"}))
}

func TestRemoteSetupFactorApplyCreatesBindingAndEnablesFactor(t *testing.T) {
	client := &fakeFactorJSONClient{}
	service := &remoteSetupFactor{client: client}
	result, err := service.Apply(context.Background(), []setupFactorItem{{
		FactorType: "cross_section",
		FactorID:   "Bias", Name: "Bias", SourceCode: "def compute(df, params):\n    return df\n", SourceHash: "hash",
		InputColumns: []string{"close"}, Outputs: []string{"bias_5"}, ParamsJSON: "{}",
		SpaceID: "crypto", SourceViewID: "view_crypto_spot_kline_1m", Freq: "1m", SubjectMode: "all", Status: "enabled",
	}})
	require.NoError(t, err)
	require.Equal(t, setupFactorSummary{Enabled: true, Planned: 1, Imported: 1, Bound: 1}, result)
	require.Equal(t, []string{
		"/api/admin/factormgr/GetFactor",
		"/api/admin/factormgr/CreateFactor",
		"/api/admin/factormgr/UpsertBinding",
		"/api/admin/factormgr/SetFactorStatus",
		"/api/admin/factormgr/UpsertBinding",
		"/api/admin/factormgr/ListBindings",
	}, client.calls)
	require.Equal(t, []string{"disabled", "enabled"}, client.bindingStatuses)
	require.Equal(t, []string{"cross_section"}, client.createdTypes)
}

func TestRemoteSetupFactorApplyRejectsDifferentExistingContract(t *testing.T) {
	client := &fakeFactorJSONClient{existing: &setupFactorItem{
		FactorID: "Bias", Name: "Bias", SourceHash: "hash", InputColumns: []string{"close"},
		Outputs: []string{"bias_5"}, ParamsJSON: `{"window":99}`, LookbackPeriods: 20,
	}}
	service := &remoteSetupFactor{client: client}
	_, err := service.Apply(context.Background(), []setupFactorItem{{
		FactorID: "Bias", Name: "Bias", SourceHash: "hash", InputColumns: []string{"close"},
		Outputs: []string{"bias_5"}, ParamsJSON: `{"window":20}`, LookbackPeriods: 20,
		SpaceID: "crypto", SourceViewID: "view", Freq: "1m", SubjectMode: "all", Status: "enabled",
	}})
	require.ErrorContains(t, err, "different definition")
	require.Equal(t, []string{"/api/admin/factormgr/GetFactor"}, client.calls)
}

func TestRemoteSetupFactorApplyRemovesObsoleteSetupBinding(t *testing.T) {
	client := &fakeFactorJSONClient{existingBindings: []factorAPIBinding{{
		BindingID: "setup-Bias-crypto-old_view-1m", FactorID: "Bias", Status: "enabled",
	}}}
	service := &remoteSetupFactor{client: client}
	_, err := service.Apply(context.Background(), []setupFactorItem{{
		FactorID: "Bias", Name: "Bias", SourceCode: "def compute(df, params):\n    return df", SourceHash: "hash",
		InputColumns: []string{"close"}, Outputs: []string{"bias_5"}, ParamsJSON: "{}", LookbackPeriods: 1,
		SpaceID: "crypto", SourceViewID: "view_crypto_spot_kline_1m", Freq: "1m", SubjectMode: "all", Status: "disabled",
	}})
	require.NoError(t, err)
	require.Contains(t, client.calls, "/api/admin/factormgr/DeleteBinding")
}
