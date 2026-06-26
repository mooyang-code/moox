package deriver

import (
	"context"
	"time"
)

type batcher[T any] struct {
	opts BatchOptions
	in   chan T
}

type timerState struct {
	t *time.Timer
	c <-chan time.Time
}

func (s *timerState) start(wait time.Duration) {
	if s.t == nil {
		s.t = time.NewTimer(wait)
	} else {
		s.t.Reset(wait)
	}
	s.c = s.t.C
}

func (s *timerState) stop() {
	if s.t == nil {
		return
	}
	if !s.t.Stop() {
		select {
		case <-s.t.C:
		default:
		}
	}
	s.c = nil
}

func newBatcher[T any](opts BatchOptions) *batcher[T] {
	opts = normalizeBatchOptions(opts)
	return &batcher[T]{
		opts: opts,
		in:   make(chan T, opts.BatchSize*2),
	}
}

func (b *batcher[T]) add(ctx context.Context, item T) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	select {
	case b.in <- item:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *batcher[T]) run(ctx context.Context, out chan<- []T) {
	batch := make([]T, 0, b.opts.BatchSize)
	var timer timerState

	stopTimer := func() {
		timer.stop()
	}
	flush := func() {
		if len(batch) == 0 {
			return
		}
		out <- append([]T(nil), batch...)
		batch = batch[:0]
		stopTimer()
	}
	addItem := func(item T, useTimer bool) {
		if len(batch) == 0 && useTimer {
			timer.start(b.opts.BatchWait)
		}
		batch = append(batch, item)
		if len(batch) >= b.opts.BatchSize {
			flush()
		}
	}

	for {
		select {
		case <-ctx.Done():
			// Close 取消 run context 后不再接收新任务，但已进入 batcher 的尾批次仍会
			// flush 给 worker，保证关闭过程中已接收的派生任务至少执行一次。
			for {
				select {
				case item := <-b.in:
					addItem(item, false)
				default:
					flush()
					stopTimer()
					return
				}
			}
		case item := <-b.in:
			addItem(item, true)
		case <-timer.c:
			flush()
		}
	}
}
