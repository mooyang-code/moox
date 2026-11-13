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

	"trpc.group/trpc-go/trpc-go/log"
)

// follower 是一个 View 的跟随协程。
type follower struct {
	m      *Manager
	key    Key
	target Target
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu sync.Mutex
	// state 是内存里的进度：游标只在条目应用完成后前进，不越过没有应用的条目。
	state  viewindex.FollowState
	loaded bool
	// persisted 是已经落盘的游标。
	persisted   viewindex.ChangeCursor
	lastPersist time.Time
	// head 是最近一次得知的日志头部序号。
	head uint64
	// caughtUpAt 是上一次扫描到日志头部、且其间的条目全部应用完的时刻；startedAt 用于还没追上过的跟随器。
	caughtUpAt time.Time
	startedAt  time.Time
	// caughtUp 为真表示上一页已扫描到头部并全部应用；waiting 为真表示此时正在服务端等待新变更。
	caughtUp bool
	waiting  bool
	// progress 在游标前进时关闭并换新，WaitApplied 据此等待。
	progress chan struct{}
	// lastError 用于同类错误只记一次日志。
	lastError string
}

func newFollower(m *Manager, key Key, target Target) *follower {
	ctx, cancel := context.WithCancel(m.ctx)
	return &follower{m: m, key: key, target: target, ctx: ctx, cancel: cancel, done: make(chan struct{}), startedAt: time.Now(), progress: make(chan struct{})}
}

func (f *follower) stop() {
	f.cancel()
	<-f.done
}

func (f *follower) run() {
	defer close(f.done)
	defer f.persistOnExit()
	retry := retryMin
	for f.ctx.Err() == nil {
		err := f.step()
		if err == nil {
			retry = retryMin
			f.resolved()
			continue
		}
		if f.ctx.Err() != nil {
			return
		}
		f.failed(err)
		timer := time.NewTimer(retry)
		select {
		case <-f.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		retry = min(retry*2, retryMax)
	}
}

// step 做一轮：必要时载入进度或重建，按间隔落盘游标，读一页变更并应用。
func (f *follower) step() error {
	if !f.isLoaded() {
		state, err := f.target.LoadState(f.ctx)
		if err != nil {
			return fmt.Errorf("读取索引里的跟随进度：%w", err)
		}
		if state.Cursor.StoreID == "" {
			log.Warnf("storage view %s/%s 的索引没有跟随进度，重建索引", f.key.SpaceID, f.key.ViewID)
			return f.rebuild()
		}
		f.setLoaded(state, true)
		log.Infof("storage view %s/%s 开始跟随变更日志：数据集=%s 游标=%d 建成序号=%d", f.key.SpaceID, f.key.ViewID, f.key.DatasetID, state.Cursor.Sequence, state.BuiltSequence)
	}
	f.maybePersist()
	rsp, err := f.read()
	if err != nil {
		return err
	}
	if rsp.GetGap() {
		log.Warnf("storage view %s/%s 跟随变更日志断档（游标 %d 早于日志保留起点，或存储已重建），重建索引", f.key.SpaceID, f.key.ViewID, f.status().State.Cursor.Sequence)
		return f.rebuild()
	}
	return f.apply(rsp)
}

func (f *follower) rebuild() error {
	state, err := f.target.Rebuild(f.ctx)
	if err != nil {
		return fmt.Errorf("重建索引：%w", err)
	}
	f.setLoaded(state, true)
	log.Infof("storage view %s/%s 索引已重建：从序号 %d 继续跟随，建成序号=%d", f.key.SpaceID, f.key.ViewID, state.Cursor.Sequence, state.BuiltSequence)
	return nil
}

func (f *follower) read() (*pb.ReadDatasetChangesRsp, error) {
	wait := f.m.opts.Wait
	ctx, cancel := context.WithTimeout(f.ctx, wait+readTimeoutMargin)
	defer cancel()
	f.mu.Lock()
	cursor := toPB(f.state.Cursor)
	f.waiting = f.caughtUp
	f.mu.Unlock()
	rsp, err := f.m.opts.Reader.ReadDatasetChanges(ctx, &pb.ReadDatasetChangesReq{
		AuthInfo: f.m.opts.Auth(), SpaceId: f.key.SpaceID, DatasetId: f.key.DatasetID, Cursor: cursor,
		WaitMs: uint32(wait / time.Millisecond),
	}, client.WithTimeout(wait+readTimeoutMargin))
	f.mu.Lock()
	f.waiting = false
	f.mu.Unlock()
	if err = responseError(rsp, err); err != nil {
		return nil, fmt.Errorf("读取变更日志：%w", err)
	}
	return rsp, nil
}

// apply 按序应用一页变更：连续的行条目合成一组回读写入，完成标记逐条发布。任何一步失败都停在那里，下一轮从游标重读。
func (f *follower) apply(rsp *pb.ReadDatasetChangesRsp) error {
	next := toCursor(rsp.GetNextCursor())
	changes := rsp.GetChanges()
	built := f.status().State.BuiltSequence
	for i := 0; i < len(changes); {
		change := changes[i]
		if len(change.GetMarker()) > 0 {
			// 建成前（不大于 H_done）的完成标记只推进游标：建索引期间的周期完成不补发就绪事件。
			if change.GetSequence() > built {
				if err := f.target.PublishMarker(f.ctx, change); err != nil {
					return fmt.Errorf("发布序号 %d 的完成标记：%w", change.GetSequence(), err)
				}
			} else {
				log.Infof("storage view %s/%s 跳过建索引期间的完成标记 序号=%d 建成序号=%d", f.key.SpaceID, f.key.ViewID, change.GetSequence(), built)
			}
			f.advance(viewindex.ChangeCursor{StoreID: next.StoreID, Sequence: change.GetSequence()}, false)
			i++
			continue
		}
		j := i
		for j < len(changes) && len(changes[j].GetMarker()) == 0 {
			j++
		}
		cursor := viewindex.ChangeCursor{StoreID: next.StoreID, Sequence: changes[j-1].GetSequence()}
		if j == len(changes) {
			// 这组之后本页没有本数据集的条目：游标直接记到已扫描的位置。
			cursor = next
		}
		persisted, err := f.target.ApplyRows(f.ctx, changes[i:j], cursor)
		if err != nil {
			return fmt.Errorf("应用序号 %d～%d 的行变更：%w", changes[i].GetSequence(), changes[j-1].GetSequence(), err)
		}
		f.advance(cursor, persisted)
		i = j
	}
	f.advance(next, false)
	f.mu.Lock()
	f.caughtUp = !rsp.GetHasMore()
	if f.caughtUp {
		f.head = max(f.head, next.Sequence)
		f.caughtUpAt = time.Now()
	}
	f.mu.Unlock()
	return nil
}

// advance 把内存游标推进到 cursor；persisted 表示它已随行写入落盘。
func (f *follower) advance(cursor viewindex.ChangeCursor, persisted bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state.Cursor.StoreID == cursor.StoreID && cursor.Sequence <= f.state.Cursor.Sequence {
		if persisted && f.state.Cursor == cursor {
			f.persisted = cursor
		}
		return
	}
	f.state.Cursor = cursor
	if persisted {
		f.persisted = cursor
	}
	f.head = max(f.head, cursor.Sequence)
	close(f.progress)
	f.progress = make(chan struct{})
}

// maybePersist 按间隔把只扫描、没有写入的前进落盘，并顺带刷新日志头部（算落后条数用）。
func (f *follower) maybePersist() {
	f.mu.Lock()
	due := time.Since(f.lastPersist) >= f.m.opts.PersistInterval
	if due {
		f.lastPersist = time.Now()
	}
	f.mu.Unlock()
	if !due {
		return
	}
	if head, err := f.m.readHead(f.ctx, f.key); err == nil {
		f.mu.Lock()
		f.head = max(f.head, head.Sequence)
		f.mu.Unlock()
	}
	if err := f.persist(f.ctx); err != nil {
		log.Warnf("storage view %s/%s 落盘跟随进度失败，下一轮再试：%v", f.key.SpaceID, f.key.ViewID, err)
	}
}

func (f *follower) persist(ctx context.Context) error {
	f.mu.Lock()
	cursor, persisted, loaded := f.state.Cursor, f.persisted, f.loaded
	f.mu.Unlock()
	if !loaded || cursor == persisted || cursor.StoreID == "" {
		return nil
	}
	if err := f.target.SaveState(ctx, viewindex.FollowState{Cursor: cursor}); err != nil {
		return err
	}
	f.mu.Lock()
	if f.state.Cursor.StoreID == cursor.StoreID && f.state.Cursor.Sequence >= cursor.Sequence {
		f.persisted = cursor
	}
	f.mu.Unlock()
	return nil
}

func (f *follower) persistOnExit() {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(f.ctx), 10*time.Second)
	defer cancel()
	if err := f.persist(ctx); err != nil {
		log.Warnf("storage view %s/%s 停止时落盘跟随进度失败：%v", f.key.SpaceID, f.key.ViewID, err)
	}
}

func (f *follower) isLoaded() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loaded
}

func (f *follower) setLoaded(state viewindex.FollowState, persisted bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state, f.loaded = state, true
	if persisted {
		f.persisted = state.Cursor
	}
	f.lastPersist = time.Now()
	f.head = max(f.head, state.Cursor.Sequence)
	close(f.progress)
	f.progress = make(chan struct{})
}

func (f *follower) status() Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return Status{Loaded: f.loaded, State: f.state}
}

func (f *follower) lag(now time.Time) Lag {
	f.mu.Lock()
	defer f.mu.Unlock()
	lag := Lag{Key: f.key}
	if f.head > f.state.Cursor.Sequence {
		lag.Changes = f.head - f.state.Cursor.Sequence
	}
	if !f.waiting {
		since := f.caughtUpAt
		if since.IsZero() {
			since = f.startedAt
		}
		lag.Seconds = max(now.Sub(since).Seconds(), 0)
	}
	return lag
}

// lastChangeBefore 从内存游标往后扫到 head（不等待），返回其中最后一条本数据集条目的位置；没有时返回游标本身。
// 断档时返回 head：等跟随器重建后跟上。
func (f *follower) lastChangeBefore(ctx context.Context, head viewindex.ChangeCursor) (viewindex.ChangeCursor, error) {
	current := f.status().State.Cursor
	if reached(current, head) {
		return current, nil
	}
	if current.StoreID != head.StoreID {
		return head, nil
	}
	need := current
	cursor := current
	for cursor.Sequence < head.Sequence {
		rsp, err := f.m.opts.Reader.ReadDatasetChanges(ctx, &pb.ReadDatasetChangesReq{
			AuthInfo: f.m.opts.Auth(), SpaceId: f.key.SpaceID, DatasetId: f.key.DatasetID, Cursor: toPB(cursor), Limit: 2000,
		}, client.WithTimeout(headReadTimeout))
		if err = responseError(rsp, err); err != nil {
			return need, fmt.Errorf("扫描变更日志：%w", err)
		}
		if rsp.GetGap() {
			return head, nil
		}
		for _, change := range rsp.GetChanges() {
			if change.GetSequence() <= head.Sequence {
				need = viewindex.ChangeCursor{StoreID: head.StoreID, Sequence: change.GetSequence()}
			}
		}
		next := toCursor(rsp.GetNextCursor())
		if next.Sequence <= cursor.Sequence {
			break
		}
		cursor = next
		if !rsp.GetHasMore() {
			break
		}
	}
	return need, nil
}

// waitScanned 等跟随器扫描到 target，返回是否到达与当时的游标。
func (f *follower) waitScanned(ctx context.Context, target viewindex.ChangeCursor) (bool, viewindex.ChangeCursor) {
	for {
		f.mu.Lock()
		current, progress := f.state.Cursor, f.progress
		f.mu.Unlock()
		if reached(current, target) {
			return true, current
		}
		select {
		case <-progress:
		case <-ctx.Done():
			return false, current
		case <-f.done:
			return false, current
		}
	}
}

// failed 记一次失败：同样的错误连续出现只记第一次。
func (f *follower) failed(err error) {
	message := err.Error()
	f.mu.Lock()
	repeated := f.lastError == message
	f.lastError = message
	f.caughtUp = false
	f.mu.Unlock()
	if !repeated && !errors.Is(err, context.Canceled) {
		log.Errorf("storage view %s/%s 跟随变更日志失败，稍后重试：%v", f.key.SpaceID, f.key.ViewID, err)
	}
}

func (f *follower) resolved() {
	f.mu.Lock()
	had := f.lastError != ""
	f.lastError = ""
	f.mu.Unlock()
	if had {
		log.Infof("storage view %s/%s 跟随变更日志已恢复", f.key.SpaceID, f.key.ViewID)
	}
}
