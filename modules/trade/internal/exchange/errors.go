package exchange

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type ErrorKind string

const (
	ErrorRejected            ErrorKind = "REJECTED"
	ErrorInsufficientBalance ErrorKind = "INSUFFICIENT_BALANCE"
	// ErrorRateLimited 是 IP 级限流（429、-1003、WAF 403），同一域名的请求都要停。
	ErrorRateLimited ErrorKind = "RATE_LIMITED"
	// ErrorOrderRateLimited 是账户下单限流（-1015），只停这个账户下单，查询照常。
	ErrorOrderRateLimited ErrorKind = "ORDER_RATE_LIMITED"
	// ErrorBanned 是 418 封禁，封禁期间同一域名的请求都不能发。
	ErrorBanned ErrorKind = "BANNED"
	// ErrorThrottled 是交易所系统限流（-1008），确定未受理。
	ErrorThrottled ErrorKind = "THROTTLED"
	// ErrorNotSent 表示限流暂停期间本地直接拦下，请求没有发出。
	ErrorNotSent          ErrorKind = "NOT_SENT"
	ErrorOrderNotFound    ErrorKind = "ORDER_NOT_FOUND"
	ErrorAuthentication   ErrorKind = "AUTHENTICATION"
	ErrorNotReady         ErrorKind = "NOT_READY"
	ErrorTransportUnknown ErrorKind = "TRANSPORT_UNKNOWN"
)

// NotAccepted 报告请求是否确定没有被交易所受理：限流、封禁、系统限流和本地拦截。
// 这类下单可以安全回到待提交，不算拒单，也不需要查询结果。
func NotAccepted(err error) bool {
	var typed *Error
	if !errors.As(err, &typed) || typed == nil {
		return false
	}
	switch typed.Kind {
	case ErrorRateLimited, ErrorOrderRateLimited, ErrorBanned, ErrorThrottled, ErrorNotSent:
		return true
	}
	return false
}

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
	case ErrorRateLimited, ErrorOrderRateLimited, ErrorThrottled, ErrorNotSent:
		return "请求过于频繁"
	case ErrorBanned:
		return "交易所已临时封禁当前 IP"
	case ErrorAuthentication:
		return "交易所鉴权失败"
	}
	if exchangeErr.Err != nil {
		return "交易所返回：" + exchangeErr.Err.Error()
	}
	return "交易所返回：" + exchangeErr.Error()
}

// FailureSummary 把交易所调用失败转成简短的中文说明：只含错误类别、HTTP 状态和错误码，
// 不含请求串、响应体原文（可能带签名、账户信息）。检测、划转等要把失败原因给人看的地方共用。
func FailureSummary(err error) string {
	var typed *Error
	switch {
	case errors.As(err, &typed):
		var kind string
		switch typed.Kind {
		case ErrorRateLimited, ErrorOrderRateLimited, ErrorThrottled, ErrorBanned, ErrorNotSent:
			kind = "被限频或本地限流暂停"
		case ErrorTransportUnknown:
			kind = "网络错误或交易所服务异常"
		case ErrorAuthentication:
			kind = "鉴权类错误"
			if typed.Code == "" {
				kind = "鉴权类错误，没有可识别的错误码"
			}
		case ErrorRejected:
			kind = "被交易所拒绝"
		case ErrorInsufficientBalance:
			kind = "余额不足"
		case ErrorNotReady:
			kind = "账户会话还没准备好"
		default:
			kind = "交易所返回了无法判断的错误"
		}
		parts := []string{kind}
		if typed.HTTPStatus != 0 {
			parts = append(parts, fmt.Sprintf("HTTP %d", typed.HTTPStatus))
		}
		if typed.Code != "" {
			parts = append(parts, "错误码 "+typed.Code)
		}
		return strings.Join(parts, "，")
	case errors.Is(err, context.DeadlineExceeded):
		return "请求超时"
	}
	return "没有得到交易所的明确答复"
}
