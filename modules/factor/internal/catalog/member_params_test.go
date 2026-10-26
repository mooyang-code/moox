package catalog

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
)

// 共享任务的成员带固定参数：没给的参数取定义的默认值补全，别名按参数生成（全默认沿用因子 ID），
// 同任务内同因子同参数只能有一个成员。
func TestAddFactorToTaskFixesParamsAndDerivesAlias(t *testing.T) {
	db := openCatalogStore(t)
	svc := newReadyService(t, db, newMetadataFake())
	ctx := context.Background()
	_, err := svc.CreateFactor(ctx, factorWithSpec("pmean", paramSpec))
	require.NoError(t, err)

	plain, err := svc.AddTaskMember(ctx, testTaskID(), "", "pmean", nil)
	require.NoError(t, err)
	require.Equal(t, "pmean", plain.MemberAlias, "全默认沿用因子 ID")
	require.Equal(t, map[string]float64{"window": 5}, plain.Params, "没给的参数按定义的默认值补全")

	wide, err := svc.AddTaskMember(ctx, testTaskID(), "", "pmean", map[string]float64{"window": 10})
	require.NoError(t, err)
	require.Equal(t, "pmean_10", wide.MemberAlias)
	require.Equal(t, map[string]float64{"window": 10}, wide.Params)

	named, err := svc.AddTaskMember(ctx, testTaskID(), "mean20", "pmean", map[string]float64{"window": 20})
	require.NoError(t, err)
	require.Equal(t, "mean20", named.MemberAlias, "手填的别名优先")

	_, err = svc.AddTaskMember(ctx, testTaskID(), "another", "pmean", map[string]float64{"window": 10})
	require.ErrorIs(t, err, store.ErrConflict, "同因子同参数不能重复")
	_, err = svc.AddTaskMember(ctx, testTaskID(), "another", "pmean", map[string]float64{})
	require.ErrorIs(t, err, store.ErrConflict, "不给参数等于全默认，与第一个重复")

	for name, params := range map[string]map[string]float64{
		"未声明的参数": {"nope": 1}, "越界": {"window": 99}, "整数参数给了小数": {"window": 7.5},
	} {
		_, err := svc.AddTaskMember(ctx, testTaskID(), "bad", "pmean", params)
		require.Error(t, err, name)
	}

	listed, err := db.ListMembers(ctx, testTaskID(), "")
	require.NoError(t, err)
	require.Len(t, listed, 3)
	byAlias := map[string]map[string]float64{}
	for _, member := range listed {
		byAlias[member.MemberAlias] = member.Params
	}
	require.Equal(t, map[string]map[string]float64{
		"pmean": {"window": 5}, "pmean_10": {"window": 10}, "mean20": {"window": 20},
	}, byAlias, "固定参数落库后能原样读回")
}

func TestAddFactorToTaskRejectsParamsWhenAliasWouldBeTooLong(t *testing.T) {
	db := openCatalogStore(t)
	svc := newReadyService(t, db, newMetadataFake())
	ctx := context.Background()
	_, err := svc.CreateFactor(ctx, factorWithSpec("a_rather_long_factor_identifier", paramSpec))
	require.NoError(t, err)
	_, err = svc.AddTaskMember(ctx, testTaskID(), "", "a_rather_long_factor_identifier", map[string]float64{"window": 10})
	require.ErrorContains(t, err, "手动指定别名")
	_, err = svc.AddTaskMember(ctx, testTaskID(), "short10", "a_rather_long_factor_identifier", map[string]float64{"window": 10})
	require.NoError(t, err)
}

// 策略实例的专属任务：参数由实例逐期提交，成员不带固定参数，同一因子可以用两个别名各自调参。
func TestPrivateTaskMembersKeepNoFixedParams(t *testing.T) {
	db := openCatalogStore(t)
	svc := newReadyService(t, db, newMetadataFake())
	ctx := context.Background()
	_, err := svc.CreateFactor(ctx, factorWithSpec("pmean", paramSpec))
	require.NoError(t, err)
	result, err := svc.EnsureInstanceTask(ctx, instanceRequest(InstanceMember{Alias: "fast", FactorID: "pmean"}, InstanceMember{Alias: "slow", FactorID: "pmean"}))
	require.NoError(t, err)
	for _, member := range result.Members {
		require.Empty(t, member.Params, "专属任务的成员没有固定参数")
	}

	_, err = svc.AddTaskMember(ctx, result.Set.TaskID, "extra", "pmean", map[string]float64{"window": 9})
	require.ErrorContains(t, err, "不能带固定参数")
}

// 定义更新：成员保存的固定参数必须仍然合法；定义默认值改了，成员的固定值不变。
func TestUpdateFactorChecksMemberFixedParams(t *testing.T) {
	db := openCatalogStore(t)
	svc := newReadyService(t, db, newMetadataFake())
	ctx := context.Background()
	_, err := svc.CreateFactor(ctx, factorWithSpec("pmean", paramSpec))
	require.NoError(t, err)
	_, err = svc.AddTaskMember(ctx, testTaskID(), "", "pmean", map[string]float64{"window": 10})
	require.NoError(t, err)

	narrow := "params:\n  window: {type: int, min: 2, max: 8, default: 5}\noutputs:\n  mean: ts_mean(close, window)\n"
	_, err = svc.UpdateFactor(ctx, factorWithSpec("pmean", narrow))
	require.ErrorContains(t, err, "固定参数不再合法")

	renamed := "params:\n  span: {type: int, min: 2, max: 30, default: 5}\noutputs:\n  mean: ts_mean(close, span)\n"
	_, err = svc.UpdateFactor(ctx, factorWithSpec("pmean", renamed))
	require.ErrorContains(t, err, "固定参数不再合法", "参数改名后成员的固定参数没有对应的声明")

	extra := "params:\n  window: {type: int, min: 2, max: 30, default: 5}\n  shift: {type: int, min: 0, max: 5, default: 0}\noutputs:\n  mean: ts_mean(close, window)\n"
	_, err = svc.UpdateFactor(ctx, factorWithSpec("pmean", extra))
	require.ErrorContains(t, err, "新增了参数 shift", "定义新增参数后，旧成员的固定参数不完整，要先移除再重加")

	newDefault := "params:\n  window: {type: int, min: 2, max: 30, default: 6}\noutputs:\n  mean: ts_mean(close, window)\n"
	_, err = svc.UpdateFactor(ctx, factorWithSpec("pmean", newDefault))
	require.NoError(t, err)
	members, err := db.ListMembers(ctx, testTaskID(), "")
	require.NoError(t, err)
	require.Len(t, members, 1)
	require.Equal(t, map[string]float64{"window": 10}, members[0].Params, "定义的默认值变了，成员的固定值不变")
}

// 带固定参数的成员启用后，结果数据集只有输出列，没有参数列；专属任务的成员仍有参数列。
func TestFixedParamMembersWriteNoParamColumns(t *testing.T) {
	db := openCatalogStore(t)
	meta := newMetadataFake()
	svc := newReadyService(t, db, meta)
	ctx := context.Background()
	_, err := svc.CreateFactor(ctx, factorWithSpec("pmean", paramSpec))
	require.NoError(t, err)
	for _, params := range []map[string]float64{nil, {"window": 10}} {
		member, err := svc.AddTaskMember(ctx, testTaskID(), "", "pmean", params)
		require.NoError(t, err)
		_, err = svc.SetTaskMemberStatus(ctx, testTaskID(), member.MemberAlias, domain.MemberStatusEnabled)
		require.NoError(t, err)
	}
	shared := resultColumns(meta, testTaskID())
	require.Contains(t, shared, "mean")
	require.Contains(t, shared, "pmean_10_mean")
	for name := range shared {
		require.NotContains(t, name, "__", "共享任务不写参数列：%s", name)
	}

	private, err := svc.EnsureInstanceTask(ctx, instanceRequest(InstanceMember{Alias: "fast", FactorID: "pmean"}))
	require.NoError(t, err)
	require.Contains(t, resultColumns(meta, private.Set.TaskID), "fast__window")
}

// 共享任务的成员每期按当前固定值冻结：同一别名移除后以不同的固定值重加，下一期用新值，不沿用旧的冻结快照。
func TestFreezeParamsFollowsReaddedFixedMember(t *testing.T) {
	db := openCatalogStore(t)
	svc := newReadyService(t, db, newMetadataFake())
	ctx := context.Background()
	_, err := svc.CreateFactor(ctx, factorWithSpec("pmean", paramSpec))
	require.NoError(t, err)
	enable := func(window float64) {
		_, err := svc.AddTaskMember(ctx, testTaskID(), "m", "pmean", map[string]float64{"window": window})
		require.NoError(t, err)
		_, err = svc.SetTaskMemberStatus(ctx, testTaskID(), "m", domain.MemberStatusEnabled)
		require.NoError(t, err)
	}
	windowOf := func(at time.Time) float64 {
		freezes, err := svc.FreezeParams(ctx, []string{testTaskID()}, at)
		require.NoError(t, err)
		require.Len(t, freezes[testTaskID()], 1)
		return freezes[testTaskID()][0].Snapshot.Value("window", "BTC")
	}

	base := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	enable(10)
	require.Equal(t, 10.0, windowOf(base))
	require.Equal(t, 10.0, windowOf(base.Add(time.Minute)))

	_, err = svc.SetTaskMemberStatus(ctx, testTaskID(), "m", domain.MemberStatusDisabled)
	require.NoError(t, err)
	require.NoError(t, svc.RemoveTaskMember(ctx, testTaskID(), "m"))
	enable(7)
	require.Equal(t, 7.0, windowOf(base.Add(2*time.Minute)), "同一别名换了固定值，下一期按新值冻结")
}
