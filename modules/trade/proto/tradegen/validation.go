package tradepb

import (
	"fmt"
	"math/big"
	"regexp"
	"slices"
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
	if !validExchange(r.Exchange) || !validMarkets(r.Markets) {
		return fmt.Errorf("exchange and markets are required")
	}
	if (r.Live == nil) == (r.Paper == nil) {
		return fmt.Errorf("exactly one of live or paper config is required")
	}
	if r.Live != nil {
		if !validAccountEnvironment(r.Live.GetEnvironment()) ||
			(r.Live.GetEnvironment() != AccountEnvironment_ACCOUNT_ENVIRONMENT_TESTNET &&
				r.Live.GetEnvironment() != AccountEnvironment_ACCOUNT_ENVIRONMENT_PRODUCTION) ||
			strings.TrimSpace(r.Live.GetCredentialSecretId()) == "" {
			return fmt.Errorf("live environment and credential_secret_id are required")
		}
		if strings.TrimSpace(r.LogicalAccountId) != "" ||
			strings.TrimSpace(r.LogicalAccountName) != "" ||
			r.ControlMode != ControlMode_CONTROL_MODE_UNSPECIFIED ||
			r.Priority != 0 {
			return fmt.Errorf("logical account creation options are only supported for paper accounts")
		}
		return nil
	}
	if strings.TrimSpace(r.SettlementAsset) == "" {
		return fmt.Errorf("settlement_asset is required for paper account")
	}
	if !validCreationControlMode(r.ControlMode) {
		return fmt.Errorf("control_mode is invalid")
	}
	if strings.TrimSpace(r.LogicalAccountId) != "" && strings.TrimSpace(r.LogicalAccountName) != "" {
		return fmt.Errorf("logical_account_id 和 logical_account_name 只能给一个：加入已有组合，或新建组合")
	}
	if r.Priority < 0 {
		return fmt.Errorf("priority must not be negative")
	}
	if err := validateOrderRetentionDays(r.Paper.OrderRetentionDays); err != nil {
		return err
	}
	if !isCanonicalPositiveDecimal(r.Paper.InitialBalance) ||
		!isOptionalCanonicalUnsignedDecimal(r.Paper.MakerFeeRate) ||
		!isOptionalCanonicalUnsignedDecimal(r.Paper.TakerFeeRate) ||
		!isOptionalCanonicalUnsignedDecimal(r.Paper.SlippageBps) {
		return fmt.Errorf("paper configuration decimals are invalid")
	}
	if !isSlippageBPSLessThan10000(r.Paper.SlippageBps) {
		return fmt.Errorf("slippage_bps must be less than 10000")
	}
	margin := strings.ToUpper(strings.TrimSpace(r.MarginMode))
	hasSwap := slices.Contains(r.Markets, marketSwap)
	if !hasSwap && margin != "" {
		return fmt.Errorf("paper account without swap cannot configure margin_mode")
	}
	if hasSwap && margin != "" && margin != "CROSS" {
		return fmt.Errorf("swap paper account requires CROSS margin_mode")
	}
	if hasSwap && !strings.EqualFold(strings.TrimSpace(r.SettlementAsset), "USDT") {
		return fmt.Errorf("swap 模拟盘的结算资产必须是 USDT")
	}
	return nil
}

func (r *UpdateTradingAccountReq) Validate() error {
	if err := requireID(r != nil, value(func() string {
		if r == nil {
			return ""
		}
		return r.TradingAccountId
	}), "trading_account_id"); err != nil {
		return err
	}
	return validateOrderRetentionDays(r.OrderRetentionDays)
}

// maxOrderRetentionDays 与交易模块 tradingaccount.MaxOrderRetentionDays 一致。
const maxOrderRetentionDays = 365

// validateOrderRetentionDays 检查订单保留天数：不传不检查，传了要在 0～365 之间（0 表示不清理）。
func validateOrderRetentionDays(days *int32) error {
	if days != nil && (*days < 0 || *days > maxOrderRetentionDays) {
		return fmt.Errorf("order_retention_days 要在 0～%d 之间（0 表示不清理）", maxOrderRetentionDays)
	}
	return nil
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
	if r.Exchange != "" && !validExchange(r.Exchange) {
		return fmt.Errorf("invalid exchange")
	}
	if !validOptionalMarketType(r.MarketType) {
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

func (r *InspectExchangeCredentialReq) Validate() error {
	if r == nil || !validExchange(r.Exchange) ||
		(r.Environment != AccountEnvironment_ACCOUNT_ENVIRONMENT_TESTNET &&
			r.Environment != AccountEnvironment_ACCOUNT_ENVIRONMENT_PRODUCTION) ||
		strings.TrimSpace(r.CredentialSecretId) == "" {
		return fmt.Errorf("exchange, environment and credential_secret_id are required")
	}
	// 市场可以不填（预览），填了就必须合法且不重复。
	if len(r.Markets) != 0 && !validMarkets(r.Markets) {
		return fmt.Errorf("markets are invalid")
	}
	return nil
}

func (r *ResolveWalletTransferReq) Validate() error {
	if r == nil || strings.TrimSpace(r.TransferId) == "" {
		return fmt.Errorf("transfer_id is required")
	}
	if r.Result != "COMPLETED" && r.Result != "FAILED" {
		return fmt.Errorf("result must be COMPLETED or FAILED")
	}
	if len([]rune(r.Note)) > 200 {
		return fmt.Errorf("note is too long")
	}
	return nil
}

func (r *ReinspectTradingAccountReq) Validate() error {
	if r == nil || strings.TrimSpace(r.TradingAccountId) == "" {
		return fmt.Errorf("trading_account_id is required")
	}
	return nil
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

// validMarkets 要求市场集合非空、每项合法且不重复；顺序和 equity 不能与现货、合约混合由服务端规范化时校验。
func validMarkets(markets []string) bool {
	if len(markets) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(markets))
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

func (r *UpdateLogicalAccountOrderSettingsReq) Validate() error {
	if r == nil || strings.TrimSpace(r.LogicalAccountId) == "" || r.Settings == nil {
		return fmt.Errorf("logical_account_id and settings are required")
	}
	return nil
}

func (r *UpdateLogicalAccountFundSettingsReq) Validate() error {
	if r == nil || strings.TrimSpace(r.LogicalAccountId) == "" || r.Settings == nil {
		return fmt.Errorf("logical_account_id and settings are required")
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

func (r *CloseTargetSessionReq) Validate() error {
	if r == nil || strings.TrimSpace(r.LogicalAccountId) == "" || strings.TrimSpace(r.InstanceId) == "" || strings.TrimSpace(r.SessionId) == "" || r.SessionStartedAt <= 0 {
		return fmt.Errorf("logical_account_id, instance_id, session_id and a positive session_started_at are required")
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

func (r *SubmitOrderReq) Validate() error {
	if r == nil || strings.TrimSpace(r.LogicalAccountId) == "" {
		return fmt.Errorf("logical_account_id is required")
	}
	if strings.TrimSpace(r.ActionId) == "" ||
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

// maxShadowTargets 是一次最多列出的影子目标条数。
const maxShadowTargets = 200

func (r *ListShadowTargetsReq) Validate() error {
	if r == nil || strings.TrimSpace(r.InstanceId) == "" {
		return fmt.Errorf("instance_id is required")
	}
	if r.Limit < 0 || r.Limit > maxShadowTargets {
		return fmt.Errorf("limit must be between 0 and %d", maxShadowTargets)
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
	if r.OwnerType != "" && !validOrderOwnerType(r.OwnerType) {
		return fmt.Errorf("owner_type is invalid")
	}
	if err := validateTimeRange(r.StartTime, r.EndTime); err != nil {
		return err
	}
	return validatePage(r.Page)
}

func validOrderOwnerType(value string) bool {
	switch value {
	case "TARGET", "OPERATOR", "EXTERNAL", "SYSTEM":
		return true
	}
	return false
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

// 交易所与市场类型在接口上是小写文本，取值与 Trade 内部常量一致。
const (
	marketSpot   = "spot"
	marketSwap   = "swap"
	marketEquity = "equity"
)

// validExchange 只收 binance、okx、ashare、hkex 四个小写值。
func validExchange(value string) bool {
	switch value {
	case "binance", "okx", "ashare", "hkex":
		return true
	default:
		return false
	}
}

// validOptionalMarketType 允许不填（空串）；填了必须是已知市场。
func validOptionalMarketType(value string) bool {
	return value == "" || validMarketType(value)
}

func validMarketType(value string) bool {
	return value == marketSpot || value == marketSwap || value == marketEquity
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

func isOptionalCanonicalUnsignedDecimal(value string) bool {
	if value == "" {
		return true
	}
	return len(value) <= maxDecimalLength && canonicalUnsignedDecimalPattern.MatchString(value)
}

var maxSlippageBPSExclusive = big.NewRat(10000, 1)

func isSlippageBPSLessThan10000(value string) bool {
	if value == "" {
		return true
	}
	number, ok := new(big.Rat).SetString(value)
	return ok && number.Sign() >= 0 && number.Cmp(maxSlippageBPSExclusive) < 0
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

// 下架名单只收现货和永续。
func validDelistMarket(market string) bool {
	return market == marketSpot || market == marketSwap
}

func validateInstrumentKey(exchange, market, symbol string) error {
	if !validExchange(exchange) || !validDelistMarket(market) || strings.TrimSpace(symbol) == "" {
		return fmt.Errorf("exchange, market_type (spot or swap) and exchange_symbol are required")
	}
	if symbol != strings.TrimSpace(symbol) {
		return fmt.Errorf("exchange_symbol must not have surrounding whitespace")
	}
	return nil
}

func (r *UnblockInstrumentReq) Validate() error {
	if r == nil {
		return fmt.Errorf("request is required")
	}
	return validateInstrumentKey(r.Exchange, r.MarketType, r.ExchangeSymbol)
}

func (r *ListBlockedInstrumentsReq) Validate() error { return nil }

func (r *ListInstrumentHoldersReq) Validate() error {
	if r == nil {
		return fmt.Errorf("request is required")
	}
	return validateInstrumentKey(r.Exchange, r.MarketType, r.ExchangeSymbol)
}

func (r *ListDelistsReq) Validate() error {
	if r == nil {
		return fmt.Errorf("request is required")
	}
	if r.Limit < 0 || r.Limit > 500 {
		return fmt.Errorf("limit must be between 0 and 500")
	}
	return nil
}

func (r *SummarizeAccountPnlReq) Validate() error {
	if r == nil || (strings.TrimSpace(r.LogicalAccountId) == "" && strings.TrimSpace(r.TradingAccountId) == "") {
		return fmt.Errorf("logical_account_id or trading_account_id is required")
	}
	if err := validateTimeRange(r.StartTime, r.EndTime); err != nil {
		return err
	}
	// 成交汇总逐行读成交表，资金费最长也只查 90 天：时间范围和资金流水一样限制在 90 天内。
	if r.StartTime > 0 {
		end := r.EndTime
		if end == 0 {
			end = time.Now().UnixMilli()
		}
		if end-r.StartTime > maxLedgerWindow {
			return fmt.Errorf("时间范围不能超过 90 天")
		}
	}
	return nil
}

var validOperatorActionTypes = map[string]struct{}{
	"MANUAL_ORDER": {}, "CANCEL_ORDER": {}, "FLATTEN": {}, "REDUCE": {}, "DELIST_FLATTEN": {},
}

func (r *ListOperatorActionsReq) Validate() error {
	if r == nil || strings.TrimSpace(r.LogicalAccountId) == "" {
		return fmt.Errorf("logical_account_id is required")
	}
	if r.ActionType != "" {
		if _, ok := validOperatorActionTypes[r.ActionType]; !ok {
			return fmt.Errorf("action_type is invalid")
		}
	}
	if err := validateTimeRange(r.StartTime, r.EndTime); err != nil {
		return err
	}
	return validatePage(r.Page)
}
