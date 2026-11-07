package order

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/stretchr/testify/require"
)

type batchAdapterSource struct{ adapter execution.ExecutionAdapter }

func (s batchAdapterSource) Adapter(string) (execution.ExecutionAdapter, error) {
	return s.adapter, nil
}

// barrierAdapter 等到同一批的请求全部到齐才返回：串行发送时第一个请求会一直等到超时。
type barrierAdapter struct {
	*adapterStub
	mu          sync.Mutex
	want        int
	calls       int
	inFlight    int
	maxInFlight int
	ready       chan struct{}
	errs        map[string]error
	status      exchange.OrderStatus
}

func (a *barrierAdapter) PlaceOrder(_ context.Context, request exchange.OrderRequest) (exchange.Order, error) {
	a.mu.Lock()
	a.calls++
	a.inFlight++
	if a.inFlight > a.maxInFlight {
		a.maxInFlight = a.inFlight
	}
	if a.calls == a.want {
		close(a.ready)
	}
	a.mu.Unlock()
	select {
	case <-a.ready:
	case <-time.After(2 * time.Second):
	}
	a.mu.Lock()
	a.inFlight--
	err := a.errs[request.ClientOrderID]
	a.mu.Unlock()
	if err != nil {
		return exchange.Order{}, err
	}
	status := a.status
	if status == "" {
		status = exchange.OrderStatusOpen
	}
	return exchange.Order{Market: exchange.MarketTypeSwap, ExchangeOrderID: "exchange-" + request.ClientOrderID, Status: status}, nil
}

func batchService(t *testing.T, count int) (*Service, *barrierAdapter, []string) {
	t.Helper()
	service, _, stub := newTestService(t)
	adapter := &barrierAdapter{adapterStub: stub, want: count, ready: make(chan struct{}), errs: map[string]error{}}
	service.Adapters = batchAdapterSource{adapter: adapter}
	next := 0
	service.NewOrderID = func() string { next++; return fmt.Sprintf("order-%d", next) }
	ids := make([]string, 0, count)
	for index := 0; index < count; index++ {
		spec := testSpec(service.now())
		spec.ClientOrderID = fmt.Sprintf("client-%d", index)
		placed, err := service.Place(context.Background(), "space-1", spec)
		require.NoError(t, err)
		ids = append(ids, string(placed.ID))
	}
	return service, adapter, ids
}

// 一批子单并发发往交易所，账户锁不跨请求。
func TestSubmitBatchSendsConcurrently(t *testing.T) {
	service, adapter, ids := batchService(t, 3)
	results, err := service.SubmitBatch(context.Background(), "space-1", ids)
	require.NoError(t, err)
	require.Len(t, results, 3)
	for _, result := range results {
		require.NoError(t, result.Err)
		require.Equal(t, orderdomain.Open, result.Order.State)
	}
	require.Equal(t, 3, adapter.maxInFlight)
}

// 部分拒单、部分限流、部分结果未知：各自只影响自己那一笔。
func TestSubmitBatchIsolatesPerOrderOutcomes(t *testing.T) {
	service, adapter, ids := batchService(t, 4)
	adapter.errs["client-1"] = &exchange.Error{Kind: exchange.ErrorRejected}
	adapter.errs["client-2"] = &exchange.Error{Kind: exchange.ErrorOrderRateLimited}
	adapter.errs["client-3"] = &exchange.Error{Kind: exchange.ErrorTransportUnknown}
	results, err := service.SubmitBatch(context.Background(), "space-1", ids)
	require.NoError(t, err)
	states := map[string]orderdomain.State{}
	for _, result := range results {
		states[result.OrderID] = result.Order.State
	}
	require.Equal(t, orderdomain.Open, states[ids[0]])
	require.Equal(t, orderdomain.Rejected, states[ids[1]])
	require.Equal(t, orderdomain.Pending, states[ids[2]], "限流确定未受理，回到待提交")
	require.Equal(t, orderdomain.SubmitUnknown, states[ids[3]])
	record, getErr := service.Store.GetOrder(context.Background(), "space-1", ids[2])
	require.NoError(t, getErr)
	require.Zero(t, record.SubmittedAt)
}

// 未知结果批量解析：同一订单在窗口内只查两次（第一次机会和窗口到期时）。
func TestResolveUnknownBatchLimitsLookups(t *testing.T) {
	service, adapter, ids := batchService(t, 1)
	adapter.errs["client-0"] = &exchange.Error{Kind: exchange.ErrorTransportUnknown}
	_, err := service.SubmitBatch(context.Background(), "space-1", ids)
	require.NoError(t, err)
	stub := adapter.adapterStub
	stub.getErr = &exchange.Error{Kind: exchange.ErrorOrderNotFound}
	base := time.Unix(1_700_000_000, 0)
	now := base
	service.Now = func() time.Time { return now }
	for range 5 {
		_, err := service.ResolveUnknownBatch(context.Background(), "space-1", ids)
		require.NoError(t, err)
		now = now.Add(time.Second)
	}
	require.Equal(t, 1, stub.getCalls)
	now = base.Add(31 * time.Second)
	_, err = service.ResolveUnknownBatch(context.Background(), "space-1", ids)
	require.NoError(t, err)
	require.Equal(t, 2, stub.getCalls)
}

// 有成交的回执由会话做防抖同步，批后不直接同步；会话不支持时才直接同步。
func TestSubmitBatchRequestsSessionSyncInsteadOfSyncing(t *testing.T) {
	for _, handled := range []bool{true, false} {
		service, adapter, ids := batchService(t, 2)
		adapter.status = exchange.OrderStatusFilled
		syncer := &syncerStub{}
		service.Syncer = syncer
		requested := 0
		service.RequestSync = func(string) bool { requested++; return handled }
		_, err := service.SubmitBatch(context.Background(), "space-1", ids)
		require.NoError(t, err)
		require.Equal(t, 1, requested, "同一账户只请求一次")
		if handled {
			require.Zero(t, syncer.calls)
		} else {
			require.Equal(t, 1, syncer.calls)
		}
	}
}
