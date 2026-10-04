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
	calls         []string
	createdTypes  []string
	createdSetIDs []string
	updatedSetIDs []string
	statuses      []string
	existing      *setupFactorItem
	existingSet   *setupFactorItem
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
			result.Factor.SetID = f.existing.SetID
			result.Factor.Name = f.existing.Name
			result.Factor.SourceHash = f.existing.SourceHash
			result.Factor.InputColumns = append([]string(nil), f.existing.InputColumns...)
			result.Factor.Outputs = append([]string(nil), f.existing.Outputs...)
			result.Factor.ParamsJSON = f.existing.ParamsJSON
			result.Factor.LookbackPeriods = f.existing.LookbackPeriods
			result.Factor.Status = f.existing.Status
		}
	case "/api/admin/factormgr/CreateFactor":
		factor := request["factor"].(map[string]any)
		f.createdTypes = append(f.createdTypes, factor["factor_type"].(string))
	case "/api/admin/factormgr/SetFactorStatus":
		f.statuses = append(f.statuses, request["status"].(string))
	}
	return nil
}

func TestLoadSetupFactorsReadsConfiguredPythonSources(t *testing.T) {
	root := t.TempDir()
	factorsDir := filepath.Join(root, "examples", "factors")
	require.NoError(t, os.MkdirAll(filepath.Join(factorsDir, "timeseries"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(factorsDir, "timeseries", "bias.py"), []byte("def compute(df, params):\n    return df\n"), 0o600))
	items, err := loadSetupFactors(setupconfig.Manifest{Factors: setupconfig.FactorSetup{
		Enabled: true, SourceDir: "./examples/factors",
		Sets: []setupconfig.FactorSetupSet{{SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m"}},
		Items: []setupconfig.FactorSetupItem{{
			FactorType: "timeseries", FactorID: "bias", File: "timeseries/bias.py", InputColumns: []string{"close"}, Outputs: []string{"bias_5"},
			ParamsJSON: `{"windows":[5]}`, SourceDatasetID: "dataset_prices", Freq: "1m",
		}},
	}}, root)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "bias", items[0].FactorID)
	require.Equal(t, "timeseries", items[0].FactorType)
	require.Equal(t, "bias", items[0].Name)
	require.NotEmpty(t, items[0].SourceHash)
	require.Equal(t, "crypto", items[0].SpaceID)
	require.Equal(t, "fset_prices_1m", items[0].SetID)
}

func TestLoadSetupFactorsRequiresMatchingSet(t *testing.T) {
	root := t.TempDir()
	factorsDir := filepath.Join(root, "factors")
	require.NoError(t, os.MkdirAll(factorsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(factorsDir, "Bias.py"), []byte("def compute(df, params):\n    return df\n"), 0o600))
	_, err := loadSetupFactors(setupconfig.Manifest{Factors: setupconfig.FactorSetup{
		Enabled: true, SourceDir: "factors", Items: []setupconfig.FactorSetupItem{{
			FactorType: "timeseries", FactorID: "Bias", File: "Bias.py", InputColumns: []string{"close"}, Outputs: []string{"bias"},
			SourceDatasetID: "dataset_prices", Freq: "1m",
		}},
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
		Items: []setupconfig.FactorSetupItem{{
			FactorType: "timeseries", FactorID: "Bias", File: "Bias.py", InputColumns: []string{"close"}, Outputs: []string{"bias"},
			SourceDatasetID: "dataset_prices", Freq: "1m",
		}},
	}}, root)
	require.ErrorContains(t, err, "no matching set")
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
	require.Equal(t, []string{"Bias", "Cci", "MinMax", "QuoteVolumeMean"}, []string{items[0].FactorID, items[1].FactorID, items[2].FactorID, items[3].FactorID})
	require.Equal(t, "dataset_binance_kline_1m", items[0].SourceDatasetID)
	require.Equal(t, "fset_binance_kline_1m", items[0].SetID)
}

func TestDefaultSetupFactorItemsMatchFactorFiles(t *testing.T) {
	items := defaultSetupFactorItems()
	byID := make(map[string]setupconfig.FactorSetupItem, len(items))
	for _, item := range items {
		byID[item.FactorID] = item
	}
	ma := byID["MinMax"]
	require.Equal(t, "MinMax.py", ma.File)
	require.Equal(t, []string{"minmax_20"}, ma.Outputs)
	require.Equal(t, 20, ma.LookbackPeriods)
	sma := byID["QuoteVolumeMean"]
	require.Equal(t, "QuoteVolumeMean.py", sma.File)
	require.Equal(t, []string{"quote_volume_mean_20"}, sma.Outputs)
	require.JSONEq(t, `{"window":20}`, sma.ParamsJSON)
	require.Equal(t, 20, sma.LookbackPeriods)
}

func TestLoadSetupFactorsRejectsSourcePathEscape(t *testing.T) {
	_, err := loadSetupFactors(setupconfig.Manifest{Factors: setupconfig.FactorSetup{
		Enabled: true, SourceDir: "./examples/factors",
		Sets: []setupconfig.FactorSetupSet{{SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m"}},
		Items: []setupconfig.FactorSetupItem{{
			FactorType: "timeseries", FactorID: "bias", File: "../bias.py", InputColumns: []string{"close"}, Outputs: []string{"bias"},
			SourceDatasetID: "dataset_prices", Freq: "1m",
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
		Items: []setupconfig.FactorSetupItem{{
			FactorType: "timeseries", FactorID: "outside", File: "outside.py", InputColumns: []string{"close"}, Outputs: []string{"value"},
			SourceDatasetID: "dataset_prices", Freq: "1m",
		}},
	}}, root)
	require.Error(t, err)
}

func TestLoadSetupFactorsRejectsMissingOrUnknownType(t *testing.T) {
	for _, factorType := range []string{"", "unknown"} {
		_, err := loadSetupFactors(setupconfig.Manifest{Factors: setupconfig.FactorSetup{
			Enabled: true, SourceDir: "factors", Items: []setupconfig.FactorSetupItem{{FactorID: "Bias", FactorType: factorType}},
		}}, t.TempDir())
		require.ErrorContains(t, err, "factor_type")
	}
}

func TestSameFactorContractIncludesExecutionTypeAndSet(t *testing.T) {
	var response factorAPIResponse
	response.Factor.FactorType = "timeseries"
	response.Factor.SetID = "fset_prices_1m"
	want := setupFactorItem{FactorType: "timeseries", SetID: "fset_prices_1m"}
	require.True(t, sameFactorContract(response.Factor, want))
	want.FactorType = "cross_section"
	require.False(t, sameFactorContract(response.Factor, want))
	want.FactorType, want.SetID = "timeseries", "fset_other_1m"
	require.False(t, sameFactorContract(response.Factor, want))
}

func TestSetupFactorsCreatesSetBeforeFactors(t *testing.T) {
	client := &fakeFactorJSONClient{}
	service := &remoteSetupFactor{client: client}
	result, err := service.Apply(context.Background(), []setupFactorItem{{
		FactorType: "cross_section", FactorID: "Bias", Name: "Bias", SourceCode: "def compute(df, params):\n    return df\n", SourceHash: "sha256:hash",
		InputColumns: []string{"close"}, Outputs: []string{"bias_5"}, ParamsJSON: "{}",
		SetID: "fset_binance_kline_1m", SpaceID: "crypto", SourceDatasetID: "dataset_binance_kline_1m", Freq: "1m", SubjectMode: "all", Status: "enabled",
	}})
	require.NoError(t, err)
	require.Equal(t, setupFactorSummary{Enabled: true, Planned: 1, SetsCreated: 1, Imported: 1, Associated: 1}, result)
	require.Equal(t, []string{
		"/api/admin/factormgr/GetFactorSet",
		"/api/admin/factormgr/CreateFactorSet",
		"/api/admin/factormgr/GetFactor",
		"/api/admin/factormgr/CreateFactor",
		"/api/admin/factormgr/SetFactorStatus",
	}, client.calls)
	require.Equal(t, []string{"enabled"}, client.statuses)
	require.Equal(t, []string{"cross_section"}, client.createdTypes)
	require.Equal(t, []string{"fset_binance_kline_1m"}, client.createdSetIDs)
}

func TestSetupFactorsIdempotent(t *testing.T) {
	item := setupFactorItem{
		FactorType: "timeseries", FactorID: "Bias", Name: "Bias", SourceCode: "def compute(df, params):\n    return df\n", SourceHash: "hash",
		InputColumns: []string{"close"}, Outputs: []string{"bias_5"}, ParamsJSON: "{}", LookbackPeriods: 1,
		SetID: "fset_prices_1m", SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m", SubjectMode: "all", Status: "enabled",
	}
	client := &fakeFactorJSONClient{existingSet: &item, existing: &item}
	service := &remoteSetupFactor{client: client}
	result, err := service.Apply(context.Background(), []setupFactorItem{item})
	require.NoError(t, err)
	require.Equal(t, setupFactorSummary{Enabled: true, Planned: 1, Associated: 1, Unchanged: 2}, result)
	require.Equal(t, []string{"/api/admin/factormgr/GetFactorSet", "/api/admin/factormgr/GetFactor"}, client.calls)
	require.Empty(t, client.statuses)
}

func TestSetupFactorsUpdatesExistingSetSubjectScope(t *testing.T) {
	want := setupFactorItem{
		SetID: "fset_prices_1m", SpaceID: "crypto", SourceDatasetID: "dataset_prices", Freq: "1m", SubjectMode: "include", Subjects: []string{"BTC"},
		FactorType: "timeseries", FactorID: "Bias", Name: "Bias", SourceCode: "source", SourceHash: "hash", InputColumns: []string{"close"}, Outputs: []string{"bias"}, ParamsJSON: "{}", Status: "disabled",
	}
	got := want
	got.SubjectMode, got.Subjects = "all", nil
	client := &fakeFactorJSONClient{existingSet: &got}
	_, err := (&remoteSetupFactor{client: client}).Apply(context.Background(), []setupFactorItem{want})
	require.NoError(t, err)
	require.Equal(t, []string{"fset_prices_1m"}, client.updatedSetIDs)
}
