package rpc

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"google.golang.org/protobuf/proto"
)

const (
	// filledInboxLimit 与 filledInboxTTL 限制暂存的补齐通知：引擎长时间离线时旧通知没有意义（引擎恢复后靠追赶循环读回）。
	filledInboxLimit = 1000
	filledInboxTTL   = 10 * time.Minute
)

// FilledInbox 暂存 Collector 发来的补齐通知，在引擎心跳响应里交给持有租约的引擎。
type FilledInbox struct {
	mu    sync.Mutex
	items []filledItem
	now   func() time.Time
}

type filledItem struct {
	at     time.Time
	filled *factorpb.PeriodsFilled
}

// NewFilledInbox 构造暂存。
func NewFilledInbox() *FilledInbox { return &FilledInbox{now: time.Now} }

// Add 暂存一批通知；超过上限时丢弃最旧的。
func (b *FilledInbox) Add(filled []*factorpb.PeriodsFilled) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	for _, item := range filled {
		b.items = append(b.items, filledItem{at: now, filled: proto.Clone(item).(*factorpb.PeriodsFilled)})
	}
	if extra := len(b.items) - filledInboxLimit; extra > 0 {
		b.items = b.items[extra:]
	}
}

// Drain 取出全部未过期的通知。
func (b *FilledInbox) Drain() []*factorpb.PeriodsFilled {
	b.mu.Lock()
	defer b.mu.Unlock()
	cutoff := b.now().Add(-filledInboxTTL)
	var out []*factorpb.PeriodsFilled
	for _, item := range b.items {
		if item.at.After(cutoff) {
			out = append(out, item.filled)
		}
	}
	b.items = nil
	return out
}

// WithFilledInbox 启用 NotifyPeriodsFilled。
func WithFilledInbox(inbox *FilledInbox) ServiceOption {
	return func(s *Service) { s.filled = inbox }
}

// NotifyPeriodsFilled 接收 Collector 的补齐通知（服务目录只放行 collector），在引擎心跳响应里转交。
func (s *Service) NotifyPeriodsFilled(_ context.Context, req *factorpb.NotifyPeriodsFilledReq) (*factorpb.NotifyPeriodsFilledRsp, error) {
	if s.filled == nil {
		return &factorpb.NotifyPeriodsFilledRsp{RetInfo: inner(fmt.Errorf("periods-filled notifications are not enabled"))}, nil
	}
	for _, item := range req.GetFilled() {
		if strings.TrimSpace(item.GetSpaceId()) == "" || strings.TrimSpace(item.GetDatasetId()) == "" || strings.TrimSpace(item.GetFrequency()) == "" || item.GetFrom() <= 0 || item.GetTo() <= item.GetFrom() {
			return &factorpb.NotifyPeriodsFilledRsp{RetInfo: invalid(fmt.Errorf("filled requires space_id, dataset_id, frequency and from < to"))}, nil
		}
	}
	s.filled.Add(req.GetFilled())
	return &factorpb.NotifyPeriodsFilledRsp{RetInfo: success()}, nil
}
