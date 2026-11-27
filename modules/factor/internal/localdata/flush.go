package localdata

import (
	"context"
	"errors"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/seriessync"
	"github.com/mooyang-code/moox/modules/factor/internal/serieswindow"
	"trpc.group/trpc-go/trpc-go/log"
)

// flushJob 是一次后台落盘。
type flushJob struct {
	done     chan struct{}
	subjects []string
	err      error
	// staged 与 writes 留着，落盘失败时下一次操作先原样重试一次。
	staged *seriessync.StagedPull
	writes []seriesstore.SubjectWrite
}

// flushTimeout 是一次后台落盘的上限。
const flushTimeout = 2 * time.Minute

// splitHot 把拉取到的写入分成热路径与其余：热路径是本地已有的标的恰好新增连续的 1 根（不需要填充缺失行），窗口也正好停在本地 Latest。
// 其余（新标的、缺期、一次拉到多根）保持同步写盘，因为窗口要按磁盘上生成的填充行重载。
func (d *Data) splitHot(st *source, writes []seriesstore.SubjectWrite, step time.Duration) (hot, cold []seriesstore.SubjectWrite) {
	for _, w := range writes {
		if w.Rows.Len() != 1 || !st.window.Has(w.Subject) {
			cold = append(cold, w)
			continue
		}
		b, ok, err := d.cfg.Store.Bounds(st.spec.Source, w.Subject)
		latest, inWindow := st.window.Latest(w.Subject)
		if err != nil || !ok || !inWindow || !latest.Equal(b.Latest) || !w.From.Equal(b.Latest.Add(step)) || w.Rows.Times[0] != w.From.Unix() {
			cold = append(cold, w)
			continue
		}
		hot = append(hot, w)
	}
	return hot, cold
}

// startFlush 在后台写入热路径的行。调用时持有数据源锁；下一次需要磁盘一致的操作会先 settleFlush。
func (d *Data) startFlush(ctx context.Context, st *source, staged *seriessync.StagedPull, writes []seriesstore.SubjectWrite) {
	if len(writes) == 0 {
		return
	}
	job := &flushJob{done: make(chan struct{}), subjects: make([]string, len(writes)), staged: staged, writes: writes}
	for i, w := range writes {
		job.subjects[i] = w.Subject
	}
	st.flushMu.Lock()
	st.flush = job
	st.flushMu.Unlock()
	flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), flushTimeout)
	go func() {
		defer cancel()
		defer close(job.done)
		_, job.err = staged.Commit(flushCtx, writes)
	}()
}

// awaitFlush 等上一期的后台落盘结束（不清除记录），返回它的任务；没有则为 nil。
func (st *source) awaitFlush() *flushJob {
	st.flushMu.Lock()
	job := st.flush
	st.flushMu.Unlock()
	if job != nil {
		<-job.done
	}
	return job
}

// settleFlush 等上一期的后台落盘结束并清除记录；调用方持有数据源锁。落盘失败时窗口比磁盘多了几根：
// 把这些标的从磁盘重载（丢掉没落盘的行），下一次拉取会重新取回，记错误日志。
func (d *Data) settleFlush(st *source) {
	job := st.awaitFlush()
	if job == nil {
		return
	}
	st.flushMu.Lock()
	if st.flush == job {
		st.flush = nil
	}
	st.flushMu.Unlock()
	if job.err == nil {
		return
	}
	log.Errorf("factor_flush_failed source=%s subjects=%d error=%v", st.spec.Source, len(job.subjects), job.err)
	// 先原样重试一次：窗口里的行已经用来计算并发布了，能写成功就保持本地与已发布的结果一致，
	// 之后 Storage 对这些行的修正（序号大于拉取前的链头位置）仍会被跟进重拉。重试也失败才丢掉没落盘的行。
	if job.staged != nil {
		ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
		_, retryErr := job.staged.Commit(ctx, job.writes)
		cancel()
		if retryErr == nil {
			log.Infof("factor_flush_retry_ok source=%s subjects=%d", st.spec.Source, len(job.subjects))
			return
		}
		log.Errorf("factor_flush_retry_failed source=%s error=%v", st.spec.Source, retryErr)
	}
	if st.window != nil {
		for _, subject := range job.subjects {
			if err := st.window.Reload(context.Background(), d.cfg.Store, subject); err != nil {
				log.Errorf("factor_flush_recover_failed source=%s subject=%s error=%v", st.spec.Source, subject, err)
			}
		}
	}
}

// Drain 等所有数据源的后台落盘结束，引擎退出、关闭本地存储之前调用。
func (d *Data) Drain() {
	d.mu.Lock()
	var srcs []*source
	for _, st := range d.sources {
		srcs = append(srcs, st)
	}
	d.mu.Unlock()
	for _, st := range srcs {
		st.awaitFlush()
	}
}

// applyToWindow 把一次同步写入的结果反映到窗口：新标的或修正了窗口里的区间就整个标的重新载入，否则追加新行。
func (d *Data) applyToWindow(ctx context.Context, st *source, subject string, res seriesstore.WriteResult) error {
	if res.Changed.Empty() {
		return nil
	}
	if res.PrevLatest.IsZero() || !st.window.Has(subject) || !res.Changed.From.After(res.PrevLatest) {
		return st.window.Reload(ctx, d.cfg.Store, subject)
	}
	got, err := d.cfg.Store.ReadRange(ctx, st.spec.Source, []string{subject}, res.PrevLatest.Add(time.Second), res.Latest.Add(time.Second))
	if err != nil {
		return err
	}
	rows := got[subject]
	if rows == nil {
		return nil
	}
	if err := st.window.Append(subject, rows); err != nil {
		if errors.Is(err, serieswindow.ErrDiscontinuous) {
			return st.window.Reload(ctx, d.cfg.Store, subject)
		}
		return err
	}
	return nil
}

// syncWindow 把 CatchUp、RealRowsAt 的一次写入反映到窗口。数据集字段变了时窗口要按新字段重建，重建完成之前按预热处理（与实时路径相同）。
func (d *Data) syncWindow(ctx context.Context, st *source, subject string, res seriesstore.WriteResult) error {
	err := d.applyToWindow(ctx, st, subject, res)
	if errors.Is(err, serieswindow.ErrSchemaChanged) {
		st.ready.Store(false)
		return nil
	}
	return err
}
