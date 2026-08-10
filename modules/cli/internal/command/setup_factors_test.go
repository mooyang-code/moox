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
	if path == "/api/admin/factormgr/GetFactor" {
		if f.existing == nil {
			result.RetInfo.Code = 9
			result.RetInfo.Msg = "not found"
		} else {
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
			FactorID: "bias", File: "timeseries/bias.py", InputColumns: []string{"close"}, Outputs: []string{"bias_5"},
			ParamsJSON: `{"windows":[5]}`, SpaceID: "crypto_market", SourceViewID: "binance_spot_kline_1m_view", Freq: "1m",
		}},
	}}, root)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "bias", items[0].FactorID)
	require.Equal(t, "bias", items[0].Name)
	require.NotEmpty(t, items[0].SourceHash)
	require.Equal(t, "crypto_market", items[0].SpaceID)
}

func TestLoadSetupFactorsUsesRepositoryDefaultsWhenItemsAreOmitted(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "../../../.."))
	items, err := loadSetupFactors(setupconfig.Manifest{Factors: setupconfig.FactorSetup{
		Enabled: true, SourceDir: "examples/factors",
	}}, root)
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, "bias", items[0].FactorID)
	require.Equal(t, "cci", items[1].FactorID)
}

func TestLoadSetupFactorsRejectsSourcePathEscape(t *testing.T) {
	_, err := loadSetupFactors(setupconfig.Manifest{Factors: setupconfig.FactorSetup{
		Enabled: true, SourceDir: "./examples/factors", Items: []setupconfig.FactorSetupItem{{
			FactorID: "bias", File: "../bias.py", InputColumns: []string{"close"}, Outputs: []string{"bias"},
			SpaceID: "crypto_market", SourceViewID: "view", Freq: "1m",
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
			FactorID: "outside", File: "outside.py", InputColumns: []string{"close"}, Outputs: []string{"value"},
			SpaceID: "crypto_market", SourceViewID: "view", Freq: "1m",
		}},
	}}, root)
	require.Error(t, err)
}

func TestRemoteSetupFactorApplyCreatesBindingAndEnablesFactor(t *testing.T) {
	client := &fakeFactorJSONClient{}
	service := &remoteSetupFactor{client: client}
	result, err := service.Apply(context.Background(), []setupFactorItem{{
		FactorID: "bias", Name: "bias", SourceCode: "def compute(df, params):\n    return df\n", SourceHash: "hash",
		InputColumns: []string{"close"}, Outputs: []string{"bias_5"}, ParamsJSON: "{}",
		SpaceID: "crypto_market", SourceViewID: "binance_spot_kline_1m_view", Freq: "1m", SubjectMode: "all", Status: "enabled",
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
}

func TestRemoteSetupFactorApplyRejectsDifferentExistingContract(t *testing.T) {
	client := &fakeFactorJSONClient{existing: &setupFactorItem{
		FactorID: "bias", Name: "bias", SourceHash: "hash", InputColumns: []string{"close"},
		Outputs: []string{"bias_5"}, ParamsJSON: `{"window":99}`, LookbackPeriods: 20,
	}}
	service := &remoteSetupFactor{client: client}
	_, err := service.Apply(context.Background(), []setupFactorItem{{
		FactorID: "bias", Name: "bias", SourceHash: "hash", InputColumns: []string{"close"},
		Outputs: []string{"bias_5"}, ParamsJSON: `{"window":20}`, LookbackPeriods: 20,
		SpaceID: "crypto_market", SourceViewID: "view", Freq: "1m", SubjectMode: "all", Status: "enabled",
	}})
	require.ErrorContains(t, err, "different definition")
	require.Equal(t, []string{"/api/admin/factormgr/GetFactor"}, client.calls)
}

func TestRemoteSetupFactorApplyRemovesObsoleteSetupBinding(t *testing.T) {
	client := &fakeFactorJSONClient{existingBindings: []factorAPIBinding{{
		BindingID: "setup-bias-crypto_market-old_view-1m", FactorID: "bias", Status: "enabled",
	}}}
	service := &remoteSetupFactor{client: client}
	_, err := service.Apply(context.Background(), []setupFactorItem{{
		FactorID: "bias", Name: "bias", SourceCode: "def compute(df, params):\n    return df", SourceHash: "hash",
		InputColumns: []string{"close"}, Outputs: []string{"bias_5"}, ParamsJSON: "{}", LookbackPeriods: 1,
		SpaceID: "crypto_market", SourceViewID: "new_view", Freq: "1m", SubjectMode: "all", Status: "disabled",
	}})
	require.NoError(t, err)
	require.Contains(t, client.calls, "/api/admin/factormgr/DeleteBinding")
}
