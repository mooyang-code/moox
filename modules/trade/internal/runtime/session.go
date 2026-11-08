package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"trpc.group/trpc-go/trpc-go/log"

	"github.com/mooyang-code/moox/modules/trade/internal/application/accountsync"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/execution/paper"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// ErrSessionConfig 是交易账户没有可用的会话（新建账户要等运行时的周期检查才建起来）；RPC 返回 TRADE_SESSION_NOT_READY。
var ErrSessionConfig = errors.New("交易账户会话还没有启动，请稍后重试")

const (
	sessionOrderWindow        = 5 * time.Minute
	pollingSyncInterval       = 3 * time.Second
	instrumentRefreshInterval = time.Hour
)

var errPrivateDisconnected = errors.New("trade runtime: private stream disconnected")

type ExchangeSession struct {
	symbolMu          sync.Mutex
	configuredSymbols map[exchange.SymbolKey]string
	// symbolLocks 按标的串行设置杠杆：同一批里不同标的可以并发设置。
	symbolLocks map[exchange.SymbolKey]*sync.Mutex
	// requestMu 保护 syncRequested 的替换，外部请求同步时要读它。
	requestMu sync.Mutex

	Account               store.TradingAccountRecord
	Adapter               execution.ExecutionAdapter
	MarketData            execution.MarketDataSource
	AccountEvents         execution.AccountEventSource
	Sync                  *accountsync.Service
	SyncInterval          time.Duration
	PaperMatcherReady     func() bool
	PaperAccountState     func(string) paper.MatcherState
	PaperAccountRecovered func(string, uint64)
	OnReady               func(string)

	ready         atomic.Bool
	opMu          sync.Mutex
	syncRequested chan struct{}
}

func (s *ExchangeSession) Ready() bool {
	if s == nil || !s.ready.Load() {
		return false
	}
	if s.Account.ExecutionMode == string(exchange.ExecutionModePaper) {
		if s.PaperMatcherReady == nil || !s.PaperMatcherReady() {
			return false
		}
		if s.PaperAccountState != nil && !s.PaperAccountState(s.Account.TradingAccountID).Ready {
			return false
		}
	}
	return true
}

// RequestSync 请求一次防抖轻量同步；会话没有私有流（模拟盘、轮询）时返回 false，由调用方自己同步。
func (s *ExchangeSession) RequestSync() bool {
	s.requestMu.Lock()
	requested := s.syncRequested
	s.requestMu.Unlock()
	if requested == nil {
		return false
	}
	select {
	case requested <- struct{}{}:
	default:
	}
	return true
}

// waitRatePause 在锁外等待限流暂停结束，不在请求内睡眠：请求带签名时间戳，等待会让它过期。
func (s *ExchangeSession) waitRatePause(ctx context.Context) error {
	source, ok := s.Adapter.(exchange.RateStatusSource)
	if !ok {
		return nil
	}
	until := source.RateStatus().PausedUntil
	wait := time.Until(until)
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *ExchangeSession) ExecutionAdapter() execution.ExecutionAdapter {
	if s == nil {
		return nil
	}
	return s.Adapter
}

func (s *ExchangeSession) Run(ctx context.Context) error {
	if s == nil || s.Adapter == nil || s.Sync == nil || s.Sync.Store == nil ||
		s.Account.TradingAccountID == "" {
		return ErrSessionConfig
	}
	account, err := s.Sync.Store.GetTradingAccountByID(
		ctx,
		s.Account.TradingAccountID,
	)
	if err != nil {
		return err
	}
	s.Account = account
	marketData := s.marketData()
	if exchange.ExecutionMode(account.ExecutionMode) == exchange.ExecutionModePaper {
		return s.runPaper(ctx)
	}
	// 限流暂停（含 418 封禁）期间不重连：重连要拉账户、持仓和成交，会延长封禁。
	if err := s.waitRatePause(ctx); err != nil {
		return err
	}
	accountEvents := s.AccountEvents
	if accountEvents == nil {
		accountEvents = execution.AccountEventsOf(s.Adapter)
	}
	if accountEvents == nil {
		return s.runPolling(ctx)
	}
	// Public metadata must be durable before private events are accepted. The
	// handler only starts buffering once Subscribe begins, so the later REST
	// snapshot can safely replay the buffered stream against canonical facts.
	instruments, err := marketData.LoadInstruments(ctx)
	if err != nil {
		return s.disconnect(ctx, err)
	}
	if err := s.persistInstruments(ctx, instruments); err != nil {
		return s.disconnect(ctx, err)
	}
	s.requestMu.Lock()
	s.syncRequested = make(chan struct{}, 1)
	s.requestMu.Unlock()
	s.ready.Store(false)
	_ = s.Sync.SetReady(ctx, s.Account.TradingAccountID, false, nil)

	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	handler := newSessionHandler(s.applyEvent)
	streamDone := make(chan error, 1)
	disconnected := make(chan struct{})
	go func() {
		err := accountEvents.Subscribe(streamCtx, handler)
		s.ready.Store(false)
		close(disconnected)
		streamDone <- err
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-streamDone:
		return s.disconnect(ctx, err)
	case <-handler.ready:
	}
	if err, ended := privateStreamError(disconnected, streamDone); ended {
		return s.disconnect(ctx, err)
	}
	accountReadAt := time.Now()
	accountSnapshot, err := s.Adapter.GetAccountSnapshot(ctx)
	if err != nil {
		return s.disconnect(ctx, err)
	}
	if err, ended := privateStreamError(disconnected, streamDone); ended {
		return s.disconnect(ctx, err)
	}
	positions, err := s.Adapter.ListPositionSnapshots(ctx)
	if err != nil {
		return s.disconnect(ctx, err)
	}
	if err, ended := privateStreamError(disconnected, streamDone); ended {
		return s.disconnect(ctx, err)
	}
	orders, err := s.Adapter.ListOpenOrders(ctx)
	if err != nil {
		return s.disconnect(ctx, err)
	}
	if err, ended := privateStreamError(disconnected, streamDone); ended {
		return s.disconnect(ctx, err)
	}
	localOrders, err := s.Sync.Store.ListOrdersForAccount(
		ctx,
		s.Account.SpaceID,
		s.Account.TradingAccountID,
		time.Now().Add(-sessionOrderWindow).UnixMilli(),
	)
	if err != nil {
		return s.disconnect(ctx, err)
	}
	instrumentRecords, err := s.Sync.Store.ListInstrumentsForAccount(ctx, s.Account.TradingAccountID)
	if err != nil {
		return s.disconnect(ctx, err)
	}
	symbols, err := s.Sync.BoundedFullSymbols(ctx, s.Account, orders, localOrders, positions, instrumentRecords, accountSnapshot)
	if err != nil {
		return s.disconnect(ctx, err)
	}
	fills := make([]exchange.Fill, 0)
	cursors := cloneFillCursors(s.Account.FillCursors)
	for _, key := range symbols {
		rows, cursor, listErr := s.Adapter.ListRecentFills(
			ctx,
			key,
			cursors[key.String()],
		)
		if listErr != nil {
			return s.disconnect(ctx, listErr)
		}
		if err, ended := privateStreamError(disconnected, streamDone); ended {
			return s.disconnect(ctx, err)
		}
		fills = append(fills, rows...)
		if cursor != "" {
			cursors[key.String()] = cursor
		}
	}

	s.opMu.Lock()
	_, err = s.Sync.ApplySnapshot(ctx, s.Account.TradingAccountID, accountsync.Snapshot{
		Fills: fills, Orders: orders, Positions: positions,
		Account: accountSnapshot, AccountReadAt: accountReadAt, FillCursors: cursors, Ready: false,
		Prices: s.Adapter,
	})
	s.opMu.Unlock()
	if err != nil {
		return s.disconnect(ctx, err)
	}
	if err := handler.activate(ctx, func() error {
		if err, ended := privateStreamError(disconnected, streamDone); ended {
			return err
		}
		if err := s.Sync.SetReady(ctx, s.Account.TradingAccountID, true, nil); err != nil {
			return err
		}
		select {
		case <-disconnected:
			_ = s.Sync.SetReady(
				context.Background(),
				s.Account.TradingAccountID,
				false,
				errPrivateDisconnected,
			)
			return errPrivateDisconnected
		default:
		}
		return nil
	}); err != nil {
		return s.disconnect(ctx, err)
	}
	if err := handler.finishActivation(ctx); err != nil {
		return s.disconnect(ctx, err)
	}
	// Keep Ready invisible until the handler has released its activation lock.
	// Events arriving in that handoff window are then applied synchronously
	// before callers can submit against this session.
	if err, ended := privateStreamError(disconnected, streamDone); ended {
		return s.disconnect(ctx, err)
	}
	previous := s.ready.Swap(true)
	if !previous && s.OnReady != nil {
		s.OnReady(s.Account.TradingAccountID)
	}

	interval := s.SyncInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	instrumentTicker := time.NewTicker(instrumentRefreshInterval)
	defer instrumentTicker.Stop()
	light := newLightSyncSchedule()
	defer light.stop()
	for {
		select {
		case <-ctx.Done():
			return s.disconnect(context.Background(), ctx.Err())
		case <-instrumentTicker.C:
			s.refreshInstruments(ctx)
		case err := <-streamDone:
			return s.disconnect(context.Background(), err)
		case <-ticker.C:
			// 权重用到 80% 时推迟这轮对账，成交事件触发的轻量同步照常。
			if s.rateUsage() > reconcileUsageLimit {
				continue
			}
			// 同步失败时账户已记录错误并保持未就绪，下一轮重试；只有私有流结束才重启会话。
			s.opMu.Lock()
			_, _ = s.Sync.SyncAccountReconcile(ctx, s.Account.TradingAccountID)
			s.opMu.Unlock()
			if err, ended := privateStreamError(disconnected, streamDone); ended {
				return s.disconnect(context.Background(), err)
			}
		case <-s.syncRequested:
			// 防抖：250 毫秒内的成交、持仓、账户事件合并成一次轻量同步。防抖只合并同步，不会下单。
			light.request()
		case <-light.channel():
			s.opMu.Lock()
			result, err := s.Sync.SyncAccountLight(ctx, s.Account.TradingAccountID)
			s.opMu.Unlock()
			light.done(err == nil && !result.BalancePending)
			if err, ended := privateStreamError(disconnected, streamDone); ended {
				return s.disconnect(context.Background(), err)
			}
		}
	}
}

const (
	// lightSyncDebounce 是私有事件触发轻量同步前的合并等待。
	lightSyncDebounce = 250 * time.Millisecond
	// reconcileUsageLimit 是推迟 30 秒对账的权重用量比例。
	reconcileUsageLimit = 0.8
)

// lightSyncRetries 是轻量同步没能恢复准入（待确认水位未过、REST 失败、限流暂停）后的短退避，用完等 30 秒对账。
var lightSyncRetries = []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 2 * time.Second, 2 * time.Second}

// lightSyncSchedule 管理轻量同步的防抖和失败重排。定时器在同步之外，不持锁。
type lightSyncSchedule struct {
	timer    *time.Timer
	pending  bool
	attempts int
}

func newLightSyncSchedule() *lightSyncSchedule { return &lightSyncSchedule{} }

func (l *lightSyncSchedule) schedule(delay time.Duration) {
	if l.timer == nil {
		l.timer = time.NewTimer(delay)
	} else {
		l.timer.Stop()
		select {
		case <-l.timer.C:
		default:
		}
		l.timer.Reset(delay)
	}
	l.pending = true
}

// request 在没有待执行的同步时开始防抖；已有待执行（防抖或重排）时合并。
func (l *lightSyncSchedule) request() {
	if !l.pending {
		l.attempts = 0
		l.schedule(lightSyncDebounce)
	}
}

func (l *lightSyncSchedule) channel() <-chan time.Time {
	if !l.pending || l.timer == nil {
		return nil
	}
	return l.timer.C
}

// done 记录一次同步结果：没能恢复准入时按退避重排，最多 5 次。
func (l *lightSyncSchedule) done(recovered bool) {
	l.pending = false
	if recovered || l.attempts >= len(lightSyncRetries) {
		l.attempts = 0
		return
	}
	l.schedule(lightSyncRetries[l.attempts])
	l.attempts++
}

func (l *lightSyncSchedule) stop() {
	if l.timer != nil {
		l.timer.Stop()
	}
}

// rateUsage 是交易所接口本分钟的权重用量比例，模拟盘为 0。
func (s *ExchangeSession) rateUsage() float64 {
	source, ok := s.Adapter.(exchange.RateStatusSource)
	if !ok {
		return 0
	}
	return source.RateStatus().UsageRatio()
}

// runPaper shares the same SQLite facts and sync projections as Live, but has
// no private stream. Readiness is owned by the single process-local matcher.
func (s *ExchangeSession) runPaper(ctx context.Context) error {
	if s.Adapter == nil || s.Sync == nil {
		return ErrSessionConfig
	}
	generation := s.paperAccountGeneration()
	// 模拟适配器保存标的到原生代码的映射，所以从它加载。
	instruments, err := s.Adapter.LoadInstruments(ctx)
	if err != nil {
		return s.disconnect(ctx, err)
	}
	if len(instruments) > 0 {
		if err := s.persistInstruments(ctx, instruments); err != nil {
			return s.disconnect(ctx, err)
		}
	}
	// SyncAccount reads and applies the complete batch under the account lock.
	// Prefetching here could overwrite a fill committed while ApplySnapshot
	// waits for that lock, then falsely retire its refresh failure.
	if _, err := s.Sync.SyncAccount(ctx, s.Account.TradingAccountID); err != nil {
		return s.disconnect(ctx, err)
	}
	orders, err := s.Adapter.ListOpenOrders(ctx)
	if err != nil {
		return s.disconnect(ctx, err)
	}
	s.recoverPaperAccountAfterSync(generation, orders)
	setReady := func(ready bool) error {
		var cause error
		if !ready && s.PaperAccountState != nil {
			state := s.PaperAccountState(s.Account.TradingAccountID)
			if !state.Ready && state.LastError != "" {
				cause = errors.New(state.LastError)
			}
		}
		if err := s.Sync.SetReady(ctx, s.Account.TradingAccountID, ready, cause); err != nil {
			return err
		}
		previous := s.ready.Swap(ready)
		if ready && !previous && s.OnReady != nil {
			s.OnReady(s.Account.TradingAccountID)
		}
		return nil
	}
	interval := s.SyncInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	// 资金费在单独的协程里结算：停机后的补算要连续请求交易所，不能拖住就绪判断和同步；会话结束前等它退出。
	fundingCtx, stopFunding := context.WithCancel(ctx)
	var fundingDone sync.WaitGroup
	fundingDone.Go(func() { s.runPaperFunding(fundingCtx) })
	defer fundingDone.Wait()
	defer stopFunding()
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	refresh := time.NewTicker(interval)
	defer refresh.Stop()
	instrumentTicker := time.NewTicker(instrumentRefreshInterval)
	defer instrumentTicker.Stop()
	for {
		matcherReady := s.PaperMatcherReady != nil && s.PaperMatcherReady()
		if s.PaperAccountState != nil {
			matcherReady = matcherReady && s.PaperAccountState(s.Account.TradingAccountID).Ready
		}
		if matcherReady != s.ready.Load() {
			if err := setReady(matcherReady); err != nil {
				return s.disconnect(ctx, err)
			}
		}
		select {
		case <-ctx.Done():
			_ = s.disconnect(context.Background(), ctx.Err())
			return ctx.Err()
		case <-poll.C:
		case <-instrumentTicker.C:
			s.refreshInstruments(ctx)
		case <-refresh.C:
			generation := s.paperAccountGeneration()
			if _, err := s.Sync.SyncAccount(ctx, s.Account.TradingAccountID); err != nil {
				return s.disconnect(ctx, err)
			}
			orders, err := s.Adapter.ListOpenOrders(ctx)
			if err != nil {
				return s.disconnect(ctx, err)
			}
			s.recoverPaperAccountAfterSync(generation, orders)
		}
	}
}

// paperFundingSettler 是能结算永续资金费的模拟盘适配器（paper.Adapter 实现）。
type paperFundingSettler interface {
	SettleFunding(context.Context) (int, error)
}

// runPaperFunding 启动时先结算一次（补算停机期间的结算点），之后每分钟检查一次。
// 失败只记日志、下一分钟重试：取不到资金费率不影响模拟账户就绪和撮合。
func (s *ExchangeSession) runPaperFunding(ctx context.Context) {
	settler, ok := s.Adapter.(paperFundingSettler)
	if !ok {
		return
	}
	ticker := time.NewTicker(paper.FundingCheckInterval)
	defer ticker.Stop()
	for {
		count, err := settler.SettleFunding(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			log.WarnContextf(ctx, "模拟盘资金费结算失败，下一分钟重试：account=%s err=%v", s.Account.TradingAccountID, err)
		case count > 0:
			log.InfoContextf(ctx, "模拟盘资金费已结算：account=%s count=%d", s.Account.TradingAccountID, count)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// runPolling 给没有私有流的场所用：短周期同步代替事件推送。
func (s *ExchangeSession) runPolling(ctx context.Context) error {
	instruments, err := s.marketData().LoadInstruments(ctx)
	if err != nil {
		return s.disconnect(ctx, err)
	}
	if err := s.persistInstruments(ctx, instruments); err != nil {
		return s.disconnect(ctx, err)
	}
	if _, err := s.Sync.SyncAccountFull(ctx, s.Account.TradingAccountID); err != nil {
		return s.disconnect(ctx, err)
	}
	s.ready.Store(true)
	if err := s.Sync.SetReady(ctx, s.Account.TradingAccountID, true, nil); err != nil {
		return s.disconnect(ctx, err)
	}
	if s.OnReady != nil {
		s.OnReady(s.Account.TradingAccountID)
	}
	ticker := time.NewTicker(pollingSyncInterval)
	defer ticker.Stop()
	instrumentTicker := time.NewTicker(instrumentRefreshInterval)
	defer instrumentTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return s.disconnect(context.Background(), ctx.Err())
		case <-instrumentTicker.C:
			s.refreshInstruments(ctx)
		case <-ticker.C:
			// 失败时账户已记录错误并保持未就绪，下一轮重试。
			_, _ = s.Sync.SyncAccount(ctx, s.Account.TradingAccountID)
		}
	}
}

// refreshInstruments 让新上市、下架的标的不必重启会话就能生效。失败时沿用旧数据，下一轮重试。
func (s *ExchangeSession) refreshInstruments(ctx context.Context) {
	instruments, err := s.marketData().LoadInstruments(ctx)
	if err == nil {
		err = s.persistInstruments(ctx, instruments)
	}
	if err != nil {
		log.ErrorContextf(ctx, "刷新标的失败：account=%s err=%v", s.Account.TradingAccountID, err)
	}
}

// EnsureSymbolConfig 把全仓和杠杆设置到交易所，结果记在会话内存里，同一标的只设置一次。
func (s *ExchangeSession) EnsureSymbolConfig(ctx context.Context, symbol exchange.SymbolKey, leverage shared.Decimal) error {
	configurer, ok := s.Adapter.(execution.SymbolConfigurer)
	if !ok || exchange.ExecutionMode(s.Account.ExecutionMode) == exchange.ExecutionModePaper {
		return nil
	}
	lock := s.symbolLock(symbol)
	lock.Lock()
	defer lock.Unlock()
	s.symbolMu.Lock()
	configured := s.configuredSymbols[symbol] == leverage.String()
	s.symbolMu.Unlock()
	if configured {
		return nil
	}
	if err := configurer.SetMarginMode(ctx, symbol, exchange.MarginModeCross); err != nil {
		return err
	}
	if err := configurer.SetLeverage(ctx, symbol, leverage); err != nil {
		return err
	}
	s.symbolMu.Lock()
	if s.configuredSymbols == nil {
		s.configuredSymbols = make(map[exchange.SymbolKey]string)
	}
	s.configuredSymbols[symbol] = leverage.String()
	s.symbolMu.Unlock()
	return nil
}

func (s *ExchangeSession) symbolLock(symbol exchange.SymbolKey) *sync.Mutex {
	s.symbolMu.Lock()
	defer s.symbolMu.Unlock()
	if s.symbolLocks == nil {
		s.symbolLocks = make(map[exchange.SymbolKey]*sync.Mutex)
	}
	lock, ok := s.symbolLocks[symbol]
	if !ok {
		lock = &sync.Mutex{}
		s.symbolLocks[symbol] = lock
	}
	return lock
}

// ForgetSymbolConfig 让标的下次开仓前重新设置。
func (s *ExchangeSession) ForgetSymbolConfig(symbol exchange.SymbolKey) {
	s.symbolMu.Lock()
	defer s.symbolMu.Unlock()
	delete(s.configuredSymbols, symbol)
}

// ForgetAllSymbolConfigs 让全部标的下次开仓前重新设置：账户默认杠杆变更时用。
func (s *ExchangeSession) ForgetAllSymbolConfigs() {
	s.symbolMu.Lock()
	defer s.symbolMu.Unlock()
	s.configuredSymbols = nil
}

func (s *ExchangeSession) marketData() execution.MarketDataSource {
	if s.MarketData != nil {
		return s.MarketData
	}
	return s.Adapter
}

func (s *ExchangeSession) paperAccountGeneration() uint64 {
	if s.PaperAccountState == nil {
		return 0
	}
	return s.PaperAccountState(s.Account.TradingAccountID).Generation
}

func (s *ExchangeSession) recoverPaperAccount(generation uint64) {
	if s.PaperAccountRecovered != nil {
		s.PaperAccountRecovered(s.Account.TradingAccountID, generation)
	}
}

func (s *ExchangeSession) recoverPaperAccountAfterSync(generation uint64, orders []exchange.Order) {
	// A balance-only sync does not prove that a failing order quote recovered.
	// Once that order is gone, sync can retire the otherwise orphaned fault.
	if s.PaperAccountState != nil && s.PaperAccountState(s.Account.TradingAccountID).Stage == "decision" && len(orders) != 0 {
		return
	}
	s.recoverPaperAccount(generation)
}

func privateStreamError(
	disconnected <-chan struct{},
	streamDone <-chan error,
) (error, bool) {
	select {
	case <-disconnected:
		err := <-streamDone
		if err == nil {
			err = errPrivateDisconnected
		}
		return err, true
	default:
		return nil, false
	}
}

func (s *ExchangeSession) disconnect(ctx context.Context, cause error) error {
	s.ready.Store(false)
	if ctx == nil {
		ctx = context.Background()
	}
	setCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := s.Sync.SetReady(setCtx, s.Account.TradingAccountID, false, cause); err != nil {
		if cause == nil {
			return err
		}
		return errors.Join(cause, err)
	}
	return cause
}

func (s *ExchangeSession) persistInstruments(
	ctx context.Context,
	instruments []exchange.Instrument,
) error {
	return s.Sync.Store.Transaction(ctx, func(tx *store.Tx) error {
		environment := s.Account.Environment
		if s.Account.ExecutionMode == "PAPER" || environment == "" {
			environment = "PRODUCTION"
		}
		for _, instrument := range instruments {
			if instrument.Exchange != exchange.Exchange(s.Account.Exchange) ||
				!s.Account.SupportsMarket(string(instrument.MarketType)) {
				return fmt.Errorf("trade runtime: conflicting instrument identity")
			}
			if err := tx.UpsertInstrument(store.InstrumentRecord{
				Exchange:       string(instrument.Exchange),
				Environment:    environment,
				MarketType:     string(instrument.MarketType),
				ExchangeSymbol: instrument.ExchangeSymbol, InstrumentID: instrument.InstrumentID,
				BaseAsset: instrument.BaseAsset, QuoteAsset: instrument.QuoteAsset,
				SettlementAsset: instrument.SettlementAsset, Linear: instrument.Linear,
				ContractValue:        instrument.ContractValue.String(),
				ContractValueAsset:   instrument.ContractValueAsset,
				ExchangeQuantityStep: instrument.ExchangeQuantityStep.String(),
				MinExchangeQuantity:  instrument.MinExchangeQuantity.String(),
				PriceTick:            instrument.PriceTick.String(),
				MinNotional:          instrument.MinNotional.String(), Status: instrument.Status,
				MaxBuyPriceRatio:  instrument.MaxBuyPriceRatio.String(),
				MinSellPriceRatio: instrument.MinSellPriceRatio.String(),
				ExchangeUpdatedAt: instrument.ExchangeUpdatedAt.UnixMilli(),
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *ExchangeSession) applyEvent(ctx context.Context, event privateEvent) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	switch event.kind {
	case privateOrder:
		return s.Sync.ApplyOrder(ctx, s.Account.TradingAccountID, event.order)
	case privateFill:
		applied, err := s.Sync.ApplyFill(ctx, s.Account.TradingAccountID, event.fill)
		if err == nil && applied {
			// 成交已使准入关闭，请求轻量同步确认余额后恢复
			s.RequestSync()
		}
		return err
	case privatePosition:
		err := s.Sync.ApplyPosition(ctx, s.Account.TradingAccountID, event.position)
		if err == nil {
			s.RequestSync()
		}
		return err
	case privateAccount:
		err := s.Sync.ApplyAccountSnapshot(
			ctx,
			s.Account.TradingAccountID,
			event.account,
		)
		if err == nil {
			s.RequestSync()
		}
		return err
	default:
		return fmt.Errorf("trade runtime: unknown private event")
	}
}

type privateEventKind uint8

const (
	privateOrder privateEventKind = iota + 1
	privateFill
	privatePosition
	privateAccount
)

type privateEvent struct {
	kind     privateEventKind
	order    exchange.Order
	fill     exchange.Fill
	position exchange.Position
	account  exchange.AccountSnapshot
}

type sessionHandler struct {
	mu        sync.Mutex
	buffering bool
	pending   []privateEvent
	ready     chan struct{}
	readyOnce sync.Once
	apply     func(context.Context, privateEvent) error
	setReady  func() error
	gateMu    sync.Mutex
	gateCond  *sync.Cond
	closing   bool
	activated bool
	inflight  int
	// dirty：激活前处理过会使持久化就绪置为“等待完整同步”的事件（成交、持仓）。此时不能再把就绪写回 true，
	// 必须等排队的完整同步确认。
	dirty bool
}

func newSessionHandler(
	apply func(context.Context, privateEvent) error,
) *sessionHandler {
	handler := &sessionHandler{
		buffering: true,
		ready:     make(chan struct{}),
		apply:     apply,
	}
	handler.gateCond = sync.NewCond(&handler.gateMu)
	return handler
}

func (h *sessionHandler) OnSubscribed() {
	h.readyOnce.Do(func() { close(h.ready) })
}

func (h *sessionHandler) OnOrder(ctx context.Context, value exchange.Order) error {
	return h.handle(ctx, privateEvent{kind: privateOrder, order: value})
}

func (h *sessionHandler) OnFill(ctx context.Context, value exchange.Fill) error {
	return h.handle(ctx, privateEvent{kind: privateFill, fill: value})
}

func (h *sessionHandler) OnPosition(
	ctx context.Context,
	value exchange.Position,
) error {
	return h.handle(ctx, privateEvent{kind: privatePosition, position: value})
}

func (h *sessionHandler) OnAccountSnapshot(
	ctx context.Context,
	value exchange.AccountSnapshot,
) error {
	return h.handle(ctx, privateEvent{kind: privateAccount, account: value})
}

func (h *sessionHandler) handle(ctx context.Context, event privateEvent) error {
	h.gateMu.Lock()
	for h.closing && !h.activated {
		h.gateCond.Wait()
	}
	if !h.activated {
		h.inflight++
	}
	activated := h.activated
	h.gateMu.Unlock()
	if activated {
		return h.apply(ctx, event)
	}
	defer func() {
		h.gateMu.Lock()
		h.inflight--
		h.gateCond.Broadcast()
		h.gateMu.Unlock()
	}()
	h.mu.Lock()
	if h.buffering {
		h.pending = append(h.pending, event)
		h.mu.Unlock()
		return nil
	}
	h.mu.Unlock()
	return h.apply(ctx, event)
}

func (h *sessionHandler) activate(
	ctx context.Context,
	setReady func() error,
) error {
	for {
		h.mu.Lock()
		if len(h.pending) == 0 {
			// Persist readiness only after the final gate has drained events
			// admitted during this initial activation pass.
			h.setReady = setReady
			h.mu.Unlock()
			return nil
		}
		pending := append([]privateEvent(nil), h.pending...)
		h.pending = h.pending[:0]
		h.mu.Unlock()
		h.markDirty(pending)
		for _, event := range pending {
			if err := h.apply(ctx, event); err != nil {
				h.gateMu.Lock()
				h.activated = true
				h.closing = false
				h.gateCond.Broadcast()
				h.gateMu.Unlock()
				return err
			}
		}
	}
}

func (h *sessionHandler) markDirty(events []privateEvent) {
	for _, event := range events {
		if event.kind == privateFill || event.kind == privatePosition || event.kind == privateAccount {
			h.mu.Lock()
			h.dirty = true
			h.mu.Unlock()
			return
		}
	}
}

// finishActivation closes admission, drains pre-ready events, then commits
// readiness before allowing synchronous event processing.
func (h *sessionHandler) finishActivation(ctx context.Context) error {
	h.gateMu.Lock()
	h.closing = true
	for h.inflight > 0 {
		h.gateCond.Wait()
	}
	h.gateMu.Unlock()
	defer func() {
		h.gateMu.Lock()
		h.activated = true
		h.closing = false
		h.gateCond.Broadcast()
		h.gateMu.Unlock()
	}()
	for {
		h.mu.Lock()
		if len(h.pending) == 0 {
			setReady := h.setReady
			dirty := h.dirty
			h.buffering = false
			h.mu.Unlock()
			if setReady != nil && !dirty {
				return setReady()
			}
			return nil
		}
		pending := append([]privateEvent(nil), h.pending...)
		h.pending = h.pending[:0]
		h.mu.Unlock()
		h.markDirty(pending)
		for _, event := range pending {
			if err := h.apply(ctx, event); err != nil {
				return err
			}
		}
	}
}

func cloneFillCursors(current store.FillCursors) store.FillCursors {
	result := make(store.FillCursors, len(current))
	for symbol, cursor := range current {
		result[symbol] = cursor
	}
	return result
}

// 编译期确认会话实现了 Manager 按类型断言使用的方法集。
var (
	_ execution.AccountEventHandler   = (*sessionHandler)(nil)
	_ ManagedSession                  = (*ExchangeSession)(nil)
	_ symbolConfigSession             = (*ExchangeSession)(nil)
	_ interface{ RequestSync() bool } = (*ExchangeSession)(nil)
	_ interface {
		ExecutionAdapter() execution.ExecutionAdapter
	} = (*ExchangeSession)(nil)
)
