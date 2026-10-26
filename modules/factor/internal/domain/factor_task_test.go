package domain

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 结果数据集的 ID 由任务 ID 的后缀推出；任务 ID 不合法时返回空串。
func TestResultDatasetIDDerivesFromTaskID(t *testing.T) {
	require.Equal(t, "dataset_factor_abc12345", ResultDatasetID("ftask_abc12345"))
	require.Equal(t, "dataset_factor_my_ema", ResultDatasetID(" ftask_my_ema "))
	require.Empty(t, ResultDatasetID("fset_abc"))
	require.Empty(t, ResultDatasetID("ftask_Bad"))
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
	task := FactorTask{
		TaskID:          "ftask_abc12345",
		SpaceID:         "crypto",
		Name:            "动量任务",
		SourceDatasetID: "dataset_binance_kline_1m",
		Freq:            "1m",
		ResultDatasetID: "dataset_factor_abc12345",
		Status:          TaskStatusPending,
	}
	require.NoError(t, ValidateTask(task))

	bad := task
	bad.ResultDatasetID = "dataset_other"
	require.ErrorContains(t, ValidateTask(bad), "result_dataset_id")

	bad = task
	bad.TaskID = "ftask_Bad"
	require.Error(t, ValidateTask(bad))

	bad = task
	bad.Status = "deleting"
	require.ErrorContains(t, ValidateTask(bad), "status")

	bad = task
	bad.BoundInstanceID = " inst-1"
	require.ErrorContains(t, ValidateTask(bad), "bound_instance_id")
	task.BoundInstanceID = "inst-1"
	require.NoError(t, ValidateTask(task))
}

func TestValidateTaskName(t *testing.T) {
	for _, name := range []string{"动量任务", "BTC动量", "一二三四五六七八九十"} {
		require.NoError(t, ValidateTaskName(name), name)
	}
	for name, text := range map[string]string{
		"空": "", "无汉字": "momentum", "超过十个字": "一二三四五六七八九十一", "首尾空白": " 动量", "控制字符": "动\t量",
	} {
		require.Error(t, ValidateTaskName(text), name)
	}
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

func member(factor FactorDef) MemberDef {
	return MemberDef{Key: factor.FactorID, Factor: factor, Kind: MemberKindFixed}
}

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
	sibling.Outputs = []string{"close_mean", "other"}
	// 多输出因子的列是 成员键__输出名，不会与单输出因子的列重名。
	require.NoError(t, ValidateMembership(FactorTask{}, member(factor), closeColumns, []MemberDef{member(sibling)}, nil))
	// 成员不会与自己冲突。
	require.NoError(t, ValidateMembership(FactorTask{}, member(factor), closeColumns, []MemberDef{member(factor)}, nil))
	// 同一因子的固定成员与可变成员：成员键不同，列名互不冲突。
	ema := compiledFactor(t, "ema", "params:\n  span: {type: int, min: 2, max: 100, default: 20}\noutput: ema(close, span)\n")
	fixed := fixedMember(t, ema, map[string]float64{"span": 30})
	variable := varMember(t, ema)
	require.NoError(t, ValidateMembership(FactorTask{}, fixed, closeColumns, []MemberDef{variable}, nil))
	require.NoError(t, ValidateMembership(FactorTask{}, variable, closeColumns, []MemberDef{fixed}, nil))
	// 同一成员键出现两次（兄弟列表里混进同键成员）按同一成员处理，不算冲突。
	require.NoError(t, ValidateMembership(FactorTask{}, fixed, closeColumns, []MemberDef{fixed}, nil))
}

func TestValidateMembershipRejectsColumnOwnedByOtherMember(t *testing.T) {
	factor := validFactor(t)
	err := ValidateMembership(FactorTask{}, member(factor), closeColumns, nil, map[string]string{"close_mean": "other"})
	require.ErrorContains(t, err, "已属于")
}

func TestValidateMembershipAllowsReaddOfSameMember(t *testing.T) {
	factor := validFactor(t)
	require.NoError(t, ValidateMembership(FactorTask{}, member(factor), closeColumns, nil,
		map[string]string{"close_mean": factor.FactorID}))
}

// 成员键必须是公共命名包按类型和参数推出的那一个；类型不合法也拒绝。
func TestValidateMembershipChecksMemberKeyAndKind(t *testing.T) {
	ema := compiledFactor(t, "ema", "params:\n  span: {type: int, min: 2, max: 100, default: 20}\noutput: ema(close, span)\n")
	fixed := fixedMember(t, ema, map[string]float64{"span": 30})
	require.Equal(t, "ema__30", fixed.Key)
	fixed.Key = "ema__31"
	require.ErrorContains(t, ValidateMembership(FactorTask{}, fixed, closeColumns, nil, nil), "成员键必须是")
	fixed.Key, fixed.Kind = "ema", "other"
	require.ErrorContains(t, ValidateMembership(FactorTask{}, fixed, closeColumns, nil, nil), "成员类型")
	require.Equal(t, "ema__var", varMember(t, ema).Key)
}

func TestMemberColumns(t *testing.T) {
	factor := compiledFactor(t, "boll", "params: {w: {type: int, min: 2, max: 50, default: 20}}\noutputs:\n  up: ts_mean(close, w)\n  down: ts_mean(close, w) * 2\n")
	require.Equal(t, []string{"boll__up", "boll__down"}, member(factor).OutputColumns())
	fast := fixedMember(t, factor, map[string]float64{"w": 10})
	require.Equal(t, []string{"boll__10__up", "boll__10__down"}, fast.OutputColumns())
	variable := varMember(t, factor)
	require.Equal(t, []string{"boll__var__up", "boll__var__down"}, variable.OutputColumns())
	require.Equal(t, "ftask_x/boll__var", variable.ProgramKey("ftask_x"))
	// 输出列与源列冲突时拒绝。
	require.ErrorContains(t, ValidateMembership(FactorTask{}, fast, append(closeColumns, SourceColumn{Name: "boll__10__up", Numeric: true}), nil, nil), "与源列重名")
	// 单输出因子的列就是成员键。
	single := compiledFactor(t, "ma", "params: {n: {type: int, min: 2, max: 50, default: 20}}\noutput: ts_mean(close, n)\n")
	require.Equal(t, []string{"ma__var"}, varMember(t, single).OutputColumns())
}

func fixedMember(t *testing.T, factor FactorDef, params map[string]float64) MemberDef {
	t.Helper()
	full, err := ResolveMemberParams(factor, params)
	require.NoError(t, err)
	key, err := DeriveMemberKey(factor, MemberKindFixed, full)
	require.NoError(t, err)
	return MemberDef{Key: key, Factor: factor, Kind: MemberKindFixed, Params: full}
}

func varMember(t *testing.T, factor FactorDef) MemberDef {
	t.Helper()
	full, err := ResolveMemberParams(factor, nil)
	require.NoError(t, err)
	key, err := DeriveMemberKey(factor, MemberKindVar, full)
	require.NoError(t, err)
	return MemberDef{Key: key, Factor: factor, Kind: MemberKindVar, Params: full}
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
