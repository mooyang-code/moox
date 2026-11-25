package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"time"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"trpc.group/trpc-go/trpc-go/log"
)

// historySeedPeriod 是向 Collector 取“历史补齐中”集合的间隔：引擎每分钟同步一次目录，补齐状态一分钟内进快照。
const historySeedPeriod = time.Minute

// seedLister 取 Collector 当前补齐中的（空间、数据集、频率）。
type seedLister interface {
	RunningSeeds(ctx context.Context) ([]store.SeedKey, error)
}

// seedStore 是补齐集合落库的能力。
type seedStore interface {
	ReplaceHistorySeeds(ctx context.Context, keys []store.SeedKey) error
	ListHistorySeeds(ctx context.Context) ([]store.SeedKey, error)
}

// seedSink 接收补齐集合（引擎目录快照据此给任务标 SourceSeeding）。
type seedSink interface {
	SetHistorySeeds(keys []store.SeedKey)
}

// gatewayCollector 经网关调用 Collector 的 CollectMgr.ListHistorySeeds。
type gatewayCollector struct {
	gateway gatewayclient.Invoker
}

func (g gatewayCollector) RunningSeeds(ctx context.Context) ([]store.SeedKey, error) {
	if g.gateway == nil {
		return nil, errors.New("读取历史补齐状态需要网关客户端")
	}
	rsp := &collectorpb.ListHistorySeedsRsp{}
	if err := g.gateway.Invoke(ctx, "trpc.moox.collector.CollectMgr", "ListHistorySeeds", &collectorpb.ListHistorySeedsReq{OnlyRunning: true}, rsp); err != nil {
		return nil, err
	}
	if code := rsp.GetRetInfo().GetCode(); code != collectorpb.ErrorCode_SUCCESS {
		return nil, fmt.Errorf("ListHistorySeeds 返回 %v：%s", code, rsp.GetRetInfo().GetMsg())
	}
	keys := make([]store.SeedKey, 0, len(rsp.GetSeeds()))
	for _, seed := range rsp.GetSeeds() {
		if seed.GetStatus() != "" && seed.GetStatus() != "running" {
			continue
		}
		keys = append(keys, store.SeedKey{SpaceID: seed.GetSpaceId(), DatasetID: seed.GetDatasetId(), Freq: seed.GetFrequency()})
	}
	return keys, nil
}

// historySeedSyncer 每分钟取一次补齐状态，存进自己的库并交给 hub。取不到时沿用上一次的结果（重启后沿用库里的）。
type historySeedSyncer struct {
	lister seedLister
	db     seedStore
	sink   seedSink
}

// load 把库里上一次保存的集合交给 hub（启动时在引擎开始同步目录之前调用）；读不到按没有补齐。
func (s *historySeedSyncer) load(ctx context.Context) {
	keys, err := s.db.ListHistorySeeds(ctx)
	if err != nil {
		log.WarnContextf(ctx, "factor_history_seeds_load_failed error=%v", err)
		return
	}
	s.sink.SetHistorySeeds(keys)
}

// refresh 取一次补齐状态；成功则落库并更新 hub，失败返回错误、保持原状。
func (s *historySeedSyncer) refresh(ctx context.Context) error {
	keys, err := s.lister.RunningSeeds(ctx)
	if err != nil {
		return fmt.Errorf("取历史补齐状态: %w", err)
	}
	if err := s.db.ReplaceHistorySeeds(ctx, keys); err != nil {
		return fmt.Errorf("保存历史补齐状态: %w", err)
	}
	s.sink.SetHistorySeeds(keys)
	return nil
}

// run 启动时取一次，之后每隔 interval 取一次，直到 ctx 结束。
func (s *historySeedSyncer) run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		callCtx, cancel := context.WithTimeout(ctx, interval)
		if err := s.refresh(callCtx); err != nil && ctx.Err() == nil {
			log.WarnContextf(ctx, "factor_history_seeds_refresh_failed error=%v（沿用上一次的结果）", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
