package rpc

import (
	"context"
	"errors"
	"strings"

	holdingapp "github.com/mooyang-code/moox/modules/trade/internal/application/holding"
	papersimulation "github.com/mooyang-code/moox/modules/trade/internal/application/papersimulation"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

var (
	errServiceUnavailable = errors.New("trade RPC: service is not configured")
	errInvalidCurveTarget = errors.New("trade RPC: exactly one equity curve target is required")
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
		ControlMode:        controlModeFromPB(req.GetControlMode()),
		Exchange:           exchangeFromPB(req.GetExchange()), MarketType: marketFromPB(req.GetMarketType()),
		SettlementAsset: strings.TrimSpace(req.GetSettlementAsset()), MarginMode: exchange.MarginMode(req.GetMarginMode()),
		InitialBalance: initial, MakerFeeRate: maker, TakerFeeRate: taker, SlippageBPS: slippage,
	})
	return &tradepb.CreatePaperSimulationRsp{
		RetInfo: errorInfo(err), Account: accountToPB(result.Account),
		LogicalAccount: s.logicalAccount(ctx, result.LogicalAccount),
	}, nil
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
	err = s.Paper.Close(ctx, spaceID, req.GetTradingAccountId())
	if err != nil {
		return &tradepb.ClosePaperSimulationRsp{RetInfo: errorInfo(err)}, nil
	}
	account, err := s.Store.GetTradingAccount(ctx, spaceID, req.GetTradingAccountId())
	if err != nil {
		return &tradepb.ClosePaperSimulationRsp{RetInfo: errorInfo(err)}, nil
	}
	var logical *tradepb.LogicalAccount
	for _, candidate := range mustLogicalAccounts(ctx, s.Store, spaceID) {
		for _, member := range mustLogicalMembers(ctx, s.Store, spaceID, candidate.LogicalAccountID) {
			if member.TradingAccountID == req.GetTradingAccountId() {
				logical = s.logicalAccount(ctx, candidate)
				break
			}
		}
		if logical != nil {
			break
		}
	}
	return &tradepb.ClosePaperSimulationRsp{RetInfo: success(), Account: accountToPB(account), LogicalAccount: logical}, nil
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
	value := ResolveExecutionCapabilities(account, s.LiveTradingEnabled, matcherReady)
	capabilities := &tradepb.ExecutionCapabilities{
		CanPlaceOrder: value.CanPlaceOrder, UnavailableReason: value.UnavailableReason,
		CanClosePaperSimulation: value.CanClosePaperSimulation,
	}
	for _, item := range value.OrderTypes {
		capabilities.OrderTypes = append(capabilities.OrderTypes, orderTypeToPB(string(item)))
	}
	for _, item := range value.FillPolicies {
		capabilities.FillPolicies = append(capabilities.FillPolicies, fillPolicyToPB(string(item)))
	}
	return &tradepb.GetExecutionCapabilitiesRsp{RetInfo: success(), Capabilities: capabilities}, nil
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
	if account.MarketType != string(exchange.MarketTypeSpot) {
		return &tradepb.ListHoldingsRsp{RetInfo: invalidInfo(errors.New("holdings are only available for SPOT accounts"))}, nil
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
				SourceTime: value.SourceTime.UnixMilli(),
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
		})
	}
	return &tradepb.ListHoldingsRsp{RetInfo: success(), Holdings: holdings}, nil
}

func domainAccountForCapabilities(record store.TradingAccountRecord) tradingaccount.Account {
	value := tradingaccount.Account{
		ID: record.TradingAccountID, SpaceID: record.SpaceID, Name: record.Name,
		Exchange: exchange.Exchange(record.Exchange), MarketType: exchange.MarketType(record.MarketType),
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

func decimalValue(raw string) shared.Decimal {
	value, err := shared.ParseDecimal(raw)
	if err != nil {
		return shared.Zero()
	}
	return value
}

func mustLogicalAccounts(ctx context.Context, s *store.Store, spaceID string) []store.LogicalAccountRecord {
	items, _ := s.ListLogicalAccounts(ctx, spaceID)
	return items
}

func mustLogicalMembers(ctx context.Context, s *store.Store, spaceID, logicalID string) []store.LogicalAccountMemberRecord {
	items, _ := s.ListLogicalAccountMembers(ctx, spaceID, logicalID, true)
	return items
}
