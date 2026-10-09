package input

import (
	"context"
	"sync"
)

// ForEvent 返回在一次事件内共享元数据读取的装配器：多个实例绑定同一 View 时，View、标的绑定与标签成员
// 每个事件只读一次。行查询不缓存；读取遇到 ErrStale 后调用 Invalidate 丢弃缓存的 View，再整体重读。
func (l Loader) ForEvent() Loader {
	if l.Client == nil {
		return l
	}
	if _, cached := l.Client.(*eventClient); cached {
		return l
	}
	return Loader{Client: &eventClient{Client: l.Client, views: map[string]ViewInfo{}, subjects: map[string][]Subject{}, members: map[string][]string{}}}
}

// Invalidate 丢弃事件内缓存的 View 元数据。
func (l Loader) Invalidate() {
	if cache, ok := l.Client.(*eventClient); ok {
		cache.mu.Lock()
		cache.views = map[string]ViewInfo{}
		cache.mu.Unlock()
	}
}

// eventClient 缓存一次事件内的元数据读取；只缓存成功的结果。
type eventClient struct {
	Client
	mu       sync.Mutex
	views    map[string]ViewInfo
	subjects map[string][]Subject
	members  map[string][]string
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
