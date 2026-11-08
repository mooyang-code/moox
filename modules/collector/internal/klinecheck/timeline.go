// Package klinecheck 检查 K 线数据的完整性并按缺口补采。
package klinecheck

import (
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/seriesset"
)

// Segment 是一个数据集在 [From, To) 期间（K 线开始时间，Unix 秒；To 为 0 表示至今）应有的标的集合。
type Segment struct {
	From     int64
	To       int64
	Subjects []string
	// Series 只在统一数据集（这一段里有 ≥2 个不同 series_tag）时有值：每个 series_tag 下应有的标的（升序）。
	// 有值时检查、补采按序列（标的、tag）进行；为空时数据集是单序列，不区分 tag，读取也不带 tag。
	Series       map[string][]string
	LatenessS    int
	OutputFields []string
}

// seriesGroup 是一批同属一个序列 tag 的标的；Tag 为空表示不区分序列。
type seriesGroup struct {
	Tag      string
	Subjects []string
}

// groups 返回这一段要检查的序列分组：单序列数据集只有一组（Tag 为空），统一数据集每个 tag 一组（按 tag 升序）。
func (s Segment) groups() []seriesGroup {
	if len(s.Series) == 0 {
		return []seriesGroup{{Subjects: s.Subjects}}
	}
	tags := make([]string, 0, len(s.Series))
	for tag := range s.Series {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	out := make([]seriesGroup, 0, len(tags))
	for _, tag := range tags {
		out = append(out, seriesGroup{Tag: tag, Subjects: s.Series[tag]})
	}
	return out
}

// segmentSeries 把计划里的序列（标的、tag）整理成一段的标的与按 tag 的成员：只有出现 ≥2 个不同 tag 才返回按序列的成员。
func segmentSeries(series []seriesset.Series) (subjects []string, byTag map[string][]string) {
	seen := map[string]bool{}
	members := map[string][]string{}
	for _, item := range series {
		subject := strings.ToUpper(item.SubjectID)
		if !seen[subject] {
			seen[subject] = true
			subjects = append(subjects, subject)
		}
		tag := strings.TrimSpace(item.SeriesTag)
		members[tag] = append(members[tag], subject)
	}
	sort.Strings(subjects)
	if len(members) < 2 {
		return subjects, nil
	}
	for tag := range members {
		sort.Strings(members[tag])
	}
	return subjects, members
}

// Timeline 是每个“数据集|频率”按时间排列的应有标的。
type Timeline map[string][]Segment

// DatasetKey 返回时间线使用的键。
func DatasetKey(datasetID, frequency string) string { return datasetID + "|" + frequency }

// BuildTimeline 由采集计划还原历史上每段时间应有的标的：已生效的计划，
// 以及已确认登记快照（P 已确定）的未生效计划。
//
// 一份计划里有被清空 hash 的数据集（marketfetch.FetchPlanContent.ClearedDatasets）表示该空间的 Storage 已被整体清空、
// 正在重新登记：此前的全部计划段连同数据一起作废，包括已退役的数据集（它们以后重新启用时同样从新的生效周期开始）。
// 每个数据集的时间线从重新登记后的新生效周期开始，新快照登记之前没有任何计划段，所以检查、自动补采与历史扫描的起点
// （最早一段的 From）都是清库后的新生效点。
func BuildTimeline(records []*store.FetchPlanRecord) (Timeline, error) {
	timeline := Timeline{}
	for _, record := range records {
		content, err := marketfetch.DecodeFetchPlanContent(record.Plan)
		if err != nil {
			return nil, err
		}
		progress, err := marketfetch.DecodeFetchPlanProgress(record.Progress)
		if err != nil {
			return nil, err
		}
		applied := record.Status == store.FetchPlanApplied
		keys := make([]string, 0, len(content.Datasets))
		for key := range content.Datasets {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			change := content.Datasets[key]
			if !change.Changed || change.P <= 0 {
				continue
			}
			switch {
			case change.Gone:
				closeSegment(timeline, key, change.P)
			case change.HashChanged && (applied || progress.Registered[key]):
				closeSegment(timeline, key, change.P)
				subjects, byTag := segmentSeries(change.Series)
				timeline[key] = append(timeline[key], Segment{From: change.P, Subjects: subjects, Series: byTag, LatenessS: change.LatenessS, OutputFields: change.OutputFields})
			}
		}
		// 快照清空发生在这份计划生效之后，所以先按上面还原它的变更，再丢掉此前的全部计划段。
		if len(content.ClearedDatasets()) > 0 {
			clear(timeline)
		}
	}
	return timeline, nil
}

// Active 报告数据集是否仍在采集：最后一段没有结束。已停止采集的数据集不再有新数据，结果数据可能已随任务删除，
// 自动补采与历史扫描都跳过它们。
func (t Timeline) Active(key string) bool {
	segments := t[key]
	return len(segments) > 0 && segments[len(segments)-1].To == 0
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
