package engine

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

type paramManagerFake struct {
	freezeErr error
	freezes   int
	reported  [][]domain.ParamFreeze
	conflicts []domain.ParamFreeze
	reportErr error
}

func (f *paramManagerFake) FreezeFactorParams(_ context.Context, setIDs []string, period time.Time) (map[string]map[string]domain.ParamFreeze, error) {
	f.freezes++
	if f.freezeErr != nil {
		return nil, f.freezeErr
	}
	snapshot := domain.ParamSnapshot{"window": {Default: 30}}
	return map[string]map[string]domain.ParamFreeze{setIDs[0]: {"fast": {SetID: setIDs[0], MemberAlias: "fast", PeriodTime: period,
		Snapshot: snapshot, SnapshotHash: snapshot.Hash(), Source: domain.ParamsRequested}}}, nil
}

func (f *paramManagerFake) ReportOfflineFreezes(_ context.Context, freezes []domain.ParamFreeze) ([]domain.ParamFreeze, error) {
	if f.reportErr != nil {
		return nil, f.reportErr
	}
	f.reported = append(f.reported, freezes)
	return f.conflicts, nil
}

func (f *paramManagerFake) ListParamIntervals(context.Context, string, time.Time, time.Time) (map[string][]domain.ParamInterval, error) {
	return nil, nil
}

func paramMember() domain.MemberDef {
	def := domain.MustDefinition("bias", "bias", "params:\n  window: {type: int, min: 2, max: 60, default: 20}\noutputs:\n  bias: close / ts_mean(close, window)\n")
	return domain.MemberDef{Alias: "fast", Factor: def}
}

var paramSet = domain.FactorSet{SetID: "fset_i_x", SpaceID: "crypto", Freq: "1m"}

func newTestFreezer(t *testing.T, manager paramManager, file string) *paramFreezer {
	t.Helper()
	freezer, err := newParamFreezer(manager, file, 30*time.Millisecond)
	require.NoError(t, err)
	freezer.retry = 5 * time.Millisecond
	return freezer
}

// 管理端不可达时离线冻结：沿用最近一次冻结的快照（从来没有过就用默认值），记下待补报；同一期重试沿用同一份、不重复记录。
func TestParamFreezerFreezesOfflineWithCarryAndDedup(t *testing.T) {
	file := filepath.Join(t.TempDir(), "params.json")
	manager := &paramManagerFake{}
	freezer := newTestFreezer(t, manager, file)
	offline := 0
	freezer.onOffline = func(string) { offline++ }
	t0 := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)

	online, err := freezer.Freeze(context.Background(), paramSet, []domain.MemberDef{paramMember()}, t0)
	require.NoError(t, err)
	require.Equal(t, domain.ParamsRequested, online["fast"].Source)

	manager.freezeErr = errors.New("dial tcp: connection refused")
	got, err := freezer.Freeze(context.Background(), paramSet, []domain.MemberDef{paramMember()}, t0.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, domain.ParamsCarriedOffline, got["fast"].Source)
	require.Equal(t, 30.0, got["fast"].Snapshot.Value("window", "BTC"), "沿用最近一次冻结")
	require.Greater(t, manager.freezes, 2, "超时之前重试过")
	again, err := freezer.Freeze(context.Background(), paramSet, []domain.MemberDef{paramMember()}, t0.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, got["fast"].SnapshotHash, again["fast"].SnapshotHash)
	require.Equal(t, 1, freezer.OfflinePending())
	require.Equal(t, 1, offline)

	// 重启后待补报的记录还在；没有冻结过的集合按默认值离线冻结。
	restarted := newTestFreezer(t, manager, file)
	require.Equal(t, 1, restarted.OfflinePending())
	other := paramSet
	other.SetID = "fset_i_y"
	fresh, err := restarted.Freeze(context.Background(), other, []domain.MemberDef{paramMember()}, t0)
	require.NoError(t, err)
	require.Equal(t, 20.0, fresh["fast"].Snapshot.Value("window", "BTC"))
}

// 补报成功后清掉记录；管理端已有不同记录的期次交给 onConflict（以管理端为准）。补报失败时记录保留。
func TestParamFreezerFlushReportsAndKeepsOnFailure(t *testing.T) {
	manager := &paramManagerFake{freezeErr: errors.New("unreachable")}
	freezer := newTestFreezer(t, manager, filepath.Join(t.TempDir(), "params.json"))
	t0 := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	_, err := freezer.Freeze(context.Background(), paramSet, []domain.MemberDef{paramMember()}, t0)
	require.NoError(t, err)

	manager.reportErr = errors.New("still down")
	require.Error(t, freezer.Flush(context.Background()))
	require.Equal(t, 1, freezer.OfflinePending())

	manager.reportErr = nil
	manager.conflicts = []domain.ParamFreeze{{SetID: paramSet.SetID, MemberAlias: "fast", PeriodTime: t0, Source: domain.ParamsRequested}}
	var conflicts []domain.ParamFreeze
	freezer.onConflict = func(freeze domain.ParamFreeze) { conflicts = append(conflicts, freeze) }
	require.NoError(t, freezer.Flush(context.Background()))
	require.Zero(t, freezer.OfflinePending())
	require.Len(t, manager.reported, 1)
	require.Equal(t, domain.ParamsCarriedOffline, manager.reported[0][0].Source)
	require.Len(t, conflicts, 1)
}

// 租约冲突（另一个引擎接管）不离线冻结：这一期不算。
func TestParamFreezerLeaseConflictIsNotOffline(t *testing.T) {
	manager := &paramManagerFake{freezeErr: ErrLeaseConflict}
	freezer := newTestFreezer(t, manager, "")
	_, err := freezer.Freeze(context.Background(), paramSet, []domain.MemberDef{paramMember()}, time.Unix(60, 0).UTC())
	require.ErrorIs(t, err, ErrLeaseConflict)
	require.Zero(t, freezer.OfflinePending())
}
