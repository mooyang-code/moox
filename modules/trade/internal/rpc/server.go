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
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
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
var _ tradepb.AccountSvcService = (*Server)(nil)
var _ tradepb.BalanceSvcService = (*Server)(nil)
var _ tradepb.FundSvcService = (*Server)(nil)
var _ tradepb.ApiKeySvcService = (*Server)(nil)
var _ tradepb.ChannelSvcService = (*Server)(nil)
var _ tradepb.TradeOpSvcService = (*Server)(nil)
var _ tradepb.OrderSvcService = (*Server)(nil)
var _ tradepb.TradeQuerySvcService = (*Server)(nil)
var _ tradepb.PositionSvcService = (*Server)(nil)
var _ tradepb.RebalanceSvcService = (*Server)(nil)
var _ tradepb.TradeOpsSvcService = (*Server)(nil)

func withRPCTrace(ctx context.Context) context.Context {
	return telemetry.WithTrace(ctx, telemetry.Trace{TraceID: string(trpc.GetMetaData(ctx, "trace_id")), RequestID: string(trpc.GetMetaData(ctx, "request_id"))})
}

func (h *Server) SetPause(ctx context.Context, req *tradepb.SetTradePauseReq) (*tradepb.SetTradePauseRsp, error) {
	if h.kernel == nil {
		return &tradepb.SetTradePauseRsp{RetInfo: retInfo(tradepb.ErrorCode_INNER_ERR, "trade kernel unavailable")}, nil
	}
	if req.GetTargetType() != "account" && req.GetTargetType() != "channel" {
		return &tradepb.SetTradePauseRsp{RetInfo: retInfo(tradepb.ErrorCode_INVALID_PARAM, "target_type must be account or channel")}, nil
	}
	if req.GetTargetId() == "" {
		return &tradepb.SetTradePauseRsp{RetInfo: retInfo(tradepb.ErrorCode_INVALID_PARAM, "target_id is required")}, nil
	}
	err := h.kernel.Store.SetControl(ctx, spaceID(ctx), store.ControlRecord{TargetType: req.GetTargetType(), TargetID: req.GetTargetId(), Paused: req.GetPaused(), Reason: req.GetReason()})
	if err != nil {
		return &tradepb.SetTradePauseRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &tradepb.SetTradePauseRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, "")}, nil
}

func (h *Server) ReconcileNow(ctx context.Context, req *tradepb.ReconcileNowReq) (*tradepb.ReconcileNowRsp, error) {
	ctx = withRPCTrace(ctx)
	if h.kernel == nil {
		return &tradepb.ReconcileNowRsp{RetInfo: retInfo(tradepb.ErrorCode_INNER_ERR, "trade kernel unavailable")}, nil
	}
	id, err := gonanoid.New()
	if err != nil {
		return &tradepb.ReconcileNowRsp{RetInfo: errToRetInfo(err)}, nil
	}
	payload, err := json.Marshal(map[string]string{"space_id": spaceID(ctx), "account_id": req.GetAccountId(), "channel_id": req.GetChannelId()})
	if err == nil {
		err = h.kernel.Store.EnqueueOutbox(ctx, id, "moox.trade.reconciliation.requested.v1", payload)
	}
	if err != nil {
		return &tradepb.ReconcileNowRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &tradepb.ReconcileNowRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), MessageId: id}, nil
}

func (h *Server) InspectSaga(ctx context.Context, req *tradepb.InspectSagaReq) (*tradepb.InspectSagaRsp, error) {
	if h.kernel == nil {
		return &tradepb.InspectSagaRsp{RetInfo: retInfo(tradepb.ErrorCode_INNER_ERR, "trade kernel unavailable")}, nil
	}
	saga, err := h.kernel.Store.GetSaga(ctx, spaceID(ctx), req.GetSagaId())
	if err != nil {
		return &tradepb.InspectSagaRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &tradepb.InspectSagaRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), SagaId: saga.SagaID, State: saga.State, OrderId: saga.OrderID, ReplacementOrderId: saga.ReplacementOrderID, LastError: saga.LastError, Version: saga.Version}, nil
}

func (h *Server) CreateRebalance(ctx context.Context, req *tradepb.CreateRebalanceReq) (*tradepb.CreateRebalanceRsp, error) {
	ctx = withRPCTrace(ctx)
	if h.kernel == nil {
		return &tradepb.CreateRebalanceRsp{RetInfo: retInfo(tradepb.ErrorCode_INNER_ERR, "trade kernel unavailable")}, nil
	}
	sid := spaceID(ctx)
	targets := make([]domainrebalance.Target, 0, len(req.GetTargets()))
	for _, v := range req.GetTargets() {
		q, e := shared.ParseDecimal(v.GetQuantity())
		if e != nil {
			return &tradepb.CreateRebalanceRsp{RetInfo: errToRetInfo(e)}, nil
		}
		targets = append(targets, domainrebalance.Target{Symbol: v.GetSymbol(), Quantity: q})
	}
	currents := make([]domainrebalance.Current, 0, len(req.GetCurrents()))
	for _, v := range req.GetCurrents() {
		q, e := shared.ParseDecimal(v.GetQuantity())
		if e != nil {
			return &tradepb.CreateRebalanceRsp{RetInfo: errToRetInfo(e)}, nil
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
		return &tradepb.CreateRebalanceRsp{RetInfo: errToRetInfo(e)}, nil
	}
	return &tradepb.CreateRebalanceRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), RunId: req.GetRunId(), Status: "PLANNED"}, nil
}
func (h *Server) AdvanceRebalance(ctx context.Context, req *tradepb.AdvanceRebalanceReq) (*tradepb.AdvanceRebalanceRsp, error) {
	if h.kernel == nil {
		return &tradepb.AdvanceRebalanceRsp{RetInfo: retInfo(tradepb.ErrorCode_INNER_ERR, "trade kernel unavailable")}, nil
	}
	status, e := (rebalanceapp.Service{Store: h.kernel.Store, Engine: h.kernel}).Advance(ctx, spaceID(ctx), req.GetRunId(), req.GetAccountId(), req.GetChannelId())
	if e != nil {
		return &tradepb.AdvanceRebalanceRsp{RetInfo: errToRetInfo(e)}, nil
	}
	return &tradepb.AdvanceRebalanceRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), RunId: req.GetRunId(), Status: status}, nil
}

// ===== AccountSvc =====

func (h *Server) CreateAccount(ctx context.Context, req *tradepb.CreateAccountReq) (*tradepb.CreateAccountRsp, error) {
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
		return &tradepb.CreateAccountRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &tradepb.CreateAccountRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), AccountId: out.AccountID, Account: accountToPB(out)}, nil
}

func (h *Server) UpdateAccount(ctx context.Context, req *tradepb.UpdateAccountReq) (*tradepb.UpdateAccountRsp, error) {
	sid := spaceID(ctx)
	a := &service.Account{
		AccountID:   req.GetAccountId(),
		AccountName: req.GetAccountName(),
		Status:      service.AccountStatus(req.GetStatus()),
		IsDefault:   req.GetIsDefault(),
		Remark:      req.GetRemark(),
	}
	if _, err := h.svc.Account.UpdateAccount(ctx, sid, a); err != nil {
		return &tradepb.UpdateAccountRsp{RetInfo: errToRetInfo(err)}, nil
	}
	got, _ := h.svc.Account.GetAccount(ctx, sid, req.GetAccountId())
	return &tradepb.UpdateAccountRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), Account: accountToPB(got)}, nil
}

func (h *Server) DeleteAccount(ctx context.Context, req *tradepb.DeleteAccountReq) (*tradepb.DeleteAccountRsp, error) {
	sid := spaceID(ctx)
	if err := h.svc.Account.DeleteAccount(ctx, sid, req.GetAccountId()); err != nil {
		return &tradepb.DeleteAccountRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &tradepb.DeleteAccountRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, "")}, nil
}

func (h *Server) GetAccount(ctx context.Context, req *tradepb.GetAccountReq) (*tradepb.GetAccountRsp, error) {
	sid := spaceID(ctx)
	a, err := h.svc.Account.GetAccount(ctx, sid, req.GetAccountId())
	if err != nil {
		return &tradepb.GetAccountRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &tradepb.GetAccountRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), Account: accountToPB(a)}, nil
}

func (h *Server) ListAccounts(ctx context.Context, req *tradepb.ListAccountsReq) (*tradepb.ListAccountsRsp, error) {
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
		return &tradepb.ListAccountsRsp{RetInfo: errToRetInfo(err)}, nil
	}
	out := make([]*tradepb.Account, 0, len(list))
	for _, a := range list {
		out = append(out, accountToPB(a))
	}
	return &tradepb.ListAccountsRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), Accounts: out, PageResult: pageResult(page, total)}, nil
}

func (h *Server) SyncExchangeAccounts(ctx context.Context, req *tradepb.SyncExchangeAccountsReq) (*tradepb.SyncExchangeAccountsRsp, error) {
	sid := spaceID(ctx)
	list, err := h.svc.SyncExchangeAccountsWithSnapshots(ctx, sid, service.SyncExchangeAccountsOptions{
		UserID:     userID(ctx),
		Provider:   req.GetProvider(),
		MarketType: req.GetMarketType(),
	})
	if err != nil {
		return &tradepb.SyncExchangeAccountsRsp{RetInfo: errToRetInfo(err)}, nil
	}
	if h.kernel != nil {
		for _, account := range list {
			bs, balanceErr := h.svc.Account.GetBalances(ctx, sid, account.AccountID, nil)
			if balanceErr != nil {
				return &tradepb.SyncExchangeAccountsRsp{RetInfo: errToRetInfo(balanceErr)}, nil
			}
			desired := map[string]map[string]shared.Decimal{}
			for _, b := range bs {
				available, e := shared.ParseDecimal(b.Available)
				if e != nil {
					return &tradepb.SyncExchangeAccountsRsp{RetInfo: errToRetInfo(e)}, nil
				}
				frozen, e := shared.ParseDecimal(b.Frozen)
				if e != nil {
					return &tradepb.SyncExchangeAccountsRsp{RetInfo: errToRetInfo(e)}, nil
				}
				desired[b.Currency] = map[string]shared.Decimal{"available": available, "frozen": frozen}
			}
			if balanceErr = h.kernel.Store.ReconcileBalances(ctx, sid, account.AccountID, desired); balanceErr != nil {
				return &tradepb.SyncExchangeAccountsRsp{RetInfo: errToRetInfo(balanceErr)}, nil
			}
		}
	}
	out := make([]*tradepb.Account, 0, len(list))
	for _, a := range list {
		out = append(out, accountToPB(a))
	}
	return &tradepb.SyncExchangeAccountsRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), Accounts: out}, nil
}

// ===== BalanceSvc =====

func (h *Server) GetBalances(ctx context.Context, req *tradepb.GetBalancesReq) (*tradepb.GetBalancesRsp, error) {
	sid := spaceID(ctx)
	if h.kernel != nil {
		rows, err := h.kernel.Store.ListBalances(ctx, sid, req.GetAccountId())
		if err != nil {
			return &tradepb.GetBalancesRsp{RetInfo: errToRetInfo(err)}, nil
		}
		type pair struct{ available, frozen, margin shared.Decimal }
		m := map[string]pair{}
		for _, r := range rows {
			p := m[r.Asset]
			v, e := shared.ParseDecimal(r.Amount)
			if e != nil {
				return &tradepb.GetBalancesRsp{RetInfo: errToRetInfo(e)}, nil
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
		out := make([]*tradepb.Balance, 0, len(m))
		for asset, p := range m {
			locked := p.frozen.Add(p.margin)
			out = append(out, &tradepb.Balance{AccountId: req.GetAccountId(), Currency: asset, Available: p.available.String(), Frozen: locked.String(), Total: p.available.Add(locked).String()})
		}
		return &tradepb.GetBalancesRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), Balances: out}, nil
	}
	bs, err := h.svc.Account.GetBalances(ctx, sid, req.GetAccountId(), req.GetCurrencies())
	if err != nil {
		return &tradepb.GetBalancesRsp{RetInfo: errToRetInfo(err)}, nil
	}
	out := make([]*tradepb.Balance, 0, len(bs))
	for _, b := range bs {
		out = append(out, balanceToPB(b))
	}
	return &tradepb.GetBalancesRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), Balances: out}, nil
}

func (h *Server) SyncBalances(ctx context.Context, req *tradepb.SyncBalancesReq) (*tradepb.SyncBalancesRsp, error) {
	sid := spaceID(ctx)
	bs, err := h.svc.Account.SyncBalances(ctx, sid, req.GetAccountId())
	if err != nil {
		return &tradepb.SyncBalancesRsp{RetInfo: errToRetInfo(err)}, nil
	}
	if h.kernel != nil {
		desired := map[string]map[string]shared.Decimal{}
		for _, b := range bs {
			available, e := shared.ParseDecimal(b.Available)
			if e != nil {
				return &tradepb.SyncBalancesRsp{RetInfo: errToRetInfo(e)}, nil
			}
			frozen, e := shared.ParseDecimal(b.Frozen)
			if e != nil {
				return &tradepb.SyncBalancesRsp{RetInfo: errToRetInfo(e)}, nil
			}
			desired[b.Currency] = map[string]shared.Decimal{"available": available, "frozen": frozen}
		}
		if err = h.kernel.Store.ReconcileBalances(ctx, sid, req.GetAccountId(), desired); err != nil {
			return &tradepb.SyncBalancesRsp{RetInfo: errToRetInfo(err)}, nil
		}
	}
	out := make([]*tradepb.Balance, 0, len(bs))
	for _, b := range bs {
		out = append(out, balanceToPB(b))
	}
	return &tradepb.SyncBalancesRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), Balances: out}, nil
}

// ===== FundSvc =====

func (h *Server) ListFundFlows(ctx context.Context, req *tradepb.ListFundFlowsReq) (*tradepb.ListFundFlowsRsp, error) {
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
		return &tradepb.ListFundFlowsRsp{RetInfo: errToRetInfo(err)}, nil
	}
	out := make([]*tradepb.FundFlow, 0, len(list))
	for _, fl := range list {
		out = append(out, fundFlowToPB(fl))
	}
	return &tradepb.ListFundFlowsRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), Flows: out, PageResult: pageResult(page, total)}, nil
}

func (h *Server) Transfer(ctx context.Context, req *tradepb.TransferReq) (*tradepb.TransferRsp, error) {
	sid := spaceID(ctx)
	outID, inID, err := h.svc.Account.Transfer(ctx, sid, req.GetFromAccountId(), req.GetToAccountId(), req.GetCurrency(), req.GetAmount(), req.GetRemark())
	if err != nil {
		return &tradepb.TransferRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &tradepb.TransferRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), OutFlowId: outID, InFlowId: inID}, nil
}

// ===== ApiKeySvc =====

func (h *Server) CreateApiKey(ctx context.Context, req *tradepb.CreateApiKeyReq) (*tradepb.CreateApiKeyRsp, error) {
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
		return &tradepb.CreateApiKeyRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &tradepb.CreateApiKeyRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), ApiKeyId: id}, nil
}

func (h *Server) DeleteApiKey(ctx context.Context, req *tradepb.DeleteApiKeyReq) (*tradepb.DeleteApiKeyRsp, error) {
	sid := spaceID(ctx)
	if err := h.svc.Account.DeleteAPIKey(ctx, sid, req.GetApiKeyId()); err != nil {
		return &tradepb.DeleteApiKeyRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &tradepb.DeleteApiKeyRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, "")}, nil
}

func (h *Server) ListApiKeys(ctx context.Context, req *tradepb.ListApiKeysReq) (*tradepb.ListApiKeysRsp, error) {
	sid := spaceID(ctx)
	list, err := h.svc.Account.ListAPIKeys(ctx, sid, req.GetAccountId())
	if err != nil {
		return &tradepb.ListApiKeysRsp{RetInfo: errToRetInfo(err)}, nil
	}
	out := make([]*tradepb.ApiKey, 0, len(list))
	for _, k := range list {
		out = append(out, apiKeyToPB(k))
	}
	return &tradepb.ListApiKeysRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), ApiKeys: out}, nil
}

// ===== ChannelSvc =====

func (h *Server) CreateChannel(ctx context.Context, req *tradepb.CreateChannelReq) (*tradepb.CreateChannelRsp, error) {
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
		return &tradepb.CreateChannelRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &tradepb.CreateChannelRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), ChannelId: id}, nil
}

func (h *Server) UpdateChannel(ctx context.Context, req *tradepb.UpdateChannelReq) (*tradepb.UpdateChannelRsp, error) {
	sid := spaceID(ctx)
	c := &service.TradeChannel{
		ChannelID:   req.GetChannelId(),
		ChannelName: req.GetChannelName(),
		Status:      int(req.GetStatus()),
		Endpoint:    req.GetEndpoint(),
		RateLimit:   int(req.GetRateLimit()),
	}
	if err := h.svc.Order.UpdateChannel(ctx, sid, c); err != nil {
		return &tradepb.UpdateChannelRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &tradepb.UpdateChannelRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, "")}, nil
}

func (h *Server) DeleteChannel(ctx context.Context, req *tradepb.DeleteChannelReq) (*tradepb.DeleteChannelRsp, error) {
	sid := spaceID(ctx)
	if err := h.svc.Order.DeleteChannel(ctx, sid, req.GetChannelId()); err != nil {
		return &tradepb.DeleteChannelRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &tradepb.DeleteChannelRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, "")}, nil
}

func (h *Server) ListChannels(ctx context.Context, req *tradepb.ListChannelsReq) (*tradepb.ListChannelsRsp, error) {
	sid := spaceID(ctx)
	f := service.ChannelFilter{AccountID: req.GetAccountId(), Exchange: req.GetExchange()}
	page := pageFromPB(req.GetPage()).Normalize()
	list, total, err := h.svc.Order.ListChannels(ctx, sid, f, page)
	if err != nil {
		return &tradepb.ListChannelsRsp{RetInfo: errToRetInfo(err)}, nil
	}
	out := make([]*tradepb.TradeChannel, 0, len(list))
	for _, c := range list {
		out = append(out, channelToPB(c))
	}
	return &tradepb.ListChannelsRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), Channels: out, PageResult: pageResult(page, total)}, nil
}

func (h *Server) TestChannel(ctx context.Context, req *tradepb.TestChannelReq) (*tradepb.TestChannelRsp, error) {
	sid := spaceID(ctx)
	reachable, latency, err := h.svc.Order.TestChannel(ctx, sid, req.GetChannelId())
	if err != nil {
		return &tradepb.TestChannelRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &tradepb.TestChannelRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), Reachable: reachable, LatencyMs: int32(latency)}, nil
}

func (h *Server) ListInstruments(ctx context.Context, req *tradepb.ListInstrumentsReq) (*tradepb.ListInstrumentsRsp, error) {
	sid := spaceID(ctx)
	var market exchange.MarketType
	if req.MarketType != nil {
		market = exchange.MarketType(marketTypeToDomain(req.GetMarketType()))
	}
	list, err := h.svc.Order.ListInstruments(ctx, sid, req.GetChannelId(), market)
	if err != nil {
		return &tradepb.ListInstrumentsRsp{RetInfo: errToRetInfo(err)}, nil
	}
	out := make([]*tradepb.Instrument, 0, len(list))
	for _, ins := range list {
		out = append(out, instrumentToPB(ins))
	}
	return &tradepb.ListInstrumentsRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), Instruments: out}, nil
}

// ===== TradeOpSvc =====

func (h *Server) PlaceOrder(ctx context.Context, req *tradepb.PlaceOrderReq) (*tradepb.PlaceOrderRsp, error) {
	ctx = withRPCTrace(ctx)
	sid := spaceID(ctx)
	if h.kernel != nil {
		orderID, _ := gonanoid.New()
		clientID := req.GetClientOrderId()
		if clientID == "" {
			clientID = orderID
		}
		side := "BUY"
		if req.GetSide() == tradepb.OrderSide_ORDER_SIDE_SELL {
			side = "SELL"
		}
		o, err := h.kernel.Place(ctx, command.PlaceInput{SpaceID: sid, OrderID: orderID, ClientOrderID: clientID, AccountID: req.GetAccountId(), ChannelID: req.GetChannelId(), Symbol: req.GetSymbol(), MarketType: marketTypeToDomain(req.GetMarketType()), Side: side, Quantity: req.GetQuantity(), Price: req.GetPrice(), ReduceOnly: req.GetReduceOnly()})
		if err != nil {
			return &tradepb.PlaceOrderRsp{RetInfo: errToRetInfo(err)}, nil
		}
		return &tradepb.PlaceOrderRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), OrderId: o.OrderID, ExchangeOrderId: o.ExchangeOrderID, Status: kernelStatusToPB(domainorder.State(o.State))}, nil
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
		return &tradepb.PlaceOrderRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &tradepb.PlaceOrderRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), OrderId: o.OrderID, ExchangeOrderId: o.ExchangeOrderID, Status: orderStatusToPB(o.Status)}, nil
}

func kernelStatusToPB(s domainorder.State) tradepb.OrderStatus {
	switch s {
	case domainorder.Open, domainorder.Submitting, domainorder.SubmitUnknown, domainorder.Canceling, domainorder.CancelUnknown:
		return tradepb.OrderStatus_ORDER_STATUS_SUBMITTED
	case domainorder.PartiallyFilled:
		return tradepb.OrderStatus_ORDER_STATUS_PARTIALLY_FILLED
	case domainorder.Filled:
		return tradepb.OrderStatus_ORDER_STATUS_FILLED
	case domainorder.Canceled:
		return tradepb.OrderStatus_ORDER_STATUS_CANCELED
	case domainorder.PartiallyCanceled:
		return tradepb.OrderStatus_ORDER_STATUS_PARTIAL_CANCELED
	case domainorder.Rejected:
		return tradepb.OrderStatus_ORDER_STATUS_REJECTED
	case domainorder.Expired:
		return tradepb.OrderStatus_ORDER_STATUS_EXPIRED
	default:
		return tradepb.OrderStatus_ORDER_STATUS_PENDING
	}
}
func kernelOrderToPB(o store.OrderRecord) *tradepb.Order {
	side := tradepb.OrderSide_ORDER_SIDE_BUY
	if o.Side == "SELL" {
		side = tradepb.OrderSide_ORDER_SIDE_SELL
	}
	return &tradepb.Order{OrderId: o.OrderID, ClientOrderId: o.ClientOrderID, ExchangeOrderId: o.ExchangeOrderID, AccountId: o.AccountID, ChannelId: o.ChannelID, Symbol: o.Symbol, Side: side, OrderType: tradepb.OrderType_ORDER_TYPE_LIMIT, TimeInForce: "IOC", Price: o.Price, Quantity: o.Quantity, FilledQty: o.FilledQuantity, Status: kernelStatusToPB(domainorder.State(o.State)), ReduceOnly: o.ReduceOnly}
}

func (h *Server) CancelOrder(ctx context.Context, req *tradepb.CancelOrderReq) (*tradepb.CancelOrderRsp, error) {
	ctx = withRPCTrace(ctx)
	sid := spaceID(ctx)
	if h.kernel != nil {
		o, err := h.kernel.Cancel(ctx, sid, req.GetOrderId())
		if err != nil {
			return &tradepb.CancelOrderRsp{RetInfo: errToRetInfo(err)}, nil
		}
		return &tradepb.CancelOrderRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), Status: kernelStatusToPB(domainorder.State(o.State))}, nil
	}
	ctx = service.WithOperator(ctx, userID(ctx))
	xreq := &exchange.CancelOrderReq{
		OrderID:       req.GetOrderId(),
		ClientOrderID: req.GetClientOrderId(),
	}
	o, err := h.svc.Order.CancelOrder(ctx, sid, req.GetChannelId(), xreq)
	if err != nil {
		return &tradepb.CancelOrderRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &tradepb.CancelOrderRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), Status: orderStatusToPB(o.Status)}, nil
}

func (h *Server) CancelAllOrders(ctx context.Context, req *tradepb.CancelAllOrdersReq) (*tradepb.CancelAllOrdersRsp, error) {
	sid := spaceID(ctx)
	if h.kernel != nil {
		orders, err := h.kernel.Store.ListOrders(ctx, sid, "", req.GetChannelId(), req.GetSymbol(), true)
		if err != nil {
			return &tradepb.CancelAllOrdersRsp{RetInfo: errToRetInfo(err)}, nil
		}
		var n int32
		for _, o := range orders {
			if _, err = h.kernel.Cancel(ctx, sid, o.OrderID); err != nil {
				return &tradepb.CancelAllOrdersRsp{RetInfo: errToRetInfo(err), CanceledCount: n}, nil
			}
			n++
		}
		return &tradepb.CancelAllOrdersRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), CanceledCount: n}, nil
	}
	n, err := h.svc.Order.CancelAllOrders(ctx, sid, req.GetChannelId(), req.GetSymbol())
	if err != nil {
		return &tradepb.CancelAllOrdersRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &tradepb.CancelAllOrdersRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), CanceledCount: int32(n)}, nil
}

func (h *Server) AmendOrder(ctx context.Context, req *tradepb.AmendOrderReq) (*tradepb.AmendOrderRsp, error) {
	ctx = withRPCTrace(ctx)
	sid := spaceID(ctx)
	if h.kernel != nil {
		old, err := h.kernel.Store.GetOrder(ctx, sid, req.GetOrderId())
		if err != nil {
			return &tradepb.AmendOrderRsp{RetInfo: errToRetInfo(err)}, nil
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
			return &tradepb.AmendOrderRsp{RetInfo: errToRetInfo(err)}, nil
		}
		_ = saga
		return &tradepb.AmendOrderRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), Status: tradepb.OrderStatus_ORDER_STATUS_PENDING}, nil
	}
	ctx = service.WithOperator(ctx, userID(ctx))
	xreq := &exchange.AmendOrderReq{
		OrderID:     req.GetOrderId(),
		NewPrice:    req.GetNewPrice(),
		NewQuantity: req.GetNewQuantity(),
	}
	o, err := h.svc.Order.AmendOrder(ctx, sid, req.GetChannelId(), xreq)
	if err != nil {
		return &tradepb.AmendOrderRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &tradepb.AmendOrderRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), Status: orderStatusToPB(o.Status)}, nil
}

func (h *Server) SetLeverage(ctx context.Context, req *tradepb.SetLeverageReq) (*tradepb.SetLeverageRsp, error) {
	sid := spaceID(ctx)
	if err := h.svc.Order.SetLeverage(ctx, sid, req.GetChannelId(), req.GetSymbol(), req.GetLeverage()); err != nil {
		return &tradepb.SetLeverageRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &tradepb.SetLeverageRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, "")}, nil
}

func (h *Server) ConvertDust(ctx context.Context, req *tradepb.ConvertDustReq) (*tradepb.ConvertDustRsp, error) {
	sid := spaceID(ctx)
	out, err := h.svc.Order.ConvertDust(ctx, sid, req.GetChannelId(), req.GetAssets())
	if err != nil {
		return &tradepb.ConvertDustRsp{RetInfo: errToRetInfo(err)}, nil
	}
	if req.GetAccountId() != "" {
		_, _ = h.svc.Account.SyncBalances(ctx, sid, req.GetAccountId())
	}
	rsp := &tradepb.ConvertDustRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, "")}
	if out == nil {
		return rsp, nil
	}
	rsp.TotalServiceCharge = out.TotalServiceCharge
	rsp.TotalTransfered = out.TotalTransfered
	rsp.Results = make([]*tradepb.DustTransferItem, 0, len(out.Results))
	for _, item := range out.Results {
		rsp.Results = append(rsp.Results, dustTransferItemToPB(item))
	}
	rsp.SkippedResults = make([]*tradepb.DustTransferSkippedItem, 0, len(out.Skipped))
	for _, item := range out.Skipped {
		rsp.SkippedResults = append(rsp.SkippedResults, dustTransferSkippedItemToPB(item))
	}
	return rsp, nil
}

// ===== OrderSvc =====

func (h *Server) GetOrder(ctx context.Context, req *tradepb.GetOrderReq) (*tradepb.GetOrderRsp, error) {
	sid := spaceID(ctx)
	if h.kernel != nil {
		o, err := h.kernel.Store.GetOrder(ctx, sid, req.GetOrderId())
		if err != nil && req.GetClientOrderId() != "" {
			o, err = h.kernel.Store.GetOrderByClientID(ctx, sid, req.GetClientOrderId())
		}
		if err != nil {
			return &tradepb.GetOrderRsp{RetInfo: errToRetInfo(err)}, nil
		}
		return &tradepb.GetOrderRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), Order: kernelOrderToPB(o)}, nil
	}
	o, err := h.svc.Order.GetOrder(ctx, sid, req.GetOrderId(), req.GetClientOrderId())
	if err != nil {
		return &tradepb.GetOrderRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &tradepb.GetOrderRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), Order: orderToPB(o)}, nil
}

func (h *Server) ListOrders(ctx context.Context, req *tradepb.ListOrdersReq) (*tradepb.ListOrdersRsp, error) {
	sid := spaceID(ctx)
	if h.kernel != nil {
		rows, err := h.kernel.Store.ListOrders(ctx, sid, req.GetAccountId(), req.GetChannelId(), req.GetSymbol(), req.GetOnlyOpen())
		if err != nil {
			return &tradepb.ListOrdersRsp{RetInfo: errToRetInfo(err)}, nil
		}
		out := make([]*tradepb.Order, len(rows))
		for i, r := range rows {
			out[i] = kernelOrderToPB(r)
		}
		page := pageFromPB(req.GetPage()).Normalize()
		return &tradepb.ListOrdersRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), Orders: out, PageResult: pageResult(page, len(out))}, nil
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
		return &tradepb.ListOrdersRsp{RetInfo: errToRetInfo(err)}, nil
	}
	out := make([]*tradepb.Order, 0, len(list))
	for _, o := range list {
		out = append(out, orderToPB(o))
	}
	return &tradepb.ListOrdersRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), Orders: out, PageResult: pageResult(page, total)}, nil
}

func (h *Server) SyncOrders(ctx context.Context, req *tradepb.SyncOrdersReq) (*tradepb.SyncOrdersRsp, error) {
	sid := spaceID(ctx)
	page := pageFromPB(req.GetPage()).Normalize()
	list, total, err := h.svc.Order.SyncOrders(ctx, sid, req.GetAccountId(), req.GetSymbol(), req.GetOnlyOpen(), req.GetStartTime(), req.GetEndTime(), page)
	if err != nil {
		return &tradepb.SyncOrdersRsp{RetInfo: errToRetInfo(err)}, nil
	}
	out := make([]*tradepb.Order, 0, len(list))
	for _, o := range list {
		out = append(out, orderToPB(o))
	}
	return &tradepb.SyncOrdersRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), Orders: out, PageResult: pageResult(page, total)}, nil
}

// ===== TradeQuerySvc =====

func (h *Server) ListTrades(ctx context.Context, req *tradepb.ListTradesReq) (*tradepb.ListTradesRsp, error) {
	sid := spaceID(ctx)
	if h.kernel != nil {
		rows, err := h.kernel.Store.ListFills(ctx, sid, req.GetOrderId())
		if err != nil {
			return &tradepb.ListTradesRsp{RetInfo: errToRetInfo(err)}, nil
		}
		out := make([]*tradepb.Trade, len(rows))
		for i, r := range rows {
			out[i] = &tradepb.Trade{TradeId: r.FillID, ExchangeTradeId: r.ExchangeTradeID, OrderId: r.OrderID, Price: r.Price, Quantity: r.Quantity, Fee: r.Fee, FeeCurrency: r.FeeAsset}
		}
		page := pageFromPB(req.GetPage()).Normalize()
		return &tradepb.ListTradesRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), Trades: out, PageResult: pageResult(page, len(out))}, nil
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
		return &tradepb.ListTradesRsp{RetInfo: errToRetInfo(err)}, nil
	}
	out := make([]*tradepb.Trade, 0, len(list))
	for _, t := range list {
		out = append(out, tradeToPB(t))
	}
	return &tradepb.ListTradesRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), Trades: out, PageResult: pageResult(page, total)}, nil
}

func (h *Server) SyncTrades(ctx context.Context, req *tradepb.SyncTradesReq) (*tradepb.SyncTradesRsp, error) {
	sid := spaceID(ctx)
	page := pageFromPB(req.GetPage()).Normalize()
	list, total, err := h.svc.Order.SyncTrades(ctx, sid, req.GetAccountId(), req.GetSymbol(), req.GetOrderId(), req.GetStartTime(), req.GetEndTime(), page)
	if err != nil {
		return &tradepb.SyncTradesRsp{RetInfo: errToRetInfo(err)}, nil
	}
	out := make([]*tradepb.Trade, 0, len(list))
	for _, tr := range list {
		out = append(out, tradeToPB(tr))
	}
	return &tradepb.SyncTradesRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), Trades: out, PageResult: pageResult(page, total)}, nil
}

// ===== PositionSvc =====

func (h *Server) ListPositions(ctx context.Context, req *tradepb.ListPositionsReq) (*tradepb.ListPositionsRsp, error) {
	sid := spaceID(ctx)
	if h.kernel != nil {
		rows, err := h.kernel.Store.ListPositions(ctx, sid, req.GetAccountId(), req.GetSymbol())
		if err != nil {
			return &tradepb.ListPositionsRsp{RetInfo: errToRetInfo(err)}, nil
		}
		out := make([]*tradepb.Position, len(rows))
		for i, r := range rows {
			out[i] = &tradepb.Position{AccountId: r.AccountID, Symbol: r.Symbol, Quantity: r.Quantity, AvgPrice: r.AveragePrice, RealizedPnl: r.RealizedPnL}
		}
		return &tradepb.ListPositionsRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), Positions: out}, nil
	}
	list, err := h.svc.Order.ListPositions(ctx, sid, req.GetAccountId(), req.GetSymbol())
	if err != nil {
		return &tradepb.ListPositionsRsp{RetInfo: errToRetInfo(err)}, nil
	}
	out := make([]*tradepb.Position, 0, len(list))
	for _, p := range list {
		out = append(out, positionToPB(p))
	}
	return &tradepb.ListPositionsRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), Positions: out}, nil
}

func (h *Server) SyncPositions(ctx context.Context, req *tradepb.SyncPositionsReq) (*tradepb.SyncPositionsRsp, error) {
	sid := spaceID(ctx)
	list, err := h.svc.Order.SyncPositions(ctx, sid, req.GetAccountId(), req.GetSymbol())
	if err != nil {
		return &tradepb.SyncPositionsRsp{RetInfo: errToRetInfo(err)}, nil
	}
	out := make([]*tradepb.Position, 0, len(list))
	for _, p := range list {
		out = append(out, positionToPB(p))
	}
	return &tradepb.SyncPositionsRsp{RetInfo: retInfo(tradepb.ErrorCode_SUCCESS, ""), Positions: out}, nil
}
