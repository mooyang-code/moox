package service

import "context"

// memStore 是用于单测的内存 Store 实现，仅覆盖测试所需行为。
type memStore struct {
	accounts map[string]*Account
	channels map[string]*TradeChannel
	orders   map[string]*Order
	apikeys  map[string]*APIKey
	flows    []*FundFlow
}

func newMemStore() *memStore {
	return &memStore{
		accounts: map[string]*Account{},
		channels: map[string]*TradeChannel{},
		orders:   map[string]*Order{},
		apikeys:  map[string]*APIKey{},
	}
}

var _ Store = (*memStore)(nil)

func (m *memStore) CreateAccount(_ context.Context, _ string, a *Account) error {
	m.accounts[a.AccountID] = a
	return nil
}
func (m *memStore) UpdateAccount(_ context.Context, _ string, a *Account) error {
	if _, ok := m.accounts[a.AccountID]; !ok {
		return ErrNotFound
	}
	m.accounts[a.AccountID] = a
	return nil
}
func (m *memStore) DeleteAccount(_ context.Context, _ string, id string) error {
	delete(m.accounts, id)
	return nil
}
func (m *memStore) GetAccount(_ context.Context, _ string, id string) (*Account, error) {
	if a, ok := m.accounts[id]; ok {
		return a, nil
	}
	return nil, ErrNotFound
}
func (m *memStore) ListAccounts(_ context.Context, _ string, _ AccountFilter, _ Page) ([]*Account, int, error) {
	out := make([]*Account, 0, len(m.accounts))
	for _, a := range m.accounts {
		out = append(out, a)
	}
	return out, len(out), nil
}

func (m *memStore) GetBalances(_ context.Context, _ string, _ string, _ []string) ([]*Balance, error) {
	return nil, nil
}
func (m *memStore) UpsertBalances(_ context.Context, _ string, _ []*Balance) error { return nil }
func (m *memStore) AdjustFrozen(_ context.Context, _ string, _, _, _ string) error  { return nil }

func (m *memStore) ListFundFlows(_ context.Context, _ string, _ FundFlowFilter, _ Page) ([]*FundFlow, int, error) {
	return m.flows, len(m.flows), nil
}
func (m *memStore) AppendFundFlows(_ context.Context, _ string, flows []*FundFlow) error {
	m.flows = append(m.flows, flows...)
	return nil
}

func (m *memStore) CreateAPIKey(_ context.Context, _ string, k *APIKey) error {
	m.apikeys[k.APIKeyID] = k
	return nil
}
func (m *memStore) DeleteAPIKey(_ context.Context, _ string, id string) error {
	delete(m.apikeys, id)
	return nil
}
func (m *memStore) ListAPIKeys(_ context.Context, _ string, _ string) ([]*APIKey, error) {
	out := make([]*APIKey, 0, len(m.apikeys))
	for _, k := range m.apikeys {
		out = append(out, k)
	}
	return out, nil
}
func (m *memStore) GetAPIKey(_ context.Context, _ string, id string) (*APIKey, error) {
	if k, ok := m.apikeys[id]; ok {
		return k, nil
	}
	return nil, ErrNotFound
}

func (m *memStore) CreateChannel(_ context.Context, _ string, c *TradeChannel) error {
	m.channels[c.ChannelID] = c
	return nil
}
func (m *memStore) UpdateChannel(_ context.Context, _ string, c *TradeChannel) error {
	m.channels[c.ChannelID] = c
	return nil
}
func (m *memStore) DeleteChannel(_ context.Context, _ string, id string) error {
	delete(m.channels, id)
	return nil
}
func (m *memStore) GetChannel(_ context.Context, _ string, id string) (*TradeChannel, error) {
	if c, ok := m.channels[id]; ok {
		return c, nil
	}
	return nil, ErrNotFound
}
func (m *memStore) ListChannels(_ context.Context, _ string, _ ChannelFilter, _ Page) ([]*TradeChannel, int, error) {
	out := make([]*TradeChannel, 0, len(m.channels))
	for _, c := range m.channels {
		out = append(out, c)
	}
	return out, len(out), nil
}

func (m *memStore) SaveOrder(_ context.Context, _ string, o *Order) error {
	m.orders[o.OrderID] = o
	return nil
}
func (m *memStore) UpdateOrder(_ context.Context, _ string, o *Order) error {
	m.orders[o.OrderID] = o
	return nil
}
func (m *memStore) GetOrder(_ context.Context, _ string, orderID, clientOrderID string) (*Order, error) {
	if o, ok := m.orders[orderID]; ok {
		return o, nil
	}
	for _, o := range m.orders {
		if clientOrderID != "" && o.ClientOrderID == clientOrderID {
			return o, nil
		}
	}
	return nil, ErrNotFound
}
func (m *memStore) ListOrders(_ context.Context, _ string, _ OrderFilter, _ Page) ([]*Order, int, error) {
	out := make([]*Order, 0, len(m.orders))
	for _, o := range m.orders {
		out = append(out, o)
	}
	return out, len(out), nil
}

func (m *memStore) AppendTrades(_ context.Context, _ string, _ []*Trade) error { return nil }
func (m *memStore) ListTrades(_ context.Context, _ string, _ TradeFilter, _ Page) ([]*Trade, int, error) {
	return nil, 0, nil
}

func (m *memStore) UpsertPositions(_ context.Context, _ string, _ []*Position) error { return nil }
func (m *memStore) ListPositions(_ context.Context, _ string, _ string, _ string) ([]*Position, error) {
	return nil, nil
}

func (m *memStore) AppendOrderOperation(_ context.Context, _ string, _ *OrderOperation) error {
	return nil
}
func (m *memStore) UpdateOrderOperation(_ context.Context, _ string, _ *OrderOperation) error {
	return nil
}
