package input

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
)

// LoadPrices 读取一期里若干（标的|市场）的收盘价（键是 engine.TargetKey），策略池的成员账本据此估值与成交。
// 价格与成员各自装配的帧无关：账本里持有、但成员本期不再选中的币也要有价，否则会被当成缺价清算，与回放读取全部标的的口径分叉。
// 只返回有价（收盘价为正）的键；同一标的同一周期有多个序列时价格不可信，按缺价处理（与回放一致）。
// 某个市场的 View 读取失败按错误返回，索引变化返回 ErrStale。
//
// fundingColumn 非空时（策略池写了 fees.funding_column）同时读永续这些键的这一列，按键返回本根累计的资金费率（只含有值的，
// 返回非 nil 的映射）；为空时 funding 为 nil。
func (l Loader) LoadPrices(ctx context.Context, spaceID string, resolved Resolved, bar Bar, keys []string, fundingColumn string) (prices, funding map[string]float64, err error) {
	if l.Client == nil {
		return nil, nil, fmt.Errorf("输入装配缺少客户端")
	}
	boundary, err := FromStorageStart(resolved.Calendar, resolved.Bar, bar.BarStart)
	if err != nil {
		return nil, nil, &SkipError{Reason: SkipConfigError, Detail: err.Error()}
	}
	if fundingColumn != "" {
		funding = map[string]float64{}
	}
	byMarket := map[string][]string{}
	for _, key := range keys {
		subjectID, market := engine.SplitTargetKey(key)
		byMarket[market] = append(byMarket[market], subjectID)
	}
	prices = make(map[string]float64, len(keys))
	for _, market := range resolved.Markets() {
		ids := byMarket[market]
		if len(ids) == 0 {
			continue
		}
		task := resolved.Tasks[market]
		view, err := l.Client.GetView(ctx, spaceID, task.ViewID)
		if err != nil {
			return nil, nil, err
		}
		if view.ActiveIndexID == "" {
			return nil, nil, fmt.Errorf("View %s 没有活动索引", task.ViewID)
		}
		if view.DatasetID != task.DatasetID {
			return nil, nil, &SkipError{Reason: SkipConfigError, Detail: fmt.Sprintf("View %s 的数据集已变化（%s → %s），请重新启用实例", task.ViewID, task.DatasetID, view.DatasetID)}
		}
		bindings, err := l.Client.ListDatasetSubjects(ctx, spaceID, view.DatasetID)
		if err != nil {
			return nil, nil, err
		}
		wanted := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			wanted[id] = struct{}{}
		}
		selected := make([]Subject, 0, len(ids))
		for _, subject := range bindings {
			if _, ok := wanted[subject.SubjectID]; ok {
				selected = append(selected, subject)
			}
		}
		if len(selected) == 0 {
			continue
		}
		sort.Slice(selected, func(i, j int) bool { return selected[i].SubjectID < selected[j].SubjectID })
		columns := []string{priceColumn}
		withFunding := funding != nil && market == dsl.MarketSwap
		if withFunding {
			columns = append(columns, fundingColumn)
		}
		rows, _, err := l.Client.QueryRows(ctx, spaceID, Query{ViewID: view.ViewID, DatasetID: view.DatasetID, Frequency: view.Frequency, Subjects: selected,
			Start: boundary.StorageStart, End: boundary.StorageStart.Add(time.Nanosecond), Columns: columns, ExpectedIndexID: view.ActiveIndexID})
		if err != nil {
			return nil, nil, err
		}
		tags := make(map[string]string, len(rows))
		ambiguous := map[string]struct{}{}
		closes := make(map[string]float64, len(rows))
		rates := map[string]float64{}
		for _, row := range rows {
			if row.SubjectID == "" || !row.DataTime.Equal(boundary.StorageStart) {
				continue
			}
			if tag, seen := tags[row.SubjectID]; seen && tag != row.SeriesTag {
				ambiguous[row.SubjectID] = struct{}{}
			}
			tags[row.SubjectID] = row.SeriesTag
			if price, ok := row.Values[priceColumn]; ok && price > 0 {
				closes[row.SubjectID] = price
			}
			if rate, ok := row.Values[fundingColumn]; withFunding && ok && !math.IsNaN(rate) && !math.IsInf(rate, 0) {
				rates[row.SubjectID] = rate
			}
		}
		for subjectID, price := range closes {
			if _, bad := ambiguous[subjectID]; !bad {
				prices[engine.TargetKey(subjectID, market)] = price
			}
		}
		for subjectID, rate := range rates {
			if _, bad := ambiguous[subjectID]; !bad {
				funding[engine.TargetKey(subjectID, market)] = rate
			}
		}
	}
	return prices, funding, nil
}
