package rpc

import (
	"context"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	mooxpb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

// Server 实现 trade 模块全部 9 个 tRPC service 接口，委托 service.Service。
// 一个 Server 实例同时满足 AccountSvcService/BalanceSvcService/.../PositionSvcService。
type Server struct {
	svc *service.Service
}

// New 创建 RPC handler。
func New(svc *service.Service) *Server { return &Server{svc: svc} }

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
		UserID:      req.GetUserId(),
		AccountType: accountTypeToDomain(req.GetAccountType()),
		Keyword:     req.GetKeyword(),
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

// ===== BalanceSvc =====

func (h *Server) GetBalances(ctx context.Context, req *mooxpb.GetBalancesReq) (*mooxpb.GetBalancesRsp, error) {
	sid := spaceID(ctx)
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
	accountID := req.GetAccountId()
	a, err := h.svc.Account.GetAccount(ctx, sid, accountID)
	if err != nil {
		return &mooxpb.SyncBalancesRsp{RetInfo: errToRetInfo(err)}, nil
	}
	if a.ChannelID == "" {
		// 未绑定通道：直接返回本地余额快照
		return h.localBalances(ctx, sid, accountID)
	}
	adapter, cred, ch, err := h.svc.Order.ResolveAdapter(ctx, sid, a.ChannelID)
	if err != nil {
		return &mooxpb.SyncBalancesRsp{RetInfo: errToRetInfo(err)}, nil
	}
	market := exchange.MarketType(ch.MarketType)
	exBs, err := adapter.GetBalances(ctx, cred, market, nil)
	if err != nil {
		return &mooxpb.SyncBalancesRsp{RetInfo: errToRetInfo(err)}, nil
	}
	domain := make([]*service.Balance, 0, len(exBs))
	for _, b := range exBs {
		domain = append(domain, &service.Balance{
			AccountID: accountID,
			Currency:  b.Currency,
			Available: b.Available,
			Frozen:    b.Frozen,
			Total:     b.Total,
		})
	}
	if err := h.svc.Account.UpsertBalances(ctx, sid, domain); err != nil {
		return &mooxpb.SyncBalancesRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return h.localBalances(ctx, sid, accountID)
}

func (h *Server) localBalances(ctx context.Context, sid, accountID string) (*mooxpb.SyncBalancesRsp, error) {
	bs, err := h.svc.Account.GetBalances(ctx, sid, accountID, nil)
	if err != nil {
		return &mooxpb.SyncBalancesRsp{RetInfo: errToRetInfo(err)}, nil
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

// ===== TradeOpSvc =====

func (h *Server) PlaceOrder(ctx context.Context, req *mooxpb.PlaceOrderReq) (*mooxpb.PlaceOrderRsp, error) {
	sid := spaceID(ctx)
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

func (h *Server) CancelOrder(ctx context.Context, req *mooxpb.CancelOrderReq) (*mooxpb.CancelOrderRsp, error) {
	sid := spaceID(ctx)
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
	n, err := h.svc.Order.CancelAllOrders(ctx, sid, req.GetChannelId(), req.GetSymbol())
	if err != nil {
		return &mooxpb.CancelAllOrdersRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &mooxpb.CancelAllOrdersRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), CanceledCount: int32(n)}, nil
}

func (h *Server) AmendOrder(ctx context.Context, req *mooxpb.AmendOrderReq) (*mooxpb.AmendOrderRsp, error) {
	sid := spaceID(ctx)
	ctx = service.WithOperator(ctx, userID(ctx))
	xreq := &exchange.AmendOrderReq{
		OrderID:       req.GetOrderId(),
		NewPrice:      req.GetNewPrice(),
		NewQuantity:   req.GetNewQuantity(),
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

// ===== OrderSvc =====

func (h *Server) GetOrder(ctx context.Context, req *mooxpb.GetOrderReq) (*mooxpb.GetOrderRsp, error) {
	sid := spaceID(ctx)
	o, err := h.svc.Order.GetOrder(ctx, sid, req.GetOrderId(), req.GetClientOrderId())
	if err != nil {
		return &mooxpb.GetOrderRsp{RetInfo: errToRetInfo(err)}, nil
	}
	return &mooxpb.GetOrderRsp{RetInfo: retInfo(mooxpb.ErrorCode_SUCCESS, ""), Order: orderToPB(o)}, nil
}

func (h *Server) ListOrders(ctx context.Context, req *mooxpb.ListOrdersReq) (*mooxpb.ListOrdersRsp, error) {
	sid := spaceID(ctx)
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

// ===== TradeQuerySvc =====

func (h *Server) ListTrades(ctx context.Context, req *mooxpb.ListTradesReq) (*mooxpb.ListTradesRsp, error) {
	sid := spaceID(ctx)
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

// ===== PositionSvc =====

func (h *Server) ListPositions(ctx context.Context, req *mooxpb.ListPositionsReq) (*mooxpb.ListPositionsRsp, error) {
	sid := spaceID(ctx)
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
