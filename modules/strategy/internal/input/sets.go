package input

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
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
	// universe 过滤后的默认集合。
	var defaultExpected []string
	defaultComputed := false
	computeDefault := func() ([]string, error) {
		if defaultComputed {
			return defaultExpected, nil
		}
		defaultComputed = true
		// 白名单 = tags 成员 ∪ include；两者都为空时不设白名单（全部 U）。
		var whitelist map[string]struct{}
		if len(strategy.Universe.Tags) > 0 || len(strategy.Universe.Include) > 0 {
			tagged, err := membersOf(strategy.Universe.Tags)
			if err != nil {
				return nil, err
			}
			whitelist = tagged
			for id := range listed(strategy.Universe.Include) {
				whitelist[id] = struct{}{}
			}
		}
		excludedTags, err := membersOf(strategy.Universe.ExcludeTags)
		if err != nil {
			return nil, err
		}
		exclude := listed(strategy.Universe.Exclude)
		for _, subject := range universe {
			if whitelist != nil && !inSet(subject, whitelist) {
				continue
			}
			if inSet(subject, excludedTags) || inSet(subject, exclude) {
				continue
			}
			defaultExpected = append(defaultExpected, subject.SubjectID)
		}
		sort.Strings(defaultExpected)
		return defaultExpected, nil
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

// ApplyAge 用年龄探针计算各规则的 G(r)：∪E(r) 中探针未返回的标的视为不满足 min_age_bars。
func (s *Sets) ApplyAge(ctx context.Context, minAgeBars int, probe AgeProbe) error {
	if minAgeBars <= 0 {
		return nil
	}
	if probe == nil {
		return fmt.Errorf("策略设置了 min_age_bars，但没有年龄探针")
	}
	aged, err := probe(ctx, s.Instruments())
	if err != nil {
		return err
	}
	for ruleID, expected := range s.Expected {
		var out []string
		for _, id := range expected {
			if _, ok := aged[id]; !ok {
				out = append(out, id)
			}
		}
		s.AgedOut[ruleID] = out
	}
	return nil
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

// NewAgeProbe 构造每期一次的探针查询：读取年龄窗口内的 close 列，有行即满足。
// revision 应与当期主查询一致；活跃序列的覆盖起点（CoverageStart）晚于目标根时无法判断年龄，返回 history_insufficient。
func NewAgeProbe(client Client, spaceID string, resolved Resolved, view ViewInfo, subjects map[string]Subject, barStart time.Time, revision uint64) AgeProbe {
	return func(ctx context.Context, instruments []string) (map[string]struct{}, error) {
		if resolved.MinAgeBars <= 0 || len(instruments) == 0 {
			all := make(map[string]struct{}, len(instruments))
			for _, id := range instruments {
				all[id] = struct{}{}
			}
			return all, nil
		}
		from, to, err := AgeWindow(resolved.Calendar, resolved.Bar, barStart, resolved.MinAgeBars)
		if err != nil {
			return nil, err
		}
		if target, start := to.Add(-time.Nanosecond), CoverageStartAt(view, resolved, barStart); !start.IsZero() && target.Before(start) {
			return nil, &SkipError{Reason: SkipHistoryInsufficient, Detail: fmt.Sprintf("min_age_bars=%d 需要 %s 的数据，但 View %s 的活跃序列当前最早只覆盖到 %s", resolved.MinAgeBars, target.Format(time.RFC3339), resolved.ViewID, start.Format(time.RFC3339))}
		}
		selected := make([]Subject, 0, len(instruments))
		for _, id := range instruments {
			if subject, ok := subjects[id]; ok {
				selected = append(selected, subject)
			}
		}
		rows, _, err := client.QueryRows(ctx, spaceID, Query{ViewID: resolved.ViewID, DatasetID: view.DatasetID, Frequency: view.Frequency, Subjects: selected, Start: from, End: to, Columns: []string{ageProbeColumn}, ExpectedIndexID: view.ActiveIndexID, ExpectedRevision: revision})
		if err != nil {
			return nil, fmt.Errorf("年龄探针查询：%w", err)
		}
		satisfied := make(map[string]struct{}, len(rows))
		for _, row := range rows {
			if _, ok := subjects[row.SubjectID]; ok {
				satisfied[row.SubjectID] = struct{}{}
			}
		}
		if len(satisfied) == 0 {
			// 候选全部不满足时，确认窗口内整个数据集是否有数据：一行都没有说明是历史缺口（停机、维护），
			// 不能把全部标的当作新上市剔除。
			all := make([]Subject, 0, len(subjects))
			for _, subject := range subjects {
				all = append(all, subject)
			}
			sort.Slice(all, func(i, j int) bool { return all[i].SubjectID < all[j].SubjectID })
			present, _, err := client.QueryRows(ctx, spaceID, Query{ViewID: resolved.ViewID, DatasetID: view.DatasetID, Frequency: view.Frequency, Subjects: all, Start: from, End: to, Columns: []string{ageProbeColumn}, ExpectedIndexID: view.ActiveIndexID, ExpectedRevision: revision, Limit: 1})
			if err != nil {
				return nil, fmt.Errorf("年龄探针确认查询：%w", err)
			}
			if len(present) == 0 {
				return nil, &SkipError{Reason: SkipHistoryInsufficient, Detail: fmt.Sprintf("min_age_bars=%d 的探针窗口 %s 至 %s 内整个数据集都没有数据（历史缺口），无法判断上市时间", resolved.MinAgeBars, from.Format(time.RFC3339), to.Add(-time.Nanosecond).Format(time.RFC3339))}
			}
		}
		return satisfied, nil
	}
}
