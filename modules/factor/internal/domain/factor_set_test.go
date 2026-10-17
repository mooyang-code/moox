package domain

import (
	"encoding/json"
	"reflect"
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

func TestValidateDefinitionPartialUniverseOnlyForCrossSection(t *testing.T) {
	factor := validFactor(t)
	factor.AllowPartialUniverse = true
	_, err := ValidateDefinition(factor)
	require.ErrorContains(t, err, "allow_partial_universe")
	cross := compiledFactor(t, "cross", "outputs: {cs_value: cs_rank(close)}")
	cross.AllowPartialUniverse = true
	_, err = ValidateDefinition(cross)
	require.NoError(t, err)
}

func TestValidateDefinitionDoesNotNeedSourceColumns(t *testing.T) {
	factor := compiledFactor(t, "f", "outputs: {value: any_column_name}")
	_, err := ValidateDefinition(factor)
	require.NoError(t, err)
}

var closeColumns = []SourceColumn{{Name: "close", Numeric: true}, {Name: "symbol_name", Numeric: false}}

func TestValidateMembershipRejectsUnknownOrNonNumericInput(t *testing.T) {
	factor := compiledFactor(t, "f", "outputs: {value: missing}")
	require.ErrorContains(t, ValidateMembership(FactorSet{}, factor, closeColumns, nil, nil), "unknown input")
	factor = compiledFactor(t, "f", "outputs: {value: symbol_name}")
	require.ErrorContains(t, ValidateMembership(FactorSet{}, factor, closeColumns, nil, nil), "not numeric")
}

func TestValidateMembershipRejectsOutputCollidingWithSourceColumn(t *testing.T) {
	factor := compiledFactor(t, "f", "outputs: {close: close * 2}")
	require.ErrorContains(t, ValidateMembership(FactorSet{}, factor, closeColumns, nil, nil), "source column")
}

func TestValidateMembershipRejectsNamesShadowingSourceColumns(t *testing.T) {
	param := compiledFactor(t, "f", "params: {close: 3}\noutputs:\n  value: ts_mean(open, close)\n")
	columns := append([]SourceColumn{{Name: "open", Numeric: true}}, closeColumns...)
	require.ErrorContains(t, ValidateMembership(FactorSet{}, param, columns, nil, nil), "参数 close")
}

func TestValidateMembershipRejectsDuplicateOutputInSetExcludingSelf(t *testing.T) {
	factor := validFactor(t)
	sibling := compiledFactor(t, "sibling", "outputs: {value: close + 1}")
	require.ErrorContains(t, ValidateMembership(FactorSet{}, factor, closeColumns, []FactorDef{sibling}, nil), "duplicate output")
	// A definition never conflicts with itself.
	require.NoError(t, ValidateMembership(FactorSet{}, factor, closeColumns, []FactorDef{factor}, nil))
}

func TestValidateMembershipRejectsOutputOwnedByOtherFactor(t *testing.T) {
	factor := validFactor(t)
	err := ValidateMembership(FactorSet{}, factor, closeColumns, nil, map[string]string{"value": "Other"})
	require.ErrorContains(t, err, "already owned")
}

func TestValidateMembershipAllowsReaddOfSameFactor(t *testing.T) {
	factor := validFactor(t)
	require.NoError(t, ValidateMembership(FactorSet{}, factor, closeColumns, nil,
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

func compiledFactor(t *testing.T, factorID, spec string) FactorDef {
	t.Helper()
	def, err := NormalizeFactorDefinition(FactorDef{FactorID: factorID, Name: factorID, Spec: spec})
	require.NoError(t, err)
	return def
}

func validFactor(t *testing.T) FactorDef {
	t.Helper()
	return compiledFactor(t, "close_mean", "outputs: {value: close}")
}
