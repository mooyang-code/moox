package order

// RejectCode 是订单被拒绝、撤销或强制结束时的结构化错误码。
// 业务状态机一律按本枚举判断，不解析展示文案 c_reject_reason。
type RejectCode string

const (
	RejectCodeNone               RejectCode = ""
	RejectCodeOutOfSession       RejectCode = "OUT_OF_SESSION"
	RejectCodeInsufficientFunds  RejectCode = "INSUFFICIENT_FUNDS"
	RejectCodeReduceOnly         RejectCode = "REDUCE_ONLY"
	RejectCodeQuantityRule       RejectCode = "QUANTITY_RULE"
	RejectCodeNotionalLimit      RejectCode = "NOTIONAL_LIMIT"
	RejectCodeLeverageLimit      RejectCode = "LEVERAGE_LIMIT"
	RejectCodeInstrumentDisabled RejectCode = "INSTRUMENT_DISABLED"
	RejectCodeInstrumentGone     RejectCode = "INSTRUMENT_GONE"
	RejectCodePriceLimit         RejectCode = "PRICE_LIMIT"
	RejectCodeInvalidSpec        RejectCode = "INVALID_SPEC"
	RejectCodeCrossZero          RejectCode = "CROSS_ZERO"
	RejectCodeAccountOwnership   RejectCode = "ACCOUNT_OWNERSHIP"
	RejectCodeExchangeRejected   RejectCode = "EXCHANGE_REJECTED"
	RejectCodeSessionClose       RejectCode = "SESSION_CLOSE"
	RejectCodePaperClosed        RejectCode = "PAPER_CLOSED"
	RejectCodeManualExpire       RejectCode = "MANUAL_EXPIRE"
	RejectCodeUnknown            RejectCode = "UNKNOWN"
)

// ValidRejectCode 报告错误码是否在约定集合内。
func ValidRejectCode(code RejectCode) bool {
	switch code {
	case RejectCodeNone,
		RejectCodeOutOfSession,
		RejectCodeInsufficientFunds,
		RejectCodeReduceOnly,
		RejectCodeQuantityRule,
		RejectCodeNotionalLimit,
		RejectCodeLeverageLimit,
		RejectCodeInstrumentDisabled,
		RejectCodeInstrumentGone,
		RejectCodePriceLimit,
		RejectCodeInvalidSpec,
		RejectCodeCrossZero,
		RejectCodeAccountOwnership,
		RejectCodeExchangeRejected,
		RejectCodeSessionClose,
		RejectCodePaperClosed,
		RejectCodeManualExpire,
		RejectCodeUnknown:
		return true
	default:
		return false
	}
}
