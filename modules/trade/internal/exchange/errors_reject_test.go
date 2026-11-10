package exchange

import (
	"errors"
	"testing"
)

func TestRejectMessageTranslatesCommonErrors(t *testing.T) {
	cases := map[string]error{
		"保证金不足":    &Error{Kind: ErrorRejected, Code: "-2019", Err: errors.New("Margin is insufficient")},
		"余额不足":     &Error{Kind: ErrorInsufficientBalance},
		"标的当前不可交易": &Error{Kind: ErrorRejected, Code: "-2010", Err: errors.New("http 400: Market is closed.")},
		"交易所拒绝新订单（例如只挂单的订单会立即成交）": &Error{Kind: ErrorRejected, Code: "-2010", Err: errors.New("Order would immediately match and take.")},
		"交易所返回：unknown": &Error{Kind: ErrorRejected, Err: errors.New("unknown")},
		"plain":         errors.New("plain"),
	}
	for want, err := range cases {
		if got := RejectMessage(err); got != want {
			t.Errorf("RejectMessage(%v) = %q, want %q", err, got, want)
		}
	}
}
