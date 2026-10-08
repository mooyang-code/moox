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
// E(r) = 规则显式 pool 的范围或 universe 过滤后的 U；G(r) 由年龄探针决定。
func BuildSets(ctx context.Context, members Membership, strategy dsl.Strategy, subjects []Subject, eventUniverse []string, probe AgeProbe) (Sets, error) {
	sets := Sets{Expected: map[string][]string{}, AgedOut: map[string][]string{}, Subjects: map[string]Subject{}}
	bySubject := make(map[string]Subject, len(subjects))
	byInstrument := make(map[string]Subject, len(subjects))
	for _, subject := range subjects {
		if !subject.Active || subject.SubjectID == "" {
			continue
		}
		bySubject[subject.SubjectID] = subject
		byInstrument[subject.InstrumentID] = subject
	}
	universe := make(map[string]Subject)
	// nil 表示事件没有携带名单；非 nil 的空名单表示本期没有标的（回放中没有任何行的周期）。
	if eventUniverse == nil {
		sets.Notes = append(sets.Notes, "事件未携带标的名单，基础集合取数据集全部活跃标的")
		for id, subject := range bySubject {
			universe[id] = subject
		}
	} else {
		for _, id := range eventUniverse {
			if subject, ok := bySubject[id]; ok {
				universe[id] = subject
			} else if subject, ok := byInstrument[id]; ok {
				universe[subject.SubjectID] = subject
			}
		}
	}
	for _, subject := range universe {
		sets.Universe = append(sets.Universe, subject.InstrumentID)
		sets.Subjects[subject.InstrumentID] = subject
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
		if _, ok := set[subject.SubjectID]; ok {
			return true
		}
		_, ok := set[subject.InstrumentID]
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
		var tagged map[string]struct{}
		if len(strategy.Universe.Tags) > 0 {
			var err error
			if tagged, err = membersOf(strategy.Universe.Tags); err != nil {
				return nil, err
			}
		}
		excludedTags, err := membersOf(strategy.Universe.ExcludeTags)
		if err != nil {
			return nil, err
		}
		include := listed(strategy.Universe.Include)
		exclude := listed(strategy.Universe.Exclude)
		for _, subject := range universe {
			if tagged != nil && !inSet(subject, tagged) && !inSet(subject, include) {
				continue
			}
			if inSet(subject, excludedTags) || inSet(subject, exclude) {
				continue
			}
			defaultExpected = append(defaultExpected, subject.InstrumentID)
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
					expected = append(expected, subject.InstrumentID)
				}
			}
		case rule.Pool.Explicit && len(rule.Pool.Tags) > 0:
			tagged, err := membersOf(rule.Pool.Tags)
			if err != nil {
				return Sets{}, err
			}
			for _, subject := range universe {
				if inSet(subject, tagged) {
					expected = append(expected, subject.InstrumentID)
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
	if strategy.Universe.MinAgeBars > 0 {
		if probe == nil {
			return Sets{}, fmt.Errorf("策略设置了 min_age_bars，但没有年龄探针")
		}
		aged, err := probe(ctx, sets.Instruments())
		if err != nil {
			return Sets{}, err
		}
		for ruleID, expected := range sets.Expected {
			var out []string
			for _, id := range expected {
				if _, ok := aged[id]; !ok {
					out = append(out, id)
				}
			}
			sets.AgedOut[ruleID] = out
		}
	}
	return sets, nil
}

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
		selected := make([]Subject, 0, len(instruments))
		for _, id := range instruments {
			if subject, ok := subjects[id]; ok {
				selected = append(selected, subject)
			}
		}
		rows, _, err := client.QueryRows(ctx, spaceID, Query{ViewID: resolved.ViewID, DatasetID: view.DatasetID, Frequency: view.Frequency, Subjects: selected, Start: from, End: to, Columns: []string{"close"}, ExpectedIndexID: view.ActiveIndexID, ExpectedRevision: revision})
		if err != nil {
			return nil, fmt.Errorf("年龄探针查询：%w", err)
		}
		bySubject := make(map[string]string, len(subjects))
		for id, subject := range subjects {
			bySubject[subject.SubjectID] = id
		}
		satisfied := make(map[string]struct{}, len(rows))
		for _, row := range rows {
			if id, ok := bySubject[row.SubjectID]; ok {
				satisfied[id] = struct{}{}
			}
		}
		return satisfied, nil
	}
}
