package rpc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/attention"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/report"
)

// StreamHeartbeatAttention 判断 collector-stream 心跳是否中断，上报关注项 stream_heartbeat_lost：
// 空间里有启用的、采集方式为推送的 K 线任务，而该空间的心跳记录不存在或早于超时。心跳时刻在库里，Collector 重启后照样能判；
// Collector 启动后一个超时之内不判断（collector-stream 可能还没来得及发心跳）。阈值是 stream.heartbeat_timeout_seconds。
type StreamHeartbeatAttention struct {
	persistence *store.Store
	timeout     time.Duration
	attention   *report.Attention
	startedAt   time.Time
	now         func() time.Time
}

// NewStreamHeartbeatAttention 构造心跳判断；timeout 不大于 0 时用默认超时。
func NewStreamHeartbeatAttention(persistence *store.Store, timeout time.Duration, attn *report.Attention) *StreamHeartbeatAttention {
	if timeout <= 0 {
		timeout = DefaultStreamHeartbeatTimeout
	}
	return &StreamHeartbeatAttention{persistence: persistence, timeout: timeout, attention: attn, startedAt: time.Now(), now: time.Now}
}

// Refresh 按库里的任务与心跳记录重算各空间的心跳情况并上报；读库失败时保留上一次的结果。
func (h *StreamHeartbeatAttention) Refresh(ctx context.Context, spaces []string) error {
	if h == nil || h.attention == nil {
		return nil
	}
	items, err := h.lost(ctx, spaces)
	if err != nil {
		return err
	}
	h.attention.Set("stream", map[string][]string{attention.KindStreamHeartbeatLost: items})
	return nil
}

func (h *StreamHeartbeatAttention) lost(ctx context.Context, spaces []string) ([]string, error) {
	now := h.now()
	if now.Sub(h.startedAt) < h.timeout {
		return nil, nil
	}
	var lost []string
	var errs []error
	for _, spaceID := range spaces {
		spaceID = normalizeSpace(spaceID)
		streaming, err := h.hasStreamingTask(ctx, spaceID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !streaming {
			continue
		}
		worker, err := h.persistence.StreamWorkers().Get(ctx, spaceID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		switch {
		case worker == nil:
			lost = append(lost, spaceID+" 从未收到心跳")
		case now.Sub(worker.HeartbeatAt) > h.timeout:
			lost = append(lost, spaceID+" 最后心跳 "+report.AttentionTime(worker.HeartbeatAt))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("判断 collector-stream 心跳: %w", err)
	}
	if len(lost) == 0 {
		return nil, nil
	}
	sort.Strings(lost)
	// 合成一条：对象按序在前、处理提示在后（多条会被重新排序）。
	detail := strings.Join(lost, "；") + "。推送采集没有写入方，用 moox-cli setup logs --component collector-stream 查原因；" +
		"配了云函数时也可用 moox-cli collector ingest-mode set --ingest scf 切回（心跳已超时，交接不等它确认）"
	return []string{detail}, nil
}

// hasStreamingTask 报告空间里有没有启用的、采集方式为推送的 K 线任务。
func (h *StreamHeartbeatAttention) hasStreamingTask(ctx context.Context, spaceID string) (bool, error) {
	tasks, err := h.persistence.Tasks().ListKline(ctx, spaceID, "")
	if err != nil {
		return false, err
	}
	for _, task := range tasks {
		if task.Enabled && task.Ingest == domain.IngestStreaming {
			return true, nil
		}
	}
	return false, nil
}
