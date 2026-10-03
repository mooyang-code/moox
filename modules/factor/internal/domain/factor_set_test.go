package domain

import (
	"strings"
	"testing"

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

func TestValidateFactorRejectsReservedOutput(t *testing.T) {
	factor := validFactor()
	factor.Outputs = []string{"series_tag"}
	require.ErrorContains(t, ValidateFactor(factor, []string{"close"}, nil), "reserved")
}

func TestValidateFactorRejectsOutputCollidingWithSourceColumn(t *testing.T) {
	factor := validFactor()
	factor.Outputs = []string{"close"}
	require.ErrorContains(t, ValidateFactor(factor, []string{"close"}, nil), "source column")
}

func TestValidateFactorRejectsDuplicateOutputInSet(t *testing.T) {
	factor := validFactor()
	sibling := validFactor()
	sibling.FactorID = "sibling"
	sibling.Outputs = []string{"value"}
	require.ErrorContains(t, ValidateFactor(factor, []string{"close"}, []FactorDef{sibling}), "duplicate output")

	// Updating one factor must not conflict with its own previous definition.
	require.NoError(t, ValidateFactor(factor, []string{"close"}, []FactorDef{factor}))
}

func TestValidateFactorRejectsUnknownInput(t *testing.T) {
	factor := validFactor()
	factor.InputColumns = []string{"missing"}
	require.ErrorContains(t, ValidateFactor(factor, []string{"close"}, nil), "unknown input")
}

func TestValidateFactorLookbackAtLeastOne(t *testing.T) {
	factor := validFactor()
	factor.LookbackPeriods = 0
	require.ErrorContains(t, ValidateFactor(factor, []string{"close"}, nil), "lookback_periods")
}

func TestValidateFactorParamsMustBeJSONObject(t *testing.T) {
	for _, raw := range []string{"", "[]", "null", `"text"`, "1", `{"window":`, `{} {}`} {
		t.Run(raw, func(t *testing.T) {
			factor := validFactor()
			factor.ParamsJSON = raw
			require.ErrorContains(t, ValidateFactor(factor, []string{"close"}, nil), "params_json")
		})
	}
	valid := validFactor()
	valid.ParamsJSON = `{"window": 20}`
	require.NoError(t, ValidateFactor(valid, []string{"close"}, nil))
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
		SetID:           "fset_binance_kline_1m",
		Name:            "CloseMean",
		FactorType:      FactorTypeTimeSeries,
		SourceCode:      "def compute(df, params):\n    return df['close']",
		SourceHash:      SourceHash("def compute(df, params):\n    return df['close']"),
		InputColumns:    []string{"close"},
		Outputs:         []string{"value"},
		ParamsJSON:      `{}`,
		LookbackPeriods: 1,
		Status:          FactorStatusDisabled,
	}
}
