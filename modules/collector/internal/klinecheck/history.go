package klinecheck

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"trpc.group/trpc-go/trpc-go/log"
)

// 历史缺口：早于数据集最早采集计划生效点的时间。
//
// 采集计划只记录“从哪个周期起实时采集”，更早的数据来自旧的采集方式或历史导入，检查与自动补采都不看这段时间，
// 那里的孔洞（例如一次写入故障漏掉的一根 K 线）没有任何东西会发现。这里按数据集定期读回生效点之前的一段，
// 报告其中的缺口；只告警不补采：更早时间的标的集合与行情源能力已无记录，由操作员确认后用 force 补采。

const (
	// historyScanBars 是每次向前读回的 K 线根数（相对数据集最早的生效点）。
	historyScanBars = 500
	// historyScanInterval 是同一数据集重复扫描的间隔：这段历史不再有新数据，缺口只会被补采修复，不需要频繁重读。
	historyScanInterval = 24 * time.Hour
	// historyScanSpacing 是同一空间两次历史扫描之间的最小间隔：一次只扫一个数据集，避开自动补采与采集高峰。
	historyScanSpacing = 5 * time.Minute
	// historyRetryAfter 是扫描失败后重试的等待时间。
	historyRetryAfter = time.Hour
	// maxHistoryGapLogs 是一次扫描最多逐条记录的缺口组数，其余只在汇总里计数。
	maxHistoryGapLogs = 50
	// historySampleSubjects 是每组缺口日志里列出的标的样例数。
	historySampleSubjects = 5
)

// HistoryGap 是同一个数据集里范围相同的历史缺口，合并了所有受影响的标的。
type HistoryGap struct {
	DatasetID string
	Frequency string
	From      time.Time
	To        time.Time
	// Missing 是每个受影响标的在 [From, To) 内缺的根数。
	Missing  int
	Subjects []string
}

// HistoryResult 是一个数据集（某个频率）生效点之前一段历史的检查结果。
type HistoryResult struct {
	DatasetID string
	Frequency string
	// From、To 是读回的范围：向前 historyScanBars 根，止于最早的生效点。
	From time.Time
	To   time.Time
	// Subjects 是数据集在最早生效点的标的数；WithHistory 是范围内读到过数据的标的数。
	Subjects    int
	WithHistory int
	Gaps        []HistoryGap
}

// CheckHistory 读回数据集最早采集计划生效点之前的一段历史，报告每个标的自己第一根 K 线之后的缺口。
// 标的第一根 K 线之前的缺失视为“尚无历史”（新上市）；范围内一根都没有的标的不报。
// 数据集不在任何已登记的计划里时返回 nil。
func (c *Checker) CheckHistory(ctx context.Context, spaceID, datasetID, frequencyName string) (*HistoryResult, error) {
	if c == nil || c.Plans == nil || c.Rows == nil {
		return nil, fmt.Errorf("kline checker is not initialized")
	}
	frequency, err := marketdata.ParseFrequency(frequencyName)
	if err != nil {
		return nil, err
	}
	if c.CalendarReady != nil {
		if err := c.CalendarReady(spaceID); err != nil {
			return nil, fmt.Errorf("trading calendar is unavailable: %w", err)
		}
	}
	records, err := c.Plans.ListAll(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	timeline, err := BuildTimeline(records)
	if err != nil {
		return nil, err
	}
	segments := timeline[DatasetKey(datasetID, frequencyName)]
	if len(segments) == 0 {
		return nil, nil
	}
	first := segments[0]
	to := time.Unix(first.From, 0).UTC()
	result := &HistoryResult{DatasetID: datasetID, Frequency: frequencyName, From: stepBack(frequency, to, historyScanBars), To: to, Subjects: len(first.Subjects)}
	bars, err := c.expectedBars(spaceID, frequency, frequencyName, result.From, to, 0)
	if err != nil {
		return nil, err
	}
	if len(bars) == 0 || len(first.Subjects) == 0 {
		return result, nil
	}
	if points := len(bars) * len(first.Subjects); points > MaxCheckPoints {
		return nil, fmt.Errorf("the history range covers about %d bars, above the limit %d", points, MaxCheckPoints)
	}

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		firstErr error
		gaps     []Gap
	)
	slots := make(chan struct{}, defaultConcurrent)
	if c.Concurrency > 0 {
		slots = make(chan struct{}, c.Concurrency)
	}
	for from := 0; from < len(first.Subjects); from += subjectsPerRead {
		batch := first.Subjects[from:min(from+subjectsPerRead, len(first.Subjects))]
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			present, err := c.readPresent(ctx, spaceID, datasetID, frequencyName, batch, bars[0], bars[len(bars)-1].Add(time.Second), []string{"close"}, false)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("read history %s %s %s 等 %d 个标的: %w", datasetID, frequencyName, batch[0], len(batch), err)
				}
				return
			}
			for _, subject := range batch {
				start := firstPresent(bars, present[subject])
				if start < 0 {
					continue
				}
				result.WithHistory++
				gaps = append(gaps, subjectGaps(datasetID, frequencyName, subject, frequency, bars[start:], present[subject])...)
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	result.Gaps = groupHistoryGaps(gaps)
	return result, nil
}

// stepBack 返回 at 之前 n 根 K 线的开始时间（月线按日历月回退）。
func stepBack(frequency marketdata.Frequency, at time.Time, n int) time.Time {
	if frequency.Duration() > 0 {
		return at.Add(-time.Duration(n) * frequency.Duration())
	}
	return at.AddDate(0, -n, 0)
}

// firstPresent 返回标的第一根已存在的 K 线在 bars 里的下标；一根都没有时返回 -1。
func firstPresent(bars []time.Time, present map[int64]bool) int {
	for i, bar := range bars {
		if present[bar.Unix()] {
			return i
		}
	}
	return -1
}

// groupHistoryGaps 把范围相同的缺口合并成一组，按起点排序，标的按字典序。
func groupHistoryGaps(gaps []Gap) []HistoryGap {
	type groupKey struct{ from, to int64 }
	groups := map[groupKey]*HistoryGap{}
	for _, gap := range gaps {
		key := groupKey{gap.From.Unix(), gap.To.Unix()}
		group := groups[key]
		if group == nil {
			group = &HistoryGap{DatasetID: gap.DatasetID, Frequency: gap.Frequency, From: gap.From, To: gap.To, Missing: gap.Missing}
			groups[key] = group
		}
		group.Subjects = append(group.Subjects, gap.SubjectID)
	}
	out := make([]HistoryGap, 0, len(groups))
	for _, group := range groups {
		sort.Strings(group.Subjects)
		out = append(out, *group)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].From.Equal(out[j].From) {
			return out[i].From.Before(out[j].From)
		}
		return out[i].To.Before(out[j].To)
	})
	return out
}

// ScanHistory 在自动补采之后调用：每个空间每 historyScanSpacing 最多扫一个数据集，每个数据集每 historyScanInterval 扫一次，
// 把发现的历史缺口写进日志（只告警，不补采）。返回本次扫描的数据集结果；没有到期的数据集时返回 nil。
func (b *Backfiller) ScanHistory(ctx context.Context, spaceID string) (*HistoryResult, error) {
	now := b.now()
	b.mu.Lock()
	if b.lastHistoryRun == nil {
		b.lastHistoryRun = map[string]time.Time{}
		b.lastHistory = map[string]time.Time{}
	}
	if now.Sub(b.lastHistoryRun[spaceID]) < historyScanSpacing {
		b.mu.Unlock()
		return nil, nil
	}
	b.mu.Unlock()

	records, err := b.Plans.ListAll(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	timeline, err := BuildTimeline(records)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(timeline))
	for key := range timeline {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	b.mu.Lock()
	var due string
	for _, key := range keys {
		if now.Sub(b.lastHistory[spaceID+"|"+key]) >= historyScanInterval {
			due = key
			break
		}
	}
	if due == "" {
		b.mu.Unlock()
		return nil, nil
	}
	b.lastHistoryRun[spaceID] = now
	b.lastHistory[spaceID+"|"+due] = now
	b.mu.Unlock()

	datasetID, frequencyName, _ := strings.Cut(due, "|")
	result, err := b.Checker.CheckHistory(ctx, spaceID, datasetID, frequencyName)
	if err != nil {
		// 失败（含被取消）后一小时再试，不等一整天。
		b.mu.Lock()
		b.lastHistory[spaceID+"|"+due] = now.Add(historyRetryAfter - historyScanInterval)
		b.mu.Unlock()
		return nil, fmt.Errorf("history scan %s: %w", due, err)
	}
	logHistory(ctx, spaceID, result)
	return result, nil
}

// logHistory 写扫描汇总与逐组缺口。缺口日志带上范围和补采命令，操作员确认行情源有这些 K 线后直接使用。
func logHistory(ctx context.Context, spaceID string, result *HistoryResult) {
	if result == nil {
		return
	}
	log.InfoContextf(ctx, "collector history scan space=%s dataset=%s frequency=%s range=%s~%s subjects=%d with_history=%d gap_groups=%d",
		spaceID, result.DatasetID, result.Frequency, result.From.Format(time.RFC3339), result.To.Format(time.RFC3339), result.Subjects, result.WithHistory, len(result.Gaps))
	for i, gap := range result.Gaps {
		if i >= maxHistoryGapLogs {
			log.WarnContextf(ctx, "collector_history_gap space=%s dataset=%s frequency=%s 还有 %d 组缺口未逐条列出", spaceID, result.DatasetID, result.Frequency, len(result.Gaps)-i)
			break
		}
		sample := gap.Subjects[:min(len(gap.Subjects), historySampleSubjects)]
		log.WarnContextf(ctx, "collector_history_gap space=%s dataset=%s frequency=%s from=%s to=%s missing_bars=%d subjects=%d sample=%s "+
			"hint=该范围早于数据集最早的采集计划生效点，检查与自动补采看不到；确认行情源有这些 K 线后用 moox-cli collector kline backfill --force --space-id %s --dataset-id %s --from %s --to %s 补采（行情源本来没有的 K 线，如尚未上市或无成交，可忽略）",
			spaceID, gap.DatasetID, gap.Frequency, gap.From.Format(time.RFC3339), gap.To.Format(time.RFC3339), gap.Missing, len(gap.Subjects), strings.Join(sample, ","),
			spaceID, gap.DatasetID, gap.From.Format(time.RFC3339), gap.To.Format(time.RFC3339))
	}
}
