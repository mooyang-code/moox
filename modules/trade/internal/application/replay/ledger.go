// Package replay 是逐期回放在 Trade 里的纸面账本（设计 9.2）：每次回放一个会话，初始资金与费用配置固定；
// 回放服务每期发来回放时钟、回放报价和策略决策，账本按报价撮合、记录订单与权益。
// 回放账本不接触实盘账户、订单、预留与执行器：回放绝不产生实际下单，也不影响实盘策略实例。
package replay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
)

const (
	// KindUpdateTarget 是带目标权重的决策；KindSkip 不改动目标，本期没有订单。
	KindUpdateTarget = "UPDATE_TARGET"
	KindSkip         = "SKIP"

	quantityScale = 8
	moneyScale    = 8

	defaultInitialCash = "100000"
	defaultFeeRate     = "0.001"
)

// 回放账本的错误：调用方按它们决定重试还是终止。
var (
	ErrInvalidRequest = errors.New("replay: invalid request")
	ErrMissingQuote   = errors.New("replay: missing quote")
	ErrConflict       = errors.New("replay: conflict")
)

// Service 是回放账本服务。
type Service struct {
	Store *store.Store
	Now   func() time.Time

	locks sync.Map
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) lock(id string) func() {
	value, _ := s.locks.LoadOrStore(id, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

// CreateRequest 是建立会话的参数。
type CreateRequest struct {
	ReplayID    string `json:"replay_id"`
	InitialCash string `json:"initial_cash,omitempty"`
	FeeRate     string `json:"fee_rate,omitempty"`
	SlippageBPS string `json:"slippage_bps,omitempty"`
	QuoteAsset  string `json:"quote_asset,omitempty"`
}

// Session 是会话的当前状态。
type Session struct {
	ReplayID    string            `json:"replay_id"`
	Status      string            `json:"status"`
	QuoteAsset  string            `json:"quote_asset"`
	InitialCash string            `json:"initial_cash"`
	FeeRate     string            `json:"fee_rate"`
	SlippageBPS string            `json:"slippage_bps"`
	Cash        string            `json:"cash"`
	Positions   map[string]string `json:"positions"`
	LastPeriod  int64             `json:"last_period"`
	Created     bool              `json:"created,omitempty"`
}

// CreateSession 创建会话；同一个 replay_id 重复创建返回已有的会话（回放服务重启后续跑）。
func (s *Service) CreateSession(ctx context.Context, req CreateRequest) (Session, error) {
	id := strings.TrimSpace(req.ReplayID)
	if id == "" {
		return Session{}, fmt.Errorf("%w: replay_id is required", ErrInvalidRequest)
	}
	cash, fee, slippage := defaultString(req.InitialCash, defaultInitialCash), defaultString(req.FeeRate, defaultFeeRate), defaultString(req.SlippageBPS, "0")
	cashD, err := shared.ParseDecimal(cash)
	if err != nil || cashD.Cmp(shared.Zero()) <= 0 {
		return Session{}, fmt.Errorf("%w: initial_cash must be a positive decimal", ErrInvalidRequest)
	}
	feeD, err := shared.ParseDecimal(fee)
	if err != nil || feeD.IsNegative() || feeD.Cmp(shared.MustDecimal("0.1")) >= 0 {
		return Session{}, fmt.Errorf("%w: fee_rate must be in [0, 0.1)", ErrInvalidRequest)
	}
	slipD, err := shared.ParseDecimal(slippage)
	if err != nil || slipD.IsNegative() || slipD.Cmp(shared.MustDecimal("10000")) >= 0 {
		return Session{}, fmt.Errorf("%w: slippage_bps must be in [0, 10000)", ErrInvalidRequest)
	}
	record, created, err := s.Store.CreateReplaySession(ctx, store.ReplaySessionRecord{
		ReplayID: id, QuoteAsset: defaultString(req.QuoteAsset, "USDT"), InitialCash: cashD.String(), FeeRate: feeD.String(),
		SlippageBPS: slipD.String(), Cash: cashD.String(), CreatedAt: s.now().UnixMilli(),
	})
	if err != nil {
		return Session{}, err
	}
	session, err := sessionOf(record)
	session.Created = created
	return session, err
}

// GetSession 返回会话的当前状态；不存在时返回 gorm.ErrRecordNotFound。
func (s *Service) GetSession(ctx context.Context, replayID string) (Session, error) {
	record, err := s.Store.GetReplaySession(ctx, replayID)
	if err != nil {
		return Session{}, err
	}
	return sessionOf(record)
}

func sessionOf(record store.ReplaySessionRecord) (Session, error) {
	positions := map[string]string{}
	if err := json.Unmarshal([]byte(record.PositionsJSON), &positions); err != nil {
		return Session{}, fmt.Errorf("replay positions are corrupt: %w", err)
	}
	return Session{
		ReplayID: record.ReplayID, Status: record.Status, QuoteAsset: record.QuoteAsset, InitialCash: record.InitialCash, FeeRate: record.FeeRate,
		SlippageBPS: record.SlippageBPS, Cash: record.Cash, Positions: positions, LastPeriod: record.LastPeriod,
	}, nil
}

// Quote 是一个标的本期的回放报价：Mark 是估值价（本期收盘价），Exec 是成交价（回放默认取下一根开盘价）。
type Quote struct {
	Mark string `json:"mark,omitempty"`
	Exec string `json:"exec,omitempty"`
}

// Target 是策略决策里一个标的的目标权重（相对总权益）；Hold 为真表示保持持仓数量不动。
type Target struct {
	InstrumentID string `json:"instrument_id"`
	TargetWeight string `json:"target_weight"`
	Hold         bool   `json:"hold,omitempty"`
}

// Decision 是本期决策：UPDATE_TARGET 带完整的目标集合（不在其中的持仓目标为 0，与实盘把空目标解释为清仓一致），SKIP 不改动目标。
type Decision struct {
	Kind    string   `json:"kind"`
	Targets []Target `json:"targets,omitempty"`
}

// AdvanceRequest 是 ReplayAdvance：每期必达，同一期重复发送返回同一条记录。
type AdvanceRequest struct {
	ReplayID string `json:"replay_id"`
	// Period 是回放时钟，即决策时刻（K 线起点 + 周期）的毫秒时间戳；必须逐期向前。
	Period   int64            `json:"period"`
	Decision Decision         `json:"decision"`
	Quotes   map[string]Quote `json:"quotes"`
}

// Order 是本期的一笔模拟订单。
type Order struct {
	OrderID           string `json:"order_id"`
	InstrumentID      string `json:"instrument_id"`
	Side              string `json:"side"`
	RequestedQuantity string `json:"requested_quantity"`
	FilledQuantity    string `json:"filled_quantity"`
	Price             string `json:"price"`
	Fee               string `json:"fee"`
	Status            string `json:"status"`
	Reason            string `json:"reason,omitempty"`
}

// Holding 是期末的一个持仓。
type Holding struct {
	InstrumentID string `json:"instrument_id"`
	Quantity     string `json:"quantity"`
	Mark         string `json:"mark"`
	Value        string `json:"value"`
}

// Result 是一期推进的确认。
type Result struct {
	ReplayID     string    `json:"replay_id"`
	Period       int64     `json:"period"`
	Kind         string    `json:"kind"`
	EquityBefore string    `json:"equity_before"`
	Equity       string    `json:"equity"`
	Cash         string    `json:"cash"`
	Fees         string    `json:"fees"`
	Turnover     string    `json:"turnover"`
	Holdings     []Holding `json:"holdings"`
	Orders       []Order   `json:"orders"`
	// Replayed 为真表示这一期早已推进过，返回的是当时的记录。
	Replayed bool `json:"replayed,omitempty"`
}

func requestHash(req AdvanceRequest) (string, error) {
	// 报价按键排序后哈希：同一期的重发必须与第一次完全一致。
	raw, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Advance 推进一期：按回放报价估值、按决策撮合、记录订单与权益。缺价时报错且不记录任何内容，不回退实时报价。
func (s *Service) Advance(ctx context.Context, req AdvanceRequest) (Result, error) {
	if strings.TrimSpace(req.ReplayID) == "" || req.Period <= 0 {
		return Result{}, fmt.Errorf("%w: replay_id and period are required", ErrInvalidRequest)
	}
	if req.Decision.Kind != KindUpdateTarget && req.Decision.Kind != KindSkip {
		return Result{}, fmt.Errorf("%w: decision kind must be %s or %s", ErrInvalidRequest, KindUpdateTarget, KindSkip)
	}
	hash, err := requestHash(req)
	if err != nil {
		return Result{}, err
	}
	unlock := s.lock(req.ReplayID)
	defer unlock()

	if recorded, err := s.Store.GetReplayPeriod(ctx, req.ReplayID, req.Period); err == nil {
		if recorded.RequestHash != hash {
			return Result{}, fmt.Errorf("%w: period %d was already advanced with a different request", ErrConflict, req.Period)
		}
		var result Result
		if err := json.Unmarshal([]byte(recorded.ResultJSON), &result); err != nil {
			return Result{}, fmt.Errorf("replay period record is corrupt: %w", err)
		}
		result.Replayed = true
		return result, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return Result{}, err
	}
	record, err := s.Store.GetReplaySession(ctx, req.ReplayID)
	if err != nil {
		return Result{}, err
	}
	if record.Status != "open" {
		return Result{}, fmt.Errorf("%w: replay session %s is closed", ErrConflict, req.ReplayID)
	}
	if req.Period <= record.LastPeriod {
		return Result{}, fmt.Errorf("%w: period %d is not after the last advanced period %d", ErrConflict, req.Period, record.LastPeriod)
	}
	session, err := sessionOf(record)
	if err != nil {
		return Result{}, err
	}
	state, err := newState(session)
	if err != nil {
		return Result{}, err
	}
	result, orders, err := state.settle(req)
	if err != nil {
		return Result{}, err
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return Result{}, err
	}
	positionsJSON, err := state.positionsJSON()
	if err != nil {
		return Result{}, err
	}
	write := store.ReplayPeriodWrite{Period: store.ReplayPeriodRecord{
		ReplayID: req.ReplayID, Period: req.Period, Kind: req.Decision.Kind, RequestHash: hash, EquityBefore: result.EquityBefore,
		Equity: result.Equity, Cash: result.Cash, PositionsJSON: positionsJSON, Fees: result.Fees, Turnover: result.Turnover,
		ResultJSON: string(resultJSON), RecordedAt: s.now().UnixMilli(),
	}}
	for _, order := range orders {
		write.Orders = append(write.Orders, store.ReplayOrderRecord{
			ReplayID: req.ReplayID, OrderID: order.OrderID, Period: req.Period, InstrumentID: order.InstrumentID, Side: order.Side,
			RequestedQuantity: order.RequestedQuantity, FilledQuantity: order.FilledQuantity, Price: order.Price, Fee: order.Fee,
			Status: order.Status, Reason: order.Reason,
		})
	}
	if err := s.Store.WriteReplayPeriod(ctx, write); err != nil {
		if errors.Is(err, store.ErrReplayOutOfOrder) {
			return Result{}, fmt.Errorf("%w: %v", ErrConflict, err)
		}
		return Result{}, err
	}
	return result, nil
}

// Periods 返回 after（决策时刻毫秒）之后的周期结果，供回放服务导出权益曲线。
func (s *Service) Periods(ctx context.Context, replayID string, after int64, limit int) ([]Result, error) {
	records, err := s.Store.ListReplayPeriods(ctx, replayID, after, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Result, 0, len(records))
	for _, record := range records {
		var result Result
		if err := json.Unmarshal([]byte(record.ResultJSON), &result); err != nil {
			return nil, fmt.Errorf("replay period record is corrupt: %w", err)
		}
		out = append(out, result)
	}
	return out, nil
}

// Close 结束会话，保留结果。
func (s *Service) Close(ctx context.Context, replayID string) error {
	unlock := s.lock(replayID)
	defer unlock()
	return s.Store.CloseReplaySession(ctx, replayID, s.now().UnixMilli())
}

// Delete 清理一次回放的全部结果。
func (s *Service) Delete(ctx context.Context, replayID string) (bool, error) {
	unlock := s.lock(replayID)
	defer unlock()
	return s.Store.DeleteReplaySession(ctx, replayID)
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

// ledgerState 是撮合用的内存状态：现金与持仓（精确小数）。
type ledgerState struct {
	cash      shared.Decimal
	positions map[string]shared.Decimal
	feeRate   shared.Decimal
	slippage  shared.Decimal // 已换算成比例（bps / 10000）
}

func newState(session Session) (*ledgerState, error) {
	cash, err := shared.ParseDecimal(session.Cash)
	if err != nil {
		return nil, fmt.Errorf("replay cash is corrupt: %w", err)
	}
	fee, err := shared.ParseDecimal(session.FeeRate)
	if err != nil {
		return nil, err
	}
	slip, err := shared.ParseDecimal(session.SlippageBPS)
	if err != nil {
		return nil, err
	}
	state := &ledgerState{cash: cash, positions: map[string]shared.Decimal{}, feeRate: fee, slippage: slip.Div(shared.MustDecimal("10000"))}
	for instrument, raw := range session.Positions {
		quantity, err := shared.ParseDecimal(raw)
		if err != nil {
			return nil, fmt.Errorf("replay position %s is corrupt: %w", instrument, err)
		}
		state.positions[instrument] = quantity
	}
	return state, nil
}

func (l *ledgerState) positionsJSON() (string, error) {
	out := make(map[string]string, len(l.positions))
	for instrument, quantity := range l.positions {
		if !quantity.IsZero() {
			out[instrument] = quantity.String()
		}
	}
	raw, err := json.Marshal(out)
	return string(raw), err
}

func parseQuote(value string) (shared.Decimal, bool) {
	if strings.TrimSpace(value) == "" {
		return shared.Decimal{}, false
	}
	d, err := shared.ParseDecimal(value)
	if err != nil || d.Cmp(shared.Zero()) <= 0 {
		return shared.Decimal{}, false
	}
	return d, true
}

// equity 按估值价计算权益；持有的标的没有估值价时报缺价。
func (l *ledgerState) equity(quotes map[string]Quote) (shared.Decimal, error) {
	total := l.cash
	for instrument, quantity := range l.positions {
		if quantity.IsZero() {
			continue
		}
		mark, ok := parseQuote(quotes[instrument].Mark)
		if !ok {
			return shared.Decimal{}, fmt.Errorf("%w: no mark price for held instrument %s", ErrMissingQuote, instrument)
		}
		total = total.Add(quantity.Mul(mark))
	}
	return total, nil
}

// settle 在内存状态上撮合一期，返回结果与订单；出错时状态不应被使用（调用方不持久化）。
func (l *ledgerState) settle(req AdvanceRequest) (Result, []Order, error) {
	equityBefore, err := l.equity(req.Quotes)
	if err != nil {
		return Result{}, nil, err
	}
	var orders []Order
	fees, turnover := shared.Zero(), shared.Zero()
	if req.Decision.Kind == KindUpdateTarget {
		orders, fees, turnover, err = l.rebalance(req, equityBefore)
		if err != nil {
			return Result{}, nil, err
		}
	}
	// 现金按 12 位小数截断后持久化：多期的费用与滑点乘积不会让小数位数无限增长。
	l.cash = l.cash.RoundDown(12)
	equityAfter, err := l.equity(req.Quotes)
	if err != nil {
		return Result{}, nil, err
	}
	result := Result{
		ReplayID: req.ReplayID, Period: req.Period, Kind: req.Decision.Kind, EquityBefore: equityBefore.RoundDown(moneyScale).String(),
		Equity: equityAfter.RoundDown(moneyScale).String(), Cash: l.cash.RoundDown(moneyScale).String(), Fees: fees.RoundDown(moneyScale).String(),
		Turnover: turnover.RoundDown(moneyScale).String(), Orders: orders, Holdings: []Holding{},
	}
	if result.Orders == nil {
		result.Orders = []Order{}
	}
	instruments := make([]string, 0, len(l.positions))
	for instrument, quantity := range l.positions {
		if !quantity.IsZero() {
			instruments = append(instruments, instrument)
		}
	}
	sort.Strings(instruments)
	for _, instrument := range instruments {
		mark, _ := parseQuote(req.Quotes[instrument].Mark)
		quantity := l.positions[instrument]
		result.Holdings = append(result.Holdings, Holding{InstrumentID: instrument, Quantity: quantity.String(), Mark: mark.String(), Value: quantity.Mul(mark).RoundDown(moneyScale).String()})
	}
	return result, orders, nil
}

// rebalance 把持仓调到目标权重：先卖后买；买单受可用现金限制（资金不足时部分成交或拒绝）。
func (l *ledgerState) rebalance(req AdvanceRequest, equity shared.Decimal) ([]Order, shared.Decimal, shared.Decimal, error) {
	targets := map[string]shared.Decimal{}
	hold := map[string]bool{}
	for _, target := range req.Decision.Targets {
		if target.InstrumentID == "" {
			return nil, shared.Decimal{}, shared.Decimal{}, fmt.Errorf("%w: target without instrument_id", ErrInvalidRequest)
		}
		if target.Hold {
			hold[target.InstrumentID] = true
			continue
		}
		weight, err := shared.ParseDecimal(target.TargetWeight)
		if err != nil {
			return nil, shared.Decimal{}, shared.Decimal{}, fmt.Errorf("%w: invalid target weight for %s", ErrInvalidRequest, target.InstrumentID)
		}
		if weight.IsNegative() {
			return nil, shared.Decimal{}, shared.Decimal{}, fmt.Errorf("%w: replay ledger is long-only spot, %s has a negative weight", ErrInvalidRequest, target.InstrumentID)
		}
		targets[target.InstrumentID] = weight
	}
	// 目标集合之外的持仓目标为 0（清仓），保持标的除外。
	instruments := map[string]struct{}{}
	for instrument := range targets {
		instruments[instrument] = struct{}{}
	}
	for instrument, quantity := range l.positions {
		if !quantity.IsZero() && !hold[instrument] {
			instruments[instrument] = struct{}{}
		}
	}
	ids := make([]string, 0, len(instruments))
	for instrument := range instruments {
		ids = append(ids, instrument)
	}
	sort.Strings(ids)
	type delta struct {
		id    string
		qty   shared.Decimal // 绝对数量
		price shared.Decimal // 基础成交价
	}
	var sells, buys []delta
	for _, id := range ids {
		exec, ok := parseQuote(req.Quotes[id].Exec)
		if !ok {
			return nil, shared.Decimal{}, shared.Decimal{}, fmt.Errorf("%w: no execution price for %s", ErrMissingQuote, id)
		}
		current := l.positions[id]
		target := targets[id].Mul(equity).Div(exec).RoundDown(quantityScale) // 不在 targets 里的权重为 0
		switch cmp := target.Cmp(current); {
		case cmp < 0:
			sells = append(sells, delta{id: id, qty: current.Sub(target), price: exec})
		case cmp > 0:
			buys = append(buys, delta{id: id, qty: target.Sub(current), price: exec})
		}
	}
	one := shared.MustDecimal("1")
	fees, turnover := shared.Zero(), shared.Zero()
	var orders []Order
	next := func() string { return fmt.Sprintf("%s-%d-%d", req.ReplayID, req.Period, len(orders)+1) }
	for _, sell := range sells {
		price := sell.price.Mul(one.Sub(l.slippage))
		proceeds := sell.qty.Mul(price)
		fee := proceeds.Mul(l.feeRate)
		l.cash = l.cash.Add(proceeds).Sub(fee)
		l.positions[sell.id] = l.positions[sell.id].Sub(sell.qty)
		fees, turnover = fees.Add(fee), turnover.Add(proceeds)
		orders = append(orders, Order{
			OrderID: next(), InstrumentID: sell.id, Side: "SELL", RequestedQuantity: sell.qty.String(), FilledQuantity: sell.qty.String(),
			Price: price.RoundDown(moneyScale).String(), Fee: fee.RoundDown(moneyScale).String(), Status: "FILLED",
		})
	}
	for _, buy := range buys {
		price := buy.price.Mul(one.Add(l.slippage))
		unit := price.Mul(one.Add(l.feeRate))
		fill, status, reason := buy.qty, "FILLED", ""
		if affordable := l.cash.Div(unit).RoundDown(quantityScale); affordable.Cmp(fill) < 0 {
			fill, status, reason = affordable, "PARTIAL", "资金不足，按可用现金部分成交"
			if fill.IsZero() {
				status, reason = "REJECTED", "资金不足"
			}
		}
		notional := fill.Mul(price)
		fee := notional.Mul(l.feeRate)
		if !fill.IsZero() {
			l.cash = l.cash.Sub(notional).Sub(fee)
			l.positions[buy.id] = l.positions[buy.id].Add(fill)
			fees, turnover = fees.Add(fee), turnover.Add(notional)
		}
		orders = append(orders, Order{
			OrderID: next(), InstrumentID: buy.id, Side: "BUY", RequestedQuantity: buy.qty.String(), FilledQuantity: fill.String(),
			Price: price.RoundDown(moneyScale).String(), Fee: fee.RoundDown(moneyScale).String(), Status: status, Reason: reason,
		})
	}
	return orders, fees, turnover, nil
}
