package rpc

import (
	"context"
	"errors"
	"strings"

	delistapp "github.com/mooyang-code/moox/modules/trade/internal/application/delist"
	holdingapp "github.com/mooyang-code/moox/modules/trade/internal/application/holding"
	papersimulation "github.com/mooyang-code/moox/modules/trade/internal/application/papersimulation"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

var (
	errServiceUnavailable = errors.New("交易服务未配置")
	errInvalidCurveTarget = errors.New("权益曲线要指定且只能指定一个账户或组合")
)

// ConsoleServer is the single public business service. The existing handlers
// remain embedded so the cutover does not duplicate command semantics.
type ConsoleServer struct {
	*AccountServer
	*LogicalAccountServer
	*ExecutionServer
	Store              *store.Store
	Paper              *papersimulation.Service
	LiveTradingEnabled bool
	MatcherReady       func() bool
	Holdings           HoldingQueryService
	Exchanges          execution.ExchangeCatalog
	Adapters           AdapterSource
	FlattenAccount     FlattenTradingAccountFunc
	// Delist 是下架名单与持有者查询；FlattenInstrumentAction 发起单标的下架清仓；ReduceAccount 发起按比例减仓。
	Delist                  *delistapp.Service
	FlattenInstrumentAction FlattenInstrumentFunc
	ReduceAccount           ReduceLogicalAccountFunc
}

type HoldingQueryService interface {
	List(context.Context, string, string) ([]holdingapp.Holding, error)
}

func (s *ConsoleServer) CreatePaperSimulation(
	ctx context.Context,
	req *tradepb.CreatePaperSimulationReq,
) (*tradepb.CreatePaperSimulationRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.CreatePaperSimulationRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	if s.Paper == nil {
		return &tradepb.CreatePaperSimulationRsp{RetInfo: errorInfo(errServiceUnavailable)}, nil
	}
	parse := func(raw string) (shared.Decimal, error) {
		if strings.TrimSpace(raw) == "" {
			return shared.Zero(), nil
		}
		return shared.ParseDecimal(raw)
	}
	initial, err := parse(req.GetInitialBalance())
	if err != nil {
		return &tradepb.CreatePaperSimulationRsp{RetInfo: invalidInfo(err)}, nil
	}
	maker, err := parse(req.GetMakerFeeRate())
	if err != nil {
		return &tradepb.CreatePaperSimulationRsp{RetInfo: invalidInfo(err)}, nil
	}
	taker, err := parse(req.GetTakerFeeRate())
	if err != nil {
		return &tradepb.CreatePaperSimulationRsp{RetInfo: invalidInfo(err)}, nil
	}
	slippage, err := parse(req.GetSlippageBps())
	if err != nil {
		return &tradepb.CreatePaperSimulationRsp{RetInfo: invalidInfo(err)}, nil
	}
	result, err := s.Paper.Create(ctx, papersimulation.CreateCommand{
		SpaceID: spaceID, AccountName: strings.TrimSpace(req.GetAccountName()),
		LogicalAccountName: strings.TrimSpace(req.GetLogicalAccountName()),
		LogicalAccountID:   strings.TrimSpace(req.GetLogicalAccountId()), Priority: int(req.GetPriority()),
		ControlMode: controlModeFromPB(req.GetControlMode()),
		Exchange:    exchangeFromPB(req.GetExchange()), Markets: marketsFromPB(req.GetMarkets()),
		SettlementAsset: strings.TrimSpace(req.GetSettlementAsset()), MarginMode: exchange.MarginMode(req.GetMarginMode()),
		InitialBalance: initial, MakerFeeRate: maker, TakerFeeRate: taker, SlippageBPS: slippage,
	})
	rsp := &tradepb.CreatePaperSimulationRsp{RetInfo: errorInfo(err), Account: accountToPB(result.Account)}
	if result.LogicalAccount.LogicalAccountID != "" {
		rsp.LogicalAccount = s.logicalAccount(ctx, result.LogicalAccount)
	}
	return rsp, nil
}

func (s *ConsoleServer) ClosePaperSimulation(
	ctx context.Context,
	req *tradepb.ClosePaperSimulationReq,
) (*tradepb.ClosePaperSimulationRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.ClosePaperSimulationRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	if s.Paper == nil || s.Store == nil {
		return &tradepb.ClosePaperSimulationRsp{RetInfo: errorInfo(errServiceUnavailable)}, nil
	}
	// 关闭会把账户移出成员，要先取组合账户。
	var logical *store.LogicalAccountRecord
	if record, _, findErr := s.Store.FindLogicalAccountByTradingAccount(ctx, spaceID, req.GetTradingAccountId()); findErr == nil {
		logical = &record
	}
	err = s.Paper.Close(ctx, spaceID, req.GetTradingAccountId())
	if err != nil {
		return &tradepb.ClosePaperSimulationRsp{RetInfo: errorInfo(err)}, nil
	}
	account, err := s.Store.GetTradingAccount(ctx, spaceID, req.GetTradingAccountId())
	if err != nil {
		return &tradepb.ClosePaperSimulationRsp{RetInfo: errorInfo(err)}, nil
	}
	rsp := &tradepb.ClosePaperSimulationRsp{RetInfo: success(), Account: accountToPB(account)}
	if logical != nil {
		rsp.LogicalAccount = s.logicalAccount(ctx, *logical)
	}
	return rsp, nil
}

func (s *ConsoleServer) GetExecutionCapabilities(
	ctx context.Context,
	req *tradepb.GetExecutionCapabilitiesReq,
) (*tradepb.GetExecutionCapabilitiesRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.GetExecutionCapabilitiesRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	if s.Store == nil {
		return &tradepb.GetExecutionCapabilitiesRsp{RetInfo: errorInfo(errServiceUnavailable)}, nil
	}
	accountRecord, err := s.Store.GetTradingAccount(ctx, spaceID, req.GetTradingAccountId())
	if err != nil {
		return &tradepb.GetExecutionCapabilitiesRsp{RetInfo: errorInfo(err)}, nil
	}
	account := domainAccountForCapabilities(accountRecord)
	matcherReady := true
	if s.MatcherReady != nil {
		matcherReady = s.MatcherReady()
	}
	if s.Exchanges == nil {
		return &tradepb.GetExecutionCapabilitiesRsp{RetInfo: errorInfo(errServiceUnavailable)}, nil
	}
	descriptor, _ := s.Exchanges.Descriptor(account.Exchange)
	value := ResolveExecutionCapabilities(account, descriptor, s.LiveTradingEnabled, matcherReady)
	capabilities := &tradepb.ExecutionCapabilities{
		CanPlaceOrder: value.CanPlaceOrder, UnavailableReason: value.UnavailableReason,
		CanClosePaperSimulation: value.CanClosePaperSimulation,
	}
	_, ledgerErr := s.ledgerSource(accountRecord.TradingAccountID)
	capabilities.Ledger = ledgerCapability(ledgerErr)
	for _, item := range value.OrderTypes {
		capabilities.OrderTypes = append(capabilities.OrderTypes, orderTypeToPB(string(item)))
	}
	for _, item := range value.FillPolicies {
		capabilities.FillPolicies = append(capabilities.FillPolicies, fillPolicyToPB(string(item)))
	}
	return &tradepb.GetExecutionCapabilitiesRsp{RetInfo: success(), Capabilities: capabilities}, nil
}

// ListTradableInstruments 返回账户当前可下单的标的和数量规则，供下单表单使用。
func (s *ConsoleServer) ListTradableInstruments(
	ctx context.Context,
	req *tradepb.ListTradableInstrumentsReq,
) (*tradepb.ListTradableInstrumentsRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.ListTradableInstrumentsRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	if s.Store == nil {
		return &tradepb.ListTradableInstrumentsRsp{RetInfo: errorInfo(errServiceUnavailable)}, nil
	}
	account, err := s.Store.GetTradingAccount(ctx, spaceID, req.GetTradingAccountId())
	if err != nil {
		return &tradepb.ListTradableInstrumentsRsp{RetInfo: errorInfo(err)}, nil
	}
	records, err := s.Store.ListInstrumentsForAccount(ctx, account.TradingAccountID)
	if err != nil {
		return &tradepb.ListTradableInstrumentsRsp{RetInfo: errorInfo(err)}, nil
	}
	rsp := &tradepb.ListTradableInstrumentsRsp{RetInfo: success()}
	for _, record := range records {
		if !exchange.StatusTradable(record.Status) {
			continue
		}
		if record.SettlementAsset != "" && record.SettlementAsset != account.SettlementAsset {
			continue
		}
		rsp.Instruments = append(rsp.Instruments, tradableInstrumentToPB(record))
	}
	return rsp, nil
}

func tradableInstrumentToPB(record store.InstrumentRecord) *tradepb.TradableInstrument {
	step := shared.MustDecimal(record.ExchangeQuantityStep)
	minimum := shared.MustDecimal(record.MinExchangeQuantity)
	if exchange.RuleFor(record.MarketType).ContractValue {
		contractValue := shared.MustDecimal(record.ContractValue)
		step, minimum = step.Mul(contractValue), minimum.Mul(contractValue)
	}
	return &tradepb.TradableInstrument{
		InstrumentId: record.InstrumentID, QuantityStep: step.String(), MinQuantity: minimum.String(),
		MinNotional: record.MinNotional, PriceTick: record.PriceTick,
	}
}

func (s *ConsoleServer) QueryEquityCurve(
	ctx context.Context,
	req *tradepb.QueryEquityCurveReq,
) (*tradepb.QueryEquityCurveRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.QueryEquityCurveRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	if s.Store == nil {
		return &tradepb.QueryEquityCurveRsp{RetInfo: errorInfo(errServiceUnavailable)}, nil
	}
	if (req.GetTradingAccountId() == "") == (req.GetLogicalAccountId() == "") {
		return &tradepb.QueryEquityCurveRsp{RetInfo: invalidInfo(errInvalidCurveTarget)}, nil
	}
	from, to := req.GetStartTime(), req.GetEndTime()
	var records []store.EquityPointRecord
	if req.GetTradingAccountId() != "" {
		records, err = s.Store.ListAccountEquityPoints(ctx, spaceID, req.GetTradingAccountId(), from, to)
	} else {
		records, err = s.Store.ListLogicalAccountEquityPoints(ctx, spaceID, req.GetLogicalAccountId(), from, to)
	}
	if err != nil {
		return &tradepb.QueryEquityCurveRsp{RetInfo: errorInfo(err)}, nil
	}
	points := make([]*tradepb.EquityPoint, 0, len(records))
	for _, record := range records {
		point := &tradepb.EquityPoint{BucketTime: record.BucketTime, Equity: record.Equity, AvailableFunds: record.AvailableFunds, UsedMargin: record.UsedMargin, SourceTime: record.SourceTime}
		if record.UnrealizedPnL != nil {
			value := *record.UnrealizedPnL
			point.UnrealizedPnl = &value
		}
		points = append(points, point)
	}
	return &tradepb.QueryEquityCurveRsp{RetInfo: success(), Points: points}, nil
}

func (s *ConsoleServer) ListHoldings(
	ctx context.Context,
	req *tradepb.ListHoldingsReq,
) (*tradepb.ListHoldingsRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.ListHoldingsRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	if s.Store == nil {
		return &tradepb.ListHoldingsRsp{RetInfo: errorInfo(errServiceUnavailable)}, nil
	}
	account, err := s.Store.GetTradingAccount(ctx, spaceID, req.GetTradingAccountId())
	if err != nil {
		return &tradepb.ListHoldingsRsp{RetInfo: errorInfo(err)}, nil
	}
	if !account.HasBalanceMarket() {
		return &tradepb.ListHoldingsRsp{RetInfo: invalidInfo(errors.New("只有开通现货的账户才有持币明细"))}, nil
	}
	if s.Holdings != nil {
		values, err := s.Holdings.List(ctx, spaceID, req.GetTradingAccountId())
		if err != nil {
			return &tradepb.ListHoldingsRsp{RetInfo: errorInfo(err)}, nil
		}
		result := make([]*tradepb.Holding, 0, len(values))
		for _, value := range values {
			item := &tradepb.Holding{
				TradingAccountId: value.TradingAccountID, InstrumentId: value.InstrumentID,
				ExchangeSymbol: value.ExchangeSymbol, Asset: value.Asset,
				Quantity: value.Quantity.String(), AverageCost: value.AverageCost.String(),
				MarkPrice: value.MarkPrice.String(), MarketValue: value.MarketValue.String(),
				SourceTime: value.SourceTime.UnixMilli(), Sellable: value.Sellable.String(),
			}
			if value.UnrealizedPnL != nil {
				pnl := value.UnrealizedPnL.String()
				item.UnrealizedPnl = &pnl
			}
			result = append(result, item)
		}
		return &tradepb.ListHoldingsRsp{RetInfo: success(), Holdings: result}, nil
	}
	holdings := make([]*tradepb.Holding, 0, len(account.Snapshot.Balances))
	for _, balance := range account.Snapshot.Balances {
		if balance.Total == "" || balance.Total == "0" {
			continue
		}
		marketValue := ""
		markPrice := ""
		if balance.Asset == account.SettlementAsset {
			marketValue, markPrice = balance.Total, "1"
		}
		holdings = append(holdings, &tradepb.Holding{
			TradingAccountId: req.GetTradingAccountId(), Asset: balance.Asset, Quantity: balance.Total,
			MarkPrice: markPrice, MarketValue: marketValue, SourceTime: account.Snapshot.ExchangeUpdatedAt,
			Sellable: balance.Available,
		})
	}
	return &tradepb.ListHoldingsRsp{RetInfo: success(), Holdings: holdings}, nil
}

func domainAccountForCapabilities(record store.TradingAccountRecord) tradingaccount.Account {
	value := tradingaccount.Account{
		ID: record.TradingAccountID, SpaceID: record.SpaceID, Name: record.Name,
		Exchange: exchange.Exchange(record.Exchange), Markets: marketTypes(record.Markets),
		ExecutionMode: exchange.ExecutionMode(record.ExecutionMode), Environment: exchange.AccountEnvironment(record.Environment),
		CredentialSecretID: record.CredentialSecretID, SettlementAsset: record.SettlementAsset,
		MarginMode: exchange.MarginMode(record.MarginMode), Status: exchange.AccountStatus(record.Status), Ready: record.Ready,
		LeverageSettings: map[string]shared.Decimal{},
	}
	if record.ExecutionMode == string(exchange.ExecutionModePaper) {
		config := record.PaperConfig
		if config == nil {
			config = &store.PaperAccountConfigRecord{}
		}
		value.Paper = &tradingaccount.PaperConfig{InitialBalance: decimalValue(config.InitialBalance), MakerFeeRate: decimalValue(config.MakerFeeRate), TakerFeeRate: decimalValue(config.TakerFeeRate), SlippageBPS: decimalValue(config.SlippageBPS)}
	} else {
		value.Live = &tradingaccount.LiveConfig{Environment: exchange.AccountEnvironment(record.Environment), CredentialSecretID: record.CredentialSecretID}
	}
	for key, raw := range record.LeverageSettings {
		value.LeverageSettings[key] = decimalValue(raw)
	}
	return value
}

func marketTypes(markets []string) []exchange.MarketType {
	result := make([]exchange.MarketType, len(markets))
	for i, market := range markets {
		result[i] = exchange.MarketType(market)
	}
	return result
}

func decimalValue(raw string) shared.Decimal {
	value, err := shared.ParseDecimal(raw)
	if err != nil {
		return shared.Zero()
	}
	return value
}
