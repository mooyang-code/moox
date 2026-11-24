// Package klinecheck 检查 K 线数据的完整性并按缺口补采。
package klinecheck

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/seriesset"
)

// Segment 是一个数据集在 [From, To) 期间（K 线开始时间，Unix 秒；To 为 0 表示至今）应有的标的集合。
// 一个数据集只放一个市场的序列，每个标的只有一条序列，检查与补采按标的进行，读取不带 series_tag。
type Segment struct {
	From     int64
	To       int64
	Subjects []string
	// Symbols 是这一段每个标的的交易所符号（标的 → 符号）；补采按它取改名、换合约之前的旧符号。
	Symbols      map[string]string
	LatenessS    int
	OutputFields []string
}

// segmentSubjects 返回时间线行里序列的标的（大写、去重、升序）。
func segmentSubjects(series []seriesset.Series) []string {
	seen := map[string]bool{}
	var subjects []string
	for _, item := range series {
		subject := strings.ToUpper(item.SubjectID)
		if !seen[subject] {
			seen[subject] = true
			subjects = append(subjects, subject)
		}
	}
	sort.Strings(subjects)
	return subjects
}

// Timeline 是每个“数据集|频率”按时间排列的应有标的。
type Timeline map[string][]Segment

// DatasetKey 返回时间线使用的键。
func DatasetKey(datasetID, frequency string) string { return datasetID + "|" + frequency }

// TimelineSource 按写入先后返回空间的标的时间线行，由 *store.SeriesTimelineRepository 实现。
type TimelineSource interface {
	ListBySpace(ctx context.Context, spaceID string) ([]store.SeriesTimelineEntry, error)
}

// BuildTimeline 把标的时间线表的行（按写入先后）整理成每个数据集各段时间应有的标的。
// 一行非空序列结束上一段并开始新的一段；一行空序列只结束上一段（从这一刻起不再采集）。
//
// 行由采集计划的对账写入：Storage 确认登记后写序列行（未生效但已登记的计划也算），数据集不再采集时写停止行。
// 重新登记（Storage 被整体清空）时整个空间的行被删掉，包括已退役的数据集：时间线从重新登记后的新生效周期重新写起，
// 新快照登记之前没有任何一段，所以检查、自动补采与历史扫描的起点（最早一段的 From）都是清库后的新生效点。
func BuildTimeline(entries []store.SeriesTimelineEntry) Timeline {
	timeline := Timeline{}
	for _, entry := range entries {
		key := DatasetKey(entry.DatasetID, entry.Frequency)
		closeSegment(timeline, key, entry.EffectiveFrom)
		if len(entry.Series) == 0 {
			continue
		}
		timeline[key] = append(timeline[key], Segment{From: entry.EffectiveFrom, Subjects: segmentSubjects(entry.Series), Symbols: entry.Symbols, LatenessS: entry.LatenessS, OutputFields: entry.OutputFields})
	}
	return timeline
}

// loadTimeline 读出空间的时间线行并整理成时间线。
func loadTimeline(ctx context.Context, source TimelineSource, spaceID string) (Timeline, error) {
	entries, err := source.ListBySpace(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	return BuildTimeline(entries), nil
}

// Active 报告数据集是否仍在采集：最后一段没有结束。已停止采集的数据集不再有新数据，结果数据可能已随任务删除，
// 自动补采与历史扫描都跳过它们。
func (t Timeline) Active(key string) bool {
	segments := t[key]
	return len(segments) > 0 && segments[len(segments)-1].To == 0
}

// SymbolAt 返回标的在 at 时刻（K 线开始时间）的交易所符号：取覆盖 at 的那一段；早于最早一段的时间沿用最早一段的符号
// （重新登记会清空时间线，此前的符号历史不保留）。这一段没有该标的时返回 false。
func (t Timeline) SymbolAt(key, subject string, at time.Time) (string, bool) {
	segments := t[key]
	if len(segments) == 0 {
		return "", false
	}
	segment := segments[0]
	for _, candidate := range segments {
		if candidate.From <= at.Unix() {
			segment = candidate
		}
	}
	symbol, ok := segment.Symbols[strings.ToUpper(subject)]
	return symbol, ok && symbol != ""
}

// closeSegment 把最后一段在 at 处结束。
func closeSegment(timeline Timeline, key string, at int64) {
	segments := timeline[key]
	if len(segments) == 0 {
		return
	}
	last := &segments[len(segments)-1]
	if last.To == 0 || last.To > at {
		last.To = at
	}
}
