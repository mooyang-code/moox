package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/marketwiring"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/internal/subjectsync"
	"github.com/mooyang-code/moox/modules/collector/internal/tradingstatus"
	"github.com/mooyang-code/moox/packages/cnstock"
	"github.com/mooyang-code/moox/packages/timerjob"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

const (
	subjectTagsTimerService       = "trpc.moox.collector.subject_tags.timer"
	subjectAttributesTimerService = "trpc.moox.collector.subject_attributes.timer"
	subjectSyncTimeout            = 10 * time.Minute
	tradingStatusStartupDelay     = time.Minute
)

func registerSubjectSyncRuntime(s *server.Server, cfg *Config, process *Runtime, dbm *store.Store) (*subjectsync.Syncer, *tradingstatus.Runner, error) {
	egress, err := httpclient.NewEgressHTTPClient(process.gateway, cfg.EgressProxy.Domains)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize subject HTTP client: %w", err)
	}
	process.beforeClose = append(process.beforeClose, func() error { egress.Close(); return nil })
	listers, indexListers, err := marketwiring.NewSubjectListers(egress)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize subject listers: %w", err)
	}
	storage, err := subjectsync.NewStorageClient(process.gateway)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize subject storage: %w", err)
	}
	for _, job := range cfg.SubjectSync.Attributes {
		kind := job.InstrumentType
		if kind == "" {
			rules, err := markets.RulesFor(job.SpaceID)
			if err != nil {
				return nil, nil, fmt.Errorf("subject_sync: %w", err)
			}
			kind = string(rules.DefaultInstrumentType(""))
		}
		for _, source := range job.Sources {
			if listers[subjectsync.ListerKey{MarketID: job.SpaceID, Source: source, InstrumentType: kind}] == nil {
				return nil, nil, fmt.Errorf("subject_sync source %s does not support %s %s subject listing", source, job.SpaceID, kind)
			}
		}
	}
	// crypto 的交易状态检测要连币安（经出口代理），跟随 delist.auto_detect：没配出口代理时渲染成 false，两者一起关掉。
	cryptoStatus := cfg.Delist.AutoDetect
	trading, err := newTradingStatusRunner(storage, listers, dbm.DelistNotices(), cryptoStatus)
	if err != nil {
		return nil, nil, err
	}
	metrics := subjectsync.NewMetrics(prometheus.DefaultRegisterer)
	tags := &subjectsync.TagRunner{Store: storage, Runs: dbm.TagSyncRuns(), Listers: listers, IndexListers: indexListers, FetchTimeout: cfg.SubjectSync.FetchTimeout, Metrics: metrics, KeepHaltedMembers: cryptoStatus}
	attributes := &subjectsync.AttributeRunner{Store: storage, Listers: listers, Jobs: cfg.SubjectSync.Attributes, FetchTimeout: cfg.SubjectSync.FetchTimeout, Metrics: metrics}
	syncer := &subjectsync.Syncer{Tags: tags, Attributes: attributes}
	// 交易状态检测挂在属性任务的每分钟定时器上：入口先固定触发时间，先跑交易状态（不进同步锁，写的是另一列），再跑属性任务。
	attributeTick := func(ctx context.Context) error {
		now := attributes.Current()
		return errors.Join(trading.RunDueAt(ctx, now), syncer.RunDueAttributesAt(ctx, now))
	}
	if err := registerSubjectSyncTimers(s, process, syncer.RunDueTags, attributeTick); err != nil {
		return nil, nil, err
	}
	// 启动后跑一次：重启跨过了定时时刻（如 9:05）时状态不至于停在旧值一整天。等一分钟，让网关目录与 Storage 连接就绪。
	process.launch(func() {
		select {
		case <-process.ctx.Done():
			return
		case <-time.After(tradingStatusStartupDelay):
		}
		_ = process.run(process.ctx, func(ctx context.Context) error {
			if err := trading.RunAll(ctx); err != nil {
				log.WarnContextf(ctx, "trading status startup run: %v", err)
			}
			return nil
		})
	})
	return syncer, trading, nil
}

// newTradingStatusRunner 按各市场 market.yaml 的 trading_status 组装检测任务：股票用腾讯快照（控制机直连），
// crypto 用标的同步的币安 lister（经出口代理）加下架记录。cryptoEnabled 为假时不装 crypto 的任务。
func newTradingStatusRunner(storage *subjectsync.StorageClient, listers subjectsync.Listers, delists *store.DelistNoticeRepository, cryptoEnabled bool) (*tradingstatus.Runner, error) {
	tencent := cnstock.TencentClient{HTTPClient: &http.Client{Timeout: 10 * time.Second}}
	stockMarkets := map[string]string{"stockcn": tradingstatus.MarketCN, "stockhk": tradingstatus.MarketHK, "stockus": tradingstatus.MarketUS}
	marketIDs := markets.IDs()
	sort.Strings(marketIDs)
	runner := &tradingstatus.Runner{Store: storage}
	for _, marketID := range marketIDs {
		config, err := markets.ConfigFor(marketID)
		if err != nil {
			return nil, err
		}
		for _, job := range config.TradingStatus {
			entry := tradingstatus.Job{SpaceID: marketID, MarketType: job.MarketType, Cron: job.Cron, Timezone: job.Timezone}
			switch job.Source {
			case markets.TradingStatusSourceTencent:
				market, ok := stockMarkets[marketID]
				if !ok {
					return nil, fmt.Errorf("trading_status: %s 不是腾讯快照支持的股票市场", marketID)
				}
				entry.Source = tradingstatus.TencentSource{Client: tencent, Market: market}
			case markets.TradingStatusSourceBinance:
				if !cryptoEnabled {
					continue
				}
				marketType := job.MarketType
				if listers[subjectsync.ListerKey{MarketID: marketID, Source: "binance", InstrumentType: marketType}] == nil {
					return nil, fmt.Errorf("trading_status: %s 没有币安 %s 的标的列表", marketID, marketType)
				}
				entry.Source = tradingstatus.BinanceSource{
					MarketType: marketType,
					List: func(ctx context.Context) ([]marketdata.Instrument, error) {
						return subjectsync.FetchSnapshot(ctx, listers, marketID, []string{"binance"}, marketType)
					},
					Delisted: func(ctx context.Context, now time.Time) (map[string]bool, error) {
						return delists.DelistedSymbols(ctx, "binance", tradingstatus.DelistMarketSpot, tradingstatus.DelistScopeAll, now)
					},
				}
			}
			runner.Jobs = append(runner.Jobs, entry)
		}
	}
	return runner, nil
}

func subjectSyncJob(name string, process *Runtime, run func(context.Context) error) (*timerjob.Job, error) {
	if process == nil || run == nil {
		return nil, fmt.Errorf("subject sync timer requires runtime and runner")
	}
	return timerjob.New(name, subjectSyncTimeout, func(ctx context.Context) error {
		return process.run(ctx, run)
	})
}

func registerSubjectSyncTimers(s *server.Server, process *Runtime, tags, attributes func(context.Context) error) error {
	if s == nil {
		return fmt.Errorf("subject sync timers require a server")
	}
	for _, entry := range []struct {
		service string
		name    string
		run     func(context.Context) error
	}{
		{subjectTagsTimerService, "collector_subject_tags", tags},
		{subjectAttributesTimerService, "collector_subject_attributes", attributes},
	} {
		service := s.Service(entry.service)
		if service == nil {
			return fmt.Errorf("subject sync timer %q is not configured", entry.service)
		}
		job, err := subjectSyncJob(entry.name, process, entry.run)
		if err != nil {
			return err
		}
		timer.RegisterHandlerService(service, job.Handle)
	}
	return nil
}
