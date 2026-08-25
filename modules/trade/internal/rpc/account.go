package rpc

import (
	"context"
	"errors"
	"fmt"
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
)

var errSpaceRequired = errors.New("trade RPC: authenticated space is required")

type AccountServer struct {
	Accounts *accountapp.Service
	Sync     *accountsync.Service
	Store    *store.Store
	NewID    func() string
}

func (h *AccountServer) CreateAccount(
	ctx context.Context,
	req *tradepb.CreateAccountReq,
) (*tradepb.CreateAccountRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err != nil {
		return &tradepb.CreateAccountRsp{RetInfo: errorInfo(err)}, nil
	}
	if err := validatePB(req); err != nil {
		return &tradepb.CreateAccountRsp{RetInfo: invalidInfo(err)}, nil
	}
	marginMode := exchange.MarginMode(normalized(req.GetMarginMode()))
	if req.GetMarketType() == tradepb.MarketType_MARKET_TYPE_SPOT {
		marginMode = exchange.MarginModeUnspecified
	}
	value := tradingaccount.Account{
		ID: h.accountID(), SpaceID: spaceID, Name: strings.TrimSpace(req.GetName()),
		Exchange:           exchangeFromPB(req.GetExchange()),
		MarketType:         marketFromPB(req.GetMarketType()),
		ExecutionMode:      executionModeFromPB(req.GetExecutionMode()),
		Environment:        environmentFromPB(req.GetEnvironment()),
		CredentialSecretID: strings.TrimSpace(req.GetCredentialSecretId()),
		SettlementAsset:    normalized(req.GetSettlementAsset()), MarginMode: marginMode,
		Status:           exchange.AccountStatusEnabled,
		SyncSymbols:      append([]string(nil), req.GetSyncSymbols()...),
		LeverageSettings: map[string]shared.Decimal{},
	}
	created, err := h.Accounts.Create(ctx, value)
	if err != nil {
		return &tradepb.CreateAccountRsp{RetInfo: errorInfo(err)}, nil
	}
	record, err := h.Store.GetTradingAccount(ctx, spaceID, created.ID)
	return &tradepb.CreateAccountRsp{RetInfo: errorInfo(err), Account: accountToPB(record)}, nil
}

func (h *AccountServer) UpdateAccount(
	ctx context.Context,
	req *tradepb.UpdateAccountReq,
) (*tradepb.UpdateAccountRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err != nil {
		return &tradepb.UpdateAccountRsp{RetInfo: errorInfo(err)}, nil
	}
	if err := validatePB(req); err != nil {
		return &tradepb.UpdateAccountRsp{RetInfo: invalidInfo(err)}, nil
	}
	if _, err := h.Store.GetTradingAccount(ctx, spaceID, req.GetExchangeAccountId()); err != nil {
		return &tradepb.UpdateAccountRsp{RetInfo: errorInfo(err)}, nil
	}
	command := accountapp.UpdateCommand{TradingAccountID: req.GetExchangeAccountId()}
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
	if req.SyncSymbols != nil {
		values := append([]string(nil), req.GetSyncSymbols()...)
		command.SyncSymbols = &values
	}
	if _, err := h.Accounts.Update(ctx, command); err != nil {
		return &tradepb.UpdateAccountRsp{RetInfo: errorInfo(err)}, nil
	}
	record, err := h.Store.GetTradingAccount(ctx, spaceID, req.GetExchangeAccountId())
	return &tradepb.UpdateAccountRsp{RetInfo: errorInfo(err), Account: accountToPB(record)}, nil
}

func (h *AccountServer) GetAccount(
	ctx context.Context,
	req *tradepb.GetAccountReq,
) (*tradepb.GetAccountRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.GetAccountRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	record, err := h.Store.GetTradingAccount(ctx, spaceID, req.GetExchangeAccountId())
	return &tradepb.GetAccountRsp{RetInfo: errorInfo(err), Account: accountToPB(record)}, nil
}

func (h *AccountServer) ListAccounts(
	ctx context.Context,
	req *tradepb.ListAccountsReq,
) (*tradepb.ListAccountsRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.ListAccountsRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	records, err := h.Store.ListTradingAccounts(ctx, spaceID)
	if err != nil {
		return &tradepb.ListAccountsRsp{RetInfo: errorInfo(err)}, nil
	}
	filtered := records[:0]
	for _, record := range records {
		if req.Exchange != nil && record.Exchange != string(exchangeFromPB(req.GetExchange())) {
			continue
		}
		if req.MarketType != nil && record.MarketType != string(marketFromPB(req.GetMarketType())) {
			continue
		}
		if req.ExecutionMode != nil &&
			record.ExecutionMode != string(executionModeFromPB(req.GetExecutionMode())) {
			continue
		}
		if req.Environment != nil &&
			record.Environment != string(environmentFromPB(req.GetEnvironment())) {
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
	accounts := make([]*tradepb.ExchangeAccount, 0, end-start)
	for _, record := range filtered[start:end] {
		accounts = append(accounts, accountToPB(record))
	}
	return &tradepb.ListAccountsRsp{
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
	if _, err := h.Store.GetTradingAccount(ctx, spaceID, req.GetExchangeAccountId()); err != nil {
		return &tradepb.SetLeverageRsp{RetInfo: errorInfo(err)}, nil
	}
	leverage, err := shared.ParseDecimal(req.GetLeverage())
	if err == nil {
		err = h.Accounts.SetLeverage(ctx, req.GetExchangeAccountId(), req.GetSymbol(), leverage)
	}
	record, getErr := h.Store.GetTradingAccount(ctx, spaceID, req.GetExchangeAccountId())
	if err == nil {
		err = getErr
	}
	return &tradepb.SetLeverageRsp{RetInfo: errorInfo(err), Account: accountToPB(record)}, nil
}

func (h *AccountServer) SyncAccount(
	ctx context.Context,
	req *tradepb.SyncAccountReq,
) (*tradepb.SyncAccountRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.SyncAccountRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	if _, err := h.Store.GetTradingAccount(ctx, spaceID, req.GetExchangeAccountId()); err != nil {
		return &tradepb.SyncAccountRsp{RetInfo: errorInfo(err)}, nil
	}
	result, err := h.Sync.SyncAccount(ctx, req.GetExchangeAccountId())
	return &tradepb.SyncAccountRsp{
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
