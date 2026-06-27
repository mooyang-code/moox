package service

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

func TestService_Health(t *testing.T) {
	svc := New("trade")
	if got := svc.Health(); got.Module != "trade" || !got.Ready {
		t.Fatalf("Health() = %+v, want ready trade module", got)
	}
}

func TestAccountService_CreateAccount(t *testing.T) {
	st := newMemStore()
	svc := New("trade", WithStore(st))

	a, err := svc.Account.CreateAccount(context.Background(), "sp1", &Account{
		UserID:      "u1",
		AccountName: "main",
	})
	if err != nil {
		t.Fatalf("CreateAccount error: %v", err)
	}
	if a.AccountID == "" {
		t.Fatal("expected generated account_id")
	}
	if a.AccountType != AccountSpot || a.BaseCurrency != "USDT" || a.Status != AccountNormal {
		t.Fatalf("defaults not applied: %+v", a)
	}

	if _, err := svc.Account.CreateAccount(context.Background(), "sp1", &Account{}); err != ErrInvalidParam {
		t.Fatalf("expected ErrInvalidParam, got %v", err)
	}
}

func TestAccountService_Transfer(t *testing.T) {
	st := newMemStore()
	svc := New("trade", WithStore(st))

	out, in, err := svc.Account.Transfer(context.Background(), "sp1", "a1", "a2", "USDT", "10", "")
	if err != nil {
		t.Fatalf("Transfer error: %v", err)
	}
	if out == "" || in == "" || len(st.flows) != 2 {
		t.Fatalf("expected paired flows, got out=%s in=%s flows=%d", out, in, len(st.flows))
	}

	if _, _, err := svc.Account.Transfer(context.Background(), "sp1", "a1", "a1", "USDT", "10", ""); err != ErrInvalidParam {
		t.Fatalf("expected ErrInvalidParam for same account, got %v", err)
	}
}

func TestOrderService_TestChannelUsesAdapter(t *testing.T) {
	st := newMemStore()
	_ = st.CreateChannel(context.Background(), "sp1", &TradeChannel{
		ChannelID: "ch1", ChannelName: "c", Exchange: "stub", MarketType: "spot",
	})
	svc := New("trade",
		WithStore(st),
		WithExchangeFactory(func(name string) (exchange.ExchangeAdapter, error) {
			return &stubAdapter{}, nil
		}),
	)

	ok, lat, err := svc.Order.TestChannel(context.Background(), "sp1", "ch1")
	if err != nil || !ok || lat != 7 {
		t.Fatalf("TestChannel = (%v,%d,%v), want (true,7,nil)", ok, lat, err)
	}
}

// stubAdapter 仅实现测试用到的方法，其余返回零值。
type stubAdapter struct{ exchange.ExchangeAdapter }

func (s *stubAdapter) Ping(ctx context.Context, cred exchange.Credential) (int64, error) {
	return 7, nil
}
