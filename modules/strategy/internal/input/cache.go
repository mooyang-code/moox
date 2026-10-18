package input

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// MetaCache 是进程级的标的元数据缓存：标的绑定、标签成员与标签详情只在 Storage 的标的修订号
// （GetDataset 返回的 subjects_revision）变化时重读。行数据不缓存，周期对齐不受影响。
type MetaCache struct {
	mu       sync.Mutex
	space    string
	revision uint64
	known    bool
	subjects map[string][]Subject
	members  map[string][]string
	tags     map[string]TagInfo

	// ageKey / ageOK 缓存已证明“年龄足够”的标的：上市时间只会越来越久，探针通过一次后不必每期重查。
	// 键是 View 的活动索引与 min_age_bars，索引重建（换了活动索引）后自动作废。
	ageKey string
	ageOK  map[string]struct{}
	// Ages 非空时，年龄探针通过的标的会持久化，重启后第一期也不必重新探针；读写失败只影响缓存效果，不影响探针结果。
	Ages AgeStore
}

// AgeKey 标识一组年龄探针结果：某个 View 的某个活动索引、某个 min_age_bars。
type AgeKey struct {
	SpaceID    string
	ViewID     string
	IndexID    string
	MinAgeBars int
}

// AgeStore 持久化年龄探针通过的标的。
type AgeStore interface {
	LoadAgePassed(ctx context.Context, spaceID, viewID, indexID string, minAgeBars int) ([]string, error)
	SaveAgePassed(ctx context.Context, spaceID, viewID, indexID string, minAgeBars int, subjectIDs []string, at time.Time) error
}

// NewMetaCache 创建空缓存；Loader 不带缓存时每个事件使用一份新的，等同于事件内缓存。
func NewMetaCache() *MetaCache {
	m := &MetaCache{}
	m.reset()
	return m
}

func (m *MetaCache) reset() {
	m.subjects = map[string][]Subject{}
	m.members = map[string][]string{}
	m.tags = map[string]TagInfo{}
}

// sync 登记空间的修订号；与已缓存的不一致（或换了空间）时丢弃全部缓存。
func (m *MetaCache) sync(spaceID string, revision uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.known && m.space == spaceID && m.revision == revision {
		return
	}
	m.reset()
	m.space, m.revision, m.known = spaceID, revision, true
}

// ForEvent 返回在一次事件内共享元数据读取的装配器：多个实例绑定同一 View 时，View、覆盖统计每个事件只读一次；
// 标的绑定、标签与标签成员由 Loader.Meta 跨事件缓存，LoadBar 先按 subjects_revision 校验。
// 行查询不缓存；读取遇到 ErrStale 后调用 Invalidate 丢弃缓存的 View 与覆盖统计，再整体重读。
func (l Loader) ForEvent() Loader {
	if l.Client == nil {
		return l
	}
	if _, cached := l.Client.(*eventClient); cached {
		return l
	}
	meta := l.Meta
	if meta == nil {
		meta = NewMetaCache()
	}
	return Loader{Meta: meta, Client: &eventClient{Client: l.Client, meta: meta, views: map[string]ViewInfo{}, coverage: map[string]Coverage{}, checked: map[string]bool{}}}
}

// Invalidate 丢弃事件内缓存的 View 元数据。
func (l Loader) Invalidate() {
	if cache, ok := l.Client.(*eventClient); ok {
		cache.mu.Lock()
		cache.views = map[string]ViewInfo{}
		cache.coverage = map[string]Coverage{}
		cache.mu.Unlock()
	}
}

// eventClient 缓存一次事件内的 View 元数据，并经 MetaCache 跨事件缓存标的元数据；只缓存成功的结果。
type eventClient struct {
	Client
	meta     *MetaCache
	mu       sync.Mutex
	views    map[string]ViewInfo
	coverage map[string]Coverage
	checked  map[string]bool
}

// Revalidate 读取数据集所属空间的标的修订号并校验缓存，同一事件内同一数据集只查一次。
// 先于任何标的元数据读取调用：期间若有变化，缓存里是旧数据但修订号也是旧的，下个事件比较不等就会重读。
func (c *eventClient) Revalidate(ctx context.Context, spaceID, datasetID string) error {
	key := spaceID + "\x00" + datasetID
	c.mu.Lock()
	done := c.checked[key]
	c.mu.Unlock()
	if done {
		return nil
	}
	dataset, err := c.Client.GetDataset(ctx, spaceID, datasetID)
	if err != nil {
		return err
	}
	c.meta.sync(spaceID, dataset.SubjectsRevision)
	c.mu.Lock()
	c.checked[key] = true
	c.mu.Unlock()
	return nil
}

func (c *eventClient) ViewCoverage(ctx context.Context, spaceID string, view ViewInfo, exact bool) (Coverage, error) {
	key := spaceID + "\x00" + view.ViewID + "\x00" + view.Generation
	c.mu.Lock()
	coverage, ok := c.coverage[key]
	c.mu.Unlock()
	if ok && (!exact || !coverage.IndexedFrom.IsZero()) {
		return coverage, nil
	}
	coverage, err := c.Client.ViewCoverage(ctx, spaceID, view, exact)
	if err != nil {
		return Coverage{}, err
	}
	c.mu.Lock()
	c.coverage[key] = coverage
	c.mu.Unlock()
	return coverage, nil
}

func (c *eventClient) GetTag(ctx context.Context, spaceID, tagID string) (TagInfo, error) {
	key := spaceID + "\x00" + tagID
	c.meta.mu.Lock()
	tag, ok := c.meta.tags[key]
	c.meta.mu.Unlock()
	if ok {
		return tag, nil
	}
	tag, err := c.Client.GetTag(ctx, spaceID, tagID)
	if err != nil {
		return TagInfo{}, err
	}
	c.meta.mu.Lock()
	c.meta.tags[key] = tag
	c.meta.mu.Unlock()
	return tag, nil
}

func (c *eventClient) GetView(ctx context.Context, spaceID, viewID string) (ViewInfo, error) {
	key := spaceID + "\x00" + viewID
	c.mu.Lock()
	view, ok := c.views[key]
	c.mu.Unlock()
	if ok {
		return view, nil
	}
	view, err := c.Client.GetView(ctx, spaceID, viewID)
	if err != nil {
		return ViewInfo{}, err
	}
	c.mu.Lock()
	c.views[key] = view
	c.mu.Unlock()
	return view, nil
}

func (c *eventClient) ListDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]Subject, error) {
	key := spaceID + "\x00" + datasetID
	c.meta.mu.Lock()
	subjects, ok := c.meta.subjects[key]
	c.meta.mu.Unlock()
	if !ok {
		var err error
		if subjects, err = c.Client.ListDatasetSubjects(ctx, spaceID, datasetID); err != nil {
			return nil, err
		}
		c.meta.mu.Lock()
		c.meta.subjects[key] = subjects
		c.meta.mu.Unlock()
	}
	return append([]Subject(nil), subjects...), nil
}

func (c *eventClient) ListTagMembers(ctx context.Context, spaceID, tagID string) ([]string, error) {
	key := spaceID + "\x00" + tagID
	c.meta.mu.Lock()
	members, ok := c.meta.members[key]
	c.meta.mu.Unlock()
	if !ok {
		var err error
		if members, err = c.Client.ListTagMembers(ctx, spaceID, tagID); err != nil {
			return nil, err
		}
		c.meta.mu.Lock()
		c.meta.members[key] = members
		c.meta.mu.Unlock()
	}
	return append([]string(nil), members...), nil
}

// ageSplit 把候选标的按是否已证明年龄足够拆成两组；键变化（换了活动索引或 min_age_bars）时换一份缓存，
// 并从持久化里读回这个键已通过的标的。
func (m *MetaCache) ageSplit(ctx context.Context, key AgeKey, ids []string) (hit, miss []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.switchAgeKeyLocked(ctx, key)
	for _, id := range ids {
		if _, ok := m.ageOK[id]; ok {
			hit = append(hit, id)
		} else {
			miss = append(miss, id)
		}
	}
	return hit, miss
}

// ageRecord 登记本期探针通过的标的，并写入持久化。
func (m *MetaCache) ageRecord(ctx context.Context, key AgeKey, ids []string) {
	if len(ids) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.switchAgeKeyLocked(ctx, key)
	for _, id := range ids {
		m.ageOK[id] = struct{}{}
	}
	if m.Ages != nil {
		_ = m.Ages.SaveAgePassed(ctx, key.SpaceID, key.ViewID, key.IndexID, key.MinAgeBars, ids, time.Now())
	}
}

func (m *MetaCache) switchAgeKeyLocked(ctx context.Context, key AgeKey) {
	label := key.SpaceID + "\x00" + key.ViewID + "\x00" + key.IndexID + "\x00" + strconv.Itoa(key.MinAgeBars)
	if m.ageKey == label && m.ageOK != nil {
		return
	}
	m.ageKey, m.ageOK = label, map[string]struct{}{}
	if m.Ages == nil {
		return
	}
	if ids, err := m.Ages.LoadAgePassed(ctx, key.SpaceID, key.ViewID, key.IndexID, key.MinAgeBars); err == nil {
		for _, id := range ids {
			m.ageOK[id] = struct{}{}
		}
	}
}
