package input

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

// 策略池的绑定解析（设计 3.5）：池要绑定的因子任务是成员所需市场的并集；每个成员各自解析出自己的绑定（只看它用到的市场与列），
// 启用时连同成员的 DSL 文本与哈希一起固化进会话的 c_members_json。

// priceColumn 是策略池成员账本估值用的价格列（与年龄探针同一列）。
const priceColumn = ageProbeColumn

// PoolMemberDef 是启用时取到的一个成员定义。
type PoolMemberDef struct {
	ID         string
	StrategyID string
	DSLYaml    string
	DSLHash    string
	Strategy   dsl.Strategy
}

// PoolMember 是固化进会话 c_members_json 的一个成员：定义（DSL 文本与哈希）与按成员自己的市场、列解析出的绑定。
type PoolMember struct {
	ID         string   `json:"id"`
	StrategyID string   `json:"strategy_id"`
	DSLHash    string   `json:"dsl_hash"`
	DSLYaml    string   `json:"dsl_yaml"`
	Resolved   Resolved `json:"resolved"`
}

// PoolResolved 是策略池的解析结果：池自己的绑定（所有成员市场的并集）与每个成员的绑定和程序。
type PoolResolved struct {
	Resolved Resolved
	Program  *dsl.Program
	Members  []PoolMember
	// Programs 是成员编译好的程序，下标与 Members 一致。
	Programs []*dsl.Program
}

// MembersJSON 返回要写进会话的 c_members_json。
func (p PoolResolved) MembersJSON() (string, error) {
	raw, err := json.Marshal(p.Members)
	return string(raw), err
}

// ResolvePool 解析策略池的绑定。members 必须覆盖池的全部成员；taskIDs 是池绑定的因子任务，它们的市场必须恰好是成员所需市场的并集。
func ResolvePool(ctx context.Context, client Client, spaceID string, taskIDs []string, pool dsl.Strategy, members []PoolMemberDef) (PoolResolved, error) {
	if client == nil {
		return PoolResolved{}, errors.New("Storage 客户端未配置")
	}
	if len(taskIDs) == 0 || len(taskIDs) > 2 {
		return PoolResolved{}, fmt.Errorf("需要绑定一个或两个（现货、永续各一个）因子任务，实际 %d 个", len(taskIDs))
	}
	if err := dsl.Validate(&pool); err != nil {
		return PoolResolved{}, err
	}
	if !pool.IsPool() {
		return PoolResolved{}, errors.New("不是策略池")
	}
	byID := make(map[string]PoolMemberDef, len(members))
	strategies := make(map[string]dsl.Strategy, len(members))
	for _, member := range members {
		byID[member.ID] = member
		strategies[member.ID] = member.Strategy
	}
	union, err := dsl.ValidatePoolMembers(pool, strategies)
	if err != nil {
		return PoolResolved{}, err
	}
	views, err := resolveViews(ctx, client, spaceID, taskIDs, pool.Bar)
	if err != nil {
		return PoolResolved{}, err
	}
	bound := make([]string, 0, len(views))
	for _, item := range views {
		bound = append(bound, item.market)
	}
	if want := union.Markets(); strings.Join(bound, ",") != strings.Join(want, ",") {
		return PoolResolved{}, fmt.Errorf("策略池的成员需要绑定 %s 的因子任务，实际绑定了 %s", strings.Join(want, "、"), strings.Join(bound, "、"))
	}
	first := views[0]
	for _, item := range views[1:] {
		if !strings.EqualFold(item.view.Frequency, first.view.Frequency) || calendarOf(item) != calendarOf(first) {
			return PoolResolved{}, fmt.Errorf("两个 View 的周期或日历不一致：%s 是 %s/%s，%s 是 %s/%s", first.view.ViewID, first.view.Frequency, calendarOf(first), item.view.ViewID, item.view.Frequency, calendarOf(item))
		}
	}
	// 数据集的保留期要能装下预热：预热期数加成员里最长的回看（上一根与年龄判定）。
	need := dsl.PoolWarmBars(pool, memberStrategies(members)) + poolLookback(members)
	for _, item := range views {
		if bars, ok := retentionBars(item.dataset.Retention, calendarOf(first), strings.ToLower(first.view.Frequency)); ok && need > bars {
			return PoolResolved{}, fmt.Errorf("View %s 的数据集保留 %d 根，策略池预热需要 %d 根（预热 %d 期 + 成员最长回看 %d 根），请调大保留期或缩短轮动、择时的窗口",
				item.view.ViewID, bars, need, need-poolLookback(members), poolLookback(members))
		}
	}
	// 估值需要 close：账本按收盘价成交与结算。
	for _, item := range views {
		if _, ok := item.columns[priceColumn]; !ok {
			return PoolResolved{}, fmt.Errorf("View %s 没有 %s 列，策略池的成员账本无法估值", item.view.ViewID, priceColumn)
		}
	}
	resolved := Resolved{Market: union, Tasks: map[string]TaskBinding{}, Columns: map[string]ColumnBinding{}, Factors: map[string]string{},
		Bar: strings.ToLower(first.view.Frequency), Calendar: calendarOf(first), ViewColumns: commonColumns(views)}
	for _, item := range views {
		resolved.Tasks[item.market] = TaskBinding{ViewID: item.view.ViewID, FactorTaskID: item.taskID, DatasetID: item.view.DatasetID, SourceDatasetID: sourceDatasetID(item)}
	}
	out := PoolResolved{Resolved: resolved}
	previous := map[string]struct{}{}
	for _, poolMember := range pool.Pool.Members {
		def := byID[poolMember.ID]
		memberViews := make([]resolvedView, 0, len(views))
		for _, item := range views {
			if def.Strategy.Market.Has(item.market) {
				memberViews = append(memberViews, item)
			}
		}
		if len(memberViews) != len(def.Strategy.Market.Markets()) {
			return PoolResolved{}, fmt.Errorf("成员 %s 需要 %s 的因子任务，池没有绑定", poolMember.ID, strings.Join(def.Strategy.Market.Markets(), "、"))
		}
		memberResolved, program, err := bindViews(ctx, client, spaceID, memberViews, def.Strategy)
		if err != nil {
			return PoolResolved{}, fmt.Errorf("成员 %s：%w", poolMember.ID, err)
		}
		for name, binding := range memberResolved.Columns {
			if _, seen := out.Resolved.Columns[name]; !seen {
				out.Resolved.Columns[name] = binding
			}
		}
		for factor, contract := range memberResolved.Factors {
			if _, seen := out.Resolved.Factors[factor]; !seen {
				out.Resolved.Factors[factor] = contract
			}
		}
		for _, factor := range memberResolved.PreviousFactors {
			previous[factor] = struct{}{}
		}
		out.Members = append(out.Members, PoolMember{ID: poolMember.ID, StrategyID: def.StrategyID, DSLHash: def.DSLHash, DSLYaml: def.DSLYaml, Resolved: memberResolved})
		out.Programs = append(out.Programs, program)
	}
	out.Resolved.PreviousFactors = sortedKeys(previous)
	if out.Program, err = dsl.Compile(pool, out.Resolved.ViewColumns); err != nil {
		return PoolResolved{}, err
	}
	return out, nil
}

// ParseMembers 解析会话保存的 c_members_json 并重新编译每个成员（重启或恢复会话时使用）：返回成员与编译好的程序（下标一致）。
// 编译失败时返回错误，成员的绑定快照不变。
func ParseMembers(raw string) ([]PoolMember, []*dsl.Program, error) {
	var members []PoolMember
	if err := json.Unmarshal([]byte(raw), &members); err != nil {
		return nil, nil, &describedError{message: "会话快照的 members_json 无法解析", cause: err}
	}
	if len(members) == 0 {
		return nil, nil, errors.New("members_json 没有成员")
	}
	programs := make([]*dsl.Program, 0, len(members))
	for i := range members {
		member := &members[i]
		if member.ID == "" || member.Resolved.Market.IsZero() || len(member.Resolved.Tasks) == 0 {
			return nil, nil, fmt.Errorf("members_json 的成员 %q 缺少 id、market 或 tasks", member.ID)
		}
		if member.Resolved.Columns == nil {
			member.Resolved.Columns = map[string]ColumnBinding{}
		}
		if member.Resolved.Factors == nil {
			member.Resolved.Factors = map[string]string{}
		}
		compiled, program, err := Compile(member.Resolved, member.DSLYaml)
		if err != nil {
			return nil, nil, fmt.Errorf("成员 %s 按会话快照重新编译 DSL 失败：%w", member.ID, err)
		}
		member.Resolved = compiled
		programs = append(programs, program)
	}
	return members, programs, nil
}

// CheckPoolMemberCoverage 在启用策略池时按每个成员自己的 min_age_bars 与 A 股行键校验 View 的覆盖（见 CheckCoverage）。
func CheckPoolMemberCoverage(ctx context.Context, client Client, spaceID string, members []PoolMember, now time.Time) error {
	for _, member := range members {
		if err := CheckCoverage(ctx, client, spaceID, member.Resolved, now); err != nil {
			return fmt.Errorf("成员 %s：%w", member.ID, err)
		}
	}
	return nil
}

func memberStrategies(members []PoolMemberDef) []dsl.Strategy {
	strategies := make([]dsl.Strategy, 0, len(members))
	for _, member := range members {
		strategies = append(strategies, member.Strategy)
	}
	return strategies
}

// poolLookback 是成员里最长的回看根数：用到上一根的至少 1 根，min_age_bars 的年龄判定要多读 N+1 根。
func poolLookback(members []PoolMemberDef) int {
	lookback := 1
	for _, member := range members {
		if age := member.Strategy.Universe.MinAgeBars; age > 0 {
			lookback = max(lookback, age+2)
		}
	}
	return lookback
}

// PoolWarmGrace 是预热终点的宽限：最新一根刚收盘时因子结果还在路上，收盘超过它才算有数据（与追赶判断的口径一致）。
const PoolWarmGrace = time.Minute

// PoolWarmWindow 返回策略池预热的区间 [start, end)（bar_start）：以 now 之前（再留 grace 宽限）最新已收盘的一期为最后一期，往前共 bars 期。
func PoolWarmWindow(resolved Resolved, bars int, now time.Time, grace time.Duration) (start, end time.Time, err error) {
	closed, err := ClosedPeriod(resolved.Calendar, resolved.Bar, now.Add(-grace))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if start, err = HistoryStart(resolved.Calendar, resolved.Bar, closed.StorageStart, bars); err != nil {
		return time.Time{}, time.Time{}, err
	}
	return start, closed.StorageStart.Add(time.Nanosecond), nil
}

// PoolWindow 复查策略池历史推进（预热）的区间 [start, end)：每个 View 按精确统计读一次覆盖范围，再按每个成员自己的 min_age_bars 与回看要求
// 校验起点（起点早于数据覆盖范围返回 *EarlyStartError）、截断终点（取各 View、各成员中最早的）；返回截断后的终点。
// includeLatest 为真时终点放宽到每个 View 里已闭合的最新一根（但不超过 end）：研究回放把最新一根当作可能还没写完而排除在外，
// 预热的终点之后没有别的路径会处理它，所以预热不能排除。views 是各市场的 View（不带覆盖统计）。
func PoolWindow(ctx context.Context, client Client, spaceID string, resolved Resolved, members []PoolMember, programs []*dsl.Program, views map[string]ViewInfo, start, end, now time.Time, includeLatest bool) (time.Time, error) {
	covered := make(map[string]ViewInfo, len(views))
	for market, view := range views {
		var err error
		if covered[market], err = WithCoverage(ctx, client, spaceID, view, true); err != nil {
			return time.Time{}, err
		}
	}
	limit := end
	for i, member := range members {
		for _, market := range member.Resolved.Markets() {
			next, err := ReplayWindow(member.Resolved, programs[i], covered[market], start, end, now)
			if err != nil {
				return time.Time{}, fmt.Errorf("成员 %s 的 %s 市场 View %s：%w", member.ID, market, covered[market].ViewID, err)
			}
			end = next
		}
	}
	if includeLatest {
		end = limit
		for market, view := range covered {
			bounds, err := CoverageBounds(view, resolved, now)
			if err != nil {
				return time.Time{}, fmt.Errorf("%s 市场 View %s：%w", market, view.ViewID, err)
			}
			if next := bounds.To.Add(time.Nanosecond); next.Before(end) {
				end = next
			}
		}
	}
	if !end.After(start) {
		return time.Time{}, errors.New("历史推进的区间在数据覆盖范围内没有完整的 bar")
	}
	return end, nil
}

// CheckPoolWarmCoverage 在启用策略池时按精确的覆盖统计核对 View 的实际历史够不够预热（只核对配置的保留期不够：历史还没攒够的 View 上
// 启用成功，预热随后会永久失败）。不够时返回说明原因的错误，不启用。
func CheckPoolWarmCoverage(ctx context.Context, client Client, spaceID string, pooled PoolResolved, now time.Time) error {
	strategies := make([]dsl.Strategy, 0, len(pooled.Programs))
	for _, program := range pooled.Programs {
		strategies = append(strategies, program.Strategy)
	}
	start, end, err := PoolWarmWindow(pooled.Resolved, dsl.PoolWarmBars(pooled.Program.Strategy, strategies), now, PoolWarmGrace)
	if err != nil {
		return err
	}
	views := make(map[string]ViewInfo, len(pooled.Resolved.Tasks))
	for market, task := range pooled.Resolved.Tasks {
		view, err := client.GetView(ctx, spaceID, task.ViewID)
		if err != nil {
			return err
		}
		view.ViewID = task.ViewID
		views[market] = view
	}
	if _, err := PoolWindow(ctx, client, spaceID, pooled.Resolved, pooled.Members, pooled.Programs, views, start, end, now, true); err != nil {
		return fmt.Errorf("View 的历史不够策略池预热，不能启用：%w", err)
	}
	return nil
}
