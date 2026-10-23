// Package paramsubmit 把策略每期求出的下一期因子参数投递给 FactorMgr：参数与结果在同一个事务里落库（待提交），
// 这里的循环负责提交并记录终态。提交与冻结在 FactorMgr 的同一个库里串行化，所以这里只管退避重试到
// 被接受、被拒绝或该期已冻结（too_late）；事件处理不在锁内等待重试。
package paramsubmit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/commonpb"
)

// 默认值。
const (
	DefaultInterval   = time.Second
	DefaultBatch      = 20
	DefaultMaxAge     = time.Hour
	DefaultMaxBackoff = 30 * time.Second
	initialBackoff    = time.Second
	submissionTimeout = 10 * time.Second
)

// Submitter 投递待提交的因子参数。
type Submitter struct {
	Store   *store.Store
	Factors input.FactorSets
	Now     func() time.Time
	// Interval 是轮询待提交记录的间隔；Batch 是每轮最多处理的条数；MaxAge 是记录超过目标期次这么久仍没有结果时放弃。
	Interval   time.Duration
	Batch      int
	MaxAge     time.Duration
	MaxBackoff time.Duration
	// Allowed 返回实例当前是否允许提交参数（模块暂停、实例暂停时不提交，记录保持待提交，因子沿用上一期的参数）；nil 表示总是允许。
	Allowed func(instanceID string) bool
	// OnResult 在每条记录有了终态时调用（指标）：accepted、too_late、rejected、abandoned。
	OnResult func(status string)
	// OnError 记录投递的异常（同一条记录每次失败都会调用，由调用方决定如何降噪）。
	OnError func(requestID string, err error)
}

func (s *Submitter) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Run 循环投递直到 ctx 结束。
func (s *Submitter) Run(ctx context.Context) {
	interval := s.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if _, err := s.RunOnce(ctx); err != nil && ctx.Err() == nil && s.OnError != nil {
			s.OnError("", err)
		}
	}
}

// RunOnce 处理一轮到期的待提交记录，返回处理的条数。
func (s *Submitter) RunOnce(ctx context.Context) (int, error) {
	if s.Store == nil || s.Factors == nil {
		return 0, errors.New("参数提交需要存储与 Factor 客户端")
	}
	batch := s.Batch
	if batch <= 0 {
		batch = DefaultBatch
	}
	due, err := s.Store.DuePendingParamSubmissions(ctx, s.now(), batch)
	if err != nil {
		return 0, err
	}
	for _, submission := range due {
		if err := s.deliver(ctx, submission); err != nil && s.OnError != nil {
			s.OnError(submission.RequestID, err)
		}
		if ctx.Err() != nil {
			break
		}
	}
	return len(due), nil
}

func (s *Submitter) finish(ctx context.Context, submission store.ParamSubmission, status string, outcome any) error {
	raw, err := json.Marshal(outcome)
	if err != nil {
		return err
	}
	if err := s.Store.FinishParamSubmission(ctx, submission.RequestID, status, raw, s.now()); err != nil {
		return err
	}
	if s.OnResult != nil {
		s.OnResult(status)
	}
	return nil
}

func (s *Submitter) deliver(ctx context.Context, submission store.ParamSubmission) error {
	if s.Allowed != nil && !s.Allowed(submission.InstanceID) {
		return nil
	}
	// 实例停用、换了会话或已删除后，旧的参数不再有意义：放弃，不去影响私有因子集。
	instance, err := s.Store.GetInstance(ctx, submission.InstanceID)
	if err != nil || !instance.Enabled || instance.DeletedAt != nil || instance.SessionID == nil || *instance.SessionID != submission.SessionID {
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		return s.finish(ctx, submission, store.ParamAbandoned, []input.MemberSubmitResult{{Result: store.ParamAbandoned, Reason: "实例已停用或换了会话"}})
	}
	if s.now().Sub(submission.PeriodTime) > s.maxAge() {
		return s.finish(ctx, submission, store.ParamAbandoned, []input.MemberSubmitResult{{Result: store.ParamAbandoned, Reason: "超过重试期限仍没有得到结果"}})
	}
	members := map[string]map[string]input.ParamValue{}
	if err := json.Unmarshal(submission.ParamsJSON, &members); err != nil {
		return s.finish(ctx, submission, store.ParamRejected, []input.MemberSubmitResult{{Result: store.ParamRejected, Reason: "待提交的参数无法解析"}})
	}
	callCtx, cancel := context.WithTimeout(ctx, submissionTimeout)
	defer cancel()
	results, err := s.Factors.SubmitFactorParams(callCtx, input.ParamSubmission{
		RequestID: submission.RequestID, Writer: submission.InstanceID, SetID: submission.SetID,
		PeriodTime: submission.PeriodTime, BasedOnPeriod: submission.BasedOnPeriod, Members: members,
	})
	if err != nil {
		var service *input.ServiceError
		if errors.As(err, &service) && service.Code == commonpb.ErrorCode_INVALID_PARAM {
			// 请求本身被判为无效：重试也不会成功。
			return s.finish(ctx, submission, store.ParamRejected, []input.MemberSubmitResult{{Result: store.ParamRejected, Reason: err.Error()}})
		}
		next := s.now().Add(s.backoff(submission.Attempts))
		if retryErr := s.Store.RetryParamSubmission(ctx, submission.RequestID, next, s.now()); retryErr != nil {
			return errors.Join(err, retryErr)
		}
		return fmt.Errorf("提交参数 %s：%w", submission.RequestID, err)
	}
	return s.finish(ctx, submission, terminalStatus(results), results)
}

// terminalStatus 把逐成员结果归并成一个终态：有被拒绝的记 rejected，否则有晚到的记 too_late，否则 accepted。
func terminalStatus(results []input.MemberSubmitResult) string {
	status := store.ParamAccepted
	for _, result := range results {
		switch result.Result {
		case input.SubmitRejected:
			return store.ParamRejected
		case input.SubmitTooLate:
			status = store.ParamTooLate
		}
	}
	return status
}

func (s *Submitter) maxAge() time.Duration {
	if s.MaxAge > 0 {
		return s.MaxAge
	}
	return DefaultMaxAge
}

// backoff 是第 attempts 次失败后的退避：1 秒起逐次翻倍，不超过上限。
func (s *Submitter) backoff(attempts int) time.Duration {
	limit := s.MaxBackoff
	if limit <= 0 {
		limit = DefaultMaxBackoff
	}
	delay := initialBackoff
	for i := 0; i < attempts && delay < limit; i++ {
		delay *= 2
	}
	if delay > limit {
		delay = limit
	}
	return delay
}
