package tradepb

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"
)

const (
	maxDecimalLength = 256
	maxPageSize      = 1000
)

var canonicalUnsignedDecimalPattern = regexp.MustCompile(
	`^(0|[1-9][0-9]*)(\.[0-9]+)?$`,
)

func (r *CreateTradingAccountReq) Validate() error {
	if r == nil || strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if !validExchange(r.Exchange) ||
		!validMarkets(r.Markets) ||
		r.Live == nil ||
		!validAccountEnvironment(r.Live.GetEnvironment()) ||
		(r.Live.GetEnvironment() != AccountEnvironment_ACCOUNT_ENVIRONMENT_TESTNET &&
			r.Live.GetEnvironment() != AccountEnvironment_ACCOUNT_ENVIRONMENT_PRODUCTION) ||
		strings.TrimSpace(r.Live.GetCredentialSecretId()) == "" {
		return fmt.Errorf("exchange, markets and live environment/credential are required")
	}
	return nil
}

func (r *UpdateTradingAccountReq) Validate() error {
	return requireID(r != nil, value(func() string {
		if r == nil {
			return ""
		}
		return r.TradingAccountId
	}), "trading_account_id")
}

func (r *GetTradingAccountReq) Validate() error {
	return requireID(r != nil, value(func() string {
		if r == nil {
			return ""
		}
		return r.TradingAccountId
	}), "trading_account_id")
}

func (r *ListTradingAccountsReq) Validate() error {
	if r == nil {
		return fmt.Errorf("request is required")
	}
	if r.Exchange != nil && !validExchange(r.GetExchange()) {
		return fmt.Errorf("invalid exchange")
	}
	if r.MarketType != nil && !validMarketType(r.GetMarketType()) {
		return fmt.Errorf("invalid market_type")
	}
	if r.ExecutionMode != nil && !validExecutionMode(r.GetExecutionMode()) {
		return fmt.Errorf("invalid execution_mode")
	}
	return validatePage(r.Page)
}

func (r *SetLeverageReq) Validate() error {
	if r == nil ||
		strings.TrimSpace(r.TradingAccountId) == "" ||
		strings.TrimSpace(r.InstrumentId) == "" {
		return fmt.Errorf("trading_account_id and instrument_id are required")
	}
	if !isCanonicalPositiveDecimal(r.Leverage) {
		return fmt.Errorf("leverage must be a canonical positive decimal")
	}
	return nil
}

func (r *SyncTradingAccountReq) Validate() error {
	if r == nil || strings.TrimSpace(r.TradingAccountId) == "" {
		return fmt.Errorf("trading_account_id is required")
	}
	return nil
}

func (r *CreatePaperSimulationReq) Validate() error {
	if r == nil || strings.TrimSpace(r.AccountName) == "" ||
		!validExchange(r.Exchange) || !validMarkets(r.Markets) || strings.TrimSpace(r.SettlementAsset) == "" {
		return fmt.Errorf("account_name, exchange, markets and settlement_asset are required")
	}
	if !validCreationControlMode(r.ControlMode) {
		return fmt.Errorf("control_mode is invalid")
	}
	if !isCanonicalPositiveDecimal(r.InitialBalance) || !canonicalUnsignedDecimalPattern.MatchString(r.MakerFeeRate) ||
		!canonicalUnsignedDecimalPattern.MatchString(r.TakerFeeRate) || !canonicalUnsignedDecimalPattern.MatchString(r.SlippageBps) {
		return fmt.Errorf("paper configuration decimals are invalid")
	}
	margin := strings.ToUpper(strings.TrimSpace(r.MarginMode))
	hasSwap := containsMarket(r.Markets, MarketType_MARKET_TYPE_SWAP)
	if !hasSwap && margin != "" {
		return fmt.Errorf("paper simulation without SWAP cannot configure margin_mode")
	}
	if hasSwap && margin != "" && margin != "CROSS" {
		return fmt.Errorf("SWAP paper simulation requires CROSS margin_mode")
	}
	if hasSwap && !strings.EqualFold(strings.TrimSpace(r.SettlementAsset), "USDT") {
		return fmt.Errorf("SWAP paper simulation requires USDT settlement_asset")
	}
	return nil
}

func (r *ClosePaperSimulationReq) Validate() error {
	return requireID(r != nil, value(func() string {
		if r == nil {
			return ""
		}
		return r.TradingAccountId
	}), "trading_account_id")
}

func (r *ListTradableInstrumentsReq) Validate() error {
	return requireID(r != nil, value(func() string {
		if r == nil {
			return ""
		}
		return r.TradingAccountId
	}), "trading_account_id")
}

func (r *GetExecutionCapabilitiesReq) Validate() error {
	return requireID(r != nil, value(func() string {
		if r == nil {
			return ""
		}
		return r.TradingAccountId
	}), "trading_account_id")
}

func (r *QueryEquityCurveReq) Validate() error {
	if r == nil || (strings.TrimSpace(r.TradingAccountId) == "" && strings.TrimSpace(r.LogicalAccountId) == "") ||
		(strings.TrimSpace(r.TradingAccountId) != "" && strings.TrimSpace(r.LogicalAccountId) != "") {
		return fmt.Errorf("exactly one equity curve target is required")
	}
	if r.StartTime < 0 || r.EndTime < 0 || (r.EndTime > 0 && r.StartTime > r.EndTime) {
		return fmt.Errorf("invalid equity curve time range")
	}
	return nil
}

func (r *ListHoldingsReq) Validate() error {
	return requireID(r != nil, value(func() string {
		if r == nil {
			return ""
		}
		return r.TradingAccountId
	}), "trading_account_id")
}

func (r *CreateLogicalAccountReq) Validate() error {
	if r == nil || strings.TrimSpace(r.Name) == "" ||
		!validExecutionMode(r.ExecutionMode) ||
		strings.TrimSpace(r.SettlementAsset) == "" {
		return fmt.Errorf(
			"name, execution_mode and settlement_asset are required",
		)
	}
	if !validCreationControlMode(r.ControlMode) {
		return fmt.Errorf("control_mode is invalid")
	}
	return nil
}

// validMarkets 要求市场集合非空、每项合法且不重复；顺序和 STOCK 不能与现货、合约混合由服务端规范化时校验。
func validMarkets(markets []MarketType) bool {
	if len(markets) == 0 {
		return false
	}
	seen := make(map[MarketType]struct{}, len(markets))
	for _, market := range markets {
		if !validMarketType(market) {
			return false
		}
		if _, duplicate := seen[market]; duplicate {
			return false
		}
		seen[market] = struct{}{}
	}
	return true
}

func containsMarket(markets []MarketType, want MarketType) bool {
	for _, market := range markets {
		if market == want {
			return true
		}
	}
	return false
}

func validCreationControlMode(mode ControlMode) bool {
	return mode == ControlMode_CONTROL_MODE_UNSPECIFIED || mode == ControlMode_CONTROL_MODE_STRATEGY || mode == ControlMode_CONTROL_MODE_MANUAL
}

func (r *GetLogicalAccountReq) Validate() error {
	if r == nil || strings.TrimSpace(r.LogicalAccountId) == "" {
		return fmt.Errorf("logical_account_id is required")
	}
	return nil
}

func (r *ListLogicalAccountsReq) Validate() error {
	if r == nil {
		return fmt.Errorf("request is required")
	}
	return validatePage(r.Page)
}

func (r *UpdateLogicalAccountReq) Validate() error {
	if r == nil || strings.TrimSpace(r.LogicalAccountId) == "" ||
		strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("logical_account_id and name are required")
	}
	return nil
}

func (r *AddLogicalAccountMemberReq) Validate() error {
	if r == nil ||
		strings.TrimSpace(r.LogicalAccountId) == "" ||
		strings.TrimSpace(r.TradingAccountId) == "" {
		return fmt.Errorf(
			"logical_account_id and trading_account_id are required",
		)
	}
	return nil
}

func (r *RemoveLogicalAccountMemberReq) Validate() error {
	if r == nil ||
		strings.TrimSpace(r.LogicalAccountId) == "" ||
		strings.TrimSpace(r.TradingAccountId) == "" {
		return fmt.Errorf(
			"logical_account_id and trading_account_id are required",
		)
	}
	return nil
}

func (r *ClaimLogicalAccountOwnerReq) Validate() error {
	if r == nil || strings.TrimSpace(r.LogicalAccountId) == "" || strings.TrimSpace(r.InstanceId) == "" || strings.TrimSpace(r.SessionId) == "" || strings.TrimSpace(r.ExpectedAuthFence) == "" {
		return fmt.Errorf("logical_account_id, instance_id, session_id and expected_auth_fence are required")
	}
	return nil
}

func (r *ReleaseLogicalAccountOwnerReq) Validate() error {
	if r == nil || strings.TrimSpace(r.LogicalAccountId) == "" || strings.TrimSpace(r.InstanceId) == "" || strings.TrimSpace(r.SessionId) == "" || strings.TrimSpace(r.ExpectedAuthFence) == "" {
		return fmt.Errorf("logical_account_id, instance_id, session_id and expected_auth_fence are required")
	}
	return nil
}

func (r *RebindLogicalAccountOwnerReq) Validate() error {
	if r == nil || strings.TrimSpace(r.LogicalAccountId) == "" || strings.TrimSpace(r.InstanceId) == "" || strings.TrimSpace(r.SessionId) == "" || strings.TrimSpace(r.ExpectedAuthFence) == "" || strings.TrimSpace(r.NewInstanceId) == "" || strings.TrimSpace(r.NewSessionId) == "" {
		return fmt.Errorf("logical_account_id, expected_auth_fence, old/new instance_id and old/new session_id are required")
	}
	return nil
}

func (r *PauseLogicalAccountReq) Validate() error {
	if r == nil ||
		strings.TrimSpace(r.LogicalAccountId) == "" ||
		strings.TrimSpace(r.Reason) == "" {
		return fmt.Errorf("logical_account_id and reason are required")
	}
	return nil
}

func (r *ResumeLogicalAccountReq) Validate() error {
	if r == nil || strings.TrimSpace(r.LogicalAccountId) == "" {
		return fmt.Errorf("logical_account_id is required")
	}
	return nil
}

func (r *FlattenLogicalAccountReq) Validate() error {
	if r == nil ||
		strings.TrimSpace(r.ActionId) == "" ||
		strings.TrimSpace(r.LogicalAccountId) == "" ||
		strings.TrimSpace(r.Reason) == "" {
		return fmt.Errorf("action_id, logical_account_id and reason are required")
	}
	return nil
}

func (r *FlattenTradingAccountReq) Validate() error {
	if r == nil ||
		strings.TrimSpace(r.ActionId) == "" ||
		strings.TrimSpace(r.TradingAccountId) == "" ||
		strings.TrimSpace(r.Reason) == "" {
		return fmt.Errorf("action_id, trading_account_id and reason are required")
	}
	return nil
}

// maxLedgerWindow 是资金流水一次查询的最长时间窗（OKX 归档账单只保留 3 个月）。
const maxLedgerWindow = 90 * 24 * 60 * 60 * 1000

func (r *ListAccountLedgerReq) Validate() error {
	if r == nil || strings.TrimSpace(r.TradingAccountId) == "" {
		return fmt.Errorf("trading_account_id is required")
	}
	if err := validateTimeRange(r.StartTime, r.EndTime); err != nil {
		return err
	}
	if r.StartTime > 0 {
		end := r.EndTime
		if end == 0 {
			end = time.Now().UnixMilli()
		}
		if end-r.StartTime > maxLedgerWindow {
			return fmt.Errorf("时间范围不能超过 90 天")
		}
	}
	if r.Limit < 0 || r.Limit > 1000 {
		return fmt.Errorf("limit must be between 0 and 1000")
	}
	if !validOptionalMarketType(r.MarketType) {
		return fmt.Errorf("market_type is invalid")
	}
	return nil
}

func (r *PlaceManualOrderReq) Validate() error {
	if r == nil ||
		strings.TrimSpace(r.ActionId) == "" ||
		strings.TrimSpace(r.TradingAccountId) == "" ||
		strings.TrimSpace(r.ClientOrderId) == "" ||
		strings.TrimSpace(r.InstrumentId) == "" ||
		strings.TrimSpace(r.Reason) == "" {
		return fmt.Errorf(
			"action_id, trading_account_id, client_order_id, instrument_id and reason are required",
		)
	}
	if r.DeadlineAt < 0 {
		return fmt.Errorf("deadline_at must be zero or positive Unix milliseconds")
	}
	if !validOptionalMarketType(r.MarketType) {
		return fmt.Errorf("market_type is invalid")
	}
	if !validOrderType(r.OrderType) ||
		!validFillPolicy(r.FillPolicy) ||
		!validOrderSide(r.Side) ||
		!validPositionSide(r.PositionSide) {
		return fmt.Errorf("order type, fill policy, side or position side is invalid")
	}
	if !isCanonicalPositiveDecimal(r.Quantity) {
		return fmt.Errorf("quantity must be a canonical positive decimal")
	}
	switch r.OrderType {
	case OrderType_ORDER_TYPE_MARKET:
		if r.LimitPrice != nil ||
			r.FillPolicy != FillPolicy_FILL_POLICY_UNSPECIFIED {
			return fmt.Errorf(
				"market order cannot have limit_price or fill_policy",
			)
		}
	case OrderType_ORDER_TYPE_LIMIT:
		if r.LimitPrice == nil ||
			!isCanonicalPositiveDecimal(r.GetLimitPrice()) ||
			r.FillPolicy == FillPolicy_FILL_POLICY_UNSPECIFIED {
			return fmt.Errorf(
				"limit order requires positive limit_price and fill_policy",
			)
		}
	default:
		return fmt.Errorf("unsupported order_type")
	}
	return nil
}

func (r *SubmitOrderReq) Validate() error {
	if r == nil || strings.TrimSpace(r.LogicalAccountId) == "" {
		return fmt.Errorf("logical_account_id is required")
	}
	return (&PlaceManualOrderReq{
		ActionId: r.ActionId, TradingAccountId: r.TradingAccountId,
		ClientOrderId: r.ClientOrderId, InstrumentId: r.InstrumentId,
		OrderType: r.OrderType, FillPolicy: r.FillPolicy, Side: r.Side,
		PositionSide: r.PositionSide, Quantity: r.Quantity, LimitPrice: r.LimitPrice, MarketType: r.MarketType,
		Reason: r.Reason, DeadlineAt: r.DeadlineAt,
	}).Validate()
}

func (r *CancelOrderReq) Validate() error {
	if r == nil ||
		strings.TrimSpace(r.ActionId) == "" ||
		strings.TrimSpace(r.OrderId) == "" ||
		strings.TrimSpace(r.Reason) == "" {
		return fmt.Errorf("action_id, order_id and reason are required")
	}
	return nil
}

func (r *GetOperatorActionReq) Validate() error {
	if r == nil || strings.TrimSpace(r.ActionId) == "" {
		return fmt.Errorf("action_id is required")
	}
	return nil
}

func (r *GetLogicalAccountTargetReq) Validate() error {
	if r == nil || strings.TrimSpace(r.LogicalAccountId) == "" {
		return fmt.Errorf("logical_account_id is required")
	}
	return nil
}

func (r *GetOrderReq) Validate() error {
	if r == nil || strings.TrimSpace(r.OrderId) == "" {
		return fmt.Errorf("order_id is required")
	}
	return nil
}

func (r *ListOrdersReq) Validate() error {
	if r == nil ||
		(strings.TrimSpace(r.LogicalAccountId) == "" &&
			strings.TrimSpace(r.TradingAccountId) == "") {
		return fmt.Errorf(
			"logical_account_id or trading_account_id is required",
		)
	}
	if err := validateTimeRange(r.StartTime, r.EndTime); err != nil {
		return err
	}
	return validatePage(r.Page)
}

func (r *ListFillsReq) Validate() error {
	if r == nil || (strings.TrimSpace(r.TradingAccountId) == "" && strings.TrimSpace(r.LogicalAccountId) == "") {
		return fmt.Errorf("trading_account_id or logical_account_id is required")
	}
	if err := validateTimeRange(r.StartTime, r.EndTime); err != nil {
		return err
	}
	return validatePage(r.Page)
}

func (r *ListPositionsReq) Validate() error {
	if r == nil ||
		(strings.TrimSpace(r.LogicalAccountId) == "" &&
			strings.TrimSpace(r.TradingAccountId) == "") {
		return fmt.Errorf(
			"logical_account_id or trading_account_id is required",
		)
	}
	return nil
}

func validExchange(value Exchange) bool {
	_, known := Exchange_name[int32(value)]
	return known && value != Exchange_EXCHANGE_UNSPECIFIED
}

// validOptionalMarketType 允许不填（未指定）；填了必须是已知市场。
func validOptionalMarketType(value MarketType) bool {
	return value == MarketType_MARKET_TYPE_UNSPECIFIED || validMarketType(value)
}

func validMarketType(value MarketType) bool {
	return value == MarketType_MARKET_TYPE_SPOT ||
		value == MarketType_MARKET_TYPE_SWAP ||
		value == MarketType_MARKET_TYPE_STOCK
}

func validExecutionMode(value ExecutionMode) bool {
	return value == ExecutionMode_EXECUTION_MODE_PAPER ||
		value == ExecutionMode_EXECUTION_MODE_LIVE
}

func validAccountEnvironment(value AccountEnvironment) bool {
	return value == AccountEnvironment_ACCOUNT_ENVIRONMENT_UNSPECIFIED ||
		value == AccountEnvironment_ACCOUNT_ENVIRONMENT_TESTNET ||
		value == AccountEnvironment_ACCOUNT_ENVIRONMENT_PRODUCTION
}

func validOrderType(value OrderType) bool {
	return value == OrderType_ORDER_TYPE_MARKET ||
		value == OrderType_ORDER_TYPE_LIMIT
}

func validFillPolicy(value FillPolicy) bool {
	switch value {
	case FillPolicy_FILL_POLICY_UNSPECIFIED,
		FillPolicy_FILL_POLICY_GTC,
		FillPolicy_FILL_POLICY_IOC,
		FillPolicy_FILL_POLICY_FOK:
		return true
	default:
		return false
	}
}

func validPositionSide(value PositionSide) bool {
	return value == PositionSide_POSITION_SIDE_UNSPECIFIED ||
		value == PositionSide_POSITION_SIDE_NET
}

func validOrderSide(value OrderSide) bool {
	return value == OrderSide_ORDER_SIDE_BUY ||
		value == OrderSide_ORDER_SIDE_SELL
}

func validatePage(page *Page) error {
	if page == nil {
		return nil
	}
	if page.Size > maxPageSize {
		return fmt.Errorf("page.size must not exceed %d", maxPageSize)
	}
	if page.Cursor != strings.TrimSpace(page.Cursor) {
		return fmt.Errorf("page.cursor must not have surrounding whitespace")
	}
	return nil
}

func validateTimeRange(startTime, endTime int64) error {
	if startTime < 0 || endTime < 0 {
		return fmt.Errorf("time range must not be negative")
	}
	if startTime > 0 && endTime > 0 && endTime < startTime {
		return fmt.Errorf("end_time must not be before start_time")
	}
	return nil
}

func isCanonicalPositiveDecimal(value string) bool {
	if len(value) == 0 ||
		len(value) > maxDecimalLength ||
		!canonicalUnsignedDecimalPattern.MatchString(value) {
		return false
	}
	number, ok := new(big.Rat).SetString(value)
	return ok && number.Sign() > 0
}

func requireID(present bool, id string, field string) error {
	if !present || strings.TrimSpace(id) == "" {
		return fmt.Errorf("%s is required", field)
	}
	return nil
}

func value(read func() string) string {
	return read()
}
