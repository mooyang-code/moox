package catalog

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
)

const paramSpec = "params:\n  window: {type: int, min: 2, max: 30, default: 5}\noutput: ts_mean(close, window)\n"

func resultColumns(meta *metadataFake, taskID string) map[string]map[string]string {
	columns := map[string]map[string]string{}
	for _, col := range meta.columns[resultKey("crypto", domain.ResultDatasetID(taskID))] {
		columns[col.ColumnName] = col.Attributes
	}
	return columns
}

// 固定成员：没给的参数取定义的默认值补全，成员键按参数生成（全默认就是因子 ID），同任务内同因子同参数只能有一个成员。
func TestAddFixedMemberFixesParamsAndDerivesKey(t *testing.T) {
	db := openCatalogStore(t)
	svc := newReadyService(t, db, newMetadataFake())
	ctx := context.Background()
	_, err := svc.CreateFactor(ctx, factorWithSpec("pmean", paramSpec))
	require.NoError(t, err)

	plain, err := svc.AddTaskMember(ctx, testTaskID(), "pmean", "", nil)
	require.NoError(t, err)
	require.Equal(t, "pmean", plain.MemberKey, "全默认沿用因子 ID")
	require.Equal(t, domain.MemberKindFixed, plain.Kind, "类型为空按固定成员")
	require.Equal(t, map[string]float64{"window": 5}, plain.Params, "没给的参数按定义的默认值补全")

	wide, err := svc.AddTaskMember(ctx, testTaskID(), "pmean", domain.MemberKindFixed, map[string]float64{"window": 10})
	require.NoError(t, err)
	require.Equal(t, "pmean__10", wide.MemberKey)
	require.Equal(t, map[string]float64{"window": 10}, wide.Params)

	_, err = svc.AddTaskMember(ctx, testTaskID(), "pmean", "", map[string]float64{"window": 10})
	require.ErrorIs(t, err, store.ErrConflict, "同因子同参数不能重复")
	_, err = svc.AddTaskMember(ctx, testTaskID(), "pmean", "", map[string]float64{})
	require.ErrorIs(t, err, store.ErrConflict, "不给参数等于全默认，与第一个重复")

	for name, params := range map[string]map[string]float64{
		"未声明的参数": {"nope": 1}, "越界": {"window": 99}, "整数参数给了小数": {"window": 7.5},
	} {
		_, err := svc.AddTaskMember(ctx, testTaskID(), "pmean", "", params)
		require.Error(t, err, name)
	}
	_, err = svc.AddTaskMember(ctx, testTaskID(), "pmean", "other", nil)
	require.ErrorContains(t, err, "成员类型")

	listed, err := db.ListMembers(ctx, testTaskID(), "")
	require.NoError(t, err)
	byKey := map[string]map[string]float64{}
	for _, member := range listed {
		byKey[member.MemberKey] = member.Params
	}
	require.Equal(t, map[string]map[string]float64{"pmean": {"window": 5}, "pmean__10": {"window": 10}}, byKey, "参数落库后能原样读回")
}

// 可变成员：成员键是 因子ID__var，params 是默认参数；同一个任务里同一个因子最多一个可变成员；没有参数的因子不能做可变成员。
func TestAddVarMember(t *testing.T) {
	db := openCatalogStore(t)
	svc := newReadyService(t, db, newMetadataFake())
	ctx := context.Background()
	_, err := svc.CreateFactor(ctx, factorWithSpec("pmean", paramSpec))
	require.NoError(t, err)

	variable, err := svc.AddTaskMember(ctx, testTaskID(), "pmean", domain.MemberKindVar, map[string]float64{"window": 8})
	require.NoError(t, err)
	require.Equal(t, "pmean__var", variable.MemberKey)
	require.Equal(t, domain.MemberKindVar, variable.Kind)
	require.Equal(t, map[string]float64{"window": 8}, variable.Params, "可变成员的默认参数")

	_, err = svc.AddTaskMember(ctx, testTaskID(), "pmean", domain.MemberKindVar, nil)
	require.ErrorIs(t, err, store.ErrConflict, "同一个因子最多一个可变成员")
	_, err = svc.AddTaskMember(ctx, testTaskID(), "pmean", domain.MemberKindFixed, map[string]float64{"window": 8})
	require.NoError(t, err, "同一个因子可以同时有固定成员")

	_, err = svc.CreateFactor(ctx, testFactor())
	require.NoError(t, err)
	_, err = svc.AddTaskMember(ctx, testTaskID(), "momentum", domain.MemberKindVar, nil)
	require.ErrorContains(t, err, "没有可调参数")
}

func TestAddMemberRejectsKeyThatIsTooLong(t *testing.T) {
	db := openCatalogStore(t)
	svc := newReadyService(t, db, newMetadataFake())
	ctx := context.Background()
	// 因子 ID 最长 24 个字符；多个参数的值都写进成员键后超过 44 个字符。
	longSpec := "params:\n  a: {type: int, min: 1, max: 99999, default: 1}\n  b: {type: float, min: 1, max: 99999999, default: 1}\n  c: {type: float, min: 1, max: 99999999, default: 1}\noutput: ts_mean(close, a) + b + c\n"
	_, err := svc.CreateFactor(ctx, factorWithSpec("a_rather_long_factor_id", longSpec))
	require.NoError(t, err)
	_, err = svc.AddTaskMember(ctx, testTaskID(), "a_rather_long_factor_id", "", map[string]float64{"a": 99999, "b": 99999999, "c": 99999999})
	require.ErrorContains(t, err, "超过")
	_, err = svc.AddTaskMember(ctx, testTaskID(), "a_rather_long_factor_id", domain.MemberKindVar, nil)
	require.NoError(t, err, "可变成员的键不带参数，不会太长")
}

// 被任务引用的定义：不允许增删参数、改类型、顺序、默认值，也不允许改输出；表达式可以改，范围可以改但必须仍包含成员保存的参数。
func TestUpdateFactorEnforcesDefinitionRestrictions(t *testing.T) {
	db := openCatalogStore(t)
	svc := newReadyService(t, db, newMetadataFake())
	ctx := context.Background()
	_, err := svc.CreateFactor(ctx, factorWithSpec("pmean", paramSpec))
	require.NoError(t, err)
	_, err = svc.AddTaskMember(ctx, testTaskID(), "pmean", "", map[string]float64{"window": 10})
	require.NoError(t, err)

	for name, spec := range map[string]string{
		"范围不再包含成员保存的参数": "params:\n  window: {type: int, min: 2, max: 8, default: 5}\noutput: ts_mean(close, window)\n",
		"参数改名": "params:\n  span: {type: int, min: 2, max: 30, default: 5}\noutput: ts_mean(close, span)\n",
		"新增参数": "params:\n  window: {type: int, min: 2, max: 30, default: 5}\n  shift: {type: int, min: 0, max: 5, default: 0}\noutput: ts_mean(close, window)\n",
		"改默认值": "params:\n  window: {type: int, min: 2, max: 30, default: 6}\noutput: ts_mean(close, window)\n",
		"删参数":  "output: ts_mean(close, 5)\n",
	} {
		_, err := svc.UpdateFactor(ctx, factorWithSpec("pmean", spec))
		require.Error(t, err, name)
	}

	wider := "params:\n  window: {type: int, min: 2, max: 60, default: 5}\noutput: ts_mean(close, window) + 1\n"
	updated, err := svc.UpdateFactor(ctx, factorWithSpec("pmean", wider))
	require.NoError(t, err, "表达式和范围可以改")
	require.Contains(t, updated.Spec, "+ 1")
	members, err := db.ListMembers(ctx, testTaskID(), "")
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"window": 10}, members[0].Params)

	// 没有被任务引用的定义不受限制。
	_, err = svc.CreateFactor(ctx, factorWithSpec("free", paramSpec))
	require.NoError(t, err)
	_, err = svc.UpdateFactor(ctx, factorWithSpec("free", "output: close\n"))
	require.NoError(t, err)
}

// 启用后：固定成员的输出列带 factor_params，可变成员的不带；每个结果数据集都有 __params 列；不再有参数列。
func TestMemberColumnsCarryContractAttributes(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	ctx := context.Background()
	_, err := svc.CreateFactor(ctx, factorWithSpec("pmean", paramSpec))
	require.NoError(t, err)
	for _, spec := range []struct {
		kind   string
		params map[string]float64
		key    string
	}{{domain.MemberKindFixed, nil, "pmean"}, {domain.MemberKindFixed, map[string]float64{"window": 10}, "pmean__10"}, {domain.MemberKindVar, nil, "pmean__var"}} {
		member, err := svc.AddTaskMember(ctx, testTaskID(), "pmean", spec.kind, spec.params)
		require.NoError(t, err)
		require.Equal(t, spec.key, member.MemberKey)
		_, err = svc.SetTaskMemberStatus(ctx, testTaskID(), member.MemberKey, domain.MemberStatusEnabled)
		require.NoError(t, err)
	}
	columns := resultColumns(meta, testTaskID())
	require.Equal(t, "fixed", columns["pmean"]["member_kind"])
	require.Equal(t, `{"window":5}`, columns["pmean"]["factor_params"])
	require.Equal(t, `{"window":10}`, columns["pmean__10"]["factor_params"])
	require.Equal(t, "var", columns["pmean__var"]["member_kind"])
	require.NotContains(t, columns["pmean__var"], "factor_params")
	require.Equal(t, "params", columns["__params"]["column_kind"])
	for name := range columns {
		require.NotContains(t, name, "__window", "不再有参数列：%s", name)
	}
}

// 关联：重复关联同一个实例幂等；换实例、清空都拒绝；没有成员的任务不能关联；一个实例只能关联一个任务。
func TestBindTask(t *testing.T) {
	ctx := context.Background()
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)

	_, err := svc.BindTask(ctx, testTaskID(), "inst-1")
	require.ErrorContains(t, err, "no members", "没有成员的任务不能关联")

	addMember(t, svc, testTaskID(), "momentum", domain.MemberStatusEnabled)
	bound, err := svc.BindTask(ctx, testTaskID(), "inst-1")
	require.NoError(t, err)
	require.Equal(t, "inst-1", bound.BoundInstanceID)
	again, err := svc.BindTask(ctx, testTaskID(), "inst-1")
	require.NoError(t, err)
	require.Equal(t, "inst-1", again.BoundInstanceID)

	_, err = svc.BindTask(ctx, testTaskID(), "inst-2")
	require.ErrorIs(t, err, store.ErrConflict, "换一个实例拒绝")
	_, err = svc.BindTask(ctx, testTaskID(), "")
	require.Error(t, err)

	other := newTask()
	other.TaskID, other.Name = "ftask_prices_other", "第二任务"
	_, err = svc.CreateTask(ctx, other)
	require.NoError(t, err)
	addMember(t, svc, other.TaskID, "momentum", domain.MemberStatusDisabled)
	_, err = svc.BindTask(ctx, other.TaskID, "inst-1")
	require.ErrorIs(t, err, store.ErrConflict, "一个实例只能关联一个任务")
}

// 任务名称含汉字、不超过 10 个字、空间内唯一；任务 ID 可以指定也可以随机生成。
func TestCreateTaskNameAndIDRules(t *testing.T) {
	ctx := context.Background()
	db := openCatalogStore(t)
	svc := NewService(db, newMetadataFake())

	bad := newTask()
	bad.Name = "momentum"
	_, err := svc.CreateTask(ctx, bad)
	require.ErrorContains(t, err, "汉字")
	bad.Name = "一二三四五六七八九十一"
	_, err = svc.CreateTask(ctx, bad)
	require.ErrorContains(t, err, "10")
	bad = newTask()
	bad.TaskID = "fset_old"
	_, err = svc.CreateTask(ctx, bad)
	require.Error(t, err)

	random := newTask()
	random.TaskID = ""
	created, err := svc.CreateTask(ctx, random)
	require.NoError(t, err)
	require.Regexp(t, `^ftask_[a-z0-9]{8}$`, created.TaskID)
	require.Equal(t, domain.ResultDatasetID(created.TaskID), created.ResultDatasetID)

	custom := newTask()
	custom.Name = "第二任务"
	custom.TaskID = "ftask_custom"
	got, err := svc.CreateTask(ctx, custom)
	require.NoError(t, err)
	require.Equal(t, "dataset_factor_custom", got.ResultDatasetID)

	dup := newTask()
	dup.TaskID = "ftask_dup"
	_, err = svc.CreateTask(ctx, dup)
	require.ErrorIs(t, err, store.ErrConflict, "名称在空间内重复")
}
