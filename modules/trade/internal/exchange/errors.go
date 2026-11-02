package exchange

import (
	"errors"
	"fmt"
	"strings"
)

type ErrorKind string

const (
	ErrorRejected            ErrorKind = "REJECTED"
	ErrorInsufficientBalance ErrorKind = "INSUFFICIENT_BALANCE"
	ErrorRateLimited         ErrorKind = "RATE_LIMITED"
	ErrorOrderNotFound       ErrorKind = "ORDER_NOT_FOUND"
	ErrorAuthentication      ErrorKind = "AUTHENTICATION"
	ErrorNotReady            ErrorKind = "NOT_READY"
	ErrorTransportUnknown    ErrorKind = "TRANSPORT_UNKNOWN"
)

type Error struct {
	Kind       ErrorKind
	HTTPStatus int
	Code       string
	Err        error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	message := string(e.Kind)
	details := make([]string, 0, 2)
	if e.HTTPStatus != 0 {
		details = append(details, fmt.Sprintf("HTTP %d", e.HTTPStatus))
	}
	if e.Code != "" {
		details = append(details, "code "+e.Code)
	}
	if len(details) != 0 {
		message += " (" + strings.Join(details, ", ") + ")"
	}
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func IsKind(err error, kind ErrorKind) bool {
	var exchangeErr *Error
	return errors.As(err, &exchangeErr) && exchangeErr != nil && exchangeErr.Kind == kind
}

// IsConfirmedOrderMissing 报告交易所是否用明确的错误码确认订单不存在。
func IsConfirmedOrderMissing(err error) bool {
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != ErrorOrderNotFound {
		return false
	}
	return typed.Code == "-2013" || typed.Code == "51603"
}

// rejectCodeMessages 是常见拒单错误码的中文说明，其余错误显示交易所原文。
var rejectCodeMessages = map[string]string{
	"-1013": "价格或数量不符合交易规则",
	"-1111": "数量精度超过交易所限制",
	"-2010": "交易所拒绝新订单",
	"-2019": "保证金不足",
	"-2022": "只减仓订单被拒绝",
	"-4164": "金额低于最小名义金额",
	"51008": "余额不足",
	"51020": "数量低于最小下单量",
}

// RejectMessage 把交易所拒单错误转成给用户看的中文原因。
func RejectMessage(err error) string {
	var exchangeErr *Error
	if !errors.As(err, &exchangeErr) {
		return err.Error()
	}
	if message, ok := rejectCodeMessages[exchangeErr.Code]; ok {
		return message
	}
	switch exchangeErr.Kind {
	case ErrorInsufficientBalance:
		return "余额不足"
	case ErrorRateLimited:
		return "请求过于频繁"
	case ErrorAuthentication:
		return "交易所鉴权失败"
	}
	if exchangeErr.Err != nil {
		return "交易所返回：" + exchangeErr.Err.Error()
	}
	return "交易所返回：" + exchangeErr.Error()
}
