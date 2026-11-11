package rpc

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	gonanoid "github.com/matoous/go-nanoid/v2"
	accountapp "github.com/mooyang-code/moox/modules/trade/internal/application/account"
	"github.com/mooyang-code/moox/modules/trade/internal/application/accountsync"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"gorm.io/gorm"
)

var errSpaceRequired = errors.New("请求没有带上已认证的使用空间")

// errInstrumentNotInAccount 是设置杠杆时执行账户的场所里没有这个合约。
var errInstrumentNotInAccount = errors.New("这个账户没有该合约")

type AccountServer struct {
	Accounts *accountapp.Service
	Sync     *accountsync.Service
	Store    *store.Store
	NewID    func() string
	// AfterInspection 处理重新检测的结果（暂停组合、告警）；为空时只写回结果。
	AfterInspection InspectionFollowUp
}

func (h *AccountServer) CreateTradingAccount(
	ctx context.Context,
	req *tradepb.CreateTradingAccountReq,
) (*tradepb.CreateTradingAccountRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err != nil {
		return &tradepb.CreateTradingAccountRsp{RetInfo: errorInfo(err)}, nil
	}
	if err := validatePB(req); err != nil {
		return &tradepb.CreateTradingAccountRsp{RetInfo: invalidInfo(err)}, nil
	}
	markets, err := exchange.NormalizeMarkets(marketsFromPB(req.GetMarkets()))
	if err != nil {
		return &tradepb.CreateTradingAccountRsp{RetInfo: invalidInfo(err)}, nil
	}
	// 决策 D5：开通合约的账户默认 1 倍杠杆，单标的配置优先；SWAP 一律全仓。
	marginMode := exchange.MarginModeUnspecified
	leverageSettings := map[string]shared.Decimal{}
	if slices.Contains(markets, exchange.MarketTypeSwap) {
		leverageSettings[store.DefaultLeverageKey] = shared.MustDecimal("1")
		marginMode = exchange.MarginModeCross
	}
	value := tradingaccount.Account{
		ID: h.accountID(), SpaceID: spaceID, Name: strings.TrimSpace(req.GetName()),
		Exchange:           exchangeFromPB(req.GetExchange()),
		Markets:            markets,
		ExecutionMode:      exchange.ExecutionModeLive,
		Environment:        environmentFromPB(req.GetLive().GetEnvironment()),
		CredentialSecretID: strings.TrimSpace(req.GetLive().GetCredentialSecretId()),
		Live:               &tradingaccount.LiveConfig{Environment: environmentFromPB(req.GetLive().GetEnvironment()), CredentialSecretID: strings.TrimSpace(req.GetLive().GetCredentialSecretId())},
		SettlementAsset:    normalized(req.GetSettlementAsset()), MarginMode: marginMode,
		Status:           exchange.AccountStatusEnabled,
		LeverageSettings: leverageSettings,
	}
	created, err := h.Accounts.Create(ctx, value)
	if err != nil {
		return &tradepb.CreateTradingAccountRsp{RetInfo: errorInfo(err)}, nil
	}
	record, err := h.Store.GetTradingAccount(ctx, spaceID, created.ID)
	return &tradepb.CreateTradingAccountRsp{RetInfo: errorInfo(err), Account: accountToPB(record)}, nil
}

func (h *AccountServer) UpdateTradingAccount(
	ctx context.Context,
	req *tradepb.UpdateTradingAccountReq,
) (*tradepb.UpdateTradingAccountRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err != nil {
		return &tradepb.UpdateTradingAccountRsp{RetInfo: errorInfo(err)}, nil
	}
	if err := validatePB(req); err != nil {
		return &tradepb.UpdateTradingAccountRsp{RetInfo: invalidInfo(err)}, nil
	}
	if _, err := h.Store.GetTradingAccount(ctx, spaceID, req.GetTradingAccountId()); err != nil {
		return &tradepb.UpdateTradingAccountRsp{RetInfo: errorInfo(err)}, nil
	}
	command := accountapp.UpdateCommand{TradingAccountID: req.GetTradingAccountId()}
	if value := strings.TrimSpace(req.GetName()); value != "" {
		command.Name = &value
	}
	if value := strings.TrimSpace(req.GetCredentialSecretId()); value != "" {
		command.CredentialSecretID = &value
	}
	if value := normalized(req.GetSettlementAsset()); value != "" {
		command.SettlementAsset = &value
	}
	if value := normalized(req.GetMarginMode()); value != "" {
		margin := exchange.MarginMode(value)
		command.MarginMode = &margin
	}
	if value := normalized(req.GetStatus()); value != "" {
		status := exchange.AccountStatus(value)
		command.Status = &status
	}
	if _, err := h.Accounts.Update(ctx, command); err != nil {
		return &tradepb.UpdateTradingAccountRsp{RetInfo: errorInfo(err)}, nil
	}
	record, err := h.Store.GetTradingAccount(ctx, spaceID, req.GetTradingAccountId())
	return &tradepb.UpdateTradingAccountRsp{RetInfo: errorInfo(err), Account: accountToPB(record)}, nil
}

func (h *AccountServer) GetTradingAccount(
	ctx context.Context,
	req *tradepb.GetTradingAccountReq,
) (*tradepb.GetTradingAccountRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.GetTradingAccountRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	record, err := h.Store.GetTradingAccount(ctx, spaceID, req.GetTradingAccountId())
	return &tradepb.GetTradingAccountRsp{RetInfo: errorInfo(err), Account: accountToPB(record)}, nil
}

func (h *AccountServer) ListTradingAccounts(
	ctx context.Context,
	req *tradepb.ListTradingAccountsReq,
) (*tradepb.ListTradingAccountsRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.ListTradingAccountsRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	records, err := h.Store.ReadOnly().ListTradingAccounts(ctx, spaceID)
	if err != nil {
		return &tradepb.ListTradingAccountsRsp{RetInfo: errorInfo(err)}, nil
	}
	filtered := records[:0]
	for _, record := range records {
		if req.Exchange != nil && record.Exchange != string(exchangeFromPB(req.GetExchange())) {
			continue
		}
		if req.MarketType != nil && !record.SupportsMarket(string(marketFromPB(req.GetMarketType()))) {
			continue
		}
		if req.ExecutionMode != nil &&
			record.ExecutionMode != string(executionModeFromPB(req.GetExecutionMode())) {
			continue
		}
		if status := normalized(req.GetStatus()); status != "" && record.Status != status {
			continue
		}
		filtered = append(filtered, record)
	}
	page := pageFromPB(req.GetPage())
	total := int64(len(filtered))
	start, end := page.offset, page.offset+page.size
	if start > len(filtered) {
		start = len(filtered)
	}
	if end > len(filtered) {
		end = len(filtered)
	}
	accounts := make([]*tradepb.TradingAccount, 0, end-start)
	for _, record := range filtered[start:end] {
		accounts = append(accounts, accountToPB(record))
	}
	return &tradepb.ListTradingAccountsRsp{
		RetInfo: success(), Accounts: accounts, PageResult: pageResult(page, total),
	}, nil
}

func (h *AccountServer) SetLeverage(
	ctx context.Context,
	req *tradepb.SetLeverageReq,
) (*tradepb.SetLeverageRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.SetLeverageRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	if _, err := h.Store.GetTradingAccount(ctx, spaceID, req.GetTradingAccountId()); err != nil {
		return &tradepb.SetLeverageRsp{RetInfo: errorInfo(err)}, nil
	}
	leverage, err := shared.ParseDecimal(req.GetLeverage())
	if err == nil {
		// 杠杆键是内部标的 ID（账户默认杠杆是 "*"）；会话要的是交易所代码。
		leverageKey, symbol := store.DefaultLeverageKey, exchange.SymbolKey{}
		if req.GetInstrumentId() != store.DefaultLeverageKey {
			// 杠杆只对合约生效，按合约市场标准化标的 ID。
			var instrumentID string
			instrumentID, err = exchange.NormalizeInstrumentID(req.GetInstrumentId(), exchange.MarketTypeSwap)
			if err != nil {
				return &tradepb.SetLeverageRsp{RetInfo: instrumentErrorInfo(err)}, nil
			}
			var instrument store.InstrumentRecord
			instrument, err = h.Store.GetInstrumentByIDForAccount(ctx, spaceID, req.GetTradingAccountId(), instrumentID)
			if errors.Is(err, gorm.ErrRecordNotFound) {
				// 账户在（上面查过），是账户的场所里没有这个合约：参数错误，不是“记录不存在”。
				err = errInstrumentNotInAccount
			}
			leverageKey, symbol = instrument.InstrumentID, exchange.NewSymbolKey(instrument.MarketType, instrument.ExchangeSymbol)
		}
		if err == nil {
			err = h.Accounts.SetLeverage(ctx, req.GetTradingAccountId(), leverageKey, symbol, leverage)
		}
	}
	record, getErr := h.Store.GetTradingAccount(ctx, spaceID, req.GetTradingAccountId())
	if err == nil {
		err = getErr
	}
	return &tradepb.SetLeverageRsp{RetInfo: errorInfo(err), Account: accountToPB(record)}, nil
}

func (h *AccountServer) SyncTradingAccount(
	ctx context.Context,
	req *tradepb.SyncTradingAccountReq,
) (*tradepb.SyncTradingAccountRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.SyncTradingAccountRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	if _, err := h.Store.GetTradingAccount(ctx, spaceID, req.GetTradingAccountId()); err != nil {
		return &tradepb.SyncTradingAccountRsp{RetInfo: errorInfo(err)}, nil
	}
	result, err := h.Sync.SyncAccountFull(ctx, req.GetTradingAccountId())
	return &tradepb.SyncTradingAccountRsp{
		RetInfo: errorInfo(err), FillsIngested: int32(result.FillsIngested),
		OrdersUpdated:          int32(result.OrdersUpdated),
		PositionsUpdated:       int32(result.PositionsUpdated),
		AccountSnapshotUpdated: result.AccountSnapshotUpdated,
		UnknownOrdersResolved:  int32(result.UnknownOrdersResolved),
		Ready:                  result.Ready, Warnings: result.Warnings,
	}, nil
}

func requiredSpace(ctx context.Context) (string, error) {
	value, ok := spacecontext.FromContext(ctx)
	if !ok {
		return "", errSpaceRequired
	}
	return value, nil
}

type validatable interface{ Validate() error }

func validatePB(value validatable) error {
	if value == nil {
		return errors.New("request is required")
	}
	return value.Validate()
}

func invalidInfo(err error) *tradepb.RetInfo {
	return retInfo(tradepb.ErrorCode_INVALID_PARAM, err.Error())
}

func invalidOrErrorInfo(err error) *tradepb.RetInfo {
	if errors.Is(err, errSpaceRequired) {
		return errorInfo(err)
	}
	return invalidInfo(err)
}

func (h *AccountServer) accountID() string {
	if h.NewID != nil {
		return h.NewID()
	}
	id, err := gonanoid.New()
	if err != nil {
		panic(fmt.Sprintf("generate Exchange account ID: %v", err))
	}
	return id
}
