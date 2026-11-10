package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/application/exposure"
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	operatordomain "github.com/mooyang-code/moox/modules/trade/internal/domain/operator"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"trpc.group/trpc-go/tnet/log"
)

// 清仓下单：永续与现货各自的平仓、子单号、交易规则校验。

// errWaitingBalance：账户在等余额确认，本轮暂停下单，属可重试。
var errWaitingBalance = errors.New("账户等待余额确认，本轮暂停下单")

// flattenNewOrderReserve 是剩余预算不足时不再发新单的余量，留给结果落库和收尾同步。
const flattenNewOrderReserve = time.Second

// waitFlattenReady 在每笔清仓子单前等账户就绪：实盘每笔成交都会关闭准入，等轻量同步确认余额后才能下下一笔。
// 等待中的未就绪不算错误；预算不够就返回 false，结束本轮，由下一轮继续。
func (s *Service) waitFlattenReady(ctx context.Context, spaceID, tradingAccountID string) bool {
	for {
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < flattenNewOrderReserve {
			return false
		}
		account, err := s.Store.GetTradingAccount(ctx, spaceID, tradingAccountID)
		if err != nil {
			return false
		}
		if account.Ready {
			return true
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}

func (s *Service) flattenSwap(
	ctx context.Context,
	command FlattenCommand,
	scope flattenScope,
	account store.TradingAccountRecord,
	result *FlattenAccountResult,
	reasons map[string]flattenReason,
) {
	positions, err := s.Store.ListPositions(
		ctx, command.SpaceID, account.TradingAccountID, exchange.SymbolKey{},
	)
	if err != nil {
		result.addError(err)
		return
	}
	instruments, err := s.Store.ListInstrumentsForAccount(ctx, account.TradingAccountID)
	if err != nil {
		result.addError(err)
		return
	}
	index := newInstrumentIndex(instruments, account.SettlementAsset)
	settings := scope.settings
	for _, position := range positions {
		quantity, parseErr := shared.ParseDecimal(position.SignedQuantity)
		if parseErr != nil {
			result.addError(parseErr)
			continue
		}
		if quantity.IsZero() {
			continue
		}
		symbolKey := exchange.NewSymbolKey(position.MarketType, position.ExchangeSymbol)
		key := symbolKey.String()
		instrument, found := index.bySymbol[symbolKey]
		if !found {
			// 标的表里都没有它：收窄范围时无从判断是不是范围内的，跳过；整组清仓记终结原因。
			if !scope.narrow() {
				reasons[key] = flattenReason{text: "持仓没有对应的可交易标的", final: true}
			}
			continue
		}
		if !scope.includes(instrument.InstrumentID) {
			continue
		}
		// 套利底仓的永续对冲腿不参与清仓，与执行器一致：平掉它会留下没对冲的底仓现货。
		// 但只平这一个标的而它正是对冲腿（下架的恰好是对冲腿）时不能静默当作平完：记一条终结原因，
		// 动作以部分完成结束（剩余统计里报出终结原因）并告警，由人决定怎么处理。
		if settings.IsArbitrageBase(instrument.BaseAsset) {
			if scope.instrumentID != "" {
				log.Warnf("交易关注项：下架清仓的标的 %s 是套利底仓的永续对冲腿，不自动平仓，需要人工处理（账户 %s）",
					instrument.InstrumentID, account.TradingAccountID)
			}
			continue
		}
		// 减仓只处理登记过的持仓，并只减到目标数量；整组清仓的目标是零。
		target, inScope := scope.target(account.TradingAccountID, instrument.InstrumentID)
		if !inScope {
			continue
		}
		quantity = excess(quantity, target)
		if quantity.IsZero() {
			continue
		}
		// 范围内但已经不可交易（例如下架）：记终结原因，不空转。
		if !exposure.Tradable(instrument, account.SettlementAsset) {
			reasons[key] = flattenReason{text: "持仓没有对应的可交易标的", final: true}
			continue
		}
		// 永续平仓单 reduceOnly，不查最小名义金额，只有数量不符合交易所规则才平不掉。
		if reason := quantityRuleReason(quantity.Abs(), instrument); reason != "" {
			reasons[key] = flattenReason{text: reason, final: true}
			continue
		}
		side := exchange.SideSell
		if quantity.IsNegative() {
			side = exchange.SideBuy
		}
		if !s.waitFlattenReady(ctx, command.SpaceID, account.TradingAccountID) {
			result.addError(&orderapp.AccountExecutionError{TradingAccountID: account.TradingAccountID, Operation: "flatten_wait_ready", Err: errWaitingBalance})
			return
		}
		if err := s.placeOrContinueFlattenChild(
			ctx, command, account, instrument, side, quantity.Abs(), result,
		); err != nil {
			reasons[key] = flattenReason{text: err.Error(), final: isFlattenFinal(err)}
			if !result.notePacing(err) { // 间隔未到只是等一等，不是错误
				result.addError(err)
			}
		}
	}
}

func (s *Service) flattenSpot(
	ctx context.Context,
	command FlattenCommand,
	scope flattenScope,
	account store.TradingAccountRecord,
	result *FlattenAccountResult,
	reasons map[string]flattenReason,
) {
	instruments, err := s.Store.ListInstrumentsForAccount(ctx, account.TradingAccountID)
	if err != nil {
		result.addError(err)
		return
	}
	managed, err := s.Store.ManagedAssets(ctx, command.SpaceID, account.TradingAccountID)
	if err != nil {
		result.addError(err)
		return
	}
	index := newInstrumentIndex(instruments, account.SettlementAsset)
	for _, balance := range account.SpotBalances() {
		if _, ok := managed[balance.Asset]; !ok || balance.Asset == account.SettlementAsset {
			continue
		}
		key := "asset:" + balance.Asset
		total, err := decimalOrZero(balance.Total)
		if err != nil {
			result.addError(err)
			continue
		}
		if total.Cmp(shared.Zero()) <= 0 {
			continue
		}
		balanceTotal := total
		instrument, found := index.spot[balance.Asset]
		if !found {
			if !scope.narrow() {
				reasons[key] = flattenReason{text: "资产没有对应结算资产的标的", final: true}
			}
			continue
		}
		if !scope.includes(instrument.InstrumentID) {
			continue
		}
		// 减仓只卖出超出目标数量的部分，整组清仓的目标是零。
		target, inScope := scope.target(account.TradingAccountID, instrument.InstrumentID)
		if !inScope {
			continue
		}
		total = excess(total, target)
		if total.Cmp(shared.Zero()) <= 0 {
			continue
		}
		if !exposure.Tradable(instrument, account.SettlementAsset) {
			reasons[key] = flattenReason{text: "资产没有对应结算资产的标的", final: true}
			continue
		}
		quote, quoteErr := s.Prices.LatestPrice(
			ctx, account.TradingAccountID, exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol),
		)
		price := shared.Zero()
		if quoteErr == nil {
			price = quote.Price
			if result.quotes == nil {
				result.quotes = make(map[string]shared.Decimal)
			}
			result.quotes[balance.Asset] = price
		}
		// 零头按总量判断，不算剩余，由剩余统计列进 Dust。
		if exposure.SpotDust(total, price, instrument) {
			continue
		}
		if quoteErr != nil {
			reasons[key] = flattenReason{text: quoteErr.Error()}
			result.addError(&orderapp.AccountExecutionError{TradingAccountID: account.TradingAccountID, Operation: "flatten_quote", Err: quoteErr})
			continue
		}
		available, err := decimalOrZero(balance.Available)
		if err != nil {
			result.addError(err)
			continue
		}
		step, _, ruleErr := baseQuantityRules(instrument)
		if ruleErr != nil {
			reasons[key] = flattenReason{text: ruleErr.Error(), final: true}
			result.addError(flattenFinal{ruleErr})
			continue
		}
		var quantity shared.Decimal
		if exchange.RuleFor(instrument.MarketType).BoardLot {
			// 整手市场（A 股）：和目标执行器同一个卖出规则，卖出全部可卖数量时零股一次卖完；只因 T+1 卖不出的部分
			// （当日买入 = 总数 − 冻结 − 可卖）这一轮不再重试，原因写明。
			quantity, _ = targetapp.SellQuantity(instrument, total, available)
			if quantity.IsZero() {
				locked, _ := decimalOrZero(balance.Locked)
				bought := balanceTotal.Sub(locked).Sub(available)
				if later, _ := targetapp.SellQuantity(instrument, total, available.Add(bought)); exchange.RuleFor(instrument.MarketType).T1 &&
					bought.Cmp(shared.Zero()) > 0 && !later.IsZero() {
					reasons[key] = flattenReason{text: "当日买入，次日可卖", final: true}
					continue
				}
				reasons[key] = flattenReason{text: "可用余额不足，部分余额仍被挂单占用"}
				continue
			}
		} else {
			// 总量不是零头而可用余额卖不出去，说明还有余额被挂单占着，下一轮再试。
			// 减仓只卖超出目标数量的部分（此时 total 已经是超出部分）。
			if available.Cmp(total) > 0 {
				available = total
			}
			quantity = exposure.FloorToStep(available, step)
			if quantityRuleReason(quantity, instrument) != "" || exposure.SpotDust(quantity, price, instrument) {
				reasons[key] = flattenReason{text: "可用余额不足，部分余额仍被挂单占用"}
				continue
			}
		}
		if !s.waitFlattenReady(ctx, command.SpaceID, account.TradingAccountID) {
			result.addError(&orderapp.AccountExecutionError{TradingAccountID: account.TradingAccountID, Operation: "flatten_wait_ready", Err: errWaitingBalance})
			return
		}
		if err := s.placeOrContinueFlattenChildWithQuote(
			ctx, command, account, instrument,
			exchange.SideSell, quantity, quote, result,
		); err != nil {
			reasons[key] = flattenReason{text: err.Error(), final: isFlattenFinal(err)}
			if !result.notePacing(err) {
				result.addError(err)
			}
		}
	}
}

func (s *Service) placeOrContinueFlattenChild(
	ctx context.Context,
	command FlattenCommand,
	account store.TradingAccountRecord,
	instrument store.InstrumentRecord,
	side exchange.Side,
	quantity shared.Decimal,
	result *FlattenAccountResult,
) error {
	quote, err := s.Prices.LatestPrice(
		ctx, account.TradingAccountID, exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol),
	)
	if err != nil {
		return &orderapp.AccountExecutionError{TradingAccountID: account.TradingAccountID, Operation: "flatten_quote", Err: err}
	}
	return s.placeOrContinueFlattenChildWithQuote(
		ctx, command, account, instrument, side, quantity, quote, result,
	)
}

func (s *Service) placeOrContinueFlattenChildWithQuote(
	ctx context.Context,
	command FlattenCommand,
	account store.TradingAccountRecord,
	instrument store.InstrumentRecord,
	side exchange.Side,
	quantity shared.Decimal,
	quote Quote,
	result *FlattenAccountResult,
) error {
	// 下架清仓用伪市价单（限价 + IOC，滑点 3%）：每一轮按最新报价重新下，零成交的 IOC 单不算终结。
	delist := command.actionType() == operatordomain.ActionDelistFlatten
	// stockIOC：A 股没有市价单，清仓、减仓也发限价 IOC。
	stockIOC := exchange.RuleFor(instrument.MarketType).PriceLimits
	// 所有市场的子单号都带上第几次尝试：零成交的子单（已过期、已撤销、本地校验没过）结束后同一个动作号换号再下，
	// 不会撞上上一笔已结束的子单；交易所明确拒绝的才终结。
	// 下架清仓和 A 股数本动作在这个标的上已结束的全部子单；加密市场清仓、减仓只数零成交的：
	// 有成交的子单（含部分成交）结束后，持仓还没同步时按旧数量再下会命中原号而不下单，同步后数量变了自然换号。
	history, err := s.loadDelistHistory(ctx, command, account.TradingAccountID, exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol))
	if err != nil {
		return err
	}
	attempt := history.finished
	if !delist && !stockIOC {
		attempt = history.zeroFilled
	}
	clientOrderID := flattenClientOrderIDForSpec(
		command.ActionID,
		account.TradingAccountID,
		exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol),
		side,
		quantity,
		attempt,
	)
	existing, found, err := s.flattenChild(
		ctx, command.SpaceID, account.TradingAccountID,
		command.ActionID, exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol), clientOrderID,
	)
	if err != nil {
		return err
	}
	if found {
		result.ChildOrderIDs = appendUnique(
			result.ChildOrderIDs, existing.OrderID,
		)
		switch orderdomain.State(existing.State) {
		case orderdomain.Pending:
			if s.pastDeadline(command) {
				if _, err = s.Orders.DiscardPending(ctx, command.SpaceID, existing.OrderID); err != nil {
					return err
				}
				return flattenFinal{errDelistDeadlinePassed}
			}
			_, err = s.Orders.Submit(ctx, command.SpaceID, existing.OrderID)
			return err
		case orderdomain.Submitting, orderdomain.SubmitUnknown:
			_, err = s.Orders.ResolveUnknown(ctx, command.SpaceID, existing.OrderID)
			return err
		case orderdomain.Canceling, orderdomain.CancelUnknown:
			_, err = s.Orders.RecoverCancel(ctx, command.SpaceID, existing.OrderID)
			return err
		default:
			return nil
		}
	}
	// 每笔新单之前都看截止时间：一轮里前面的单耗掉时间，过了下架时间后面的就不再下。
	if s.pastDeadline(command) {
		return flattenFinal{errDelistDeadlinePassed}
	}
	if delist {
		// 要下新的一笔了：被拒绝、连续没成交封顶、离上一笔结束太近都先不下。
		if err := s.checkDelistAttempt(history); err != nil {
			return err
		}
	} else if stockIOC {
		if err := checkStockFlattenAttempt(history, side, quote); err != nil {
			return err
		}
	} else if err := checkFlattenAttempt(history); err != nil {
		return err
	}
	spec := orderdomain.OrderSpec{
		ClientOrderSpec: orderdomain.ClientOrderSpec{
			TradingAccountID: account.TradingAccountID,
			ClientOrderID:    clientOrderID,
			InstrumentID:     instrument.InstrumentID, Type: exchange.OrderTypeMarket,
			Side: side, Quantity: quantity,
		},
		ReferencePrice: quote.Price, ReferencePriceAt: quote.UpdatedAt,
		ReducePositionOnly: exchange.RuleFor(instrument.MarketType).ReduceOnly,
		Owner: orderdomain.OrderOwner{
			Type: orderdomain.OwnerOperator, OwnerID: command.ActionID,
			LogicalAccountID: command.LogicalAccountID,
		},
	}
	spec.PositionSide = exchange.RuleFor(instrument.MarketType).PositionSide
	if delist || exchange.RuleFor(instrument.MarketType).PriceLimits {
		// 3% 滑点可能超出交易所的价格偏离限制（币安 PERCENT_PRICE、A 股价格笼子），和目标执行器的伪市价单用同一个夹价函数，免得被拒。
		// A 股没有市价单：清仓同样发限价 IOC，再截到涨跌停价之间。
		limit := targetapp.ClampToPriceBand(delistLimitPrice(quote.Price, side, instrument.PriceTick), targetapp.CageBasis(quote, side), side, instrument)
		limit = targetapp.ClampToPriceLimits(limit, quote)
		spec.Type, spec.FillPolicy, spec.LimitPrice = exchange.OrderTypeLimit, exchange.FillPolicyIOC, &limit
	}
	placed, err := s.Orders.Place(ctx, command.SpaceID, spec)
	if err != nil {
		// 资金不足（预留不足时也返回它）和只减仓被拒可能只是持仓、余额还没同步，按可重试处理。
		if orderapp.IsPermanentValidationError(err) &&
			!errors.Is(err, orderapp.ErrInsufficientFunds) && !errors.Is(err, orderapp.ErrReduceOnly) {
			return flattenFinal{err}
		}
		return err
	}
	result.ChildOrderIDs = appendUnique(
		result.ChildOrderIDs, string(placed.ID),
	)
	_, err = s.Orders.Submit(ctx, command.SpaceID, string(placed.ID))
	// IOC 单发出后马上就结束了：下一笔最早时间直接记成现在加间隔，下一轮先睡再同步，不用再多跑一轮被间隔挡住的空轮。
	if err == nil && delist {
		if interval := s.delistAttemptInterval(); interval > 0 {
			if until := time.Now().Add(interval); until.After(result.pacingUntil) {
				result.pacingUntil = until
			}
		}
	}
	return err
}

func (s *Service) flattenChild(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	actionID string,
	key exchange.SymbolKey,
	clientOrderID string,
) (store.OrderRecord, bool, error) {
	records, err := s.Store.ListOrdersForLane(
		ctx, spaceID, tradingAccountID, key,
	)
	if err != nil {
		return store.OrderRecord{}, false, err
	}
	var active []store.OrderRecord
	var matching *store.OrderRecord
	for _, current := range records {
		if current.OwnerType == string(orderdomain.OwnerOperator) &&
			current.OwnerID == actionID {
			if !orderdomain.State(current.State).Terminal() {
				active = append(active, current)
			}
			if current.ClientOrderID == clientOrderID {
				value := current
				matching = &value
			}
		}
	}
	if len(active) > 1 {
		return store.OrderRecord{}, false,
			fmt.Errorf("multiple active flatten children exist for %s", key)
	}
	if len(active) == 1 {
		return active[0], true, nil
	}
	if matching != nil {
		return *matching, true, nil
	}
	return store.OrderRecord{}, false, nil
}

// flattenClientOrderIDForSpec 生成清仓子单的客户端订单号：固定 32 位（"mf" + 30 位十六进制），所有交易所都不超过
// （OKX 的 clOrdId 上限 32 位）。下架清仓的第几次尝试并进哈希输入，不能再往后面拼后缀把号拼长。
func flattenClientOrderIDForSpec(
	actionID string,
	accountID string,
	key exchange.SymbolKey,
	side exchange.Side,
	quantity shared.Decimal,
	attempt int,
) string {
	sum := sha256.Sum256([]byte(
		actionID + "\x00" + accountID + "\x00" + key.String() + "\x00" +
			string(side) + "\x00" + quantity.String() + "\x00" + strconv.Itoa(attempt),
	))
	return "mf" + hex.EncodeToString(sum[:15])
}

func quantityRuleReason(
	quantity shared.Decimal,
	instrument store.InstrumentRecord,
) string {
	step, minimum, err := baseQuantityRules(instrument)
	if err != nil {
		return err.Error()
	}
	if quantity.Cmp(shared.Zero()) <= 0 ||
		quantity.Cmp(minimum) < 0 ||
		!quantity.Div(step).IsInteger() {
		return "数量低于最小值或不符合步长"
	}
	return ""
}

func baseQuantityRules(
	instrument store.InstrumentRecord,
) (shared.Decimal, shared.Decimal, error) {
	step, err := shared.ParseDecimal(instrument.ExchangeQuantityStep)
	if err != nil || step.Cmp(shared.Zero()) <= 0 {
		return shared.Decimal{}, shared.Decimal{},
			errors.New("invalid exchange quantity step")
	}
	minimum, err := decimalOrZero(instrument.MinExchangeQuantity)
	if err != nil || minimum.IsNegative() {
		return shared.Decimal{}, shared.Decimal{},
			errors.New("invalid minimum exchange quantity")
	}
	if exchange.RuleFor(instrument.MarketType).ContractValue {
		contractValue, parseErr := shared.ParseDecimal(instrument.ContractValue)
		if parseErr != nil || contractValue.Cmp(shared.Zero()) <= 0 {
			return shared.Decimal{}, shared.Decimal{},
				errors.New("invalid SWAP contract value")
		}
		step = step.Mul(contractValue)
		minimum = minimum.Mul(contractValue)
	}
	return step, minimum, nil
}
