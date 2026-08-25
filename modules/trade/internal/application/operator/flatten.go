package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	operatordomain "github.com/mooyang-code/moox/modules/trade/internal/domain/operator"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

type FlattenCommand struct {
	SpaceID          string
	ActionID         string
	LogicalAccountID string
	Reason           string
}

type RemainingPosition struct {
	InstrumentID string `json:"instrument_id,omitempty"`
	Asset        string `json:"asset,omitempty"`
	Quantity     string `json:"quantity"`
	Reason       string `json:"reason"`
}

type FlattenAccountResult struct {
	TradingAccountID string              `json:"trading_account_id"`
	Status           string              `json:"status"`
	ChildOrderIDs    []string            `json:"child_order_ids,omitempty"`
	Remaining        []RemainingPosition `json:"remaining_positions,omitempty"`
	Error            string              `json:"error,omitempty"`
}

type FlattenResult struct {
	Action   store.OperatorActionRecord `json:"-"`
	Accounts []FlattenAccountResult     `json:"accounts"`
}

type flattenRequest struct {
	LogicalAccountID string `json:"logical_account_id"`
}

func (s *Service) FlattenLogicalAccount(
	ctx context.Context,
	command FlattenCommand,
) (FlattenResult, error) {
	if err := s.validate(); err != nil {
		return FlattenResult{}, err
	}
	requestJSON, err := flattenRequestJSON(command)
	if err != nil {
		return FlattenResult{}, err
	}
	unlock := s.Store.LockLogicalAccount(command.SpaceID, command.LogicalAccountID)
	defer unlock()

	expectedAction := store.OperatorActionRecord{
		SpaceID: command.SpaceID, ActionID: command.ActionID,
		LogicalAccountID: command.LogicalAccountID,
		ActionType:       "FLATTEN", Reason: strings.TrimSpace(command.Reason),
		RequestJSON: requestJSON, Status: "RUNNING",
	}
	existing, found, err := s.existingAction(ctx, expectedAction)
	if err != nil {
		return FlattenResult{}, err
	}
	if found && existing.Status == string(operatordomain.StatusCompleted) {
		return decodeFlattenResult(existing)
	}
	var action store.OperatorActionRecord
	err = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.SetLogicalAccountAutomation(
			command.SpaceID,
			command.LogicalAccountID,
			"PAUSED",
			strings.TrimSpace(command.Reason),
		); err != nil {
			return err
		}
		var ensureErr error
		action, _, ensureErr = tx.EnsureOperatorAction(expectedAction)
		if ensureErr != nil {
			return ensureErr
		}
		if action.Status != string(operatordomain.StatusRunning) {
			action.Status = string(operatordomain.StatusRunning)
			action.LastError = ""
			return tx.UpdateOperatorAction(action)
		}
		return nil
	})
	if err != nil {
		return FlattenResult{}, err
	}
	members, err := s.Store.ListLogicalAccountMembers(
		ctx, command.SpaceID, command.LogicalAccountID, true,
	)
	if err != nil {
		return FlattenResult{}, err
	}
	result := FlattenResult{
		Action:   action,
		Accounts: make([]FlattenAccountResult, 0, len(members)),
	}
	deadline := time.Now().Add(s.flattenTimeout())
	executable := 0
	for _, member := range members {
		accountResult, wasExecutable := s.flattenAccount(
			ctx, command, member.TradingAccountID, deadline,
		)
		if wasExecutable {
			executable++
		}
		result.Accounts = append(result.Accounts, accountResult)
		if err := s.persistFlattenProgress(ctx, &result, "RUNNING"); err != nil {
			return FlattenResult{}, err
		}
	}
	status := flattenStatus(result.Accounts, executable)
	if err := s.persistFlattenProgress(ctx, &result, status); err != nil {
		return FlattenResult{}, err
	}
	result.Action, err = s.Store.GetOperatorAction(
		ctx, command.SpaceID, command.ActionID,
	)
	return result, err
}

func flattenRequestJSON(command FlattenCommand) (string, error) {
	if blank(command.SpaceID) || blank(command.ActionID) ||
		blank(command.LogicalAccountID) || blank(command.Reason) {
		return "", ErrInvalidCommand
	}
	data, err := json.Marshal(flattenRequest{
		LogicalAccountID: command.LogicalAccountID,
	})
	return string(data), err
}

func (s *Service) flattenAccount(
	ctx context.Context,
	command FlattenCommand,
	tradingAccountID string,
	deadline time.Time,
) (FlattenAccountResult, bool) {
	result := FlattenAccountResult{
		TradingAccountID: tradingAccountID,
		Status:           "RUNNING",
	}
	recoveryCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	ctx = recoveryCtx
	if err := s.Syncer.SyncAccount(ctx, tradingAccountID); err != nil {
		result.Status = "FAILED"
		result.Error = err.Error()
		return result, false
	}
	if err := s.cancelAccountOrders(
		ctx,
		command.SpaceID,
		tradingAccountID,
		command.ActionID,
	); err != nil {
		result.Status = "PARTIAL"
		result.Error = err.Error()
		result.Remaining = s.remainingForAccount(
			ctx, command.SpaceID, tradingAccountID, nil,
		)
		return result, false
	}
	if err := s.Syncer.SyncAccount(ctx, tradingAccountID); err != nil {
		result.Status = "PARTIAL"
		result.Error = err.Error()
		result.Remaining = s.remainingForAccount(
			ctx, command.SpaceID, tradingAccountID, nil,
		)
		return result, false
	}
	if err := s.confirmAccountCancellations(
		ctx, command.SpaceID, tradingAccountID, command.ActionID,
	); err != nil {
		result.Status = "PARTIAL"
		result.Error = err.Error()
		result.Remaining = s.remainingForAccount(
			ctx, command.SpaceID, tradingAccountID, nil,
		)
		return result, false
	}
	account, err := s.Store.GetTradingAccount(
		ctx, command.SpaceID, tradingAccountID,
	)
	if err != nil {
		result.Status = "PARTIAL"
		result.Error = err.Error()
		return result, false
	}
	attempts := s.flattenAttempts()
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 && !time.Now().Before(deadline) {
			break
		}
		if attempt > 0 {
			if err := s.waitFlattenRetry(ctx); err != nil {
				result.Error = err.Error()
				break
			}
			if err := s.Syncer.SyncAccount(ctx, tradingAccountID); err != nil {
				result.Error = err.Error()
				break
			}
			if err := s.cancelAccountOrders(
				ctx, command.SpaceID, tradingAccountID, command.ActionID,
			); err != nil {
				result.Error = err.Error()
				break
			}
			if err := s.Syncer.SyncAccount(ctx, tradingAccountID); err != nil {
				result.Error = err.Error()
				break
			}
			if err := s.confirmAccountCancellations(
				ctx, command.SpaceID, tradingAccountID, command.ActionID,
			); err != nil {
				result.Error = err.Error()
				break
			}
			account, err = s.Store.GetTradingAccount(
				ctx, command.SpaceID, tradingAccountID,
			)
			if err != nil {
				result.Error = err.Error()
				break
			}
		}
		result.Error = ""
		reasons := make(map[string]string)
		switch exchange.MarketType(account.MarketType) {
		case exchange.MarketTypeSwap:
			s.flattenSwap(ctx, command, account, &result, reasons)
		case exchange.MarketTypeSpot:
			s.flattenSpot(ctx, command, account, &result, reasons)
		default:
			result.Error = "unsupported market type " + account.MarketType
		}
		if err := s.Syncer.SyncAccount(ctx, tradingAccountID); err != nil {
			result.Error = joinError(result.Error, err.Error())
		}
		result.Remaining = s.remainingForAccount(
			ctx, command.SpaceID, tradingAccountID, reasons,
		)
		if result.Error == "" && len(result.Remaining) == 0 {
			result.Status = "COMPLETED"
			return result, true
		}
	}
	result.Status = "PARTIAL"
	return result, true
}

func (s *Service) cancelAccountOrders(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	actionID string,
) error {
	records, err := s.Store.ListOrdersForAccount(
		ctx, spaceID, tradingAccountID, 1,
	)
	if err != nil {
		return err
	}
	var cancelErrors []error
	for _, current := range records {
		if orderdomain.State(current.State).Terminal() ||
			current.OwnerType == string(orderdomain.OwnerOperator) &&
				current.OwnerID == actionID {
			continue
		}
		if err := s.stopOrder(ctx, current); err != nil {
			cancelErrors = append(cancelErrors, fmt.Errorf(
				"cancel order %s: %w",
				current.OrderID,
				err,
			))
		}
	}
	return errors.Join(cancelErrors...)
}

func (s *Service) confirmAccountCancellations(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	actionID string,
) error {
	records, err := s.Store.ListOrdersForAccount(
		ctx, spaceID, tradingAccountID, 1,
	)
	if err != nil {
		return err
	}
	for _, current := range records {
		if orderdomain.State(current.State).Terminal() ||
			current.OwnerType == string(orderdomain.OwnerOperator) &&
				current.OwnerID == actionID {
			continue
		}
		return fmt.Errorf(
			"%w: order %s remains %s",
			ErrCancelUnconfirmed,
			current.OrderID,
			current.State,
		)
	}
	return nil
}

func (s *Service) flattenSwap(
	ctx context.Context,
	command FlattenCommand,
	account store.TradingAccountRecord,
	result *FlattenAccountResult,
	reasons map[string]string,
) {
	positions, err := s.Store.ListPositions(
		ctx, command.SpaceID, account.TradingAccountID, "",
	)
	if err != nil {
		result.Error = joinError(result.Error, err.Error())
		return
	}
	instruments, err := s.Store.ListInstrumentsForAccount(ctx, account.TradingAccountID)
	if err != nil {
		result.Error = joinError(result.Error, err.Error())
		return
	}
	bySymbol := make(map[string]store.InstrumentRecord, len(instruments))
	for _, instrument := range instruments {
		if tradableForSettlement(instrument, account.SettlementAsset) {
			bySymbol[instrument.Symbol] = instrument
		}
	}
	for _, position := range positions {
		quantity, parseErr := shared.ParseDecimal(position.SignedQuantity)
		if parseErr != nil {
			result.Error = joinError(result.Error, parseErr.Error())
			continue
		}
		if quantity.IsZero() {
			continue
		}
		instrument, found := bySymbol[position.Symbol]
		if !found {
			reasons[position.Symbol] = "position has no tradable instrument mapping"
			continue
		}
		if reason := quantityRuleReason(quantity.Abs(), instrument); reason != "" {
			reasons[position.Symbol] = reason
			continue
		}
		side := exchange.SideSell
		if quantity.IsNegative() {
			side = exchange.SideBuy
		}
		if err := s.placeOrContinueFlattenChild(
			ctx, command, account, instrument, side, quantity.Abs(), result,
		); err != nil {
			reasons[position.Symbol] = err.Error()
		}
	}
}

func (s *Service) flattenSpot(
	ctx context.Context,
	command FlattenCommand,
	account store.TradingAccountRecord,
	result *FlattenAccountResult,
	reasons map[string]string,
) {
	instruments, err := s.Store.ListInstrumentsForAccount(ctx, account.TradingAccountID)
	if err != nil {
		result.Error = joinError(result.Error, err.Error())
		return
	}
	byAsset := make(map[string]store.InstrumentRecord)
	for _, instrument := range instruments {
		if !tradableForSettlement(instrument, account.SettlementAsset) ||
			instrument.QuoteAsset != account.SettlementAsset ||
			instrument.BaseAsset == account.SettlementAsset {
			continue
		}
		if _, exists := byAsset[instrument.BaseAsset]; !exists {
			byAsset[instrument.BaseAsset] = instrument
		}
	}
	for _, balance := range account.Snapshot.Balances {
		if balance.Asset == account.SettlementAsset {
			continue
		}
		total, err := decimalOrZero(balance.Total)
		if err != nil {
			result.Error = joinError(result.Error, err.Error())
			continue
		}
		if total.Cmp(shared.Zero()) <= 0 {
			continue
		}
		instrument, found := byAsset[balance.Asset]
		if !found {
			reasons["asset:"+balance.Asset] = "asset has no settlement-market mapping"
			continue
		}
		available, err := decimalOrZero(balance.Available)
		if err != nil {
			result.Error = joinError(result.Error, err.Error())
			continue
		}
		step, _, ruleErr := baseQuantityRules(instrument)
		if ruleErr != nil {
			reasons["asset:"+balance.Asset] = ruleErr.Error()
			continue
		}
		quantity := floorToStep(available, step)
		if reason := quantityRuleReason(quantity, instrument); reason != "" {
			reasons["asset:"+balance.Asset] = reason
			continue
		}
		quote, quoteErr := s.Prices.LatestPrice(
			ctx, account.TradingAccountID, instrument.Symbol,
		)
		if quoteErr != nil {
			reasons["asset:"+balance.Asset] = quoteErr.Error()
			continue
		}
		minNotional, parseErr := decimalOrZero(instrument.MinNotional)
		if parseErr != nil {
			reasons["asset:"+balance.Asset] = parseErr.Error()
			continue
		}
		if minNotional.Cmp(shared.Zero()) > 0 &&
			quantity.Mul(quote.Price).Cmp(minNotional) < 0 {
			reasons["asset:"+balance.Asset] = "quantity is below minimum notional"
			continue
		}
		if err := s.placeOrContinueFlattenChildWithQuote(
			ctx, command, account, instrument,
			exchange.SideSell, quantity, quote, result,
		); err != nil {
			reasons["asset:"+balance.Asset] = err.Error()
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
		ctx, account.TradingAccountID, instrument.Symbol,
	)
	if err != nil {
		return err
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
	clientOrderID := flattenClientOrderIDForSpec(
		command.ActionID,
		account.TradingAccountID,
		instrument.Symbol,
		side,
		quantity,
	)
	existing, found, err := s.flattenChild(
		ctx, command.SpaceID, account.TradingAccountID,
		command.ActionID, instrument.Symbol, clientOrderID,
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
			_, err = s.Orders.Submit(ctx, command.SpaceID, existing.OrderID)
			return err
		case orderdomain.Submitting, orderdomain.SubmitUnknown:
			_, err = s.Orders.ResolveUnknown(ctx, command.SpaceID, existing.OrderID)
			return err
		case orderdomain.Canceling, orderdomain.CancelUnknown:
			_, err = s.Orders.RecoverCancel(ctx, command.SpaceID, existing.OrderID)
			return err
		case orderdomain.Rejected, orderdomain.Canceled,
			orderdomain.PartiallyCanceled, orderdomain.Expired:
			return fmt.Errorf("existing flatten child ended %s", existing.State)
		default:
			return nil
		}
	}
	spec := orderdomain.OrderSpec{
		ClientOrderSpec: orderdomain.ClientOrderSpec{
			TradingAccountID: account.TradingAccountID,
			ClientOrderID:    clientOrderID,
			InstrumentID:     instrument.Symbol, Type: exchange.OrderTypeMarket,
			Side: side, Quantity: quantity,
		},
		ReferencePrice: quote.Price, ReferencePriceAt: quote.UpdatedAt,
		ReducePositionOnly: account.MarketType == string(exchange.MarketTypeSwap),
		Owner: orderdomain.OrderOwner{
			Type: orderdomain.OwnerOperator, OwnerID: command.ActionID,
			LogicalAccountID: command.LogicalAccountID,
		},
	}
	if account.MarketType == string(exchange.MarketTypeSwap) {
		spec.PositionSide = exchange.PositionSideNet
	}
	placed, err := s.Orders.Place(ctx, command.SpaceID, spec)
	if err != nil {
		return err
	}
	result.ChildOrderIDs = appendUnique(
		result.ChildOrderIDs, string(placed.ID),
	)
	_, err = s.Orders.Submit(ctx, command.SpaceID, string(placed.ID))
	return err
}

func (s *Service) flattenChild(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	actionID string,
	symbol string,
	clientOrderID string,
) (store.OrderRecord, bool, error) {
	records, err := s.Store.ListOrdersForLane(
		ctx, spaceID, tradingAccountID, symbol,
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
			fmt.Errorf("multiple active flatten children exist for %s", symbol)
	}
	if len(active) == 1 {
		return active[0], true, nil
	}
	if matching != nil {
		return *matching, true, nil
	}
	return store.OrderRecord{}, false, nil
}

func (s *Service) remainingForAccount(
	ctx context.Context,
	spaceID string,
	tradingAccountID string,
	reasons map[string]string,
) []RemainingPosition {
	account, err := s.Store.GetTradingAccount(ctx, spaceID, tradingAccountID)
	if err != nil {
		return []RemainingPosition{{
			Quantity: "unknown", Reason: err.Error(),
		}}
	}
	var remaining []RemainingPosition
	if account.MarketType == string(exchange.MarketTypeSwap) {
		positions, listErr := s.Store.ListPositions(
			ctx, spaceID, tradingAccountID, "",
		)
		if listErr != nil {
			return []RemainingPosition{{
				Quantity: "unknown", Reason: listErr.Error(),
			}}
		}
		for _, position := range positions {
			quantity, parseErr := decimalOrZero(position.SignedQuantity)
			if parseErr != nil || quantity.IsZero() {
				continue
			}
			reason := reasonFor(reasons, position.Symbol, "position remains after final sync")
			instrumentID := position.InstrumentID
			if instrumentID == "" {
				instrumentID = position.ExchangeSymbol
			}
			remaining = append(remaining, RemainingPosition{
				InstrumentID: instrumentID, Quantity: quantity.String(), Reason: reason,
			})
		}
	} else {
		for _, balance := range account.Snapshot.Balances {
			if balance.Asset == account.SettlementAsset {
				continue
			}
			quantity, parseErr := decimalOrZero(balance.Total)
			if parseErr != nil || quantity.Cmp(shared.Zero()) <= 0 {
				continue
			}
			reason := reasonFor(
				reasons, "asset:"+balance.Asset, "asset remains after final sync",
			)
			remaining = append(remaining, RemainingPosition{
				Asset: balance.Asset, Quantity: quantity.String(), Reason: reason,
			})
		}
	}
	sort.Slice(remaining, func(i, j int) bool {
		return remaining[i].InstrumentID+remaining[i].Asset <
			remaining[j].InstrumentID+remaining[j].Asset
	})
	return remaining
}

func (s *Service) persistFlattenProgress(
	ctx context.Context,
	result *FlattenResult,
	status string,
) error {
	data, err := json.Marshal(struct {
		Accounts []FlattenAccountResult `json:"accounts"`
	}{Accounts: result.Accounts})
	if err != nil {
		return err
	}
	raw := string(data)
	result.Action.Status = status
	result.Action.ResultJSON = &raw
	result.Action.LastError = ""
	for _, account := range result.Accounts {
		if account.Error != "" {
			result.Action.LastError = joinError(
				result.Action.LastError,
				account.TradingAccountID+": "+account.Error,
			)
		}
	}
	return s.updateAction(ctx, result.Action)
}

func decodeFlattenResult(action store.OperatorActionRecord) (FlattenResult, error) {
	result := FlattenResult{Action: action}
	if action.ResultJSON == nil {
		if action.LastError == "" {
			return result, nil
		}
		return result, errors.New(action.LastError)
	}
	var persisted struct {
		Accounts []FlattenAccountResult `json:"accounts"`
	}
	if err := json.Unmarshal([]byte(*action.ResultJSON), &persisted); err != nil {
		return result, err
	}
	result.Accounts = persisted.Accounts
	return result, nil
}

func flattenStatus(accounts []FlattenAccountResult, executable int) string {
	if executable == 0 {
		return string(operatordomain.StatusFailed)
	}
	for _, account := range accounts {
		if account.Status != "COMPLETED" {
			return string(operatordomain.StatusPartial)
		}
	}
	return string(operatordomain.StatusCompleted)
}

func flattenClientOrderIDForSpec(
	actionID string,
	accountID string,
	symbol string,
	side exchange.Side,
	quantity shared.Decimal,
) string {
	sum := sha256.Sum256([]byte(
		actionID + "\x00" + accountID + "\x00" + symbol + "\x00" +
			string(side) + "\x00" + quantity.String(),
	))
	return "mf" + hex.EncodeToString(sum[:15])
}

func (s *Service) flattenAttempts() int {
	if s.FlattenMaxAttempts > 0 {
		return s.FlattenMaxAttempts
	}
	return int(^uint(0) >> 1)
}

func (s *Service) waitFlattenRetry(ctx context.Context) error {
	interval := s.FlattenRetryInterval
	if interval < 0 {
		return nil
	}
	if interval == 0 {
		interval = 200 * time.Millisecond
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Service) flattenTimeout() time.Duration {
	if s.FlattenTimeout > 0 {
		return s.FlattenTimeout
	}
	return 5 * time.Second
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
		return "quantity is below minimum or not aligned to step"
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
	if instrument.MarketType == string(exchange.MarketTypeSwap) {
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

func floorToStep(value, step shared.Decimal) shared.Decimal {
	if value.Cmp(shared.Zero()) <= 0 || step.Cmp(shared.Zero()) <= 0 {
		return shared.Zero()
	}
	ratio := new(big.Rat)
	if _, ok := ratio.SetString(value.Div(step).String()); !ok {
		return shared.Zero()
	}
	units := new(big.Int).Quo(ratio.Num(), ratio.Denom())
	whole, err := shared.ParseDecimal(units.String())
	if err != nil {
		return shared.Zero()
	}
	return whole.Mul(step)
}

func tradableForSettlement(
	instrument store.InstrumentRecord,
	settlementAsset string,
) bool {
	return (instrument.Status == "TRADING" || instrument.Status == "live") &&
		(instrument.SettlementAsset == "" ||
			instrument.SettlementAsset == settlementAsset)
}

func decimalOrZero(raw string) (shared.Decimal, error) {
	if strings.TrimSpace(raw) == "" {
		return shared.Zero(), nil
	}
	return shared.ParseDecimal(raw)
}

func reasonFor(values map[string]string, key, fallback string) string {
	if reason := values[key]; reason != "" {
		return reason
	}
	return fallback
}

func joinError(current, next string) string {
	if current == "" {
		return next
	}
	if next == "" {
		return current
	}
	return current + "; " + next
}

func appendUnique(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}
