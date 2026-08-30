package tradepb

import (
	"fmt"
	"math/big"
	"regexp"
	"strings"
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
		!validMarketType(r.MarketType) ||
		r.Live == nil ||
		!validAccountEnvironment(r.Live.GetEnvironment()) ||
		(r.Live.GetEnvironment() != AccountEnvironment_ACCOUNT_ENVIRONMENT_TESTNET &&
			r.Live.GetEnvironment() != AccountEnvironment_ACCOUNT_ENVIRONMENT_PRODUCTION) ||
		strings.TrimSpace(r.Live.GetCredentialSecretId()) == "" {
		return fmt.Errorf("exchange, market_type and live environment/credential are required")
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
	if r == nil || strings.TrimSpace(r.AccountName) == "" || strings.TrimSpace(r.LogicalAccountName) == "" ||
		!validExchange(r.Exchange) || !validMarketType(r.MarketType) || strings.TrimSpace(r.SettlementAsset) == "" {
		return fmt.Errorf("account_name, logical_account_name, exchange, market_type and settlement_asset are required")
	}
	if !isCanonicalPositiveDecimal(r.InitialBalance) || !canonicalUnsignedDecimalPattern.MatchString(r.MakerFeeRate) ||
		!canonicalUnsignedDecimalPattern.MatchString(r.TakerFeeRate) || !canonicalUnsignedDecimalPattern.MatchString(r.SlippageBps) {
		return fmt.Errorf("paper configuration decimals are invalid")
	}
	margin := strings.ToUpper(strings.TrimSpace(r.MarginMode))
	if r.MarketType == MarketType_MARKET_TYPE_SPOT && margin != "" {
		return fmt.Errorf("SPOT paper simulation cannot configure margin_mode")
	}
	if r.MarketType == MarketType_MARKET_TYPE_SWAP && margin != "" && margin != "CROSS" {
		return fmt.Errorf("SWAP paper simulation requires CROSS margin_mode")
	}
	if r.MarketType == MarketType_MARKET_TYPE_SWAP && !strings.EqualFold(strings.TrimSpace(r.SettlementAsset), "USDT") {
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

// Validate enforces the transport-level bounds for the batch DNS resolver
// request. Domain syntax and the configured allowlist are checked by the
// resolver service after normalization; this keeps generated RPC validation
// aligned with the public request contract without duplicating resolver state.
func (r *ResolveDomainsReq) Validate() error {
	if r == nil || len(r.Domains) == 0 {
		return fmt.Errorf("domains are required")
	}
	if len(r.Domains) > 16 {
		return fmt.Errorf("at most 16 domains are allowed")
	}
	if r.MaxIpsPerDomain > 4 {
		return fmt.Errorf("max_ips_per_domain must be at most 4")
	}
	for _, domain := range r.Domains {
		if strings.TrimSpace(domain) == "" {
			return fmt.Errorf("domain must not be empty")
		}
	}
	return nil
}

func (r *CreateLogicalAccountReq) Validate() error {
	if r == nil || strings.TrimSpace(r.Name) == "" ||
		!validExecutionMode(r.ExecutionMode) ||
		!validMarketType(r.MarketType) ||
		strings.TrimSpace(r.SettlementAsset) == "" {
		return fmt.Errorf(
			"name, execution_mode, market_type and settlement_asset are required",
		)
	}
	return nil
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
	return validateLogicalOwner(r != nil, value(func() string {
		if r == nil {
			return ""
		}
		return r.LogicalAccountId
	}), value(func() string {
		if r == nil {
			return ""
		}
		return r.RunnerId
	}))
}

func (r *ReleaseLogicalAccountOwnerReq) Validate() error {
	return validateLogicalOwner(r != nil, value(func() string {
		if r == nil {
			return ""
		}
		return r.LogicalAccountId
	}), value(func() string {
		if r == nil {
			return ""
		}
		return r.RunnerId
	}))
}

func (r *RebindLogicalAccountOwnerReq) Validate() error {
	if err := validateLogicalOwner(r != nil, value(func() string {
		if r == nil {
			return ""
		}
		return r.LogicalAccountId
	}), value(func() string {
		if r == nil {
			return ""
		}
		return r.RunnerId
	})); err != nil {
		return err
	}
	if strings.TrimSpace(r.RebindKey) == "" {
		return fmt.Errorf("rebind_key is required")
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
	if r == nil || strings.TrimSpace(r.TradingAccountId) == "" {
		return fmt.Errorf("trading_account_id is required")
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
	return value == Exchange_EXCHANGE_BINANCE ||
		value == Exchange_EXCHANGE_OKX
}

func validMarketType(value MarketType) bool {
	return value == MarketType_MARKET_TYPE_SPOT ||
		value == MarketType_MARKET_TYPE_SWAP
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

func validateLogicalOwner(
	present bool,
	logicalAccountID string,
	runnerID string,
) error {
	if !present ||
		strings.TrimSpace(logicalAccountID) == "" ||
		strings.TrimSpace(runnerID) == "" {
		return fmt.Errorf("logical_account_id and runner_id are required")
	}
	return nil
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
