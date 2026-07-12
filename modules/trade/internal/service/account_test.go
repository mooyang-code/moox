package service

import (
	"context"
	"testing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mocker "github.com/tencent/goom"
)

type memoryAccountStore struct {
	accounts map[string]*Account
	channels map[string]*TradeChannel
}

func (m *memoryAccountStore) CreateAccount(_ context.Context, spaceID string, a *Account) error {
	if m.accounts == nil {
		m.accounts = map[string]*Account{}
	}
	cp := *a
	cp.SpaceID = spaceID
	m.accounts[a.AccountID] = &cp
	return nil
}
func (m *memoryAccountStore) UpdateAccount(_ context.Context, spaceID string, a *Account) error {
	if m.accounts[a.AccountID] == nil {
		return ErrNotFound
	}
	cp := *a
	cp.SpaceID = spaceID
	m.accounts[a.AccountID] = &cp
	return nil
}
func (m *memoryAccountStore) DeleteAccount(_ context.Context, _, accountID string) error {
	if m.accounts[accountID] == nil {
		return ErrNotFound
	}
	delete(m.accounts, accountID)
	return nil
}
func (m *memoryAccountStore) GetAccount(_ context.Context, _, accountID string) (*Account, error) {
	a := m.accounts[accountID]
	if a == nil {
		return nil, ErrNotFound
	}
	cp := *a
	return &cp, nil
}
func (m *memoryAccountStore) ListAccounts(_ context.Context, _ string, _ AccountFilter, _ Page) ([]*Account, int, error) {
	out := make([]*Account, 0, len(m.accounts))
	for _, a := range m.accounts {
		cp := *a
		out = append(out, &cp)
	}
	return out, len(out), nil
}
func (m *memoryAccountStore) GetBalances(context.Context, string, string, []string) ([]*Balance, error) {
	return nil, nil
}
func (m *memoryAccountStore) UpsertBalances(context.Context, string, []*Balance) error { return nil }
func (m *memoryAccountStore) AdjustFrozen(context.Context, string, string, string, string) error {
	return nil
}
func (m *memoryAccountStore) ListFundFlows(context.Context, string, FundFlowFilter, Page) ([]*FundFlow, int, error) {
	return nil, 0, nil
}
func (m *memoryAccountStore) AppendFundFlows(context.Context, string, []*FundFlow) error { return nil }
func (m *memoryAccountStore) CreateAPIKey(context.Context, string, *APIKey) error       { return nil }
func (m *memoryAccountStore) DeleteAPIKey(context.Context, string, string) error          { return nil }
func (m *memoryAccountStore) ListAPIKeys(context.Context, string, string) ([]*APIKey, error) {
	return nil, nil
}
func (m *memoryAccountStore) GetAPIKey(context.Context, string, string) (*APIKey, error) {
	return nil, ErrNotFound
}
func (m *memoryAccountStore) CreateChannel(_ context.Context, spaceID string, c *TradeChannel) error {
	if m.channels == nil {
		m.channels = map[string]*TradeChannel{}
	}
	cp := *c
	cp.SpaceID = spaceID
	m.channels[c.ChannelID] = &cp
	return nil
}
func (m *memoryAccountStore) UpdateChannel(_ context.Context, spaceID string, c *TradeChannel) error {
	if m.channels == nil || m.channels[c.ChannelID] == nil {
		return ErrNotFound
	}
	cp := *c
	cp.SpaceID = spaceID
	m.channels[c.ChannelID] = &cp
	return nil
}
func (m *memoryAccountStore) DeleteChannel(_ context.Context, _, channelID string) error {
	if m.channels == nil || m.channels[channelID] == nil {
		return ErrNotFound
	}
	delete(m.channels, channelID)
	return nil
}
func (m *memoryAccountStore) GetChannel(_ context.Context, _, channelID string) (*TradeChannel, error) {
	if m.channels == nil {
		return nil, ErrNotFound
	}
	c := m.channels[channelID]
	if c == nil {
		return nil, ErrNotFound
	}
	cp := *c
	return &cp, nil
}
func (m *memoryAccountStore) ListChannels(_ context.Context, _ string, _ ChannelFilter, _ Page) ([]*TradeChannel, int, error) {
	out := make([]*TradeChannel, 0)
	for _, c := range m.channels {
		cp := *c
		out = append(out, &cp)
	}
	return out, len(out), nil
}
func (m *memoryAccountStore) SaveOrder(context.Context, string, *Order) error { return nil }
func (m *memoryAccountStore) UpsertOrders(context.Context, string, []*Order) error        { return nil }
func (m *memoryAccountStore) UpdateOrder(context.Context, string, *Order) error           { return nil }
func (m *memoryAccountStore) GetOrder(context.Context, string, string, string) (*Order, error) {
	return nil, ErrNotFound
}
func (m *memoryAccountStore) ListOrders(context.Context, string, OrderFilter, Page) ([]*Order, int, error) {
	return nil, 0, nil
}
func (m *memoryAccountStore) AppendTrades(context.Context, string, []*Trade) error { return nil }
func (m *memoryAccountStore) ListTrades(context.Context, string, TradeFilter, Page) ([]*Trade, int, error) {
	return nil, 0, nil
}
func (m *memoryAccountStore) UpsertPositions(context.Context, string, []*Position) error { return nil }
func (m *memoryAccountStore) ReplacePositions(context.Context, string, string, string, []*Position) error {
	return nil
}
func (m *memoryAccountStore) ListPositions(context.Context, string, string, string) ([]*Position, error) {
	return nil, nil
}
func (m *memoryAccountStore) AppendOrderOperation(context.Context, string, *OrderOperation) error {
	return nil
}
func (m *memoryAccountStore) UpdateOrderOperation(context.Context, string, *OrderOperation) error {
	return nil
}
func (m *memoryAccountStore) GetSyncCursor(context.Context, string, string, SyncType, string) (*SyncCursor, error) {
	return nil, ErrNotFound
}
func (m *memoryAccountStore) UpsertSyncCursor(context.Context, string, *SyncCursor) error { return nil }
func (m *memoryAccountStore) ListSyncCursors(context.Context, string, string, SyncType) ([]*SyncCursor, error) {
	return nil, nil
}

func TestAccountService_CreateAccount_ValidInput_ShouldPersist(t *testing.T) {
	store := &memoryAccountStore{}
	svc := &AccountService{store: store}
	ctx := context.Background()
	got, err := svc.CreateAccount(ctx, "crypto", &Account{
		UserID: "user-1", AccountName: "main",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, got.AccountID)
	assert.Equal(t, AccountSpot, got.AccountType)
	assert.Equal(t, "USDT", got.BaseCurrency)

	fetched, err := svc.GetAccount(ctx, "crypto", got.AccountID)
	require.NoError(t, err)
	assert.Equal(t, "main", fetched.AccountName)
}

func TestAccountService_CreateAccount_InvalidInput_ShouldReject(t *testing.T) {
	svc := &AccountService{store: &memoryAccountStore{}}
	_, err := svc.CreateAccount(context.Background(), "crypto", &Account{UserID: "u"})
	assert.ErrorIs(t, err, ErrInvalidParam)
}

func TestAccountService_CreateAccount_MockStoreError_ShouldPropagate(t *testing.T) {
	mock := mocker.Create()
	defer mock.Reset()

	store := (Store)(nil)
	mock.Interface(&store).Method("CreateAccount").Apply(
		func(_ *mocker.IContext, _ context.Context, _ string, _ *Account) error {
			return assert.AnError
		},
	)

	svc := &AccountService{store: store}
	_, err := svc.CreateAccount(context.Background(), "crypto", &Account{UserID: "u", AccountName: "x"})
	assert.Error(t, err)
}

func TestAccountService_ListAccounts_ShouldDelegateStore(t *testing.T) {
	store := &memoryAccountStore{}
	svc := &AccountService{store: store}
	ctx := context.Background()
	_, err := svc.CreateAccount(ctx, "crypto", &Account{UserID: "u1", AccountName: "a1"})
	require.NoError(t, err)
	list, total, err := svc.ListAccounts(ctx, "crypto", AccountFilter{}, Page{PageNo: 1, PageSize: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, list, 1)
}

func TestAccountService_UpsertBalances_EmptySlice_ShouldNoop(t *testing.T) {
	svc := &AccountService{store: &memoryAccountStore{}}
	assert.NoError(t, svc.UpsertBalances(context.Background(), "crypto", nil))
}

func TestAccountService_Transfer_InvalidParams_ShouldReject(t *testing.T) {
	svc := &AccountService{store: &memoryAccountStore{}}
	_, _, err := svc.Transfer(context.Background(), "crypto", "", "b", "USDT", "1", "")
	assert.ErrorIs(t, err, ErrInvalidParam)
}
