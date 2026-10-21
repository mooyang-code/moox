package stream

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"trpc.group/trpc-go/trpc-go/log"
)

// defaultCommitReserve 是最晚开始提交时刻 D 之前给提交留出的时间：D = 收盘 + 截止 − 预留。
const defaultCommitReserve = time.Second

// statGrace 是截止之后再等多久生成周期统计（截止由存储机判定，留出两端时钟与网络的余量）。
const statGrace = time.Second

// PeriodStat 是一个（数据集, 周期）结束时的统计；时间都相对收盘。
type PeriodStat struct {
	Market          string
	DatasetID       string
	Frequency       string
	Period          time.Time
	Expected        int
	Arrived         int
	Missing         int
	MissingSubjects []string
	// Suspected 是缺失里已判定为疑似下架的标的数（告警不计入）。
	Suspected       int
	LateAfterCommit int
	Duplicates      int
	Conflicts       int
	Unplanned       int
	LastArrival     time.Duration
	CommitDone      time.Duration
	CommitDuration  time.Duration
	Committed       bool
	Err             string
}

// GateConfig 是一个（数据集, 频率）闸门的依赖。
type GateConfig struct {
	SpaceID       string
	Market        marketfetch.PlanMarket
	DatasetID     string
	Frequency     marketdata.Frequency
	Committer     PeriodCommitter
	Queue         *CommitQueue
	Clock         Clock
	CommitReserve time.Duration
	Report        func(PeriodStat)
	Metrics       *Metrics
}

type slotState int

const (
	slotCollecting slotState = iota
	slotCommitting
	slotCommitted
	slotIdle // 到 D 一个都没到：不提交
)

type periodSlot struct {
	period      time.Time
	close       time.Time
	deadline    time.Time
	dataset     marketfetch.PlanDataset // 只带这一期生效的那一份
	symbols     func(string) string
	index       map[string]int // 订阅符号 → 该份标的下标
	bars        []marketdata.NormalizedKline
	got         []bool
	sent        []bool
	arrived     int
	state       slotState
	pendingLate bool

	lastArrival time.Time
	commitDone  time.Time
	commitTook  time.Duration
	late        int
	duplicates  int
	conflicts   int
	unplanned   int
	err         string
	reported    bool
}

// Gate 是周期闸门：按周期收集定稿、去重，到齐即提交，到最晚提交时刻 D 提交已到的，提交后截止前到的增量再提交。
// 每个闸门一个协程，独占自己的状态，不加锁；外部只通过 post 投递。
type Gate struct {
	cfg  GateConfig
	step time.Duration
	in   chan func()

	plan       *marketfetch.Plan
	dataset    *marketfetch.PlanDataset
	pending    map[int64]*periodSlot
	closeTimer Stopper
}

// NewGate 构造闸门；Start 之后才处理消息。
func NewGate(cfg GateConfig) (*Gate, error) {
	step := cfg.Frequency.Duration()
	if step <= 0 {
		return nil, fmt.Errorf("stream gate does not support frequency %s", cfg.Frequency)
	}
	if cfg.CommitReserve <= 0 {
		cfg.CommitReserve = defaultCommitReserve
	}
	if cfg.Clock == nil {
		cfg.Clock = SystemClock{}
	}
	return &Gate{cfg: cfg, step: step, in: make(chan func(), 4096), pending: map[int64]*periodSlot{}}, nil
}

func (g *Gate) post(f func()) { g.in <- f }

// Start 启动闸门协程，并安排第一个收盘时刻。
func (g *Gate) Start(ctx context.Context) {
	go func() {
		for {
			select {
			case f := <-g.in:
				f()
			case <-ctx.Done():
				if g.closeTimer != nil {
					g.closeTimer.Stop()
				}
				return
			}
		}
	}()
	g.post(g.scheduleClose)
}

// Sync 等闸门处理完此前投递的全部消息（测试与关闭时使用）。
func (g *Gate) Sync() {
	done := make(chan struct{})
	g.post(func() { close(done) })
	<-done
}

// SetPlan 换成新的计划：已建立的周期不受影响，之后建立的周期按各自生效的那份判断期望标的。
// dataset 为空表示该数据集已不在计划里，闸门只处理完已建立的周期。
func (g *Gate) SetPlan(plan *marketfetch.Plan, dataset *marketfetch.PlanDataset) {
	g.post(func() { g.plan, g.dataset = plan, dataset })
}

// SetCommitReserve 更新提交预留：之后建立的周期按新值计算最晚提交时刻。
func (g *Gate) SetCommitReserve(reserve time.Duration) {
	if reserve > 0 {
		g.post(func() { g.cfg.CommitReserve = reserve })
	}
}

// Offer 投递一条解码后的事件；只处理收盘定稿。
func (g *Gate) Offer(event Event) {
	if event.Kind != EventKlineFinal {
		return
	}
	received := g.cfg.Clock.Now()
	g.post(func() { g.onFinal(event, received) })
}

// Idle 判断闸门没有计划也没有待处理的周期（可以停掉）。
func (g *Gate) Idle() bool {
	result := make(chan bool, 1)
	g.post(func() { result <- g.dataset == nil && len(g.pending) == 0 })
	return <-result
}

func (g *Gate) scheduleClose() {
	now := g.cfg.Clock.Now()
	next := now.Truncate(g.step).Add(g.step)
	g.closeTimer = g.cfg.Clock.AfterFunc(next.Sub(now), func() { g.post(func() { g.onClose(next) }) })
}

// onClose 在收盘时刻 C 主动建立刚收盘的那一期（一条推送都没来的周期也能统计为零到达），并回收旧周期。
func (g *Gate) onClose(close time.Time) {
	g.scheduleClose()
	g.ensureSlot(close.Add(-g.step))
	for key, slot := range g.pending {
		if slot.reported && slot.period.Before(close.Add(-2*g.step)) {
			delete(g.pending, key)
		}
	}
}

func (g *Gate) ensureSlot(period time.Time) *periodSlot {
	if slot := g.pending[period.Unix()]; slot != nil {
		return slot
	}
	if g.plan == nil || g.dataset == nil {
		return nil
	}
	selected, ok := g.dataset.SelectCopy(period)
	if !ok || len(selected.Subjects) == 0 {
		return nil
	}
	dataset := *g.dataset
	dataset.Copies = []marketfetch.PlanCopy{selected}
	plan := g.plan
	slot := &periodSlot{
		period: period, close: period.Add(g.step), dataset: dataset, symbols: plan.ExchangeSymbol,
		index: make(map[string]int, len(selected.Subjects)),
		bars:  make([]marketdata.NormalizedKline, len(selected.Subjects)), got: make([]bool, len(selected.Subjects)), sent: make([]bool, len(selected.Subjects)),
	}
	for position, subject := range selected.Subjects {
		slot.index[strings.ToUpper(plan.Pair(subject.ID))] = position
	}
	slot.deadline = slot.close.Add(time.Duration(dataset.LatenessS) * time.Second)
	due := slot.deadline.Add(-g.cfg.CommitReserve)
	if due.Before(slot.close) {
		due = slot.close
	}
	now := g.cfg.Clock.Now()
	g.pending[period.Unix()] = slot
	g.cfg.Clock.AfterFunc(due.Sub(now), func() { g.post(func() { g.onDue(slot) }) })
	g.cfg.Clock.AfterFunc(slot.deadline.Add(statGrace).Sub(now), func() { g.post(func() { g.onEnd(slot) }) })
	return slot
}

func (g *Gate) onFinal(event Event, received time.Time) {
	period := event.Kline.BarStart.UTC()
	slot := g.pending[period.Unix()]
	if slot == nil {
		// 推送早于本机定时器到达（时钟稍慢）：已收盘的这一期就地建立；更早的过期推送丢弃。
		closeAt := period.Add(g.step)
		if closeAt.After(received.Add(5*time.Second)) || closeAt.Before(received.Add(-g.step)) {
			g.cfg.Metrics.observeStale(g.cfg.Market.MarketType)
			return
		}
		if slot = g.ensureSlot(period); slot == nil {
			return
		}
	}
	position, ok := slot.index[strings.ToUpper(event.Symbol)]
	if !ok {
		// 不在这一期的期望标的里（含 P 之前就已订阅的新标的）。
		slot.unplanned++
		return
	}
	if slot.got[position] {
		slot.duplicates++
		if !sameKline(slot.bars[position], event.Kline) {
			slot.conflicts++
			log.Warnf("stream %s %s %s period=%s conflicting final klines for %s", g.cfg.Market.MarketType, g.cfg.DatasetID, g.cfg.Frequency, period.Format(time.RFC3339), event.Symbol)
		}
		return
	}
	bar := event.Kline
	bar.SubjectID = slot.dataset.Copies[0].Subjects[position].ID
	slot.bars[position], slot.got[position] = bar, true
	slot.arrived++
	slot.lastArrival = received
	g.cfg.Metrics.observeArrival(g.cfg.Market.MarketType, string(g.cfg.Frequency), received.Sub(slot.close))
	switch slot.state {
	case slotCollecting:
		if slot.arrived == len(slot.got) {
			g.commit(slot)
		}
	case slotCommitting:
		slot.pendingLate = true
	case slotCommitted, slotIdle:
		if received.Before(slot.deadline) {
			slot.late++
			g.commit(slot)
		}
	}
}

// onDue 是最晚开始提交的时刻 D：没到齐就提交已到的；一个都没到就不提交，只统计零到达。
func (g *Gate) onDue(slot *periodSlot) {
	if slot.state != slotCollecting {
		return
	}
	if slot.arrived == 0 {
		slot.state = slotIdle
		return
	}
	g.commit(slot)
}

// commit 把已到而尚未提交的 K 线交给提交队列。
func (g *Gate) commit(slot *periodSlot) {
	var bars []marketdata.NormalizedKline
	var positions []int
	for position := range slot.got {
		if slot.got[position] && !slot.sent[position] {
			bars = append(bars, slot.bars[position])
			positions = append(positions, position)
		}
	}
	if len(bars) == 0 {
		return
	}
	for _, position := range positions {
		slot.sent[position] = true
	}
	slot.state = slotCommitting
	slot.pendingLate = false
	batch := marketfetch.KlineBatch{
		SpaceID: g.cfg.SpaceID, BatchID: fmt.Sprintf("stream|%s|%s|%s|%d", g.cfg.Market.MarketType, slot.dataset.DatasetID, slot.dataset.Frequency, slot.period.Unix()),
		Market: g.cfg.Market, Dataset: slot.dataset, Period: slot.period, Symbols: slot.symbols, Bars: bars, Origin: marketfetch.KlineOriginStreaming,
	}
	g.cfg.Queue.submit(&commitJob{committer: g.cfg.Committer, batch: batch, deadline: slot.deadline, step: g.step, done: func(outcome commitOutcome) {
		g.post(func() { g.onCommitted(slot, positions, outcome) })
	}})
}

func (g *Gate) onCommitted(slot *periodSlot, positions []int, outcome commitOutcome) {
	slot.state = slotCommitted
	if outcome.err != nil {
		slot.err = outcome.err.Error()
		// 失败的标的留给截止判定与补采；允许截止前再次到达或增量提交时重试。
		for _, position := range positions {
			slot.sent[position] = false
		}
		log.Warnf("stream %s %s %s period=%s commit failed after %d attempts: %v", g.cfg.Market.MarketType, slot.dataset.DatasetID, slot.dataset.Frequency, slot.period.Format(time.RFC3339), outcome.attempts, outcome.err)
	} else {
		slot.err = ""
		if slot.commitDone.IsZero() {
			slot.commitDone = outcome.finished
			slot.commitTook = outcome.finished.Sub(outcome.started)
		}
		g.cfg.Metrics.observeCommit(g.cfg.Market.MarketType, string(g.cfg.Frequency), outcome.finished.Sub(slot.close), outcome.finished.Sub(outcome.started))
	}
	if slot.pendingLate && g.cfg.Clock.Now().Before(slot.deadline) {
		g.commit(slot)
	}
}

// onEnd 在截止之后生成这一期的统计。
func (g *Gate) onEnd(slot *periodSlot) {
	if slot.reported {
		return
	}
	slot.reported = true
	stat := PeriodStat{
		Market: g.cfg.Market.MarketType, DatasetID: slot.dataset.DatasetID, Frequency: slot.dataset.Frequency, Period: slot.period,
		Expected: len(slot.got), Arrived: slot.arrived, LateAfterCommit: slot.late, Duplicates: slot.duplicates, Conflicts: slot.conflicts,
		Unplanned: slot.unplanned, Committed: !slot.commitDone.IsZero(), Err: slot.err,
	}
	for position, got := range slot.got {
		if !got || !slot.sent[position] {
			stat.MissingSubjects = append(stat.MissingSubjects, slot.dataset.Copies[0].Subjects[position].ID)
		}
	}
	sort.Strings(stat.MissingSubjects)
	stat.Missing = len(stat.MissingSubjects)
	if !slot.lastArrival.IsZero() {
		stat.LastArrival = slot.lastArrival.Sub(slot.close)
	}
	if !slot.commitDone.IsZero() {
		stat.CommitDone, stat.CommitDuration = slot.commitDone.Sub(slot.close), slot.commitTook
	}
	g.cfg.Metrics.observePeriod(stat)
	if g.cfg.Report != nil {
		g.cfg.Report(stat)
	}
}

func sameKline(left, right marketdata.NormalizedKline) bool {
	return left.Open == right.Open && left.High == right.High && left.Low == right.Low && left.Close == right.Close &&
		left.VolumeShares == right.VolumeShares && left.AmountCNY == right.AmountCNY && left.TradeCount == right.TradeCount
}
