package exchange

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubExchangeAdapter struct{}

func (stubExchangeAdapter) Name() string { return "stub" }
func (stubExchangeAdapter) Ping(context.Context, Credential) (int64, error) {
	return 0, nil
}
func (stubExchangeAdapter) GetInstruments(context.Context, MarketType) ([]Instrument, error) {
	return nil, nil
}
func (stubExchangeAdapter) GetAccountInfo(context.Context, Credential, MarketType) (*AccountInfo, error) {
	return nil, nil
}
func (stubExchangeAdapter) GetBalances(context.Context, Credential, MarketType, []string) ([]Balance, error) {
	return nil, nil
}
func (stubExchangeAdapter) GetTradeFee(context.Context, Credential, MarketType, string) (*FeeRate, error) {
	return nil, nil
}
func (stubExchangeAdapter) ListFundFlows(context.Context, Credential, *FundFlowQuery) ([]FundFlow, error) {
	return nil, nil
}
func (stubExchangeAdapter) Transfer(context.Context, Credential, *TransferReq) (*TransferResult, error) {
	return nil, nil
}
func (stubExchangeAdapter) ListConvertibleDustAssets(context.Context, Credential, *DustConvertibleReq) ([]DustConvertibleAsset, error) {
	return nil, nil
}
func (stubExchangeAdapter) ConvertDust(context.Context, Credential, *DustTransferReq) (*DustTransferResult, error) {
	return nil, nil
}
func (stubExchangeAdapter) PlaceOrder(context.Context, Credential, *PlaceOrderReq) (*OrderResult, error) {
	return nil, nil
}
func (stubExchangeAdapter) CancelOrder(context.Context, Credential, *CancelOrderReq) (*OrderResult, error) {
	return nil, nil
}
func (stubExchangeAdapter) CancelAllOrders(context.Context, Credential, MarketType, string) (int, error) {
	return 0, nil
}
func (stubExchangeAdapter) AmendOrder(context.Context, Credential, *AmendOrderReq) (*OrderResult, error) {
	return nil, nil
}
func (stubExchangeAdapter) SetLeverage(context.Context, Credential, MarketType, string, string) error {
	return nil
}
func (stubExchangeAdapter) ClosePosition(context.Context, Credential, MarketType, string, string) error {
	return nil
}
func (stubExchangeAdapter) GetOrder(context.Context, Credential, *GetOrderReq) (*Order, error) {
	return nil, nil
}
func (stubExchangeAdapter) ListOpenOrders(context.Context, Credential, *ListOrdersReq) ([]Order, error) {
	return nil, nil
}
func (stubExchangeAdapter) ListOrders(context.Context, Credential, *ListOrdersReq) ([]Order, error) {
	return nil, nil
}
func (stubExchangeAdapter) ListTrades(context.Context, Credential, *ListTradesReq) ([]Trade, error) {
	return nil, nil
}
func (stubExchangeAdapter) ListPositions(context.Context, Credential, MarketType, string) ([]Position, error) {
	return nil, nil
}

func TestRegistry_RegisterAndNew_KnownExchange_ShouldReturnAdapter(t *testing.T) {
	name := "stub-" + t.Name()
	Register(name, func() ExchangeAdapter { return stubExchangeAdapter{} })

	adapter, err := New(name)
	require.NoError(t, err)
	assert.Equal(t, "stub", adapter.Name())
}

func TestRegistry_New_UnknownExchange_ShouldReturnError(t *testing.T) {
	_, err := New("unknown-exchange-" + t.Name())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown exchange")
}

func TestClassifiedError_IsCategory_ShouldMatchCategory(t *testing.T) {
	err := &ClassifiedError{Category: ErrorTransportUncertain, Err: errors.New("timeout")}
	assert.True(t, IsCategory(err, ErrorTransportUncertain))
	assert.False(t, IsCategory(err, ErrorOrderNotFound))
}

func TestClassifiedError_ErrorAndUnwrap_ShouldExposeCategoryAndCause(t *testing.T) {
	cause := errors.New("timeout")
	err := &ClassifiedError{Category: ErrorTransportUncertain, Err: cause}

	assert.Equal(t, "TRANSPORT_UNCERTAIN: timeout", err.Error())
	assert.True(t, errors.Is(err, cause))
	assert.Equal(t, cause, err.Unwrap())
}

func TestNotifyPrivateStreamState_WithCallback_ShouldInvoke(t *testing.T) {
	called := false
	ctx := WithPrivateStreamState(context.Background(), func(ready bool) {
		called = ready
	})
	NotifyPrivateStreamState(ctx, true)
	assert.True(t, called)
}

func TestNotifyPrivateStreamState_WithoutCallback_ShouldNoop(t *testing.T) {
	assert.NotPanics(t, func() {
		NotifyPrivateStreamState(context.Background(), true)
	})
}
