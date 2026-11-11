package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

const (
	defaultLedgerWindow = 7 * 24 * time.Hour
	defaultLedgerLimit  = 200
	// ledgerQueryTimeout 要明显小于网关（15 秒）和服务端的超时：到点时适配器返回已取到的部分，而不是被网关截断。
	ledgerQueryTimeout = 11 * time.Second
)

var (
	// errLedgerUnsupported：交易所没有统一流水接口（例如币安现货）。
	errLedgerUnsupported = errors.New("该账户不支持资金流水")
	// errLedgerUnavailable：账户会话还没建立（停用、凭证有问题或刚启动）。
	errLedgerUnavailable = errors.New("账户会话未就绪，暂时查不了资金流水")
)

// AdapterSource 按执行账户取当前会话的交易所适配器。
type AdapterSource interface {
	Adapter(string) (execution.ExecutionAdapter, error)
}

// FlattenTradingAccountFunc 只平组合账户里的一个执行账户。
type FlattenTradingAccountFunc func(ctx context.Context, spaceID, actionID, tradingAccountID, reason string) (store.OperatorActionRecord, error)

func (s *ConsoleServer) ledgerSource(tradingAccountID string) (execution.LedgerSource, error) {
	if s.Adapters == nil {
		return nil, errServiceUnavailable
	}
	adapter, err := s.Adapters.Adapter(tradingAccountID)
	if err != nil {
		return nil, errLedgerUnavailable
	}
	source, ok := adapter.(execution.LedgerSource)
	if !ok || !source.LedgerSupported() {
		return nil, errLedgerUnsupported
	}
	return source, nil
}

// ledgerCapability 把取流水来源的结果换成“能否查流水”：只有交易所确实没有流水接口才是 false。
// 会话还没建立等暂时性原因不算不支持，页面照常查询，由 ListAccountLedger 说明“会话未就绪”。
func ledgerCapability(sourceErr error) bool {
	return !errors.Is(sourceErr, errLedgerUnsupported)
}

// ListAccountLedger 从交易所即查资金流水（手续费、资金费、已实现盈亏、划转等），按时间倒序。
func (s *ConsoleServer) ListAccountLedger(
	ctx context.Context,
	req *tradepb.ListAccountLedgerReq,
) (*tradepb.ListAccountLedgerRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.ListAccountLedgerRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	if s.Store == nil {
		return &tradepb.ListAccountLedgerRsp{RetInfo: errorInfo(errServiceUnavailable)}, nil
	}
	st := s.Store.ReadOnly()
	// 先按空间读一次账户，保证只能查自己空间的账户。
	account, err := st.GetTradingAccount(ctx, spaceID, req.GetTradingAccountId())
	if err != nil {
		return &tradepb.ListAccountLedgerRsp{RetInfo: errorInfo(err)}, nil
	}
	market := marketFromPB(req.GetMarketType())
	if market != exchange.MarketTypeUnspecified && !account.SupportsMarket(string(market)) {
		return &tradepb.ListAccountLedgerRsp{RetInfo: invalidInfo(fmt.Errorf("账户没有开通市场 %s", market))}, nil
	}
	if market == exchange.MarketTypeUnspecified && len(account.Markets) == 1 {
		market = exchange.MarketType(account.Markets[0])
	}
	source, err := s.ledgerSource(req.GetTradingAccountId())
	if err != nil {
		return &tradepb.ListAccountLedgerRsp{RetInfo: errorInfo(err)}, nil
	}
	end := time.Now().UTC()
	if req.GetEndTime() > 0 {
		end = time.UnixMilli(req.GetEndTime()).UTC()
	}
	start := end.Add(-defaultLedgerWindow)
	if req.GetStartTime() > 0 {
		start = time.UnixMilli(req.GetStartTime()).UTC()
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = defaultLedgerLimit
	}
	queryCtx, cancel := context.WithTimeout(ctx, ledgerQueryTimeout)
	defer cancel()
	page, err := source.ListLedger(queryCtx, exchange.LedgerQuery{Market: market, Start: start, End: end, Limit: limit})
	if err != nil {
		return &tradepb.ListAccountLedgerRsp{RetInfo: errorInfo(err)}, nil
	}
	// 普通账户的钱包划转记在本地划转表里，合并进流水（流水本身仍然即查、不落库）。
	transfers, err := st.ListWalletTransfersByAccount(ctx, spaceID, req.GetTradingAccountId(), start.UnixMilli(), end.UnixMilli())
	if err != nil {
		return &tradepb.ListAccountLedgerRsp{RetInfo: errorInfo(err)}, nil
	}
	page.Entries, page.HasMore = mergeTransferEntries(page.Entries, transfers, limit, page.HasMore)
	rsp := &tradepb.ListAccountLedgerRsp{RetInfo: success(), HasMore: page.HasMore}
	for _, entry := range page.Entries {
		// 流水所属市场只给按市场区分的类型；划转、利息等账户级流水不属于某个市场，留空。
		entryMarket := tradepb.MarketType_MARKET_TYPE_UNSPECIFIED
		if entry.Market != exchange.MarketTypeUnspecified {
			entryMarket = marketToPB(string(entry.Market))
		} else if entry.Type.MarketScoped() {
			entryMarket = marketToPB(string(market))
		}
		rsp.Entries = append(rsp.Entries, &tradepb.LedgerEntry{
			Id: entry.ID, Time: entry.Time.UnixMilli(), Type: string(entry.Type), ExchangeType: entry.ExchangeType,
			Asset: entry.Asset, Amount: entry.Amount.String(), ExchangeSymbol: entry.ExchangeSymbol,
			TradeId: entry.TradeID, Info: entry.Info, MarketType: entryMarket,
		})
	}
	return rsp, nil
}

// mergeTransferEntries 把已完成的钱包划转合并进流水，按时间倒序取最新的 limit 条；
// 划转是账户级流水，不属于某个市场；方向写在说明里。多出来的部分标记还有更多。
func mergeTransferEntries(entries []exchange.LedgerEntry, transfers []store.WalletTransferRecord, limit int, hasMore bool) ([]exchange.LedgerEntry, bool) {
	merged := append([]exchange.LedgerEntry(nil), entries...)
	// 合约的收益流水（/fapi/v1/income）本来就带 TRANSFER 类型，流水号是划转的 tranId（ID 形如 “tranId:TRANSFER”）：
	// 交易所流水里已经有的划转不再合并本地记录，否则同一笔划转出现两次、正负号还不一样。
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.Type == exchange.LedgerTransfer {
			id, _, _ := strings.Cut(entry.ID, ":")
			seen[id] = true
		}
	}
	for _, transfer := range transfers {
		if transfer.State != store.TransferStateCompleted || (transfer.ExchangeTransferID != "" && seen[transfer.ExchangeTransferID]) {
			continue
		}
		amount, err := shared.ParseDecimal(transfer.Amount)
		if err != nil {
			continue
		}
		at := transfer.CompletedAt
		if at <= 0 {
			at = transfer.RequestedAt
		}
		info := "合约钱包 → 现货钱包"
		if transfer.Direction == store.TransferToFutures {
			info = "现货钱包 → 合约钱包（归集）"
		}
		merged = append(merged, exchange.LedgerEntry{
			ID: "transfer:" + transfer.TransferID, Time: time.UnixMilli(at).UTC(), Type: exchange.LedgerTransfer,
			ExchangeType: "WALLET_TRANSFER_" + transfer.Direction, Asset: transfer.Asset, Amount: amount, Info: info,
		})
	}
	exchange.SortLedgerNewestFirst(merged)
	if limit > 0 && len(merged) > limit {
		merged, hasMore = merged[:limit], true
	}
	return merged, hasMore
}

// FlattenTradingAccount 只平组合账户里的一个执行账户。
func (s *ConsoleServer) FlattenTradingAccount(
	ctx context.Context,
	req *tradepb.FlattenTradingAccountReq,
) (*tradepb.FlattenTradingAccountRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.FlattenTradingAccountRsp{RetInfo: invalidOrErrorInfo(err)}, nil
	}
	if s.FlattenAccount == nil || s.Store == nil {
		return &tradepb.FlattenTradingAccountRsp{RetInfo: errorInfo(errServiceUnavailable)}, nil
	}
	if _, err := s.Store.GetTradingAccount(ctx, spaceID, req.GetTradingAccountId()); err != nil {
		return &tradepb.FlattenTradingAccountRsp{RetInfo: errorInfo(err)}, nil
	}
	action, err := s.FlattenAccount(ctx, spaceID, req.GetActionId(), req.GetTradingAccountId(), req.GetReason())
	return &tradepb.FlattenTradingAccountRsp{RetInfo: errorInfo(err), Action: operatorActionToPB(action)}, nil
}
