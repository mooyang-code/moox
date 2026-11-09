package input

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/packages/seriesset"
)

// Membership 按标签查询成员；实时与回放共用。
type Membership func(ctx context.Context, tagID string) ([]string, error)

// BuildSets 划分一期的标的集合：
// U = 事件名单 ∩ 活跃标的（事件没有名单时为全部活跃标的）；
// E(r) = 规则显式 pool 的范围或 universe 过滤后的 U。年龄剔除 G(r) 由 ApplyAge 另行计算，
// 以便实时装配先读当期行、再用同一修订号执行年龄探针。
func BuildSets(ctx context.Context, members Membership, strategy dsl.Strategy, subjects []Subject, eventUniverse []string) (Sets, error) {
	sets := Sets{Expected: map[string][]string{}, AgedOut: map[string][]string{}, Subjects: map[string]Subject{}}
	active := make(map[string]Subject, len(subjects))
	for _, subject := range subjects {
		if !subject.Active || subject.SubjectID == "" {
			continue
		}
		active[subject.SubjectID] = subject
	}
	universe := make(map[string]Subject)
	// nil 表示事件没有携带名单；非 nil 的空名单表示本期没有标的（回放中没有任何行的周期）。
	if eventUniverse == nil {
		sets.Notes = append(sets.Notes, "事件未携带标的名单，基础集合取数据集全部活跃标的")
		universe = active
	} else {
		for _, id := range eventUniverse {
			if subject, ok := active[id]; ok {
				universe[id] = subject
			}
		}
	}
	for id, subject := range universe {
		sets.Universe = append(sets.Universe, id)
		sets.Subjects[id] = subject
	}
	sort.Strings(sets.Universe)
	tagCache := make(map[string]map[string]struct{})
	membersOf := func(tags []string) (map[string]struct{}, error) {
		union := make(map[string]struct{})
		for _, tag := range tags {
			if _, ok := tagCache[tag]; !ok {
				if members == nil {
					return nil, fmt.Errorf("策略引用了标签 %s，但没有标签查询能力", tag)
				}
				ids, err := members(ctx, tag)
				if err != nil {
					return nil, fmt.Errorf("读取标签 %s 的成员：%w", tag, err)
				}
				set := make(map[string]struct{}, len(ids))
				for _, id := range ids {
					set[id] = struct{}{}
				}
				tagCache[tag] = set
			}
			for id := range tagCache[tag] {
				union[id] = struct{}{}
			}
		}
		return union, nil
	}
	inSet := func(subject Subject, set map[string]struct{}) bool {
		_, ok := set[subject.SubjectID]
		return ok
	}
	listed := func(ids []string) map[string]struct{} {
		set := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			set[strings.TrimSpace(id)] = struct{}{}
		}
		return set
	}
	// filtered 返回 universe 过滤后的集合：白名单 = tags 成员 ∪ include，两者都为空时不设白名单（全部 U）；再去掉 exclude_tags 与 exclude。
	filtered := func(u dsl.Universe) ([]string, error) {
		var whitelist map[string]struct{}
		if len(u.Tags) > 0 || len(u.Include) > 0 {
			tagged, err := membersOf(u.Tags)
			if err != nil {
				return nil, err
			}
			whitelist = tagged
			for id := range listed(u.Include) {
				whitelist[id] = struct{}{}
			}
		}
		excludedTags, err := membersOf(u.ExcludeTags)
		if err != nil {
			return nil, err
		}
		exclude := listed(u.Exclude)
		var out []string
		for _, subject := range universe {
			if whitelist != nil && !inSet(subject, whitelist) {
				continue
			}
			if inSet(subject, excludedTags) || inSet(subject, exclude) {
				continue
			}
			out = append(out, subject.SubjectID)
		}
		sort.Strings(out)
		return out, nil
	}
	// 策略级 universe 的默认集合只算一次，给没有改写标的范围的规则共用；改写了标签或黑白名单的规则按合并后的 universe 各算一份。
	var defaultExpected []string
	defaultComputed := false
	computeDefault := func() ([]string, error) {
		if defaultComputed {
			return defaultExpected, nil
		}
		defaultComputed = true
		var err error
		defaultExpected, err = filtered(strategy.Universe)
		return defaultExpected, err
	}
	for _, rule := range strategy.Rules {
		var expected []string
		switch {
		case rule.Pool.Explicit && len(rule.Pool.Fixed) > 0:
			fixed := listed(rule.Pool.Fixed)
			for _, subject := range universe {
				if inSet(subject, fixed) {
					expected = append(expected, subject.SubjectID)
				}
			}
		case rule.Pool.Explicit && len(rule.Pool.Tags) > 0:
			tagged, err := membersOf(rule.Pool.Tags)
			if err != nil {
				return Sets{}, err
			}
			for _, subject := range universe {
				if inSet(subject, tagged) {
					expected = append(expected, subject.SubjectID)
				}
			}
		case rule.Universe.OverridesMembers():
			var err error
			if expected, err = filtered(strategy.RuleUniverse(rule)); err != nil {
				return Sets{}, err
			}
		default:
			var err error
			if expected, err = computeDefault(); err != nil {
				return Sets{}, err
			}
		}
		sort.Strings(expected)
		sets.Expected[rule.ID] = append([]string(nil), expected...)
	}
	return sets, nil
}

// ageProbeColumn 是年龄探针读取的列：只要该列在目标窗口内有行，就认为标的已有足够历史。
const ageProbeColumn = "close"

// AgeProbe 返回满足年龄要求的标的集合（有历史行即满足）；失败返回基础设施错误。
type AgeProbe func(ctx context.Context, instruments []string) (map[string]struct{}, error)

// AgeWindow 返回年龄探针的读取窗口：目标是 T − (N−1) 根，向前容忍两根，即 [T−(N+1), T−(N−1)] 三根 bar 的 bar_start。
func AgeWindow(calendar, bar string, barStart time.Time, minAgeBars int) (time.Time, time.Time, error) {
	from, err := HistoryStart(calendar, bar, barStart, minAgeBars+2)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	target, err := HistoryStart(calendar, bar, barStart, minAgeBars)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return from, target.Add(time.Nanosecond), nil
}

// NewAgeProbe 构造每期一次、判定 n 根年龄的探针查询：读取年龄窗口内的 close 列，有行即满足。
// revision 应与当期主查询一致；活跃序列的覆盖起点（CoverageStartAt）晚于目标根时无法判断年龄，返回 history_insufficient。
// market 是探针判断的市场：统一数据集上一个 View 里现货、永续各有各的历史，只有这个市场自己的序列有行才算年龄足够
// （老的现货币新上永续时，不能用现货的历史直接通过永续的年龄检查），通过记录也按（标的、市场）保存；方式 A 里 View 本身就决定了市场。
func NewAgeProbe(client Client, spaceID string, resolved Resolved, n int, view ViewInfo, market string, subjects map[string]Subject, barStart time.Time, revision uint64, meta *MetaCache) AgeProbe {
	return func(ctx context.Context, instruments []string) (map[string]struct{}, error) {
		if n <= 0 || len(instruments) == 0 {
			all := make(map[string]struct{}, len(instruments))
			for _, id := range instruments {
				all[id] = struct{}{}
			}
			return all, nil
		}
		from, to, err := AgeWindow(resolved.Calendar, resolved.Bar, barStart, n)
		if err != nil {
			// 日历换算是确定性的（数据不变、重试结果不变）：记 config_error，而不是按基础设施错误重试到预算耗尽。
			return nil, &SkipError{Reason: SkipConfigError, Detail: fmt.Sprintf("%s 的探针窗口无法换算：%v", resolved.AgeLabel(n), err)}
		}
		if target, start := to.Add(-time.Nanosecond), CoverageStartAt(view, resolved, barStart); !start.IsZero() && target.Before(start) {
			return nil, &SkipError{Reason: SkipHistoryInsufficient, Detail: fmt.Sprintf("%s 需要 %s 的数据，但 View %s 的活跃序列当前最早只覆盖到 %s", resolved.AgeLabel(n), PeriodLabel(resolved.Calendar, resolved.Bar, target), view.ViewID, PeriodLabel(resolved.Calendar, resolved.Bar, start))}
		}
		// 已证明年龄足够的标的直接放行，只探针其余的；全部已知时不发查询。
		cacheKey := AgeKey{SpaceID: spaceID, ViewID: view.ViewID, IndexID: view.ActiveIndexID, MinAgeBars: n, Market: market}
		candidates := instruments
		var hits []string
		if meta != nil {
			hits, candidates = meta.ageSplit(ctx, cacheKey, instruments)
		}
		satisfied := make(map[string]struct{}, len(instruments))
		for _, id := range hits {
			satisfied[id] = struct{}{}
		}
		if len(candidates) == 0 {
			return satisfied, nil
		}
		selected := make([]Subject, 0, len(candidates))
		for _, id := range candidates {
			if subject, ok := subjects[id]; ok {
				if resolved.Unified() {
					subject.SeriesTag = "" // 读全部序列，再按市场取
				}
				selected = append(selected, subject)
			}
		}
		rows, _, err := client.QueryRows(ctx, spaceID, Query{ViewID: view.ViewID, DatasetID: view.DatasetID, Frequency: view.Frequency, Subjects: selected, Start: from, End: to, Columns: []string{ageProbeColumn}, ExpectedIndexID: view.ActiveIndexID, ExpectedRevision: revision})
		if err != nil {
			return nil, fmt.Errorf("年龄探针查询：%w", err)
		}
		var passed []string
		for _, row := range rows {
			if resolved.Unified() && seriesset.SeriesTagMarket(row.SeriesTag) != market {
				continue
			}
			if _, ok := subjects[row.SubjectID]; ok {
				if _, dup := satisfied[row.SubjectID]; !dup {
					passed = append(passed, row.SubjectID)
				}
				satisfied[row.SubjectID] = struct{}{}
			}
		}
		if meta != nil {
			meta.ageRecord(ctx, cacheKey, passed)
		}
		if len(satisfied) == 0 {
			// 候选全部不满足时，确认窗口内整个数据集是否有数据：一行都没有说明是历史缺口（停机、维护），
			// 不能把全部标的当作新上市剔除。统一数据集上只看这个市场自己的序列（现货有数据、永续整段缺口时，
			// 不能因为读到了现货行就把永续全部判成新上市），所以读全部序列再按市场过滤，不能只取一行。
			all := make([]Subject, 0, len(subjects))
			for _, subject := range subjects {
				if resolved.Unified() {
					subject.SeriesTag = ""
				}
				all = append(all, subject)
			}
			sort.Slice(all, func(i, j int) bool { return all[i].SubjectID < all[j].SubjectID })
			confirm := Query{ViewID: view.ViewID, DatasetID: view.DatasetID, Frequency: view.Frequency, Subjects: all, Start: from, End: to, Columns: []string{ageProbeColumn}, ExpectedIndexID: view.ActiveIndexID, ExpectedRevision: revision}
			if !resolved.Unified() {
				confirm.Limit = 1
			}
			present, _, err := client.QueryRows(ctx, spaceID, confirm)
			if err != nil {
				return nil, fmt.Errorf("年龄探针确认查询：%w", err)
			}
			if resolved.Unified() {
				present = rowsOfMarket(present, market)
			}
			if len(present) == 0 {
				return nil, &SkipError{Reason: SkipHistoryInsufficient, Detail: fmt.Sprintf("%s 的探针窗口 %s 至 %s 内整个数据集都没有数据（历史缺口），无法判断上市时间", resolved.AgeLabel(n), PeriodLabel(resolved.Calendar, resolved.Bar, from), PeriodLabel(resolved.Calendar, resolved.Bar, to.Add(-time.Nanosecond)))}
			}
		}
		return satisfied, nil
	}
}
