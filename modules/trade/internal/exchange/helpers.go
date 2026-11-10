package exchange

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

// HMACSHA256 返回 HMAC-SHA256 摘要，币安与 OKX 签名共用。
func HMACSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

// Rejected 构造“交易所确定拒绝”的错误；err 非空时附在说明后面。
func Rejected(message string, err error) error {
	return &Error{Kind: ErrorRejected, Err: wrapMessage(message, err)}
}

// TransportUnknown 构造“结果未知”的错误，调用方应走查询恢复。
func TransportUnknown(message string, err error) error {
	return &Error{Kind: ErrorTransportUnknown, Err: wrapMessage(message, err)}
}

func wrapMessage(message string, err error) error {
	if err == nil {
		return errors.New(message)
	}
	return fmt.Errorf("%s: %w", message, err)
}

// DecimalOrZero 解析交易所返回的十进制数；空串按零处理，格式错误按拒绝处理。
func DecimalOrZero(raw string) (shared.Decimal, error) {
	if raw == "" {
		return shared.Zero(), nil
	}
	value, err := shared.ParseDecimal(raw)
	if err != nil {
		return shared.Decimal{}, Rejected("invalid Exchange decimal", err)
	}
	return value, nil
}

// ParseQuote 解析行情报价；解析失败按零处理，表示该侧没有报价。
func ParseQuote(raw string) shared.Decimal {
	value, err := shared.ParseDecimal(raw)
	if err != nil {
		return shared.Zero()
	}
	return value
}

// ValidateOrderRequest 校验下单请求的公共部分。validClientID 非空时额外校验客户端订单号。
func ValidateOrderRequest(request OrderRequest, market MarketType, validClientID func(string) bool) error {
	if strings.TrimSpace(request.ClientOrderID) == "" ||
		strings.TrimSpace(request.ExchangeSymbol) == "" ||
		!request.Side.Valid() ||
		request.Quantity.Cmp(shared.Zero()) <= 0 {
		return Rejected("invalid order request", nil)
	}
	if validClientID != nil && !validClientID(request.ClientOrderID) {
		return Rejected("client order ID must be 1-32 ASCII alphanumeric characters", nil)
	}
	switch request.OrderType {
	case OrderTypeMarket:
		if request.LimitPrice != nil ||
			request.EffectiveFillPolicy() != FillPolicyUnspecified {
			return Rejected("MARKET order cannot carry price or TimeInForce", nil)
		}
	case OrderTypeLimit:
		if request.LimitPrice == nil ||
			request.LimitPrice.Cmp(shared.Zero()) <= 0 ||
			!request.EffectiveFillPolicy().ValidForLimit() {
			return Rejected("invalid LIMIT order", nil)
		}
	default:
		return Rejected("unsupported order type", nil)
	}
	if market == MarketTypeSpot {
		if request.PositionSide != PositionSideUnspecified || request.ReduceOnly {
			return Rejected("SPOT order cannot set position side or reduce-only", nil)
		}
	} else if request.PositionSide != PositionSideNet {
		return Rejected("SWAP order requires NET position side", nil)
	}
	return nil
}
