package input

import (
	"context"
	"sync"
)

// ForEvent 返回在一次事件内共享元数据读取的装配器：多个实例绑定同一 View 时，View、覆盖统计、标的绑定、标签与
// 标签成员每个事件只读一次。行查询不缓存；读取遇到 ErrStale 后调用 Invalidate 丢弃缓存的 View 与覆盖统计，再整体重读。
func (l Loader) ForEvent() Loader {
	if l.Client == nil {
		return l
	}
	if _, cached := l.Client.(*eventClient); cached {
		return l
	}
	return Loader{Client: &eventClient{Client: l.Client, views: map[string]ViewInfo{}, coverage: map[string]Coverage{}, subjects: map[string][]Subject{}, members: map[string][]string{}, tags: map[string]TagInfo{}}}
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

// eventClient 缓存一次事件内的元数据读取；只缓存成功的结果。
type eventClient struct {
	Client
	mu       sync.Mutex
	views    map[string]ViewInfo
	coverage map[string]Coverage
	subjects map[string][]Subject
	members  map[string][]string
	tags     map[string]TagInfo
}

func (c *eventClient) ViewCoverage(ctx context.Context, spaceID string, view ViewInfo, exact bool) (Coverage, error) {
	key := spaceID + "\x00" + view.ViewID + "\x00" + view.ActiveIndexID
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
	c.mu.Lock()
	tag, ok := c.tags[key]
	c.mu.Unlock()
	if ok {
		return tag, nil
	}
	tag, err := c.Client.GetTag(ctx, spaceID, tagID)
	if err != nil {
		return TagInfo{}, err
	}
	c.mu.Lock()
	c.tags[key] = tag
	c.mu.Unlock()
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
	c.mu.Lock()
	subjects, ok := c.subjects[key]
	c.mu.Unlock()
	if !ok {
		var err error
		if subjects, err = c.Client.ListDatasetSubjects(ctx, spaceID, datasetID); err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.subjects[key] = subjects
		c.mu.Unlock()
	}
	return append([]Subject(nil), subjects...), nil
}

func (c *eventClient) ListTagMembers(ctx context.Context, spaceID, tagID string) ([]string, error) {
	key := spaceID + "\x00" + tagID
	c.mu.Lock()
	members, ok := c.members[key]
	c.mu.Unlock()
	if !ok {
		var err error
		if members, err = c.Client.ListTagMembers(ctx, spaceID, tagID); err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.members[key] = members
		c.mu.Unlock()
	}
	return append([]string(nil), members...), nil
}
