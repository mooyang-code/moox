package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/factorwire"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"trpc.group/trpc-go/trpc-go/log"
)

// 参数冻结（因子动态调参执行计划 2.4、2.6）：开算一期之前向 FactorMgr 冻结这一期的参数，提交与冻结在管理端同一个库里
// 串行化，冻结记录是唯一事实来源。FactorMgr 在 FreezeTimeout 内都不可达时，引擎在本地按沿用规则冻结（沿用每个成员
// 最近一次冻结的快照，从来没有过就用默认值），计算不阻塞在参数上；离线冻结持久记在本地，恢复后补报，管理端已有
// 不同记录的期次以管理端为准并告警。

// FreezeFactorParams 冻结一组集合在 period 这一期的参数。
func (c *ManagerClient) FreezeFactorParams(ctx context.Context, setIDs []string, period time.Time) (map[string]map[string]domain.ParamFreeze, error) {
	var rsp factorpb.FreezeFactorParamsRsp
	req := &factorpb.FreezeFactorParamsReq{Engine: factorwire.EngineIdentityToPB(c.identity), SetIds: setIDs, PeriodTime: period.Unix()}
	if err := c.call(ctx, "FreezeFactorParams", req, &rsp, rsp.GetRetInfo); err != nil {
		return nil, err
	}
	out := make(map[string]map[string]domain.ParamFreeze, len(rsp.GetSets()))
	for _, set := range rsp.GetSets() {
		members := make(map[string]domain.ParamFreeze, len(set.GetMembers()))
		for _, member := range set.GetMembers() {
			members[member.GetMemberAlias()] = factorwire.FreezeFromPB(set.GetSetId(), period, member)
		}
		out[set.GetSetId()] = members
	}
	return out, nil
}

// ReportOfflineFreezes 补报离线冻结的期次，返回管理端已有不同记录的期次。
func (c *ManagerClient) ReportOfflineFreezes(ctx context.Context, freezes []domain.ParamFreeze) ([]domain.ParamFreeze, error) {
	var rsp factorpb.ReportOfflineFreezesRsp
	req := &factorpb.ReportOfflineFreezesReq{Engine: factorwire.EngineIdentityToPB(c.identity)}
	for _, freeze := range freezes {
		req.Freezes = append(req.Freezes, &factorpb.OfflineFreeze{SetId: freeze.SetID, MemberAlias: freeze.MemberAlias,
			PeriodTime: freeze.PeriodTime.Unix(), Params: factorwire.SnapshotToPB(freeze.Snapshot), Source: freeze.Source})
	}
	if err := c.call(ctx, "ReportOfflineFreezes", req, &rsp, rsp.GetRetInfo); err != nil {
		return nil, err
	}
	conflicts := make([]domain.ParamFreeze, 0, len(rsp.GetConflicts()))
	for _, item := range rsp.GetConflicts() {
		snapshot := factorwire.SnapshotFromPB(item.GetParams())
		conflicts = append(conflicts, domain.ParamFreeze{SetID: item.GetSetId(), MemberAlias: item.GetMemberAlias(),
			PeriodTime: time.Unix(item.GetPeriodTime(), 0).UTC(), Snapshot: snapshot, SnapshotHash: snapshot.Hash(), Source: item.GetSource()})
	}
	return conflicts, nil
}

// ListParamIntervals 返回集合在 [from, to] 上的冻结区间（含 from 之前最近的一个）：别名 → 按起点排序。
func (c *ManagerClient) ListParamIntervals(ctx context.Context, setID string, from, to time.Time) (map[string][]domain.ParamInterval, error) {
	var rsp factorpb.ListParamIntervalsRsp
	req := &factorpb.ListParamIntervalsReq{Engine: factorwire.EngineIdentityToPB(c.identity), SetId: setID, From: from.Unix(), To: to.Unix()}
	if err := c.call(ctx, "ListParamIntervals", req, &rsp, rsp.GetRetInfo); err != nil {
		return nil, err
	}
	out := map[string][]domain.ParamInterval{}
	for _, item := range rsp.GetIntervals() {
		interval := factorwire.IntervalFromPB(setID, item)
		out[interval.MemberAlias] = append(out[interval.MemberAlias], interval)
	}
	for alias := range out {
		sort.Slice(out[alias], func(i, j int) bool { return out[alias][i].Start.Before(out[alias][j].Start) })
	}
	return out, nil
}

// paramManager 是参数冻结用到的管理端接口。
type paramManager interface {
	FreezeFactorParams(ctx context.Context, setIDs []string, period time.Time) (map[string]map[string]domain.ParamFreeze, error)
	ReportOfflineFreezes(ctx context.Context, freezes []domain.ParamFreeze) ([]domain.ParamFreeze, error)
	ListParamIntervals(ctx context.Context, setID string, from, to time.Time) (map[string][]domain.ParamInterval, error)
}

// paramState 是本地持久化的参数状态：每个成员最近一次冻结的快照（离线冻结时沿用）与待补报的离线冻结。
type paramState struct {
	Last    map[string]lastFreeze `json:"last"`
	Offline []offlineFreeze       `json:"offline"`
}

type lastFreeze struct {
	Period   time.Time            `json:"period"`
	Snapshot domain.ParamSnapshot `json:"snapshot"`
}

type offlineFreeze struct {
	SetID    string               `json:"set_id"`
	Member   string               `json:"member"`
	Period   time.Time            `json:"period"`
	Snapshot domain.ParamSnapshot `json:"snapshot"`
}

// paramFreezer 实现 trigger.ParamFreezer 与 paramIntervalSource。
type paramFreezer struct {
	manager paramManager
	file    string
	timeout time.Duration
	retry   time.Duration
	// onOffline 每离线冻结一个成员一期时调用（指标与告警）；onConflict 在补报发现管理端记录不同时调用。
	onOffline  func(setID string)
	onConflict func(freeze domain.ParamFreeze)

	mu    sync.Mutex
	state paramState
	// lastOffline 是各成员最近一次离线冻结的时间（集合/别名 → 时间），监控据此告警；只在内存里，重启后清空。
	lastOffline map[string]time.Time
}

func newParamFreezer(manager paramManager, file string, timeout time.Duration) (*paramFreezer, error) {
	f := &paramFreezer{manager: manager, file: file, timeout: timeout, retry: 500 * time.Millisecond,
		state: paramState{Last: map[string]lastFreeze{}}}
	if timeout <= 0 {
		f.timeout = 5 * time.Second
	}
	raw, err := os.ReadFile(file)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return f, nil
	case err != nil:
		return nil, fmt.Errorf("read factor param state: %w", err)
	}
	if err := json.Unmarshal(raw, &f.state); err != nil {
		return nil, fmt.Errorf("decode factor param state %s: %w", file, err)
	}
	if f.state.Last == nil {
		f.state.Last = map[string]lastFreeze{}
	}
	return f, nil
}

func lastKey(setID, alias string) string { return setID + "/" + alias }

// Freeze 冻结集合在 period 这一期的参数。FactorMgr 在 timeout 内都不可达时离线冻结；租约冲突（另一个引擎接管）返回错误，
// 这一期不算。
func (f *paramFreezer) Freeze(ctx context.Context, set domain.FactorSet, members []domain.MemberDef, period time.Time) (map[string]domain.ParamFreeze, error) {
	deadline := time.Now().Add(f.timeout)
	var lastErr error
	for attempt := 0; ; attempt++ {
		callCtx, cancel := context.WithDeadline(ctx, deadline)
		frozen, err := f.manager.FreezeFactorParams(callCtx, []string{set.SetID}, period)
		cancel()
		if err == nil {
			out := frozen[set.SetID]
			f.remember(set.SetID, out)
			return out, nil
		}
		if errors.Is(err, ErrLeaseConflict) || ctx.Err() != nil {
			return nil, err
		}
		lastErr = err
		if time.Now().Add(f.retry).After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(f.retry):
		}
	}
	log.ErrorContextf(ctx, "factor_param_freeze_offline set_id=%s period=%s error=%v", set.SetID, period.UTC().Format(time.RFC3339), lastErr)
	return f.freezeOffline(set, members, period)
}

// remember 记下每个成员最近一次冻结的快照（只前进，不回退）并持久化。
func (f *paramFreezer) remember(setID string, freezes map[string]domain.ParamFreeze) {
	f.mu.Lock()
	defer f.mu.Unlock()
	changed := false
	for alias, freeze := range freezes {
		key := lastKey(setID, alias)
		if current, ok := f.state.Last[key]; ok && current.Period.After(freeze.PeriodTime) {
			continue
		}
		f.state.Last[key] = lastFreeze{Period: freeze.PeriodTime.UTC(), Snapshot: freeze.Snapshot}
		changed = true
	}
	if changed {
		if err := f.persistLocked(); err != nil {
			log.Errorf("factor_param_state_persist_failed error=%v", err)
		}
	}
}

func (f *paramFreezer) freezeOffline(set domain.FactorSet, members []domain.MemberDef, period time.Time) (map[string]domain.ParamFreeze, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]domain.ParamFreeze, len(members))
	for _, member := range members {
		// 同一期重试（事件重投）沿用已经离线冻结的那一份，不重复记录。
		snapshot, pending := f.pendingOfflineLocked(set.SetID, member.Alias, period)
		if !pending {
			snapshot = domain.MemberSnapshot(member)
			// 带固定参数的成员离线时也取自己的固定值；只有逐期提交参数的成员才沿用上一期的快照。
			if last, ok := f.state.Last[lastKey(set.SetID, member.Alias)]; ok && last.Snapshot != nil && !member.FixedParams() {
				snapshot = last.Snapshot
			}
			f.state.Offline = append(f.state.Offline, offlineFreeze{SetID: set.SetID, Member: member.Alias, Period: period.UTC(), Snapshot: snapshot})
			if f.lastOffline == nil {
				f.lastOffline = map[string]time.Time{}
			}
			f.lastOffline[lastKey(set.SetID, member.Alias)] = time.Now().UTC()
			if f.onOffline != nil {
				f.onOffline(set.SetID)
			}
		}
		out[member.Alias] = domain.ParamFreeze{SetID: set.SetID, MemberAlias: member.Alias, PeriodTime: period.UTC(), Snapshot: snapshot,
			SnapshotHash: snapshot.Hash(), Source: domain.ParamsCarriedOffline}
	}
	if err := f.persistLocked(); err != nil {
		// 离线冻结没能落盘：这一期仍按它计算，但重启后无法补报；按基础设施错误让这一期重试更稳妥。
		return nil, fmt.Errorf("persist offline param freeze: %w", err)
	}
	return out, nil
}

func (f *paramFreezer) pendingOfflineLocked(setID, alias string, period time.Time) (domain.ParamSnapshot, bool) {
	for _, item := range f.state.Offline {
		if item.SetID == setID && item.Member == alias && item.Period.Equal(period) {
			return item.Snapshot, true
		}
	}
	return nil, false
}

// Flush 补报离线冻结。成功后清掉已补报的记录；冲突的期次以管理端为准，交给 onConflict。
func (f *paramFreezer) Flush(ctx context.Context) error {
	f.mu.Lock()
	pending := append([]offlineFreeze(nil), f.state.Offline...)
	f.mu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	freezes := make([]domain.ParamFreeze, 0, len(pending))
	for _, item := range pending {
		freezes = append(freezes, domain.ParamFreeze{SetID: item.SetID, MemberAlias: item.Member, PeriodTime: item.Period,
			Snapshot: item.Snapshot, SnapshotHash: item.Snapshot.Hash(), Source: domain.ParamsCarriedOffline})
	}
	conflicts, err := f.manager.ReportOfflineFreezes(ctx, freezes)
	if err != nil {
		return err
	}
	for _, conflict := range conflicts {
		log.Errorf("factor_param_offline_conflict set_id=%s member=%s period=%s（管理端已有不同的冻结记录，以管理端为准；请对这一期手工重算）",
			conflict.SetID, conflict.MemberAlias, conflict.PeriodTime.Format(time.RFC3339))
		if f.onConflict != nil {
			f.onConflict(conflict)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state.Offline = f.state.Offline[len(pending):]
	return f.persistLocked()
}

// LastOffline 返回成员最近一次离线冻结的时间（没有为零值）。
func (f *paramFreezer) LastOffline(setID, member string) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastOffline[lastKey(setID, member)]
}

// OfflinePending 返回待补报的离线冻结数。
func (f *paramFreezer) OfflinePending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.state.Offline)
}

// Intervals 实现 paramIntervalSource：补算、恢复与回放按冻结记录计算历史周期。
func (f *paramFreezer) Intervals(ctx context.Context, setID string, from, to time.Time) (map[string][]domain.ParamInterval, error) {
	return f.manager.ListParamIntervals(ctx, setID, from, to)
}

// Run 定期补报离线冻结，直到 ctx 结束。
func (f *paramFreezer) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := f.Flush(ctx); err != nil && ctx.Err() == nil {
				log.WarnContextf(ctx, "factor_param_offline_flush_failed pending=%d error=%v", f.OfflinePending(), err)
			}
		}
	}
}

func (f *paramFreezer) persistLocked() error {
	if f.file == "" {
		return nil
	}
	raw, err := json.Marshal(f.state)
	if err != nil {
		return err
	}
	dir := filepath.Dir(f.file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".params-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.file)
}

var (
	_ trigger.ParamFreezer = (*paramFreezer)(nil)
	_ paramIntervalSource  = (*paramFreezer)(nil)
)
