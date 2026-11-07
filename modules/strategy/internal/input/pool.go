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
	// 数据集的保留期要能装下预热：预热期数（多取一期抵消终点前移）加成员里最长的回看（上一根与年龄判定）。
	need := dsl.PoolWarmBars(pool, memberStrategies(members)) + 1 + poolLookback(members)
	for _, item := range views {
		if bars, ok := retentionBars(item.dataset.Retention, calendarOf(first), strings.ToLower(first.view.Frequency)); ok && need > bars {
			return PoolResolved{}, fmt.Errorf("View %s 的数据集保留 %d 根，策略池预热需要 %d 根（预热 %d 期 + 成员最长回看 %d 根），请调大保留期或缩短轮动、择时的窗口",
				item.view.ViewID, bars, need, need-1-poolLookback(members), poolLookback(members))
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

// CheckPoolCoverage 在启用策略池时按每个成员自己的 min_age_bars 与 A 股行键校验 View 的覆盖（见 CheckCoverage）。
func CheckPoolCoverage(ctx context.Context, client Client, spaceID string, members []PoolMember, now time.Time) error {
	for _, member := range members {
		if err := CheckCoverage(ctx, client, spaceID, member.Resolved, now); err != nil {
			return fmt.Errorf("成员 %s：%w", member.ID, err)
		}
	}
	return nil
}

// PoolMemberIDs 返回成员 id（按池里的顺序）。
func PoolMemberIDs(members []PoolMember) []string {
	ids := make([]string, 0, len(members))
	for _, member := range members {
		ids = append(ids, member.ID)
	}
	return ids
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
