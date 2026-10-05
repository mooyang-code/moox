package domain

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSetIDAndResultDatasetID(t *testing.T) {
	tests := []struct {
		name      string
		datasetID string
		freq      string
		setID     string
		resultID  string
	}{
		{
			name:      "source already ends in frequency",
			datasetID: "dataset_binance_kline_1m",
			freq:      "1m",
			setID:     "fset_binance_kline_1m",
			resultID:  "dataset_factor_binance_kline_1m",
		},
		{
			name:      "frequency appended to source suffix",
			datasetID: "dataset_spot_kline",
			freq:      "1h",
			setID:     "fset_spot_kline_1h",
			resultID:  "dataset_factor_spot_kline_1h",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.setID, SetID(tt.datasetID, tt.freq))
			require.Equal(t, tt.resultID, ResultDatasetID(tt.datasetID, tt.freq))
		})
	}
}

func TestSourceHash(t *testing.T) {
	got := SourceHash("def compute(df, params):\n    return df")
	require.True(t, strings.HasPrefix(got, "sha256:"))
	require.Equal(t, got, SourceHash("def compute(df, params):\n    return df"))
	require.NotEqual(t, got, SourceHash("different source"))
}

func TestFactorDefUsesDatasetPipelineFields(t *testing.T) {
	typeOf := reflect.TypeOf(FactorDef{})
	createdAt, ok := typeOf.FieldByName("CreatedAt")
	require.True(t, ok)
	require.Equal(t, "column:c_ctime", createdAt.Tag.Get("gorm"))
	updatedAt, ok := typeOf.FieldByName("UpdatedAt")
	require.True(t, ok)
	require.Equal(t, "column:c_mtime", updatedAt.Tag.Get("gorm"))
	_, hasSourcePath := typeOf.FieldByName("SourcePath")
	require.False(t, hasSourcePath)

	encoded, err := json.Marshal(FactorDef{FactorID: "f", CreatedAt: time.Unix(1, 0).UTC()})
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(encoded, &fields))
	require.Contains(t, fields, "factor_id")
	require.NotContains(t, fields, "set_id")
	require.NotContains(t, fields, "status")
	require.Contains(t, fields, "created_at")
	require.NotContains(t, fields, "source_path")
}

func TestValidateSetRequiresCanonicalIdentityAndScope(t *testing.T) {
	set := FactorSet{
		SetID:           SetID("dataset_binance_kline_1m", "1m"),
		SpaceID:         "crypto",
		SourceDatasetID: "dataset_binance_kline_1m",
		Freq:            "1m",
		SubjectMode:     SubjectModeAll,
		ResultDatasetID: ResultDatasetID("dataset_binance_kline_1m", "1m"),
		Status:          SetStatusPending,
	}
	require.NoError(t, ValidateSet(set))

	set.ResultDatasetID = "dataset_other"
	require.ErrorContains(t, ValidateSet(set), "result_dataset_id")

	set.ResultDatasetID = ResultDatasetID(set.SourceDatasetID, set.Freq)
	set.SubjectMode = SubjectModeInclude
	require.ErrorContains(t, ValidateSet(set), "at least one subject")
	set.Subjects = []string{"BTC", "BTC"}
	require.ErrorContains(t, ValidateSet(set), "duplicate subject")
}

func TestValidateDefinitionRejectsReservedOutput(t *testing.T) {
	for _, name := range []string{"series_tag", "data_time", "subject_id", "freq"} {
		factor := validFactor()
		factor.Outputs = []string{name}
		require.ErrorContains(t, ValidateDefinition(factor), "reserved", name)
	}
}

func TestValidateDefinitionRejectsDuplicateInputsAndOutputs(t *testing.T) {
	factor := validFactor()
	factor.InputColumns = []string{"close", "close"}
	require.ErrorContains(t, ValidateDefinition(factor), "duplicate")
	factor = validFactor()
	factor.Outputs = []string{"value", "value"}
	require.ErrorContains(t, ValidateDefinition(factor), "duplicate")
}

func TestValidateDefinitionRequiresNonEmptyInputsAndOutputs(t *testing.T) {
	factor := validFactor()
	factor.InputColumns = nil
	require.ErrorContains(t, ValidateDefinition(factor), "input_columns")
	factor = validFactor()
	factor.Outputs = nil
	require.ErrorContains(t, ValidateDefinition(factor), "outputs")
}

func TestValidateDefinitionParamsMustBeJSONObject(t *testing.T) {
	for _, raw := range []string{"", "[]", "null", `"text"`, "1", `{"window":`, `{} {}`} {
		t.Run(raw, func(t *testing.T) {
			factor := validFactor()
			factor.ParamsJSON = raw
			require.ErrorContains(t, ValidateDefinition(factor), "params_json")
		})
	}
	valid := validFactor()
	valid.ParamsJSON = `{"window": 20}`
	require.NoError(t, ValidateDefinition(valid))
}

func TestValidateDefinitionLookbackAtLeastOne(t *testing.T) {
	factor := validFactor()
	factor.LookbackPeriods = 0
	require.ErrorContains(t, ValidateDefinition(factor), "lookback_periods")
}

func TestValidateDefinitionPartialUniverseOnlyForCrossSection(t *testing.T) {
	factor := validFactor()
	factor.AllowPartialUniverse = true
	require.ErrorContains(t, ValidateDefinition(factor), "allow_partial_universe")
	factor.FactorType = FactorTypeCrossSection
	require.NoError(t, ValidateDefinition(factor))
}

func TestValidateDefinitionDoesNotNeedSourceColumns(t *testing.T) {
	factor := validFactor()
	factor.InputColumns = []string{"any_column_name"}
	require.NoError(t, ValidateDefinition(factor))
}

func TestValidateMembershipRejectsUnknownInput(t *testing.T) {
	factor := validFactor()
	factor.InputColumns = []string{"missing"}
	require.ErrorContains(t, ValidateMembership(FactorSet{}, factor, []string{"close"}, nil, nil), "unknown input")
}

func TestValidateMembershipRejectsOutputCollidingWithSourceColumn(t *testing.T) {
	factor := validFactor()
	factor.Outputs = []string{"close"}
	require.ErrorContains(t, ValidateMembership(FactorSet{}, factor, []string{"close"}, nil, nil), "source column")
}

func TestValidateMembershipRejectsDuplicateOutputInSetExcludingSelf(t *testing.T) {
	factor := validFactor()
	sibling := validFactor()
	sibling.FactorID = "sibling"
	sibling.Outputs = []string{"value"}
	require.ErrorContains(t, ValidateMembership(FactorSet{}, factor, []string{"close"}, []FactorDef{sibling}, nil), "duplicate output")
	// A definition never conflicts with itself.
	require.NoError(t, ValidateMembership(FactorSet{}, factor, []string{"close"}, []FactorDef{factor}, nil))
}

func TestValidateMembershipRejectsOutputOwnedByOtherFactor(t *testing.T) {
	factor := validFactor()
	err := ValidateMembership(FactorSet{}, factor, []string{"close"}, nil, map[string]string{"value": "Other"})
	require.ErrorContains(t, err, "already owned")
}

func TestValidateMembershipAllowsReaddOfSameFactor(t *testing.T) {
	factor := validFactor()
	require.NoError(t, ValidateMembership(FactorSet{}, factor, []string{"close"}, nil,
		map[string]string{"value": factor.FactorID}))
}

func TestSetInScope(t *testing.T) {
	all := FactorSet{SubjectMode: SubjectModeAll}
	require.True(t, all.InScope("ETH"))
	require.True(t, all.InScope(""))

	include := FactorSet{SubjectMode: SubjectModeInclude, Subjects: []string{"BTC", "ETH"}}
	require.True(t, include.InScope("BTC"))
	require.False(t, include.InScope("SOL"))
	require.False(t, include.InScope(" "))
}

func validFactor() FactorDef {
	return FactorDef{
		FactorID:        "close_mean",
		Name:            "CloseMean",
		FactorType:      FactorTypeTimeSeries,
		SourceCode:      "def compute(df, params):\n    return df['close']",
		SourceHash:      SourceHash("def compute(df, params):\n    return df['close']"),
		InputColumns:    []string{"close"},
		Outputs:         []string{"value"},
		ParamsJSON:      `{}`,
		LookbackPeriods: 1,
	}
}
