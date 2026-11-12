// Package follow 让 View 按游标跟随 PrimaryStore 的变更日志：每个 View 一个跟随协程，游标、重试各自独立，
// 一个 View 卡住（索引写入出错、就绪事件发不出去）不影响其他 View。
//
// 游标是“已扫描到的位置”：没有命中本数据集的日志条目时也会前进，空闲的数据集不会因为日志清理而断档。
// 行与游标在索引的同一次提交里落盘；只扫描、没有写入的前进先放在内存里，由每分钟一次的落盘写进索引元数据。
// 完成标记轮到它时，该数据集在它之前的行必然已经应用，直接发布就绪事件；发布失败就停在这条标记上稍后再试。
package follow

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/client"
)

const (
	// DefaultWait 是没有新变更时让 PrimaryStore 等待的时长：写入提交后立即返回，空闲时每轮最多等这么久。
	DefaultWait = 20 * time.Second
	// readTimeoutMargin 是单次读取的超时在等待时长之外留的余量：服务端在截止前返回已扫描的位置。
	readTimeoutMargin = 5 * time.Second
	// DefaultPersistInterval 是只扫描、没有写入的游标前进落盘到索引元数据的间隔。
	DefaultPersistInterval = time.Minute
	retryMin               = 500 * time.Millisecond
	retryMax               = 10 * time.Second
	// headReadTimeout 限制取日志头部的一次调用。
	headReadTimeout = 5 * time.Second
)

// Reader 读取变更日志（PrimaryStore.ReadDatasetChanges）。
type Reader interface {
	ReadDatasetChanges(ctx context.Context, req *pb.ReadDatasetChangesReq, opts ...client.Option) (*pb.ReadDatasetChangesRsp, error)
}

// Target 是一个 View 的应用端，由 View 服务实现。
type Target interface {
	// LoadState 返回索引里落盘的跟随进度；索引没有记录过进度时 Cursor.StoreID 为空，跟随器会重建索引。
	LoadState(ctx context.Context) (viewindex.FollowState, error)
	// ApplyRows 回读一组连续的行变更并写入索引，cursor 与最后一批行同一次提交落盘；返回 cursor 是否已经落盘
	// （变更里的行全部过期、或已不存在时没有写入，cursor 只在内存里前进）。
	ApplyRows(ctx context.Context, changes []*pb.DatasetChange, cursor viewindex.ChangeCursor) (bool, error)
	// PublishMarker 为一条完成标记发布就绪事件。
	PublishMarker(ctx context.Context, change *pb.DatasetChange) error
	// SaveState 单独落盘跟随进度（BuiltSequence 为 0 时保留索引里原有的值）。
	SaveState(ctx context.Context, state viewindex.FollowState) error
	// Rebuild 在断档或索引没有进度时重建索引，返回重建后的进度：游标是开始建时的头部 H_start，BuiltSequence 是建成时的头部 H_done。
	Rebuild(ctx context.Context) (viewindex.FollowState, error)
}

// Key 标识一个跟随器：View 与它读取的数据集。
type Key struct {
	SpaceID   string
	ViewID    string
	DatasetID string
}

// Options 是跟随器的公共参数。
type Options struct {
	Reader Reader
	// Auth 返回调用 PrimaryStore 的身份。
	Auth func() *pb.AuthInfo
	// Wait 是单次读取在服务端等待新变更的时长，默认 DefaultWait，上限 30 秒。
	Wait time.Duration
	// PersistInterval 是只扫描、没有写入的前进落盘的间隔，默认 DefaultPersistInterval。
	PersistInterval time.Duration
}

// Status 是一个跟随器的进度。
type Status struct {
	// Loaded 为假表示还没读到索引里的进度（启动中或重建中）。
	Loaded bool
	State  viewindex.FollowState
}

// Lag 是一个跟随器的落后情况。
type Lag struct {
	Key Key
	// Changes 是最近一次得知的日志头部序号减去已扫描序号。
	Changes uint64
	// Seconds 是距离上一次追上日志头部的时长；正在服务端等待新变更（已追上）时为 0。
	Seconds float64
}

// Manager 管理全部跟随器。
type Manager struct {
	opts      Options
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	followers map[viewKey]*follower
}

type viewKey struct{ spaceID, viewID string }

// NewManager 创建跟随器管理器；Close 停止全部跟随器并落盘它们的进度。
func NewManager(ctx context.Context, opts Options) (*Manager, error) {
	if opts.Reader == nil || opts.Auth == nil {
		return nil, errors.New("View 跟随变更日志需要 PrimaryStore 读取客户端与鉴权")
	}
	if opts.Wait <= 0 {
		opts.Wait = DefaultWait
	}
	opts.Wait = min(opts.Wait, 30*time.Second)
	if opts.PersistInterval <= 0 {
		opts.PersistInterval = DefaultPersistInterval
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	return &Manager{opts: opts, ctx: runCtx, cancel: cancel, followers: make(map[viewKey]*follower)}, nil
}

// Ensure 保证 View 有一个跟随器：已经在跟随同一个数据集时不动，数据集变了先停掉旧的。
func (m *Manager) Ensure(key Key, target Target) {
	if m == nil || key.SpaceID == "" || key.ViewID == "" || key.DatasetID == "" || target == nil {
		return
	}
	ref := viewKey{key.SpaceID, key.ViewID}
	m.mu.Lock()
	if m.ctx.Err() != nil {
		m.mu.Unlock()
		return
	}
	old := m.followers[ref]
	if old != nil && old.key == key {
		m.mu.Unlock()
		return
	}
	f := newFollower(m, key, target)
	m.followers[ref] = f
	m.mu.Unlock()
	if old != nil {
		old.stop()
	}
	go f.run()
}

// Stop 停掉一个 View 的跟随器（View 被删除、或要从头建索引）。
func (m *Manager) Stop(spaceID, viewID string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	f := m.followers[viewKey{spaceID, viewID}]
	delete(m.followers, viewKey{spaceID, viewID})
	m.mu.Unlock()
	if f != nil {
		f.stop()
	}
}

// Retain 停掉 keep 不包含的跟随器（View 已删除或停用）。
func (m *Manager) Retain(keep func(spaceID, viewID string) bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	var stopped []*follower
	for ref, f := range m.followers {
		if !keep(ref.spaceID, ref.viewID) {
			stopped = append(stopped, f)
			delete(m.followers, ref)
		}
	}
	m.mu.Unlock()
	for _, f := range stopped {
		f.stop()
	}
}

// Close 停止全部跟随器，各自落盘最后的进度。
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.cancel()
	followers := make([]*follower, 0, len(m.followers))
	for ref, f := range m.followers {
		followers = append(followers, f)
		delete(m.followers, ref)
	}
	m.mu.Unlock()
	for _, f := range followers {
		f.stop()
	}
}

func (m *Manager) follower(spaceID, viewID string) *follower {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.followers[viewKey{spaceID, viewID}]
}

// Status 返回 View 跟随器的进度；没有跟随器时 ok 为假。
func (m *Manager) Status(spaceID, viewID string) (Status, bool) {
	f := m.follower(spaceID, viewID)
	if f == nil {
		return Status{}, false
	}
	return f.status(), true
}

// Lags 返回全部跟随器的落后情况。
func (m *Manager) Lags(now time.Time) []Lag {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	followers := make([]*follower, 0, len(m.followers))
	for _, f := range m.followers {
		followers = append(followers, f)
	}
	m.mu.Unlock()
	lags := make([]Lag, 0, len(followers))
	for _, f := range followers {
		lags = append(lags, f.lag(now))
	}
	return lags
}

// ErrNotFollowing 表示 View 没有跟随器（View 不存在、或索引还没建好）。
var ErrNotFollowing = errors.New("View 还没有开始跟随变更日志")

// WaitApplied 等 View 跟上调用这一刻的日志头部，最多等 timeout。返回是否跟上、请求时的头部与返回时的进度。
//
// 跟随器在服务端等待新变更期间，扫描过的、不属于本数据集的条目不会马上反映到游标上，空闲数据集的游标会落后头部最多一轮等待。
// 所以先从游标往后扫一遍（不等待）：头部之前没有本数据集的条目就直接算跟上，有的话等跟随器扫到最后一条。
func (m *Manager) WaitApplied(ctx context.Context, spaceID, viewID string, timeout time.Duration) (bool, viewindex.ChangeCursor, viewindex.ChangeCursor, error) {
	f := m.follower(spaceID, viewID)
	if f == nil {
		return false, viewindex.ChangeCursor{}, viewindex.ChangeCursor{}, ErrNotFollowing
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	head, err := m.readHead(waitCtx, f.key)
	if err != nil {
		return false, viewindex.ChangeCursor{}, f.status().State.Cursor, err
	}
	need, err := f.lastChangeBefore(waitCtx, head)
	if err != nil {
		return false, head, f.status().State.Cursor, err
	}
	reached, current := f.waitScanned(waitCtx, need)
	return reached, head, current, nil
}

// readHead 取日志头部。
func (m *Manager) readHead(ctx context.Context, key Key) (viewindex.ChangeCursor, error) {
	callCtx, cancel := context.WithTimeout(ctx, headReadTimeout)
	defer cancel()
	rsp, err := m.opts.Reader.ReadDatasetChanges(callCtx, &pb.ReadDatasetChangesReq{
		AuthInfo: m.opts.Auth(), SpaceId: key.SpaceID, DatasetId: key.DatasetID, FromHead: true,
	}, client.WithTimeout(headReadTimeout))
	if err = responseError(rsp, err); err != nil {
		return viewindex.ChangeCursor{}, fmt.Errorf("读取变更日志头部：%w", err)
	}
	return toCursor(rsp.GetNextCursor()), nil
}

func responseError(rsp *pb.ReadDatasetChangesRsp, err error) error {
	if err != nil {
		return err
	}
	if rsp == nil {
		return errors.New("读取变更日志返回为空")
	}
	if info := rsp.GetRetInfo(); info != nil && info.GetCode() != pb.ErrorCode_SUCCESS {
		return fmt.Errorf("读取变更日志失败：%s", info.GetMsg())
	}
	return nil
}

func toCursor(cursor *pb.DatasetChangeCursor) viewindex.ChangeCursor {
	return viewindex.ChangeCursor{StoreID: cursor.GetStoreId(), Sequence: cursor.GetSequence()}
}

func toPB(cursor viewindex.ChangeCursor) *pb.DatasetChangeCursor {
	return &pb.DatasetChangeCursor{StoreId: cursor.StoreID, Sequence: cursor.Sequence}
}

// reached 报告 current 是否已扫描到 target（不同存储的位置不可比较，视为没到）。
func reached(current, target viewindex.ChangeCursor) bool {
	return current.StoreID == target.StoreID && current.Sequence >= target.Sequence
}

// Head 取 View 所读数据集的日志头部（建索引时记 H_start、H_done 用）。
func (m *Manager) Head(ctx context.Context, spaceID, datasetID string) (viewindex.ChangeCursor, error) {
	if m == nil {
		return viewindex.ChangeCursor{}, ErrNotFollowing
	}
	return m.readHead(ctx, Key{SpaceID: spaceID, DatasetID: datasetID})
}
