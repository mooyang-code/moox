package service

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type transferStore struct {
	Store
	flows []*FundFlow
}

func (s *transferStore) AppendFundFlows(_ context.Context, _ string, flows []*FundFlow) error {
	for _, f := range flows {
		cp := *f
		s.flows = append(s.flows, &cp)
	}
	return nil
}

type syncBalanceStore struct {
	testServiceStore
	balances []*Balance
}

func (s *syncBalanceStore) UpsertBalances(_ context.Context, spaceID string, balances []*Balance) error {
	s.balances = nil
	for _, b := range balances {
		cp := *b
		cp.SpaceID = spaceID
		s.balances = append(s.balances, &cp)
	}
	return nil
}

func (s *syncBalanceStore) GetBalances(_ context.Context, _, _ string, _ []string) ([]*Balance, error) {
	out := make([]*Balance, len(s.balances))
	for i, b := range s.balances {
		cp := *b
		out[i] = &cp
	}
	return out, nil
}

type syncBalanceAdapter struct {
	testExchangeAdapter
}

func (syncBalanceAdapter) GetBalances(context.Context, exchange.Credential, exchange.MarketType, []string) ([]exchange.Balance, error) {
	return []exchange.Balance{{Currency: "USDT", Available: "100", Total: "100"}}, nil
}

func TestAccountService_Transfer_ValidAccounts_ShouldCreatePairedFlows(t *testing.T) {
	store := &transferStore{}
	svc := &AccountService{store: store}
	outID, inID, err := svc.Transfer(context.Background(), "crypto", "acc-a", "acc-b", "USDT", "25", "test")
	require.NoError(t, err)
	assert.NotEmpty(t, outID)
	assert.NotEmpty(t, inID)
	require.Len(t, store.flows, 2)
	assert.Equal(t, "transfer_out", store.flows[0].BizType)
	assert.Equal(t, "transfer_in", store.flows[1].BizType)
}

func TestAccountService_SyncBalances_WithLinkedChannel_ShouldUpsert(t *testing.T) {
	store := &syncBalanceStore{testServiceStore: testServiceStore{
		account: &Account{AccountID: "acc_1", ChannelID: "ch_1"},
		channel: &TradeChannel{ChannelID: "ch_1", Exchange: "binance", MarketType: "spot", AccountID: "acc_1", APIKeyID: "ak_1"},
		apiKey:  &APIKey{APIKeyID: "ak_1", APIKey: "k", APISecret: "s"},
	}}
	svc := New("trade", WithStore(store), WithExchangeFactory(func(string) (exchange.ExchangeAdapter, error) {
		return &syncBalanceAdapter{}, nil
	}))
	got, err := svc.Account.SyncBalances(context.Background(), "crypto", "acc_1")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "USDT", got[0].Currency)
	assert.Equal(t, "100", got[0].Available)
}
