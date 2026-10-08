package outbox

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

// ResultStore 是投递需要的存储能力。
type ResultStore interface {
	ListPendingResults(context.Context, int) ([]store.Result, error)
	PreparePendingResult(context.Context, string, time.Time) (store.Result, bool, error)
	TransitionPublishStatus(context.Context, string, store.PublishStatus, store.PublishStatus) error
}

// ResultPublisher 发布一条待投递结果的事件。
type ResultPublisher interface {
	PublishResult(context.Context, store.Result) error
}

// PublishFailure 表示向 EventBus 发布失败（连接或 broker 问题）：运行时据此断开重连；
// 存储错误不是连接问题，不应触发重连。
type PublishFailure struct{ Err error }

func (e *PublishFailure) Error() string { return "发布策略目标事件失败：" + e.Err.Error() }
func (e *PublishFailure) Unwrap() error { return e.Err }

// Relay 把待投递的 ok 结果发布到 EventBus；投递前复查有效性，不满足的结果被取消。
type Relay struct {
	Store     ResultStore
	Publisher ResultPublisher
	mu        sync.Mutex
}

// PublishPending 扫描一批待投递结果。永久失败的事件被隔离（cancelled），不阻塞后续结果。
func (r *Relay) PublishPending(ctx context.Context, limit int) error {
	if r == nil || r.Store == nil || r.Publisher == nil {
		return errors.New("策略投递缺少存储或发布器")
	}
	if limit <= 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rows, err := r.Store.ListPendingResults(ctx, limit)
	if err != nil {
		return err
	}
	var firstErr error
	keep := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}
	// 列出之后、准备或迁移之前，更新的 ok 结果可能已把这一行取消：ErrNotFound 是正常竞争，跳过即可，
	// 不能当作连接错误去断开重连。
	for _, row := range rows {
		prepared, valid, err := r.Store.PreparePendingResult(ctx, row.ResultID, time.Now().UTC())
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			keep(err)
			continue
		}
		if !valid {
			continue
		}
		if err := r.Publisher.PublishResult(ctx, prepared); err != nil {
			var permanent *PermanentPublishError
			if errors.As(err, &permanent) {
				if cancelErr := r.Store.TransitionPublishStatus(ctx, prepared.ResultID, store.PublishPending, store.PublishCancelled); cancelErr != nil && !errors.Is(cancelErr, store.ErrNotFound) {
					err = errors.Join(err, cancelErr)
				}
				keep(err)
				continue
			}
			keep(&PublishFailure{Err: err})
			continue
		}
		if err := r.Store.TransitionPublishStatus(ctx, prepared.ResultID, store.PublishPending, store.PublishSent); err != nil && !errors.Is(err, store.ErrNotFound) {
			keep(err)
		}
	}
	return firstErr
}
