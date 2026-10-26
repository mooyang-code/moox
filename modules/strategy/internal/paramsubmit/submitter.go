// Package paramsubmit 把策略每期求出的下一期可变成员参数投递给 FactorMgr：参数和结果在同一个事务里落库
// （t_strategy_results.c_next_params_json），这里的循环只投每个启用实例当前会话里最新一条还没送达的结果，
// request_id 取结果 ID。FactorMgr 只接受目标期不减的提交（同一目标期按 request_id 幂等），所以重试的旧请求即使晚到
// 也不会让参数倒退；被明确拒绝的记录标为已处理，不再重试。
package paramsubmit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/commonpb"
)

// 默认值。
const (
	DefaultInterval   = time.Second
	DefaultBatch      = 20
	DefaultMaxBackoff = 30 * time.Second
	initialBackoff    = time.Second
	submissionTimeout = 10 * time.Second
)

// 投递结果（指标）。
const (
	ResultDelivered = "delivered"
	ResultRejected  = "rejected"
)

// Submitter 投递待送达的因子参数。
type Submitter struct {
	Store   *store.Store
	Factors input.FactorTasks
	Now     func() time.Time
	// Interval 是轮询的间隔；Batch 是每轮最多处理的条数；MaxBackoff 是失败重试退避的上限。
	Interval   time.Duration
	Batch      int
	MaxBackoff time.Duration
	// Allowed 返回实例当前是否允许提交参数（模块暂停、实例暂停时不提交，记录保持待送达，因子沿用最近一次提交的参数）；nil 表示总是允许。
	Allowed func(instanceID string) bool
	// OnResult 在每条记录有了终态时调用（指标）：delivered、rejected。
	OnResult func(result string)
	// OnError 记录投递的异常（同一条记录每次失败都会调用，由调用方决定如何降噪）。
	OnError func(resultID string, err error)

	mu sync.Mutex
	// retries 记下失败过的记录的退避：结果 ID → 已失败次数与下次可重试的时间。只在内存里，重启后立即重试。
	retries map[string]retryState
}

type retryState struct {
	failures int
	next     time.Time
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

// RunOnce 处理一轮待送达的记录，返回处理（含因退避或暂停而跳过）的条数。
func (s *Submitter) RunOnce(ctx context.Context) (int, error) {
	if s.Store == nil || s.Factors == nil {
		return 0, errors.New("参数提交需要存储与 Factor 客户端")
	}
	batch := s.Batch
	if batch <= 0 {
		batch = DefaultBatch
	}
	pending, err := s.Store.PendingParamDeliveries(ctx, batch)
	if err != nil {
		return 0, err
	}
	for _, delivery := range pending {
		if err := s.deliver(ctx, delivery); err != nil && s.OnError != nil {
			s.OnError(delivery.ResultID, err)
		}
		if ctx.Err() != nil {
			break
		}
	}
	return len(pending), nil
}

func (s *Submitter) deliver(ctx context.Context, delivery store.ParamDelivery) error {
	if s.Allowed != nil && !s.Allowed(delivery.InstanceID) {
		return nil // 暂停时不提交，记录保持待送达。
	}
	s.mu.Lock()
	retry := s.retries[delivery.ResultID]
	s.mu.Unlock()
	if retry.failures > 0 && s.now().Before(retry.next) {
		return nil
	}
	instance, err := s.Store.GetInstance(ctx, delivery.InstanceID)
	if err != nil {
		return err
	}
	members := map[string]map[string]input.ParamValue{}
	if err := json.Unmarshal(delivery.Members, &members); err != nil {
		return s.reject(ctx, delivery, "待投递的参数无法解析")
	}
	callCtx, cancel := context.WithTimeout(ctx, submissionTimeout)
	defer cancel()
	results, err := s.Factors.SubmitFactorParams(callCtx, input.ParamSubmission{
		RequestID: delivery.ResultID, Writer: delivery.InstanceID, TaskID: instance.FactorTaskID,
		PeriodTime: delivery.Period, Members: members,
	})
	if err != nil {
		var service *input.ServiceError
		if errors.As(err, &service) && service.Code == commonpb.ErrorCode_INVALID_PARAM {
			// 请求本身被判为无效：重试也不会成功。
			return s.reject(ctx, delivery, err.Error())
		}
		s.recordFailure(delivery.ResultID)
		return fmt.Errorf("提交参数 %s：%w", delivery.ResultID, err)
	}
	if reason := rejection(results); reason != "" {
		return s.reject(ctx, delivery, reason)
	}
	if err := s.Store.MarkParamsDelivered(ctx, delivery.ResultID, s.now()); err != nil {
		return err
	}
	s.clear(delivery.ResultID)
	if s.OnResult != nil {
		s.OnResult(ResultDelivered)
	}
	return nil
}

// reject 把记录标为被拒绝：不再重试，也不算送达。
func (s *Submitter) reject(ctx context.Context, delivery store.ParamDelivery, reason string) error {
	if err := s.Store.MarkParamsRejected(ctx, delivery.ResultID, reason); err != nil {
		return err
	}
	s.clear(delivery.ResultID)
	if s.OnResult != nil {
		s.OnResult(ResultRejected)
	}
	return nil
}

// rejection 汇总被拒绝的成员及原因；全部成员都被接受（或幂等重复）时返回空串。
func rejection(results []input.MemberSubmitResult) string {
	var reasons []string
	for _, result := range results {
		if result.Result == input.SubmitRejected {
			reasons = append(reasons, fmt.Sprintf("%s：%s", result.Member, result.Reason))
		}
	}
	return strings.Join(reasons, "；")
}

func (s *Submitter) recordFailure(resultID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retries == nil {
		s.retries = map[string]retryState{}
	}
	state := s.retries[resultID]
	state.failures++
	state.next = s.now().Add(s.backoff(state.failures - 1))
	s.retries[resultID] = state
}

func (s *Submitter) clear(resultID string) {
	s.mu.Lock()
	delete(s.retries, resultID)
	s.mu.Unlock()
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
