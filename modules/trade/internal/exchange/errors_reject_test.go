package exchange

import (
	"errors"
	"testing"
)

func TestRejectMessageTranslatesCommonErrors(t *testing.T) {
	cases := map[string]error{
		"保证金不足":         &Error{Kind: ErrorRejected, Code: "-2019", Err: errors.New("Margin is insufficient")},
		"余额不足":          &Error{Kind: ErrorInsufficientBalance},
		"交易所返回：unknown": &Error{Kind: ErrorRejected, Err: errors.New("unknown")},
		"plain":         errors.New("plain"),
	}
	for want, err := range cases {
		if got := RejectMessage(err); got != want {
			t.Errorf("RejectMessage(%v) = %q, want %q", err, got, want)
		}
	}
}
