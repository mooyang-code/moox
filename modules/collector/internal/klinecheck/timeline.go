// Package klinecheck 检查 K 线数据的完整性并按缺口补采。
package klinecheck

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/seriesset"
)

// Segment 是一个数据集在 [From, To) 期间（K 线开始时间，Unix 秒；To 为 0 表示至今）应有的标的集合。
// 一个数据集只放一个市场的序列，每个标的只有一条序列，检查与补采按标的进行，读取不带 series_tag。
type Segment struct {
	From         int64
	To           int64
	Subjects     []string
	LatenessS    int
	OutputFields []string
	// TimelineFrom 只在检查把最早一段前移到时间线起点之前时有值（见 extendEarliestSegments），是原来的起点：
	// 早于它的 K 线按“每个标的从自己的第一根起算”判断缺口。
	TimelineFrom int64
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

// incrementalTimelineSource 是支持按 c_id 轻量探测与增量读取的时间线存储（生产 *store.SeriesTimelineRepository 实现）。
type incrementalTimelineSource interface {
	TimelineSource
	TimelineStats(ctx context.Context, spaceID string) (count, minID, maxID int64, err error)
	ListBySpaceWithBounds(ctx context.Context, spaceID string) (entries []store.SeriesTimelineEntry, minID, maxID int64, err error)
	ListBySpaceAfter(ctx context.Context, spaceID string, afterID int64) (entries []store.SeriesTimelineEntry, lastID int64, err error)
}

// BuildTimeline 把标的时间线表的行（按写入先后）整理成每个数据集各段时间应有的标的。
// 一行非空序列结束上一段并开始新的一段；一行空序列只结束上一段（从这一刻起不再采集）。
//
// 行由采集计划的对账写入：Storage 确认登记后写序列行（未生效但已登记的计划也算），数据集不再采集时写停止行。
// 重新登记（Storage 被整体清空）时整个空间的行被删掉，包括已退役的数据集：时间线从重新登记后的新生效周期重新写起，
// 新快照登记之前没有任何一段，所以检查与自动补采的起点（最早一段的 From）都是清库后的新生效点。
func BuildTimeline(entries []store.SeriesTimelineEntry) Timeline {
	timeline := Timeline{}
	for _, entry := range entries {
		applyTimelineEntry(timeline, entry)
	}
	return timeline
}

func applyTimelineEntry(timeline Timeline, entry store.SeriesTimelineEntry) {
	key := DatasetKey(entry.DatasetID, entry.Frequency)
	closeSegment(timeline, key, entry.EffectiveFrom)
	if len(entry.Series) == 0 {
		return
	}
	timeline[key] = append(timeline[key], Segment{From: entry.EffectiveFrom, Subjects: segmentSubjects(entry.Series), LatenessS: entry.LatenessS, OutputFields: entry.OutputFields})
}

// cloneTimeline 复制时间线的 map 和各键的 Segment 切片，确保调用方修改（如 extendEarliestSegments）或增量构建不影响已发布的只读缓存。
func cloneTimeline(src Timeline) Timeline {
	out := make(Timeline, len(src))
	for key, segments := range src {
		copied := make([]Segment, len(segments))
		copy(copied, segments)
		out[key] = copied
	}
	return out
}

type spaceTimelineCache struct {
	mu       sync.Mutex
	loaded   bool
	count    int64
	minID    int64
	maxID    int64
	timeline Timeline
}

// timelineCache 按空间在内存中增量缓存只读 Timeline，避免每次分块执行都全表反序列化大 JSON。
// 每次调用先查轻量 (count, min_id, max_id) 统计：
//   - 三者未变时直接命中内存缓存（零 JSON 反序列化）；
//   - 仅追加新行时（count 增加量等于增量行数且 min_id 不变）只解码 c_id > max_id 的增量行并构造新不可变快照；
//   - 发生覆盖删行或整空间清空重登记时自动回退全量重建，绝不残留已删旧行。
type timelineCache struct {
	mu     sync.Mutex
	spaces map[string]*spaceTimelineCache
}

func (c *timelineCache) spaceEntry(spaceID string) *spaceTimelineCache {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.spaces == nil {
		c.spaces = map[string]*spaceTimelineCache{}
	}
	entry := c.spaces[spaceID]
	if entry == nil {
		entry = &spaceTimelineCache{}
		c.spaces[spaceID] = entry
	}
	return entry
}

// Load 返回该空间最新的只读 Timeline。调用方不得原地修改返回的 Timeline 或其切片元素（需修改时请先调用 cloneTimeline）。
func (c *timelineCache) Load(ctx context.Context, source TimelineSource, spaceID string) (Timeline, error) {
	if c == nil {
		return loadTimeline(ctx, source, spaceID)
	}
	inc, ok := source.(incrementalTimelineSource)
	if !ok {
		return loadTimeline(ctx, source, spaceID)
	}
	key := strings.TrimSpace(spaceID)
	entry := c.spaceEntry(key)
	entry.mu.Lock()
	defer entry.mu.Unlock()

	count, minID, maxID, err := inc.TimelineStats(ctx, key)
	if err != nil {
		return nil, err
	}
	if count == 0 {
		if !entry.loaded || entry.count != 0 {
			entry.loaded, entry.count, entry.minID, entry.maxID, entry.timeline = true, 0, 0, 0, Timeline{}
		}
		return entry.timeline, nil
	}
	if entry.loaded && entry.count == count && entry.minID == minID && entry.maxID == maxID {
		return entry.timeline, nil
	}
	// 尝试增量拉取：只有当 c_id <= entry.maxID 的旧行一行未删（min_id 不变且增量行数恰好补足 count）时才走增量路径。
	if entry.loaded && entry.count > 0 && minID == entry.minID && maxID > entry.maxID && count > entry.count {
		delta, lastID, deltaErr := inc.ListBySpaceAfter(ctx, key, entry.maxID)
		if deltaErr != nil {
			return nil, deltaErr
		}
		if len(delta) > 0 && entry.count+int64(len(delta)) == count && lastID == maxID {
			next := cloneTimeline(entry.timeline)
			for _, item := range delta {
				applyTimelineEntry(next, item)
			}
			entry.count, entry.maxID, entry.timeline = count, maxID, next
			return next, nil
		}
	}
	// 冷启动、同生效点覆盖删旧行、或整空间清库重登记：全量重建，并以读回行的真实首尾 ID 作为缓存水位。
	rows, gotMinID, gotMaxID, err := inc.ListBySpaceWithBounds(ctx, key)
	if err != nil {
		return nil, err
	}
	next := BuildTimeline(rows)
	entry.loaded = true
	entry.count = int64(len(rows))
	entry.minID, entry.maxID = gotMinID, gotMaxID
	entry.timeline = next
	return next, nil
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
// 自动补采跳过它们。
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
