// Package seeding 记录 Collector 正在做历史补齐的数据集，供监控观察器把对应实例的期望置为“暂停（历史补齐中）”。
// 状态每分钟向 Collector 取一次（ListHistorySeeds，只要补齐中的），调不通沿用上一次，从没取到过按没有补齐。
package seeding

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

// collectorService 是 Collector 管理服务；ListHistorySeeds 在服务目录里已允许 strategy 调用。
const collectorService = "trpc.moox.collector.CollectMgr"

// RefreshEvery 是取补齐状态的间隔。
const RefreshEvery = time.Minute

// Lister 是取历史补齐记录的最小方法集合，便于测试替换。
type Lister interface {
	ListHistorySeeds(ctx context.Context, request *collectorpb.ListHistorySeedsReq) (*collectorpb.ListHistorySeedsRsp, error)
}

// GatewayLister 经网关客户端调用 Collector 的 ListHistorySeeds。
type GatewayLister struct {
	Gateway gatewayclient.Invoker
	// Timeout 是单次调用的超时；零表示沿用调用方 ctx。
	Timeout time.Duration
}

// ListHistorySeeds 实现 Lister。
func (g GatewayLister) ListHistorySeeds(ctx context.Context, request *collectorpb.ListHistorySeedsReq) (*collectorpb.ListHistorySeedsRsp, error) {
	if g.Gateway == nil {
		return nil, errors.New("strategy 网关客户端未配置")
	}
	if g.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, g.Timeout)
		defer cancel()
	}
	response := new(collectorpb.ListHistorySeedsRsp)
	if err := g.Gateway.Invoke(ctx, collectorService, "ListHistorySeeds", request, response); err != nil {
		return nil, err
	}
	return response, nil
}

// Tracker 保存最近一次取到的补齐中数据集。零值不可用，用 New 构造；nil 的 Tracker 视为没有补齐。
type Tracker struct {
	lister Lister
	logf   func(format string, args ...any)

	mu      sync.RWMutex
	running map[string]struct{}
}

// New 构造 Tracker；logf 为空时不记日志。
func New(lister Lister, logf func(format string, args ...any)) *Tracker {
	return &Tracker{lister: lister, logf: logf, running: map[string]struct{}{}}
}

func key(spaceID, datasetID, freq string) string {
	return strings.TrimSpace(spaceID) + "\x00" + strings.TrimSpace(datasetID) + "\x00" + strings.ToLower(strings.TrimSpace(freq))
}

// Refresh 取一次补齐状态。失败时保留上一次的结果并返回错误。
func (t *Tracker) Refresh(ctx context.Context) error {
	if t == nil || t.lister == nil {
		return errors.New("历史补齐状态来源未配置")
	}
	rsp, err := t.lister.ListHistorySeeds(ctx, &collectorpb.ListHistorySeedsReq{OnlyRunning: true})
	if err != nil {
		return fmt.Errorf("读取历史补齐状态：%w", err)
	}
	if code := rsp.GetRetInfo().GetCode(); code != collectorpb.ErrorCode_SUCCESS {
		return fmt.Errorf("读取历史补齐状态：Collector 返回 %v：%s", code, rsp.GetRetInfo().GetMsg())
	}
	next := make(map[string]struct{}, len(rsp.GetSeeds()))
	for _, seed := range rsp.GetSeeds() {
		if seed.GetStatus() == "running" {
			next[key(seed.GetSpaceId(), seed.GetDatasetId(), seed.GetFrequency())] = struct{}{}
		}
	}
	t.mu.Lock()
	t.running = next
	t.mu.Unlock()
	return nil
}

// Run 按间隔刷新直到 ctx 结束；启动时先取一次。取不到只记日志，沿用上一次。
func (t *Tracker) Run(ctx context.Context, every time.Duration) {
	if t == nil {
		return
	}
	if every <= 0 {
		every = RefreshEvery
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		if err := t.Refresh(ctx); err != nil && ctx.Err() == nil && t.logf != nil {
			t.logf("历史补齐状态未刷新，沿用上一次：%v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Active 报告数据集（某频率）是否在历史补齐中。
func (t *Tracker) Active(spaceID, datasetID, freq string) bool {
	if t == nil {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, ok := t.running[key(spaceID, datasetID, freq)]
	return ok
}

// Bound 报告实例绑定的主数据集是否在补齐中：任一绑定任务的结果数据集或其源 K 线数据集在补齐中即算。
// 绑因子结果 View 的实例，源 K 线数据集在补齐时因子侧也不出结果，这里一并算上，状态才能与因子侧一致。
func (t *Tracker) Bound(spaceID string, resolved input.Resolved) bool {
	if t == nil {
		return false
	}
	for _, task := range resolved.Tasks {
		if t.Active(spaceID, task.DatasetID, resolved.Bar) || (task.SourceDatasetID != "" && t.Active(spaceID, task.SourceDatasetID, resolved.Bar)) {
			return true
		}
	}
	return false
}
