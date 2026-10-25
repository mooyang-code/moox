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
			require.Equal(t, tt.resultID, ResultDatasetID(SetID(tt.datasetID, tt.freq)))
		})
	}
}

// 私有集合的 ID 由实例 ID 的摘要推出：满足 Storage 的小写下划线规则与 50 个字符上限，同一实例总是同一个 ID。
func TestInstanceSetIDFitsStorageRules(t *testing.T) {
	instanceID := "5f0c3a4e-8d2b-4c1a-9e7f-1234567890ab"
	setID := InstanceSetID(instanceID)
	require.Equal(t, setID, InstanceSetID(instanceID))
	require.NotEqual(t, setID, InstanceSetID("another"))
	require.True(t, strings.HasPrefix(setID, "fset_i"))
	dataset := ResultDatasetID(setID)
	require.LessOrEqual(t, len(dataset), 50)
	require.Regexp(t, `^dataset_factor_i[0-9a-f]{20}$`, dataset)
	require.Equal(t, "view_factor_"+strings.TrimPrefix(setID, "fset_"), ResultViewID(setID))
	require.LessOrEqual(t, len([]rune(InstanceDatasetName(instanceID))), 10)
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

func TestValidateSetRequiresCanonicalIdentity(t *testing.T) {
	set := FactorSet{
		SetID:           SetID("dataset_binance_kline_1m", "1m"),
		SpaceID:         "crypto",
		SourceDatasetID: "dataset_binance_kline_1m",
		Freq:            "1m",
		ResultDatasetID: ResultDatasetID(SetID("dataset_binance_kline_1m", "1m")),
		Status:          SetStatusPending,
	}
	require.NoError(t, ValidateSet(set))

	set.ResultDatasetID = "dataset_other"
	require.ErrorContains(t, ValidateSet(set), "result_dataset_id")

	set.ResultDatasetID = ResultDatasetID(set.SetID)

	// 私有集合的 ID 必须由实例推出。
	private := set
	private.OwnerInstanceID = "inst-1"
	require.ErrorContains(t, ValidateSet(private), "set_id must be")
	private.SetID = InstanceSetID("inst-1")
	private.ResultDatasetID = ResultDatasetID(private.SetID)
	require.NoError(t, ValidateSet(private))
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
	require.Equal(t, DefaultCategoryID, factor.CategoryID)
	_, err := ValidateDefinition(factor)
	require.NoError(t, err)
}

var closeColumns = []SourceColumn{{Name: "close", Numeric: true}, {Name: "symbol_name", Numeric: false}}

func member(factor FactorDef) MemberDef { return MemberDef{Alias: factor.FactorID, Factor: factor} }

func TestValidateMembershipRejectsUnknownOrNonNumericInput(t *testing.T) {
	factor := compiledFactor(t, "f", "outputs: {value: missing}")
	require.ErrorContains(t, ValidateMembership(FactorSet{}, member(factor), closeColumns, nil, nil), "unknown input")
	factor = compiledFactor(t, "f", "outputs: {value: symbol_name}")
	require.ErrorContains(t, ValidateMembership(FactorSet{}, member(factor), closeColumns, nil, nil), "not numeric")
}

func TestValidateMembershipRejectsOutputCollidingWithSourceColumn(t *testing.T) {
	factor := compiledFactor(t, "f", "outputs: {close: close * 2}")
	require.ErrorContains(t, ValidateMembership(FactorSet{}, member(factor), closeColumns, nil, nil), "与源列重名")
}

func TestValidateMembershipRejectsNamesShadowingSourceColumns(t *testing.T) {
	param := compiledFactor(t, "f", "params: {close: {type: int, min: 2, max: 9, default: 3}}\noutputs:\n  value: ts_mean(open, close)\n")
	columns := append([]SourceColumn{{Name: "open", Numeric: true}}, closeColumns...)
	require.ErrorContains(t, ValidateMembership(FactorSet{}, member(param), columns, nil, nil), "参数 close")
}

func TestValidateMembershipRejectsDuplicateColumnInSetExcludingSelf(t *testing.T) {
	factor := validFactor(t)
	sibling := compiledFactor(t, "sibling", "outputs: {value: close + 1}")
	require.ErrorContains(t, ValidateMembership(FactorSet{}, member(factor), closeColumns, []MemberDef{member(sibling)}, nil), "重复")
	// A member never conflicts with itself.
	require.NoError(t, ValidateMembership(FactorSet{}, member(factor), closeColumns, []MemberDef{member(factor)}, nil))
	// 同一定义用不同别名加入：列名带别名前缀，互不冲突。
	fast := MemberDef{Alias: "fast", Factor: factor}
	slow := MemberDef{Alias: "slow", Factor: factor}
	require.NoError(t, ValidateMembership(FactorSet{}, fast, closeColumns, []MemberDef{slow}, nil))
}

func TestValidateMembershipRejectsColumnOwnedByOtherMember(t *testing.T) {
	factor := validFactor(t)
	err := ValidateMembership(FactorSet{}, member(factor), closeColumns, nil, map[string]string{"value": "Other"})
	require.ErrorContains(t, err, "已属于")
}

func TestValidateMembershipAllowsReaddOfSameMember(t *testing.T) {
	factor := validFactor(t)
	require.NoError(t, ValidateMembership(FactorSet{}, member(factor), closeColumns, nil,
		map[string]string{"value": factor.FactorID}))
}

func TestMemberColumns(t *testing.T) {
	factor := compiledFactor(t, "Boll", "params: {w: {type: int, min: 2, max: 50, default: 20}}\noutputs:\n  up: ts_mean(close, w)\n  down: ts_mean(close, w) * 2\n")
	require.Equal(t, []string{"up", "down"}, member(factor).OutputColumns())
	fast := MemberDef{Alias: "fast", Factor: factor}
	require.Equal(t, []string{"fast_up", "fast_down"}, fast.OutputColumns())
	require.Equal(t, []string{"fast__w"}, fast.ParamColumns())
	require.Equal(t, "fset_x/fast", fast.Key("fset_x"))
	// 参数列与源列冲突、别名不合法都拒绝。
	require.ErrorContains(t, ValidateMembership(FactorSet{}, MemberDef{Alias: "x__y", Factor: factor}, closeColumns, nil, nil), "别名")
	require.ErrorContains(t, ValidateMembership(FactorSet{}, fast, append(closeColumns, SourceColumn{Name: "fast__w", Numeric: true}), nil, nil), "与源列重名")
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
