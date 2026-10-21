package stream

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"trpc.group/trpc-go/trpc-go/log"
)

// PlanItem 是计划内容里分给本进程的一项（一个市场）：执行端为 stream 的由运行时接收推送，local 的交给本地执行端定时拉取。
type PlanItem struct {
	Name     string
	Executor string
	Market   string
	Plan     *marketfetch.Plan
}

// Suspect 是疑似下架的标的。
type Suspect struct {
	Market    string
	SubjectID string
	Reason    string
}

// RuntimeConfig 是运行时的依赖。
type RuntimeConfig struct {
	SpaceID string
	// Providers 按 provider ID（如 binance）给出 WS 来源。
	Providers map[string]Provider
	// NewCommitter 为一个市场构造提交器（marketfetch.KlineCommitter，写入走本进程的网关客户端）。
	NewCommitter  func(marketfetch.PlanMarket) (PeriodCommitter, error)
	Clock         Clock
	CommitReserve time.Duration
	Manager       ManagerOptions
	Metrics       *Metrics
	Report        func(PeriodStat)
	Suspect       func(Suspect)
}

// Runtime 按计划为每个市场维护来源、连接与各（数据集, 频率）的闸门；所有闸门共用一个串行提交队列。
type Runtime struct {
	cfg     RuntimeConfig
	queue   *CommitQueue
	ctx     context.Context
	mu      sync.RWMutex
	markets map[string]*marketRuntime
}

type marketRuntime struct {
	name      string
	market    marketfetch.PlanMarket
	source    Source
	manager   *ConnManager
	committer PeriodCommitter
	tracker   *delistTracker

	mu     sync.RWMutex
	plan   *marketfetch.Plan
	gates  map[string]*Gate
	byFreq map[string][]*Gate
}

// NewRuntime 构造运行时；Start 之后才处理计划。
func NewRuntime(cfg RuntimeConfig) *Runtime {
	if cfg.Clock == nil {
		cfg.Clock = SystemClock{}
	}
	return &Runtime{cfg: cfg, queue: NewCommitQueue(cfg.Clock), markets: map[string]*marketRuntime{}}
}

// Start 启动提交队列与订阅刷新（每分钟按各份的生效时间重算订阅：移除的标的在它最后负责的周期提交之后才退订）。
func (r *Runtime) Start(ctx context.Context) {
	r.ctx = ctx
	go r.queue.Run(ctx)
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				r.mu.Lock()
				for _, market := range r.markets {
					market.manager.Close()
				}
				r.mu.Unlock()
				return
			case <-ticker.C:
				r.refreshAll()
			}
		}
	}()
}

// Apply 换成新的推送计划；不在计划里的市场在它的订阅全部不再需要后关闭。
func (r *Runtime) Apply(items []PlanItem) error {
	wanted := map[string]PlanItem{}
	for _, item := range items {
		if item.Executor != marketfetch.PlanExecutorStream || item.Plan == nil {
			continue
		}
		wanted[strings.ToLower(item.Plan.Market.MarketType)] = item
	}
	r.mu.Lock()
	var firstErr error
	for name, item := range wanted {
		market := r.markets[name]
		if market == nil {
			created, err := r.newMarket(name, item.Plan)
			if err != nil {
				log.Errorf("stream runtime cannot start market %s: %v", name, err)
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			market = created
			r.markets[name] = market
		}
		market.setPlan(r, item.Plan)
	}
	for name, market := range r.markets {
		if _, ok := wanted[name]; !ok {
			market.setPlan(r, nil)
		}
	}
	r.mu.Unlock()
	r.refreshAll()
	return firstErr
}

func (r *Runtime) newMarket(name string, plan *marketfetch.Plan) (*marketRuntime, error) {
	provider := r.cfg.Providers[strings.ToLower(plan.Market.Provider)]
	if provider == nil {
		return nil, fmt.Errorf("no stream provider for %s", plan.Market.Provider)
	}
	source, err := provider.StreamSource(plan.Market.MarketType)
	if err != nil {
		return nil, err
	}
	if key := source.Key(); !strings.EqualFold(key.SourceID, plan.Market.SourceID) || !strings.EqualFold(key.ProviderID, plan.Market.Provider) {
		return nil, fmt.Errorf("stream source %s does not match plan market %s/%s", key, plan.Market.Provider, plan.Market.SourceID)
	}
	committer, err := r.cfg.NewCommitter(plan.Market)
	if err != nil {
		return nil, err
	}
	market := &marketRuntime{name: name, market: plan.Market, source: source, committer: committer, gates: map[string]*Gate{}, byFreq: map[string][]*Gate{}}
	market.tracker = newDelistTracker(name, r.cfg.Suspect)
	options := r.cfg.Manager
	options.OnReconnect = func(group string) { r.cfg.Metrics.observeReconnect(name, group) }
	market.manager = NewConnManager(source, market.onEvent, options)
	market.manager.Start(r.ctx)
	return market, nil
}

func (m *marketRuntime) onEvent(event Event) {
	switch event.Kind {
	case EventKlineFinal:
		m.mu.RLock()
		gates := m.byFreq[event.Kline.Frequency]
		m.mu.RUnlock()
		for _, gate := range gates {
			gate.Offer(event)
		}
	case EventStatus:
		if !event.Trading {
			m.mu.RLock()
			plan := m.plan
			m.mu.RUnlock()
			if subject := subjectOfSymbol(plan, event.Symbol); subject != "" {
				m.tracker.flag(subject, "交易所推送合约状态已不在交易")
			}
		}
	case EventError:
		log.Warnf("stream %s message error: %s", m.name, event.Err)
	}
}

func subjectOfSymbol(plan *marketfetch.Plan, symbol string) string {
	if plan == nil {
		return ""
	}
	for _, dataset := range plan.Datasets {
		for _, copy := range dataset.Copies {
			for _, subject := range copy.Subjects {
				if strings.EqualFold(plan.Pair(subject.ID), symbol) {
					return subject.ID
				}
			}
		}
	}
	return ""
}

// setPlan 更新各闸门：新数据集建闸门，已有的换计划，计划里没有的数据集让闸门只处理完已建立的周期。
func (m *marketRuntime) setPlan(r *Runtime, plan *marketfetch.Plan) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.plan = plan
	seen := map[string]bool{}
	if plan != nil {
		for index := range plan.Datasets {
			dataset := &plan.Datasets[index]
			key := marketfetch.PlanDatasetKeyOf(*dataset)
			seen[key] = true
			gate := m.gates[key]
			if gate == nil {
				frequency, err := marketdata.ParseFrequency(dataset.Frequency)
				if err != nil {
					log.Errorf("stream %s dataset %s: %v", m.name, key, err)
					continue
				}
				gate, err = NewGate(GateConfig{SpaceID: r.cfg.SpaceID, Market: plan.Market, DatasetID: dataset.DatasetID, Frequency: frequency, Committer: m.committer,
					Queue: r.queue, Clock: r.cfg.Clock, CommitReserve: r.cfg.CommitReserve, Metrics: r.cfg.Metrics, Report: func(stat PeriodStat) {
						m.tracker.observe(&stat)
						if r.cfg.Report != nil {
							r.cfg.Report(stat)
						}
					}})
				if err != nil {
					log.Errorf("stream %s dataset %s: %v", m.name, key, err)
					continue
				}
				gate.Start(r.ctx)
				m.gates[key] = gate
				m.byFreq[dataset.Frequency] = append(m.byFreq[dataset.Frequency], gate)
			}
			gate.SetPlan(plan, dataset)
		}
	}
	for key, gate := range m.gates {
		if !seen[key] {
			gate.SetPlan(nil, nil)
		}
	}
	m.tracker.setPlan(plan)
}

// SetCommitReserve 更新提交预留（随计划与心跳下发）：新建的闸门与已有闸门之后建立的周期都用新值。
func (r *Runtime) SetCommitReserve(reserve time.Duration) {
	if reserve <= 0 {
		return
	}
	r.mu.Lock()
	if r.cfg.CommitReserve == reserve {
		r.mu.Unlock()
		return
	}
	r.cfg.CommitReserve = reserve
	markets := make([]*marketRuntime, 0, len(r.markets))
	for _, market := range r.markets {
		markets = append(markets, market)
	}
	r.mu.Unlock()
	for _, market := range markets {
		market.mu.Lock()
		for _, gate := range market.gates {
			gate.SetCommitReserve(reserve)
		}
		market.mu.Unlock()
	}
}

func (r *Runtime) refreshAll() {
	r.mu.RLock()
	markets := make([]*marketRuntime, 0, len(r.markets))
	for _, market := range r.markets {
		markets = append(markets, market)
	}
	r.mu.RUnlock()
	now := r.cfg.Clock.Now()
	for _, market := range markets {
		desired := market.desiredStreams(now)
		market.manager.Apply(desired)
		r.cfg.Metrics.observeConns(market.name, market.manager.Status())
	}
}

// desiredStreams 计算此刻需要的订阅：每个数据集取"最近收盘那一期生效的份"与之后生效的各份里的标的。
// 新增的标的拿到计划就订阅（P 之前的定稿不进闸门）；移除的标的等它最后负责的那一期过去之后才退订。
func (m *marketRuntime) desiredStreams(now time.Time) map[string][]string {
	m.mu.RLock()
	plan := m.plan
	m.mu.RUnlock()
	result := map[string][]string{}
	if plan == nil {
		return result
	}
	seen := map[string]map[string]struct{}{}
	smallest := time.Duration(0)
	smallestGroup := ""
	for _, dataset := range plan.Datasets {
		frequency, err := marketdata.ParseFrequency(dataset.Frequency)
		if err != nil || frequency.Duration() <= 0 {
			continue
		}
		step := frequency.Duration()
		if smallest == 0 || step < smallest {
			smallest, smallestGroup = step, dataset.Frequency
		}
		lastClosed := now.Truncate(step).Add(-step)
		for _, copy := range dataset.Copies {
			if selected, ok := dataset.SelectCopy(lastClosed); ok && copy.From < selected.From {
				continue
			}
			for _, subject := range copy.Subjects {
				name, ok := m.source.Stream(plan.Pair(subject.ID), frequency)
				if !ok {
					continue
				}
				if seen[dataset.Frequency] == nil {
					seen[dataset.Frequency] = map[string]struct{}{}
				}
				seen[dataset.Frequency][name] = struct{}{}
			}
		}
	}
	for group, streams := range seen {
		for name := range streams {
			result[group] = append(result[group], name)
		}
		sort.Strings(result[group])
	}
	if smallestGroup != "" && len(result[smallestGroup]) > 0 {
		result[smallestGroup] = append(result[smallestGroup], m.source.StatusStreams()...)
	}
	return result
}

// MarketStatus 是一个市场的连接状态。
type MarketStatus struct {
	Market string
	Conns  []ConnStatus
}

// Status 返回各市场的连接状态。
func (r *Runtime) Status() []MarketStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.markets))
	for name := range r.markets {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]MarketStatus, 0, len(names))
	for _, name := range names {
		result = append(result, MarketStatus{Market: name, Conns: r.markets[name].manager.Status()})
	}
	return result
}

// Ready 判断全部市场的连接都已订阅完成。
func (r *Runtime) Ready() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, market := range r.markets {
		if !market.manager.Ready() {
			return false
		}
	}
	return true
}
