package rpc

import (
	"context"
	"encoding/json"

	gonanoid "github.com/matoous/go-nanoid/v2"
	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	rebalanceapp "github.com/mooyang-code/moox/modules/trade/internal/application/rebalance"
	domainorder "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	domainrebalance "github.com/mooyang-code/moox/modules/trade/internal/domain/rebalance"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/observability"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	mooxpb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	trpc "trpc.group/trpc-go/trpc-go"
)

// Server 实现 trade 模块全部 9 个 tRPC service 接口，委托 service.Service。
// 一个 Server 实例同时满足 AccountSvcService/BalanceSvcService/.../PositionSvcService。
type Server struct {
	svc    *service.Service
	kernel *command.Engine
}

// New 创建 RPC handler。
func New(svc *service.Service, kernel ...*command.Engine) *Server {
	h := &Server{svc: svc}
	if len(kernel) > 0 {
		h.kernel = kernel[0]
	}
	return h
}

// 编译期断言：Server 实现各 service 接口。
var _ mooxpb.AccountSvcService = (*Server)(nil)
var _ mooxpb.BalanceSvcService = (*Server)(nil)
var _ mooxpb.FundSvcService = (*Server)(nil)
var _ mooxpb.ApiKeySvcService = (*Server)(nil)
var _ mooxpb.ChannelSvcService = (*Server)(nil)
var _ mooxpb.TradeOpSvcService = (*Server)(nil)
var _ mooxpb.OrderSvcService = (*Server)(nil)
var _ mooxpb.TradeQuerySvcService = (*Server)(nil)
var _ mooxpb.PositionSvcService = (*Server)(nil)
var _ mooxpb.RebalanceSvcService = (*Server)(nil)
var _ mooxpb.TradeOpsSvcService = (*Server)(nil)

func withRPCTrace(ctx context.Context) context.Context {
	return observability.WithTrace(ctx, observability.Trace{TraceID: string(trpc.GetMetaData(ctx, "trace_id")), RequestID: string(trpc.GetMetaData(ctx, "request_id"))})
}

func (h *Server) SetPause(ctx context.Context, req *mooxpb.SetTradePauseReq) (*mooxpb.SetTradePauseRsp, error) {
	if h.kernel == nil {
		return &mooxpb.SetTradePauseRsp{RetInfo: retInfo(mooxpb.ErrorCode_INNER_ERR, "trade kernel unavailable")}, nil
	}
	if req.GetTargetType() != "account" && req.GetTargetType() != "channel" {
		return &mooxpb.SetTradePauseRsp{RetInfo: retInfo(mooxpb.ErrorCode_INVALID_PARAM, "target_type must be account or channel")}, nil
	}
	if req.GetTargetId() == "" {
		return &mooxpb.SetTradePauseRsp{RetInfo: retInfo(mooxpb.ErrorCode_INVALID_PARAM, "target_id is required")}, nil
	}
	err := h.kernel.Store.SetControl(ctx, spaceID(ctx), store.ControlRecord{TargetType: req.GetTargetType(), TargetID: req.GetTargetId(), Paused: req.GetPaused(), Reason: req.GetReason()})
	if err != nil {
		return &mooxpb.SetTradePauseRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &mooxpb.SetTradePauseRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, "")}, nil
}

func (h *Server) ReconcileNow(ctx context.Context, req *mooxpb.ReconcileNowReq) (*mooxpb.ReconcileNowRsp, error) {
	ctx = withRPCTrace(ctx)
	if h.kernel == nil {
		return &mooxpb.ReconcileNowRsp{RetInfo: retInfo(mooxpb.ErrorCode_INNER_ERR, "trade kernel unavailable")}, nil
	}
	id, err := gonanoid.New()
	if err != nil {
		return &mooxpb.ReconcileNowRsp{RetInfo: errToRetInfo(err)}, nil
	}
	payload, err := json.Marshal(map[string]string{"space_id": spaceID(ctx), "account_id": req.GetAccountId(), "channel_id": req.GetChannelId()})
	if err == nil {
		err = h.kernel.Store.EnqueueOutbox(ctx, id, "moox.trade.reconciliation.requested.v1", payload)
	}
	if err != nil {
		return &mooxpb.ReconcileNowRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &mooxpb.ReconcileNowRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), MessageId: id}, nil
}

func (h *Server) InspectSaga(ctx context.Context, req *mooxpb.InspectSagaReq) (*mooxpb.InspectSagaRsp, error) {
	if h.kernel == nil {
		return &mooxpb.InspectSagaRsp{RetInfo: retInfo(mooxpb.ErrorCode_INNER_ERR, "trade kernel unavailable")}, nil
	}
	saga, err := h.kernel.Store.GetSaga(ctx, spaceID(ctx), req.GetSagaId())
	if err != nil {
		return &mooxpb.InspectSagaRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &mooxpb.InspectSagaRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), SagaId: saga.SagaID, State: saga.State, OrderId: saga.OrderID, ReplacementOrderId: saga.ReplacementOrderID, LastError: saga.LastError, Version: saga.Version}, nil
}

func (h *Server) CreateRebalance(ctx context.Context, req *mooxpb.CreateRebalanceReq) (*mooxpb.CreateRebalanceRsp, error) {
	ctx = withRPCTrace(ctx)
	if h.kernel == nil {
		return &mooxpb.CreateRebalanceRsp{RetInfo: retInfo(mooxpb.ErrorCode_INNER_ERR, "trade kernel unavailable")}, nil
	}
	sid := spaceID(ctx)
	targets := make([]domainrebalance.Target, 0, len(req.GetTargets()))
	for _, v := range req.GetTargets() {
		q, e := shared.ParseDecimal(v.GetQuantity())
		if e != nil {
			return &mooxpb.CreateRebalanceRsp{RetInfo: errToRetInfo(e)}, nil
		}
		targets = append(targets, domainrebalance.Target{Symbol: v.GetSymbol(), Quantity: q})
	}
	currents := make([]domainrebalance.Current, 0, len(req.GetCurrents()))
	for _, v := range req.GetCurrents() {
		q, e := shared.ParseDecimal(v.GetQuantity())
		if e != nil {
			return &mooxpb.CreateRebalanceRsp{RetInfo: errToRetInfo(e)}, nil
		}
		currents = append(currents, domainrebalance.Current{Symbol: v.GetSymbol(), Quantity: q})
	}
	markets := map[string]rebalanceapp.Market{}
	for _, v := range req.GetMarkets() {
		markets[v.GetSymbol()] = rebalanceapp.Market{MarketType: v.GetMarketType(), BaseAsset: v.GetBaseAsset(), QuoteAsset: v.GetQuoteAsset(), Price: v.GetPrice()}
	}
	mode := domainrebalance.TargetMode(req.GetTargetMode())
	if mode == "" {
		mode = domainrebalance.FullTarget
	}
	svc := rebalanceapp.Service{Store: h.kernel.Store, Engine: h.kernel}
	e := svc.Create(ctx, rebalanceapp.CreateInput{SpaceID: sid, RunID: req.GetRunId(), IdempotencyKey: req.GetIdempotencyKey(), AccountID: req.GetAccountId(), ChannelID: req.GetChannelId(), MarketSnapshotID: req.GetMarketSnapshotId(), PositionSnapshotID: req.GetPositionSnapshotId(), RulesVersion: req.GetRulesVersion(), Mode: mode, Targets: targets, Currents: currents, Markets: markets})
	if e != nil {
		return &mooxpb.CreateRebalanceRsp{RetInfo: errToRetInfo(e)}, nil
	}
	return &mooxpb.CreateRebalanceRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), RunId: req.GetRunId(), Status: "PLANNED"}, nil
}
func (h *Server) AdvanceRebalance(ctx context.Context, req *mooxpb.AdvanceRebalanceReq) (*mooxpb.AdvanceRebalanceRsp, error) {
	if h.kernel == nil {
		return &mooxpb.AdvanceRebalanceRsp{RetInfo: retInfo(mooxpb.ErrorCode_INNER_ERR, "trade kernel unavailable")}, nil
	}
	status, e := (rebalanceapp.Service{Store: h.kernel.Store, Engine: h.kernel}).Advance(ctx, spaceID(ctx), req.GetRunId(), req.GetAccountId(), req.GetChannelId())
	if e != nil {
		return &mooxpb.AdvanceRebalanceRsp{RetInfo: errToRetInfo(e)}, nil
	}
	return &mooxpb.AdvanceRebalanceRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), RunId: req.GetRunId(), Status: status}, nil
}

// ===== AccountSvc =====

func (h *Server) CreateAccount(ctx context.Context, req *mooxpb.CreateAccountReq) (*mooxpb.CreateAccountRsp, error) {
	sid := spaceID(ctx)
	a := &service.Account{
		UserID:       userID(ctx),
		AccountName:  req.GetAccountName(),
		AccountType:  accountTypeToDomain(req.GetAccountType()),
		ChannelID:    req.GetChannelId(),
		BaseCurrency: req.GetBaseCurrency(),
		Remark:       req.GetRemark(),
	}
	out, err := h.svc.Account.CreateAccount(ctx, sid, a)
	if err != nil {
		return &mooxpb.CreateAccountRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &mooxpb.CreateAccountRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), AccountId: out.AccountID, Account: accountToPB(out)}, nil
}

func (h *Server) UpdateAccount(ctx context.Context, req *mooxpb.UpdateAccountReq) (*mooxpb.UpdateAccountRsp, error) {
	sid := spaceID(ctx)
	a := &service.Account{
		AccountID:   req.GetAccountId(),
		AccountName: req.GetAccountName(),
		Status:      service.AccountStatus(req.GetStatus()),
		IsDefault:   req.GetIsDefault(),
		Remark:      req.GetRemark(),
	}
	if _, err := h.svc.Account.UpdateAccount(ctx, sid, a); err != nil {
		return &mooxpb.UpdateAccountRsp{RetInfo: errToRetInfo(err)}, nil
	}
	got, _ := h.svc.Account.GetAccount(ctx, sid, req.GetAccountId())
	return &mooxpb.UpdateAccountRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Account: accountToPB(got)}, nil
}

func (h *Server) DeleteAccount(ctx context.Context, req *mooxpb.DeleteAccountReq) (*mooxpb.DeleteAccountRsp, error) {
	sid := spaceID(ctx)
	if err := h.svc.Account.DeleteAccount(ctx, sid, req.GetAccountId()); err != nil {
		return &mooxpb.DeleteAccountRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &mooxpb.DeleteAccountRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, "")}, nil
}

func (h *Server) GetAccount(ctx context.Context, req *mooxpb.GetAccountReq) (*mooxpb.GetAccountRsp, error) {
	sid := spaceID(ctx)
	a, err := h.svc.Account.GetAccount(ctx, sid, req.GetAccountId())
	if err != nil {
		return &mooxpb.GetAccountRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &mooxpb.GetAccountRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Account: accountToPB(a)}, nil
}

func (h *Server) ListAccounts(ctx context.Context, req *mooxpb.ListAccountsReq) (*mooxpb.ListAccountsRsp, error) {
	sid := spaceID(ctx)
	f := service.AccountFilter{
		UserID:  req.GetUserId(),
		Keyword: req.GetKeyword(),
	}
	if req.AccountType != nil {
		f.AccountType = accountTypeToDomain(req.GetAccountType())
	}
	page := pageFromPB(req.GetPage()).Normalize()
	list, total, err := h.svc.Account.ListAccounts(ctx, sid, f, page)
	if err != nil {
		return &mooxpb.ListAccountsRsp{RetInfo: errToRetInfo(err)}, nil
	}
	out := make([]*mooxpb.Account, 0, len(list))
	for _, a := range list {
		out = append(out, accountToPB(a))
	}
	return &mooxpb.ListAccountsRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Accounts: out, PageResult: pageResult(page, total)}, nil
}

func (h *Server) SyncExchangeAccounts(ctx context.Context, req *mooxpb.SyncExchangeAccountsReq) (*mooxpb.SyncExchangeAccountsRsp, error) {
	sid := spaceID(ctx)
	list, err := h.svc.SyncExchangeAccountsWithSnapshots(ctx, sid, service.SyncExchangeAccountsOptions{
		UserID:     userID(ctx),
		Provider:   req.GetProvider(),
		MarketType: req.GetMarketType(),
	})
	if err != nil {
		return &mooxpb.SyncExchangeAccountsRsp{RetInfo: errToRetInfo(err)}, nil
	}
	if h.kernel != nil {
		for _, account := range list {
			bs, balanceErr := h.svc.Account.GetBalances(ctx, sid, account.AccountID, nil)
			if balanceErr != nil {
				return &mooxpb.SyncExchangeAccountsRsp{RetInfo: errToRetInfo(balanceErr)}, nil
			}
			desired := map[string]map[string]shared.Decimal{}
			for _, b := range bs {
				available, e := shared.ParseDecimal(b.Available)
				if e != nil {
					return &mooxpb.SyncExchangeAccountsRsp{RetInfo: errToRetInfo(e)}, nil
				}
				frozen, e := shared.ParseDecimal(b.Frozen)
				if e != nil {
					return &mooxpb.SyncExchangeAccountsRsp{RetInfo: errToRetInfo(e)}, nil
				}
				desired[b.Currency] = map[string]shared.Decimal{"available": available, "frozen": frozen}
			}
			if balanceErr = h.kernel.Store.ReconcileBalances(ctx, sid, account.AccountID, desired); balanceErr != nil {
				return &mooxpb.SyncExchangeAccountsRsp{RetInfo: errToRetInfo(balanceErr)}, nil
			}
		}
	}
	out := make([]*mooxpb.Account, 0, len(list))
	for _, a := range list {
		out = append(out, accountToPB(a))
	}
	return &mooxpb.SyncExchangeAccountsRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Accounts: out}, nil
}

// ===== BalanceSvc =====

func (h *Server) GetBalances(ctx context.Context, req *mooxpb.GetBalancesReq) (*mooxpb.GetBalancesRsp, error) {
	sid := spaceID(ctx)
	if h.kernel != nil {
		rows, err := h.kernel.Store.ListBalances(ctx, sid, req.GetAccountId())
		if err != nil {
			return &mooxpb.GetBalancesRsp{RetInfo: errToRetInfo(err)}, nil
		}
		type pair struct{ available, frozen, margin shared.Decimal }
		m := map[string]pair{}
		for _, r := range rows {
			p := m[r.Asset]
			v, e := shared.ParseDecimal(r.Amount)
			if e != nil {
				return &mooxpb.GetBalancesRsp{RetInfo: errToRetInfo(e)}, nil
			}
			if r.Bucket == "available" {
				p.available = v
			} else if r.Bucket == "frozen" {
				p.frozen = v
			} else if r.Bucket == "margin" {
				p.margin = v
			}
			m[r.Asset] = p
		}
		out := make([]*mooxpb.Balance, 0, len(m))
		for asset, p := range m {
			locked := p.frozen.Add(p.margin)
			out = append(out, &mooxpb.Balance{AccountId: req.GetAccountId(), Currency: asset, Available: p.available.String(), Frozen: locked.String(), Total: p.available.Add(locked).String()})
		}
		return &mooxpb.GetBalancesRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Balances: out}, nil
	}
	bs, err := h.svc.Account.GetBalances(ctx, sid, req.GetAccountId(), req.GetCurrencies())
	if err != nil {
		return &mooxpb.GetBalancesRsp{RetInfo: errToRetInfo(err)}, nil
	}
	out := make([]*mooxpb.Balance, 0, len(bs))
	for _, b := range bs {
		out = append(out, balanceToPB(b))
	}
	return &mooxpb.GetBalancesRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Balances: out}, nil
}

func (h *Server) SyncBalances(ctx context.Context, req *mooxpb.SyncBalancesReq) (*mooxpb.SyncBalancesRsp, error) {
	sid := spaceID(ctx)
	bs, err := h.svc.Account.SyncBalances(ctx, sid, req.GetAccountId())
	if err != nil {
		return &mooxpb.SyncBalancesRsp{RetInfo: errToRetInfo(err)}, nil
	}
	if h.kernel != nil {
		desired := map[string]map[string]shared.Decimal{}
		for _, b := range bs {
			available, e := shared.ParseDecimal(b.Available)
			if e != nil {
				return &mooxpb.SyncBalancesRsp{RetInfo: errToRetInfo(e)}, nil
			}
			frozen, e := shared.ParseDecimal(b.Frozen)
			if e != nil {
				return &mooxpb.SyncBalancesRsp{RetInfo: errToRetInfo(e)}, nil
			}
			desired[b.Currency] = map[string]shared.Decimal{"available": available, "frozen": frozen}
		}
		if err = h.kernel.Store.ReconcileBalances(ctx, sid, req.GetAccountId(), desired); err != nil {
			return &mooxpb.SyncBalancesRsp{RetInfo: errToRetInfo(err)}, nil
		}
	}
	out := make([]*mooxpb.Balance, 0, len(bs))
	for _, b := range bs {
		out = append(out, balanceToPB(b))
	}
	return &mooxpb.SyncBalancesRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Balances: out}, nil
}

// ===== FundSvc =====

func (h *Server) ListFundFlows(ctx context.Context, req *mooxpb.ListFundFlowsReq) (*mooxpb.ListFundFlowsRsp, error) {
	sid := spaceID(ctx)
	f := service.FundFlowFilter{
		AccountID: req.GetAccountId(),
		Currency:  req.GetCurrency(),
		BizType:   req.GetBizType(),
		StartTime: req.GetStartTime(),
		EndTime:   req.GetEndTime(),
	}
	page := pageFromPB(req.GetPage()).Normalize()
	list, total, err := h.svc.Account.ListFundFlows(ctx, sid, f, page)
	if err != nil {
		return &mooxpb.ListFundFlowsRsp{RetInfo: errToRetInfo(err)}, nil
	}
	out := make([]*mooxpb.FundFlow, 0, len(list))
	for _, fl := range list {
		out = append(out, fundFlowToPB(fl))
	}
	return &mooxpb.ListFundFlowsRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Flows: out, PageResult: pageResult(page, total)}, nil
}

func (h *Server) Transfer(ctx context.Context, req *mooxpb.TransferReq) (*mooxpb.TransferRsp, error) {
	sid := spaceID(ctx)
	outID, inID, err := h.svc.Account.Transfer(ctx, sid, req.GetFromAccountId(), req.GetToAccountId(), req.GetCurrency(), req.GetAmount(), req.GetRemark())
	if err != nil {
		return &mooxpb.TransferRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &mooxpb.TransferRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), OutFlowId: outID, InFlowId: inID}, nil
}

// ===== ApiKeySvc =====

func (h *Server) CreateApiKey(ctx context.Context, req *mooxpb.CreateApiKeyReq) (*mooxpb.CreateApiKeyRsp, error) {
	sid := spaceID(ctx)
	k := &service.APIKey{
		AccountID:      req.GetAccountId(),
		Exchange:       req.GetExchange(),
		APIKey:         req.GetApiKey(),
		APISecret:      req.GetApiSecret(),
		Passphrase:     req.GetPassphrase(),
		PermissionsRaw: req.GetPermissions(),
	}
	id, err := h.svc.Account.CreateAPIKey(ctx, sid, k)
	if err != nil {
		return &mooxpb.CreateApiKeyRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &mooxpb.CreateApiKeyRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), ApiKeyId: id}, nil
}

func (h *Server) DeleteApiKey(ctx context.Context, req *mooxpb.DeleteApiKeyReq) (*mooxpb.DeleteApiKeyRsp, error) {
	sid := spaceID(ctx)
	if err := h.svc.Account.DeleteAPIKey(ctx, sid, req.GetApiKeyId()); err != nil {
		return &mooxpb.DeleteApiKeyRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &mooxpb.DeleteApiKeyRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, "")}, nil
}

func (h *Server) ListApiKeys(ctx context.Context, req *mooxpb.ListApiKeysReq) (*mooxpb.ListApiKeysRsp, error) {
	sid := spaceID(ctx)
	list, err := h.svc.Account.ListAPIKeys(ctx, sid, req.GetAccountId())
	if err != nil {
		return &mooxpb.ListApiKeysRsp{RetInfo: errToRetInfo(err)}, nil
	}
	out := make([]*mooxpb.ApiKey, 0, len(list))
	for _, k := range list {
		out = append(out, apiKeyToPB(k))
	}
	return &mooxpb.ListApiKeysRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), ApiKeys: out}, nil
}

// ===== ChannelSvc =====

func (h *Server) CreateChannel(ctx context.Context, req *mooxpb.CreateChannelReq) (*mooxpb.CreateChannelRsp, error) {
	sid := spaceID(ctx)
	c := &service.TradeChannel{
		ChannelName: req.GetChannelName(),
		Exchange:    req.GetExchange(),
		MarketType:  marketTypeToDomain(req.GetMarketType()),
		AccountID:   req.GetAccountId(),
		APIKeyID:    req.GetApiKeyId(),
		Endpoint:    req.GetEndpoint(),
		IsSimulated: req.GetIsSimulated(),
		RateLimit:   int(req.GetRateLimit()),
	}
	id, err := h.svc.Order.CreateChannel(ctx, sid, c)
	if err != nil {
		return &mooxpb.CreateChannelRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &mooxpb.CreateChannelRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), ChannelId: id}, nil
}

func (h *Server) UpdateChannel(ctx context.Context, req *mooxpb.UpdateChannelReq) (*mooxpb.UpdateChannelRsp, error) {
	sid := spaceID(ctx)
	c := &service.TradeChannel{
		ChannelID:   req.GetChannelId(),
		ChannelName: req.GetChannelName(),
		Status:      int(req.GetStatus()),
		Endpoint:    req.GetEndpoint(),
		RateLimit:   int(req.GetRateLimit()),
	}
	if err := h.svc.Order.UpdateChannel(ctx, sid, c); err != nil {
		return &mooxpb.UpdateChannelRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &mooxpb.UpdateChannelRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, "")}, nil
}

func (h *Server) DeleteChannel(ctx context.Context, req *mooxpb.DeleteChannelReq) (*mooxpb.DeleteChannelRsp, error) {
	sid := spaceID(ctx)
	if err := h.svc.Order.DeleteChannel(ctx, sid, req.GetChannelId()); err != nil {
		return &mooxpb.DeleteChannelRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &mooxpb.DeleteChannelRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, "")}, nil
}

func (h *Server) ListChannels(ctx context.Context, req *mooxpb.ListChannelsReq) (*mooxpb.ListChannelsRsp, error) {
	sid := spaceID(ctx)
	f := service.ChannelFilter{AccountID: req.GetAccountId(), Exchange: req.GetExchange()}
	page := pageFromPB(req.GetPage()).Normalize()
	list, total, err := h.svc.Order.ListChannels(ctx, sid, f, page)
	if err != nil {
		return &mooxpb.ListChannelsRsp{RetInfo: errToRetInfo(err)}, nil
	}
	out := make([]*mooxpb.TradeChannel, 0, len(list))
	for _, c := range list {
		out = append(out, channelToPB(c))
	}
	return &mooxpb.ListChannelsRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Channels: out, PageResult: pageResult(page, total)}, nil
}

func (h *Server) TestChannel(ctx context.Context, req *mooxpb.TestChannelReq) (*mooxpb.TestChannelRsp, error) {
	sid := spaceID(ctx)
	reachable, latency, err := h.svc.Order.TestChannel(ctx, sid, req.GetChannelId())
	if err != nil {
		return &mooxpb.TestChannelRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &mooxpb.TestChannelRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Reachable: reachable, LatencyMs: int32(latency)}, nil
}

func (h *Server) ListInstruments(ctx context.Context, req *mooxpb.ListInstrumentsReq) (*mooxpb.ListInstrumentsRsp, error) {
	sid := spaceID(ctx)
	var market exchange.MarketType
	if req.MarketType != nil {
		market = exchange.MarketType(marketTypeToDomain(req.GetMarketType()))
	}
	list, err := h.svc.Order.ListInstruments(ctx, sid, req.GetChannelId(), market)
	if err != nil {
		return &mooxpb.ListInstrumentsRsp{RetInfo: errToRetInfo(err)}, nil
	}
	out := make([]*mooxpb.Instrument, 0, len(list))
	for _, ins := range list {
		out = append(out, instrumentToPB(ins))
	}
	return &mooxpb.ListInstrumentsRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Instruments: out}, nil
}

// ===== TradeOpSvc =====

func (h *Server) PlaceOrder(ctx context.Context, req *mooxpb.PlaceOrderReq) (*mooxpb.PlaceOrderRsp, error) {
	ctx = withRPCTrace(ctx)
	sid := spaceID(ctx)
	if h.kernel != nil {
		orderID, _ := gonanoid.New()
		clientID := req.GetClientOrderId()
		if clientID == "" {
			clientID = orderID
		}
		side := "BUY"
		if req.GetSide() == mooxpb.OrderSide_ORDER_SIDE_SELL {
			side = "SELL"
		}
		o, err := h.kernel.Place(ctx, command.PlaceInput{SpaceID: sid, OrderID: orderID, ClientOrderID: clientID, AccountID: req.GetAccountId(), ChannelID: req.GetChannelId(), Symbol: req.GetSymbol(), MarketType: marketTypeToDomain(req.GetMarketType()), Side: side, Quantity: req.GetQuantity(), Price: req.GetPrice(), ReduceOnly: req.GetReduceOnly()})
		if err != nil {
			return &mooxpb.PlaceOrderRsp{RetInfo: errToRetInfo(err)}, nil
		}
		return &mooxpb.PlaceOrderRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), OrderId: o.OrderID, ExchangeOrderId: o.ExchangeOrderID, Status: kernelStatusToPB(domainorder.State(o.State))}, nil
	}
	ctx = service.WithOperator(ctx, userID(ctx))
	xreq := &exchange.PlaceOrderReq{
		Market:        exchange.MarketType(marketTypeToDomain(req.GetMarketType())),
		Symbol:        req.GetSymbol(),
		Side:          exchange.OrderSide(orderSideToDomain(req.GetSide())),
		PosSide:       req.GetPosSide(),
		Type:          exchange.OrderType(orderTypeToDomain(req.GetOrderType())),
		TimeInForce:   req.GetTimeInForce(),
		Price:         req.GetPrice(),
		Quantity:      req.GetQuantity(),
		Amount:        req.GetAmount(),
		ClientOrderID: req.GetClientOrderId(),
		ReduceOnly:    req.GetReduceOnly(),
		TriggerPrice:  req.GetTriggerPrice(),
	}
	o, err := h.svc.Order.PlaceOrder(ctx, sid, req.GetChannelId(), xreq)
	if err != nil {
		return &mooxpb.PlaceOrderRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &mooxpb.PlaceOrderRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), OrderId: o.OrderID, ExchangeOrderId: o.ExchangeOrderID, Status: orderStatusToPB(o.Status)}, nil
}

func kernelStatusToPB(s domainorder.State) mooxpb.OrderStatus {
	switch s {
	case domainorder.Open, domainorder.Submitting, domainorder.SubmitUnknown, domainorder.Canceling, domainorder.CancelUnknown:
		return mooxpb.OrderStatus_ORDER_STATUS_SUBMITTED
	case domainorder.PartiallyFilled:
		return mooxpb.OrderStatus_ORDER_STATUS_PARTIALLY_FILLED
	case domainorder.Filled:
		return mooxpb.OrderStatus_ORDER_STATUS_FILLED
	case domainorder.Canceled:
		return mooxpb.OrderStatus_ORDER_STATUS_CANCELED
	case domainorder.PartiallyCanceled:
		return mooxpb.OrderStatus_ORDER_STATUS_PARTIAL_CANCELED
	case domainorder.Rejected:
		return mooxpb.OrderStatus_ORDER_STATUS_REJECTED
	case domainorder.Expired:
		return mooxpb.OrderStatus_ORDER_STATUS_EXPIRED
	default:
		return mooxpb.OrderStatus_ORDER_STATUS_PENDING
	}
}
func kernelOrderToPB(o store.OrderRecord) *mooxpb.Order {
	side := mooxpb.OrderSide_ORDER_SIDE_BUY
	if o.Side == "SELL" {
		side = mooxpb.OrderSide_ORDER_SIDE_SELL
	}
	return &mooxpb.Order{OrderId: o.OrderID, ClientOrderId: o.ClientOrderID, ExchangeOrderId: o.ExchangeOrderID, AccountId: o.AccountID, ChannelId: o.ChannelID, Symbol: o.Symbol, Side: side, OrderType: mooxpb.OrderType_ORDER_TYPE_LIMIT, TimeInForce: "IOC", Price: o.Price, Quantity: o.Quantity, FilledQty: o.FilledQuantity, Status: kernelStatusToPB(domainorder.State(o.State)), ReduceOnly: o.ReduceOnly}
}

func (h *Server) CancelOrder(ctx context.Context, req *mooxpb.CancelOrderReq) (*mooxpb.CancelOrderRsp, error) {
	ctx = withRPCTrace(ctx)
	sid := spaceID(ctx)
	if h.kernel != nil {
		o, err := h.kernel.Cancel(ctx, sid, req.GetOrderId())
		if err != nil {
			return &mooxpb.CancelOrderRsp{RetInfo: errToRetInfo(err)}, nil
		}
		return &mooxpb.CancelOrderRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Status: kernelStatusToPB(domainorder.State(o.State))}, nil
	}
	ctx = service.WithOperator(ctx, userID(ctx))
	xreq := &exchange.CancelOrderReq{
		OrderID:       req.GetOrderId(),
		ClientOrderID: req.GetClientOrderId(),
	}
	o, err := h.svc.Order.CancelOrder(ctx, sid, req.GetChannelId(), xreq)
	if err != nil {
		return &mooxpb.CancelOrderRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &mooxpb.CancelOrderRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Status: orderStatusToPB(o.Status)}, nil
}

func (h *Server) CancelAllOrders(ctx context.Context, req *mooxpb.CancelAllOrdersReq) (*mooxpb.CancelAllOrdersRsp, error) {
	sid := spaceID(ctx)
	if h.kernel != nil {
		orders, err := h.kernel.Store.ListOrders(ctx, sid, "", req.GetChannelId(), req.GetSymbol(), true)
		if err != nil {
			return &mooxpb.CancelAllOrdersRsp{RetInfo: errToRetInfo(err)}, nil
		}
		var n int32
		for _, o := range orders {
			if _, err = h.kernel.Cancel(ctx, sid, o.OrderID); err != nil {
				return &mooxpb.CancelAllOrdersRsp{RetInfo: errToRetInfo(err), CanceledCount: n}, nil
			}
			n++
		}
		return &mooxpb.CancelAllOrdersRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), CanceledCount: n}, nil
	}
	n, err := h.svc.Order.CancelAllOrders(ctx, sid, req.GetChannelId(), req.GetSymbol())
	if err != nil {
		return &mooxpb.CancelAllOrdersRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &mooxpb.CancelAllOrdersRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), CanceledCount: int32(n)}, nil
}

func (h *Server) AmendOrder(ctx context.Context, req *mooxpb.AmendOrderReq) (*mooxpb.AmendOrderRsp, error) {
	ctx = withRPCTrace(ctx)
	sid := spaceID(ctx)
	if h.kernel != nil {
		old, err := h.kernel.Store.GetOrder(ctx, sid, req.GetOrderId())
		if err != nil {
			return &mooxpb.AmendOrderRsp{RetInfo: errToRetInfo(err)}, nil
		}
		price, qty := req.GetNewPrice(), req.GetNewQuantity()
		if price == "" {
			price = old.Price
		}
		if qty == "" {
			qty = old.Quantity
		}
		replacementID, _ := gonanoid.New()
		clientID, _ := gonanoid.New()
		sagaID, _ := gonanoid.New()
		saga, err := h.kernel.Replace(ctx, sagaID, old.OrderID, command.PlaceInput{SpaceID: sid, OrderID: replacementID, ClientOrderID: clientID, AccountID: old.AccountID, ChannelID: old.ChannelID, Symbol: old.Symbol, MarketType: old.MarketType, BaseAsset: old.BaseAsset, QuoteAsset: old.QuoteAsset, Side: old.Side, Quantity: qty, Price: price, ReduceOnly: old.ReduceOnly})
		if err != nil {
			return &mooxpb.AmendOrderRsp{RetInfo: errToRetInfo(err)}, nil
		}
		_ = saga
		return &mooxpb.AmendOrderRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Status: mooxpb.OrderStatus_ORDER_STATUS_PENDING}, nil
	}
	ctx = service.WithOperator(ctx, userID(ctx))
	xreq := &exchange.AmendOrderReq{
		OrderID:     req.GetOrderId(),
		NewPrice:    req.GetNewPrice(),
		NewQuantity: req.GetNewQuantity(),
	}
	o, err := h.svc.Order.AmendOrder(ctx, sid, req.GetChannelId(), xreq)
	if err != nil {
		return &mooxpb.AmendOrderRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &mooxpb.AmendOrderRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Status: orderStatusToPB(o.Status)}, nil
}

func (h *Server) SetLeverage(ctx context.Context, req *mooxpb.SetLeverageReq) (*mooxpb.SetLeverageRsp, error) {
	sid := spaceID(ctx)
	if err := h.svc.Order.SetLeverage(ctx, sid, req.GetChannelId(), req.GetSymbol(), req.GetLeverage()); err != nil {
		return &mooxpb.SetLeverageRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &mooxpb.SetLeverageRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, "")}, nil
}

func (h *Server) ConvertDust(ctx context.Context, req *mooxpb.ConvertDustReq) (*mooxpb.ConvertDustRsp, error) {
	sid := spaceID(ctx)
	out, err := h.svc.Order.ConvertDust(ctx, sid, req.GetChannelId(), req.GetAssets())
	if err != nil {
		return &mooxpb.ConvertDustRsp{RetInfo: errToRetInfo(err)}, nil
	}
	if req.GetAccountId() != "" {
		_, _ = h.svc.Account.SyncBalances(ctx, sid, req.GetAccountId())
	}
	rsp := &mooxpb.ConvertDustRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, "")}
	if out == nil {
		return rsp, nil
	}
	rsp.TotalServiceCharge = out.TotalServiceCharge
	rsp.TotalTransfered = out.TotalTransfered
	rsp.Results = make([]*mooxpb.DustTransferItem, 0, len(out.Results))
	for _, item := range out.Results {
		rsp.Results = append(rsp.Results, dustTransferItemToPB(item))
	}
	rsp.SkippedResults = make([]*mooxpb.DustTransferSkippedItem, 0, len(out.Skipped))
	for _, item := range out.Skipped {
		rsp.SkippedResults = append(rsp.SkippedResults, dustTransferSkippedItemToPB(item))
	}
	return rsp, nil
}

// ===== OrderSvc =====

func (h *Server) GetOrder(ctx context.Context, req *mooxpb.GetOrderReq) (*mooxpb.GetOrderRsp, error) {
	sid := spaceID(ctx)
	if h.kernel != nil {
		o, err := h.kernel.Store.GetOrder(ctx, sid, req.GetOrderId())
		if err != nil && req.GetClientOrderId() != "" {
			o, err = h.kernel.Store.GetOrderByClientID(ctx, sid, req.GetClientOrderId())
		}
		if err != nil {
			return &mooxpb.GetOrderRsp{RetInfo: errToRetInfo(err)}, nil
		}
		return &mooxpb.GetOrderRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Order: kernelOrderToPB(o)}, nil
	}
	o, err := h.svc.Order.GetOrder(ctx, sid, req.GetOrderId(), req.GetClientOrderId())
	if err != nil {
		return &mooxpb.GetOrderRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &mooxpb.GetOrderRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Order: orderToPB(o)}, nil
}

func (h *Server) ListOrders(ctx context.Context, req *mooxpb.ListOrdersReq) (*mooxpb.ListOrdersRsp, error) {
	sid := spaceID(ctx)
	if h.kernel != nil {
		rows, err := h.kernel.Store.ListOrders(ctx, sid, req.GetAccountId(), req.GetChannelId(), req.GetSymbol(), req.GetOnlyOpen())
		if err != nil {
			return &mooxpb.ListOrdersRsp{RetInfo: errToRetInfo(err)}, nil
		}
		out := make([]*mooxpb.Order, len(rows))
		for i, r := range rows {
			out[i] = kernelOrderToPB(r)
		}
		page := pageFromPB(req.GetPage()).Normalize()
		return &mooxpb.ListOrdersRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Orders: out, PageResult: pageResult(page, len(out))}, nil
	}
	f := service.OrderFilter{
		AccountID: req.GetAccountId(),
		ChannelID: req.GetChannelId(),
		Symbol:    req.GetSymbol(),
		Status:    int(req.GetStatus()),
		OnlyOpen:  req.GetOnlyOpen(),
		StartTime: req.GetStartTime(),
		EndTime:   req.GetEndTime(),
	}
	page := pageFromPB(req.GetPage()).Normalize()
	list, total, err := h.svc.Order.ListOrders(ctx, sid, f, page)
	if err != nil {
		return &mooxpb.ListOrdersRsp{RetInfo: errToRetInfo(err)}, nil
	}
	out := make([]*mooxpb.Order, 0, len(list))
	for _, o := range list {
		out = append(out, orderToPB(o))
	}
	return &mooxpb.ListOrdersRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Orders: out, PageResult: pageResult(page, total)}, nil
}

func (h *Server) SyncOrders(ctx context.Context, req *mooxpb.SyncOrdersReq) (*mooxpb.SyncOrdersRsp, error) {
	sid := spaceID(ctx)
	page := pageFromPB(req.GetPage()).Normalize()
	list, total, err := h.svc.Order.SyncOrders(ctx, sid, req.GetAccountId(), req.GetSymbol(), req.GetOnlyOpen(), req.GetStartTime(), req.GetEndTime(), page)
	if err != nil {
		return &mooxpb.SyncOrdersRsp{RetInfo: errToRetInfo(err)}, nil
	}
	out := make([]*mooxpb.Order, 0, len(list))
	for _, o := range list {
		out = append(out, orderToPB(o))
	}
	return &mooxpb.SyncOrdersRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Orders: out, PageResult: pageResult(page, total)}, nil
}

// ===== TradeQuerySvc =====

func (h *Server) ListTrades(ctx context.Context, req *mooxpb.ListTradesReq) (*mooxpb.ListTradesRsp, error) {
	sid := spaceID(ctx)
	if h.kernel != nil {
		rows, err := h.kernel.Store.ListFills(ctx, sid, req.GetOrderId())
		if err != nil {
			return &mooxpb.ListTradesRsp{RetInfo: errToRetInfo(err)}, nil
		}
		out := make([]*mooxpb.Trade, len(rows))
		for i, r := range rows {
			out[i] = &mooxpb.Trade{TradeId: r.FillID, ExchangeTradeId: r.ExchangeTradeID, OrderId: r.OrderID, Price: r.Price, Quantity: r.Quantity, Fee: r.Fee, FeeCurrency: r.FeeAsset}
		}
		page := pageFromPB(req.GetPage()).Normalize()
		return &mooxpb.ListTradesRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Trades: out, PageResult: pageResult(page, len(out))}, nil
	}
	f := service.TradeFilter{
		AccountID: req.GetAccountId(),
		OrderID:   req.GetOrderId(),
		Symbol:    req.GetSymbol(),
		StartTime: req.GetStartTime(),
		EndTime:   req.GetEndTime(),
	}
	page := pageFromPB(req.GetPage()).Normalize()
	list, total, err := h.svc.Order.ListTrades(ctx, sid, f, page)
	if err != nil {
		return &mooxpb.ListTradesRsp{RetInfo: errToRetInfo(err)}, nil
	}
	out := make([]*mooxpb.Trade, 0, len(list))
	for _, t := range list {
		out = append(out, tradeToPB(t))
	}
	return &mooxpb.ListTradesRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Trades: out, PageResult: pageResult(page, total)}, nil
}

func (h *Server) SyncTrades(ctx context.Context, req *mooxpb.SyncTradesReq) (*mooxpb.SyncTradesRsp, error) {
	sid := spaceID(ctx)
	page := pageFromPB(req.GetPage()).Normalize()
	list, total, err := h.svc.Order.SyncTrades(ctx, sid, req.GetAccountId(), req.GetSymbol(), req.GetOrderId(), req.GetStartTime(), req.GetEndTime(), page)
	if err != nil {
		return &mooxpb.SyncTradesRsp{RetInfo: errToRetInfo(err)}, nil
	}
	out := make([]*mooxpb.Trade, 0, len(list))
	for _, tr := range list {
		out = append(out, tradeToPB(tr))
	}
	return &mooxpb.SyncTradesRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Trades: out, PageResult: pageResult(page, total)}, nil
}

// ===== PositionSvc =====

func (h *Server) ListPositions(ctx context.Context, req *mooxpb.ListPositionsReq) (*mooxpb.ListPositionsRsp, error) {
	sid := spaceID(ctx)
	if h.kernel != nil {
		rows, err := h.kernel.Store.ListPositions(ctx, sid, req.GetAccountId(), req.GetSymbol())
		if err != nil {
			return &mooxpb.ListPositionsRsp{RetInfo: errToRetInfo(err)}, nil
		}
		out := make([]*mooxpb.Position, len(rows))
		for i, r := range rows {
			out[i] = &mooxpb.Position{AccountId: r.AccountID, Symbol: r.Symbol, Quantity: r.Quantity, AvgPrice: r.AveragePrice, RealizedPnl: r.RealizedPnL}
		}
		return &mooxpb.ListPositionsRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Positions: out}, nil
	}
	list, err := h.svc.Order.ListPositions(ctx, sid, req.GetAccountId(), req.GetSymbol())
	if err != nil {
		return &mooxpb.ListPositionsRsp{RetInfo: errToRetInfo(err)}, nil
	}
	out := make([]*mooxpb.Position, 0, len(list))
	for _, p := range list {
		out = append(out, positionToPB(p))
	}
	return &mooxpb.ListPositionsRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Positions: out}, nil
}

func (h *Server) SyncPositions(ctx context.Context, req *mooxpb.SyncPositionsReq) (*mooxpb.SyncPositionsRsp, error) {
	sid := spaceID(ctx)
	list, err := h.svc.Order.SyncPositions(ctx, sid, req.GetAccountId(), req.GetSymbol())
	if err != nil {
		return &mooxpb.SyncPositionsRsp{RetInfo: errToRetInfo(err)}, nil
	}
	out := make([]*mooxpb.Position, 0, len(list))
	for _, p := range list {
		out = append(out, positionToPB(p))
	}
	return &mooxpb.SyncPositionsRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Positions: out}, nil
}
