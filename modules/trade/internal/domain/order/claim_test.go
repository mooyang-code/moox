package order

import (
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

// 提交结果未知的订单不能直接撤单：要先解析成挂单（认领）或退回待提交。
func TestBeginCancelRejectsSubmitUnknown(t *testing.T) {
	value, _, _ := New("order-1", validSpec())
	_, _ = value.BeginSubmit()
	_, _ = value.MarkSubmitUnknown()
	if _, err := value.BeginCancel(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("BeginCancel() error = %v, want %v", err, ErrInvalidTransition)
	}
	if value.State != SubmitUnknown {
		t.Fatalf("state = %s, want %s", value.State, SubmitUnknown)
	}
}

func TestClaimMovesPendingToOpenAndRecordsExchangeOrderID(t *testing.T) {
	value, _, _ := New("order-1", validSpec())
	if _, err := value.Claim("exchange-1"); err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if value.State != Open || value.ExchangeOrderID != "exchange-1" {
		t.Fatalf("state = %s, exchange order ID = %q", value.State, value.ExchangeOrderID)
	}
	if _, err := value.Claim("exchange-1"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("second Claim() error = %v, want %v", err, ErrInvalidTransition)
	}

	conflicting, _, _ := New("order-2", validSpec())
	conflicting.ExchangeOrderID = "exchange-1"
	if _, err := conflicting.Claim("exchange-2"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("conflicting Claim() error = %v, want %v", err, ErrInvalidTransition)
	}

	anonymous, _, _ := New("order-3", validSpec())
	if _, err := anonymous.Claim(""); err != nil || anonymous.State != Open || anonymous.ExchangeOrderID != "" {
		t.Fatalf("Claim(\"\") state = %s, error = %v", anonymous.State, err)
	}
}

func TestClaimedPendingOrderAcceptsFills(t *testing.T) {
	value, _, _ := New("order-1", validSpec())
	if _, err := value.ApplyFill(Fill{ID: "fill-1", Quantity: shared.MustDecimal("0.4")}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("ApplyFill on Pending error = %v, want %v", err, ErrInvalidTransition)
	}
	_, _ = value.Claim("exchange-1")
	if _, err := value.ApplyFill(Fill{ID: "fill-1", Quantity: shared.MustDecimal("0.4")}); err != nil {
		t.Fatalf("ApplyFill() error = %v", err)
	}
	if value.State != PartiallyFilled {
		t.Fatalf("state = %s, want %s", value.State, PartiallyFilled)
	}
}

func TestConfirmCancelAcceptsPendingOrder(t *testing.T) {
	value, _, _ := New("order-1", validSpec())
	if _, err := value.ConfirmCancel(); err != nil {
		t.Fatalf("ConfirmCancel() error = %v", err)
	}
	if value.State != Canceled {
		t.Fatalf("state = %s, want %s", value.State, Canceled)
	}
}

// 合成外部订单：数量只是已知成交之和，后到的成交把数量一起加上，订单始终是已成交。
func TestApplySyntheticFillGrowsFilledOrder(t *testing.T) {
	spec := validSpec()
	spec.Quantity = shared.MustDecimal("0.2")
	value, _, _ := New("order-1", spec)
	_, _ = value.Claim("exchange-1")
	if _, err := value.ApplySyntheticFill(Fill{ID: "fill-1", Quantity: shared.MustDecimal("0.2")}); err != nil {
		t.Fatalf("first fill error = %v", err)
	}
	if value.State != Filled {
		t.Fatalf("state = %s, want %s", value.State, Filled)
	}
	if _, err := value.ApplySyntheticFill(Fill{ID: "fill-2", Quantity: shared.MustDecimal("0.3")}); err != nil {
		t.Fatalf("second fill error = %v", err)
	}
	if value.State != Filled || value.FilledQuantity.String() != "0.5" || value.Spec.Quantity.String() != "0.5" {
		t.Fatalf("state = %s, filled = %s, quantity = %s", value.State, value.FilledQuantity, value.Spec.Quantity)
	}
	// 重复的成交不再放大。
	if events, err := value.ApplySyntheticFill(Fill{ID: "fill-2", Quantity: shared.MustDecimal("0.3")}); err != nil || len(events) != 0 {
		t.Fatalf("duplicate fill events = %v, error = %v", events, err)
	}
	if value.Spec.Quantity.String() != "0.5" {
		t.Fatalf("quantity = %s", value.Spec.Quantity)
	}
}

// 普通订单超量仍然是非法迁移。
func TestApplyFillStillRejectsFilledOrderGrowth(t *testing.T) {
	spec := validSpec()
	spec.Quantity = shared.MustDecimal("0.2")
	value, _, _ := New("order-1", spec)
	_, _ = value.Claim("exchange-1")
	_, _ = value.ApplyFill(Fill{ID: "fill-1", Quantity: shared.MustDecimal("0.2")})
	if _, err := value.ApplyFill(Fill{ID: "fill-2", Quantity: shared.MustDecimal("0.3")}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("ApplyFill() error = %v, want %v", err, ErrInvalidTransition)
	}
}
