package domain

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTaskIDAndResultDatasetID(t *testing.T) {
	tests := []struct {
		name      string
		datasetID string
		freq      string
		taskID    string
		resultID  string
	}{
		{
			name:      "source already ends in frequency",
			datasetID: "dataset_binance_kline_1m",
			freq:      "1m",
			taskID:    "ftask_binance_kline_1m",
			resultID:  "dataset_factor_binance_kline_1m",
		},
		{
			name:      "frequency appended to source suffix",
			datasetID: "dataset_spot_kline",
			freq:      "1h",
			taskID:    "ftask_spot_kline_1h",
			resultID:  "dataset_factor_spot_kline_1h",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.taskID, TaskID(tt.datasetID, tt.freq))
			require.Equal(t, tt.resultID, ResultDatasetID(TaskID(tt.datasetID, tt.freq)))
		})
	}
}

// 私有任务的 ID 由实例 ID 的摘要推出：满足 Storage 的小写下划线规则与 50 个字符上限，同一实例总是同一个 ID。
func TestInstanceTaskIDFitsStorageRules(t *testing.T) {
	instanceID := "5f0c3a4e-8d2b-4c1a-9e7f-1234567890ab"
	taskID := InstanceTaskID(instanceID)
	require.Equal(t, taskID, InstanceTaskID(instanceID))
	require.NotEqual(t, taskID, InstanceTaskID("another"))
	require.True(t, strings.HasPrefix(taskID, "ftask_i"))
	dataset := ResultDatasetID(taskID)
	require.LessOrEqual(t, len(dataset), 50)
	require.Regexp(t, `^dataset_factor_i[0-9a-f]{20}$`, dataset)
	require.Equal(t, "view_factor_"+strings.TrimPrefix(taskID, "ftask_"), ResultViewID(taskID))
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
	require.NotContains(t, fields, "task_id")
	require.NotContains(t, fields, "status")
	require.Contains(t, fields, "created_at")
	require.NotContains(t, fields, "source_path")
}

func TestValidateTaskRequiresCanonicalIdentity(t *testing.T) {
	set := FactorTask{
		TaskID:          TaskID("dataset_binance_kline_1m", "1m"),
		SpaceID:         "crypto",
		SourceDatasetID: "dataset_binance_kline_1m",
		Freq:            "1m",
		ResultDatasetID: ResultDatasetID(TaskID("dataset_binance_kline_1m", "1m")),
		Status:          TaskStatusPending,
	}
	require.NoError(t, ValidateTask(set))

	set.ResultDatasetID = "dataset_other"
	require.ErrorContains(t, ValidateTask(set), "result_dataset_id")

	set.ResultDatasetID = ResultDatasetID(set.TaskID)

	// 私有任务的 ID 必须由实例推出。
	private := set
	private.OwnerInstanceID = "inst-1"
	require.ErrorContains(t, ValidateTask(private), "task_id must be")
	private.TaskID = InstanceTaskID("inst-1")
	private.ResultDatasetID = ResultDatasetID(private.TaskID)
	require.NoError(t, ValidateTask(private))
}

func TestValidateDefinitionPartialUniverseOnlyForCrossSection(t *testing.T) {
	factor := validFactor(t)
	factor.AllowPartialUniverse = true
	_, err := ValidateDefinition(factor)
	require.ErrorContains(t, err, "allow_partial_universe")
	cross := compiledFactor(t, "cross", "output: cs_rank(close)")
	cross.AllowPartialUniverse = true
	_, err = ValidateDefinition(cross)
	require.NoError(t, err)
}

func TestValidateDefinitionDoesNotNeedSourceColumns(t *testing.T) {
	factor := compiledFactor(t, "f", "output: any_column_name")
	require.Equal(t, DefaultCategoryID, factor.CategoryID)
	_, err := ValidateDefinition(factor)
	require.NoError(t, err)
}

var closeColumns = []SourceColumn{{Name: "close", Numeric: true}, {Name: "symbol_name", Numeric: false}}

func member(factor FactorDef) MemberDef { return MemberDef{Alias: factor.FactorID, Factor: factor} }

func TestValidateMembershipRejectsUnknownOrNonNumericInput(t *testing.T) {
	factor := compiledFactor(t, "f", "output: missing")
	require.ErrorContains(t, ValidateMembership(FactorTask{}, member(factor), closeColumns, nil, nil), "unknown input")
	factor = compiledFactor(t, "f", "output: symbol_name")
	require.ErrorContains(t, ValidateMembership(FactorTask{}, member(factor), closeColumns, nil, nil), "not numeric")
}

func TestValidateMembershipRejectsOutputCollidingWithSourceColumn(t *testing.T) {
	// 单输出因子的输出名就是因子 ID：因子 ID 叫 close，输出列就与源列 close 重名。
	factor := compiledFactor(t, "close", "output: close * 2")
	require.ErrorContains(t, ValidateMembership(FactorTask{}, member(factor), closeColumns, nil, nil), "与源列重名")
}

func TestValidateMembershipRejectsNamesShadowingSourceColumns(t *testing.T) {
	param := compiledFactor(t, "f", "params: {close: {type: int, min: 2, max: 9, default: 3}}\noutput: ts_mean(open, close)\n")
	columns := append([]SourceColumn{{Name: "open", Numeric: true}}, closeColumns...)
	require.ErrorContains(t, ValidateMembership(FactorTask{}, member(param), columns, nil, nil), "参数 close")
}

func TestValidateMembershipRejectsDuplicateColumnInTaskExcludingSelf(t *testing.T) {
	factor := validFactor(t)
	// 两个输出的因子，其中一个输出与 validFactor 的列重名。
	sibling := compiledFactor(t, "sibling", "outputs:\n  close_mean: close + 1\n  other: close + 2\n")
	require.ErrorContains(t, ValidateMembership(FactorTask{}, member(factor), closeColumns, []MemberDef{member(sibling)}, nil), "重复")
	// A member never conflicts with itself.
	require.NoError(t, ValidateMembership(FactorTask{}, member(factor), closeColumns, []MemberDef{member(factor)}, nil))
	// 同一定义用不同别名加入：列名带别名前缀，互不冲突。
	fast := MemberDef{Alias: "fast", Factor: factor}
	slow := MemberDef{Alias: "slow", Factor: factor}
	require.NoError(t, ValidateMembership(FactorTask{}, fast, closeColumns, []MemberDef{slow}, nil))
	// 同名成员（别名与因子 ID 相同）输出列重名，要拒绝。
	require.ErrorContains(t, ValidateMembership(FactorTask{}, member(factor), closeColumns, []MemberDef{{Alias: "sibling", Factor: sibling}}, nil), "重复")
}

func TestValidateMembershipRejectsColumnOwnedByOtherMember(t *testing.T) {
	factor := validFactor(t)
	err := ValidateMembership(FactorTask{}, member(factor), closeColumns, nil, map[string]string{"close_mean": "Other"})
	require.ErrorContains(t, err, "已属于")
}

func TestValidateMembershipAllowsReaddOfSameMember(t *testing.T) {
	factor := validFactor(t)
	require.NoError(t, ValidateMembership(FactorTask{}, member(factor), closeColumns, nil,
		map[string]string{"close_mean": factor.FactorID}))
}

func TestMemberColumns(t *testing.T) {
	factor := compiledFactor(t, "boll", "params: {w: {type: int, min: 2, max: 50, default: 20}}\noutputs:\n  up: ts_mean(close, w)\n  down: ts_mean(close, w) * 2\n")
	require.Equal(t, []string{"up", "down"}, member(factor).OutputColumns())
	fast := MemberDef{Alias: "fast", Factor: factor}
	require.Equal(t, []string{"fast_up", "fast_down"}, fast.OutputColumns())
	require.Equal(t, []string{"fast__w"}, fast.ParamColumns())
	require.Equal(t, "ftask_x/fast", fast.Key("ftask_x"))
	// 参数列与源列冲突、别名不合法都拒绝。
	require.ErrorContains(t, ValidateMembership(FactorTask{}, MemberDef{Alias: "x__y", Factor: factor}, closeColumns, nil, nil), "别名")
	require.ErrorContains(t, ValidateMembership(FactorTask{}, fast, append(closeColumns, SourceColumn{Name: "fast__w", Numeric: true}), nil, nil), "与源列重名")
}

func compiledFactor(t *testing.T, factorID, spec string) FactorDef {
	t.Helper()
	def, err := NormalizeFactorDefinition(FactorDef{FactorID: factorID, Name: factorID, Spec: spec})
	require.NoError(t, err)
	return def
}

func validFactor(t *testing.T) FactorDef {
	t.Helper()
	return compiledFactor(t, "close_mean", "output: close")
}
