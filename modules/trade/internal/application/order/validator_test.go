package order

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/exchangeaccount"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

type accountEligibilityStub struct {
	account exchangeaccount.Account
	err     error
}

type accountEligibilityFunc func(
	context.Context,
	string,
) (exchangeaccount.Account, error)

func (f accountEligibilityFunc) ExecutionEligibility(
	ctx context.Context,
	exchangeAccountID string,
) (exchangeaccount.Account, error) {
	return f(ctx, exchangeAccountID)
}

func (s accountEligibilityStub) ExecutionEligibility(
	context.Context,
	string,
) (exchangeaccount.Account, error) {
	return s.account, s.err
}

type instrumentSourceStub struct {
	instrument exchange.Instrument
	err        error
}

func (s instrumentSourceStub) GetInstrument(
	context.Context,
	exchange.Exchange,
	exchange.MarketType,
	string,
) (exchange.Instrument, error) {
	return s.instrument, s.err
}

type positionSourceStub struct {
	position exchange.Position
	err      error
}

func (s positionSourceStub) GetPosition(
	context.Context,
	string,
	string,
) (exchange.Position, error) {
	return s.position, s.err
}

func TestValidatorReservationMatrix(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tests := []struct {
		name       string
		market     exchange.MarketType
		orderType  exchange.OrderType
		side       exchange.Side
		limit      *shared.Decimal
		reduceOnly bool
		wantAsset  string
		wantAmount string
	}{
		{
			name:   "SPOT MARKET buy uses reference price and fee buffer",
			market: exchange.MarketTypeSpot, orderType: exchange.OrderTypeMarket,
			side: exchange.SideBuy, wantAsset: "USDT", wantAmount: "101",
		},
		{
			name:   "SPOT MARKET sell reserves base quantity",
			market: exchange.MarketTypeSpot, orderType: exchange.OrderTypeMarket,
			side: exchange.SideSell, wantAsset: "BTC", wantAmount: "1",
		},
		{
			name:   "SPOT LIMIT buy does not add market fee buffer",
			market: exchange.MarketTypeSpot, orderType: exchange.OrderTypeLimit,
			side: exchange.SideBuy, limit: decimalPointer("90"),
			wantAsset: "USDT", wantAmount: "90",
		},
		{
			name:   "SWAP open reserves margin and fee buffer",
			market: exchange.MarketTypeSwap, orderType: exchange.OrderTypeMarket,
			side: exchange.SideBuy, wantAsset: "USDT", wantAmount: "20.2",
		},
		{
			name:   "SWAP LIMIT still reserves reference notional",
			market: exchange.MarketTypeSwap, orderType: exchange.OrderTypeLimit,
			side: exchange.SideBuy, limit: decimalPointer("90"),
			wantAsset: "USDT", wantAmount: "20.2",
		},
		{
			name:   "SWAP reduce-only does not reserve opening margin",
			market: exchange.MarketTypeSwap, orderType: exchange.OrderTypeMarket,
			side: exchange.SideSell, reduceOnly: true,
			wantAsset: "USDT", wantAmount: "0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := executableAccount(tt.market)
			validator := Validator{
				Accounts: accountEligibilityStub{account: account},
				Instruments: instrumentSourceStub{
					instrument: testInstrument(tt.market),
				},
				Positions: positionSourceStub{
					position: exchange.Position{SignedQuantity: shared.MustDecimal("2")},
				},
				SpaceID: "space-1", Now: func() time.Time { return now },
				MaxReferenceAge:  time.Second,
				MaxChildNotional: shared.MustDecimal("1000"),
				MaxLeverage:      shared.MustDecimal("10"),
				FeeBufferRate:    shared.MustDecimal("0.01"),
			}
			spec := testSpec(now)
			spec.OrderType, spec.Side, spec.ReduceOnly = tt.orderType, tt.side, tt.reduceOnly
			spec.LimitPrice = tt.limit
			if tt.orderType == exchange.OrderTypeLimit {
				spec.TimeInForce = exchange.TimeInForceGTC
			}
			if tt.market == exchange.MarketTypeSwap {
				spec.PositionSide = exchange.PositionSideNet
			}

			got, err := validator.Validate(context.Background(), spec)
			require.NoError(t, err)
			require.Equal(t, tt.wantAsset, got.ReservedAsset)
			require.Equal(t, tt.wantAmount, got.ReservedQuantity.String())
		})
	}
}

func TestValidatorRejectsOwnershipAndStalePrice(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	account := executableAccount(exchange.MarketTypeSpot)
	validator := Validator{
		Accounts: accountEligibilityStub{account: account},
		Instruments: instrumentSourceStub{
			instrument: testInstrument(exchange.MarketTypeSpot),
		},
		SpaceID: "other-space", Now: func() time.Time { return now },
		MaxReferenceAge: time.Second,
	}
	_, err := validator.Validate(context.Background(), testSpec(now))
	require.ErrorIs(t, err, ErrAccountOwnership)

	validator.SpaceID = "space-1"
	spec := testSpec(now.Add(-2 * time.Second))
	_, err = validator.Validate(context.Background(), spec)
	require.ErrorIs(t, err, orderdomain.ErrInvalidSpec)
}

func TestValidatorPropagatesNotExecutable(t *testing.T) {
	validator := Validator{
		Accounts: accountEligibilityStub{err: exchangeaccount.ErrAccountNotExecutable},
		Instruments: instrumentSourceStub{
			instrument: testInstrument(exchange.MarketTypeSpot),
		},
		SpaceID: "space-1", MaxReferenceAge: time.Second,
	}
	_, err := validator.Validate(context.Background(), testSpec(time.Now()))
	require.True(t, errors.Is(err, exchangeaccount.ErrAccountNotExecutable))
}

func TestValidatorRejectsDisabledInstrumentAndInvalidQuantity(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tests := []struct {
		name     string
		market   exchange.MarketType
		status   string
		quantity string
		wantErr  error
	}{
		{
			name: "disabled instrument", market: exchange.MarketTypeSpot,
			status: "BREAK", quantity: "1", wantErr: ErrInstrumentDisabled,
		},
		{
			name: "below minimum", market: exchange.MarketTypeSpot,
			status: "TRADING", quantity: "0.05", wantErr: ErrQuantityRule,
		},
		{
			name: "off quantity step", market: exchange.MarketTypeSpot,
			status: "TRADING", quantity: "0.15", wantErr: ErrQuantityRule,
		},
		{
			name:   "SWAP base quantity converts to fractional contracts",
			market: exchange.MarketTypeSwap, status: "TRADING",
			quantity: "0.15", wantErr: ErrQuantityRule,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instrument := testInstrument(tt.market)
			instrument.Status = tt.status
			validator := testValidator(now, executableAccount(tt.market), instrument)
			spec := testSpec(now)
			spec.Quantity = shared.MustDecimal(tt.quantity)
			if tt.market == exchange.MarketTypeSwap {
				spec.PositionSide = exchange.PositionSideNet
			}

			_, err := validator.Validate(context.Background(), spec)
			require.ErrorIs(t, err, tt.wantErr)
		})
	}

	validator := testValidator(
		now,
		executableAccount(exchange.MarketTypeSwap),
		testInstrument(exchange.MarketTypeSwap),
	)
	spec := testSpec(now)
	spec.Quantity = shared.MustDecimal("0.2")
	spec.PositionSide = exchange.PositionSideNet
	_, err := validator.Validate(context.Background(), spec)
	require.NoError(t, err, "0.2 base units must convert to two whole contracts")
}

func TestValidatorRejectsNotionalAndInsufficientFunds(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	validator := testValidator(
		now,
		executableAccount(exchange.MarketTypeSpot),
		testInstrument(exchange.MarketTypeSpot),
	)
	validator.MaxChildNotional = shared.MustDecimal("99")
	_, err := validator.Validate(context.Background(), testSpec(now))
	require.ErrorIs(t, err, ErrNotionalLimit)

	spotBuy := executableAccount(exchange.MarketTypeSpot)
	spotBuy.Snapshot.Balances[0].Available = shared.MustDecimal("100")
	validator = testValidator(now, spotBuy, testInstrument(exchange.MarketTypeSpot))
	_, err = validator.Validate(context.Background(), testSpec(now))
	require.ErrorIs(t, err, ErrInsufficientFunds)

	spotSell := executableAccount(exchange.MarketTypeSpot)
	spotSell.Snapshot.Balances[1].Available = shared.MustDecimal("0.5")
	validator = testValidator(now, spotSell, testInstrument(exchange.MarketTypeSpot))
	spec := testSpec(now)
	spec.Side = exchange.SideSell
	_, err = validator.Validate(context.Background(), spec)
	require.ErrorIs(t, err, ErrInsufficientFunds)

	swap := executableAccount(exchange.MarketTypeSwap)
	swap.Snapshot.AvailableFunds = shared.MustDecimal("20")
	validator = testValidator(now, swap, testInstrument(exchange.MarketTypeSwap))
	spec = testSpec(now)
	spec.PositionSide = exchange.PositionSideNet
	_, err = validator.Validate(context.Background(), spec)
	require.ErrorIs(t, err, ErrInsufficientFunds)
}

func TestValidatorRejectsInvalidLeverage(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	instrument := testInstrument(exchange.MarketTypeSwap)
	spec := testSpec(now)
	spec.PositionSide = exchange.PositionSideNet

	account := executableAccount(exchange.MarketTypeSwap)
	delete(account.LeverageSettings, spec.Symbol)
	validator := testValidator(now, account, instrument)
	_, err := validator.Validate(context.Background(), spec)
	require.ErrorIs(t, err, ErrLeverageLimit)

	account = executableAccount(exchange.MarketTypeSwap)
	account.LeverageSettings[spec.Symbol] = shared.MustDecimal("11")
	validator = testValidator(now, account, instrument)
	_, err = validator.Validate(context.Background(), spec)
	require.ErrorIs(t, err, ErrLeverageLimit)
}

func TestValidatorRejectsInvalidReduceOnlyOrder(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	spec := testSpec(now)
	spec.PositionSide = exchange.PositionSideNet
	spec.ReduceOnly = true

	tests := []struct {
		name     string
		side     exchange.Side
		quantity string
	}{
		{name: "wrong side", side: exchange.SideBuy, quantity: "1"},
		{name: "over close", side: exchange.SideSell, quantity: "3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			validator := testValidator(
				now,
				executableAccount(exchange.MarketTypeSwap),
				testInstrument(exchange.MarketTypeSwap),
			)
			validator.Positions = positionSourceStub{
				position: exchange.Position{SignedQuantity: shared.MustDecimal("2")},
			}
			candidate := spec
			candidate.Side = tt.side
			candidate.Quantity = shared.MustDecimal(tt.quantity)

			_, err := validator.Validate(context.Background(), candidate)
			require.ErrorIs(t, err, ErrReduceOnly)
		})
	}
}

func testValidator(
	now time.Time,
	account exchangeaccount.Account,
	instrument exchange.Instrument,
) Validator {
	return Validator{
		Accounts:         accountEligibilityStub{account: account},
		Instruments:      instrumentSourceStub{instrument: instrument},
		SpaceID:          "space-1",
		Now:              func() time.Time { return now },
		MaxReferenceAge:  time.Second,
		MaxChildNotional: shared.MustDecimal("1000"),
		MaxLeverage:      shared.MustDecimal("10"),
		FeeBufferRate:    shared.MustDecimal("0.01"),
	}
}

func executableAccount(market exchange.MarketType) exchangeaccount.Account {
	account := exchangeaccount.Account{
		ID: "account-1", SpaceID: "space-1", Name: "main",
		Exchange: exchange.ExchangeBinance, MarketType: market,
		ExecutionMode: exchange.ExecutionModeLive, CredentialSecretID: "secret-1",
		SettlementAsset: "USDT", Status: exchange.AccountStatusEnabled, Ready: true,
		SyncSymbols: []string{"BTC-USDT"},
		Snapshot: exchange.AccountSnapshot{
			Balances: []exchange.AssetBalance{
				{Asset: "USDT", Available: shared.MustDecimal("1000")},
				{Asset: "BTC", Available: shared.MustDecimal("10")},
			},
			AvailableFunds: shared.MustDecimal("1000"),
		},
	}
	if market == exchange.MarketTypeSwap {
		account.MarginMode = exchange.MarginModeCross
		account.LeverageSettings = map[string]shared.Decimal{
			"BTC-USDT": shared.MustDecimal("5"),
		}
	}
	return account
}

func testInstrument(market exchange.MarketType) exchange.Instrument {
	instrument := exchange.Instrument{
		Exchange: exchange.ExchangeBinance, MarketType: market, Symbol: "BTC-USDT",
		BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT",
		ExchangeQuantityStep: shared.MustDecimal("0.1"),
		MinExchangeQuantity:  shared.MustDecimal("0.1"),
		PriceTick:            shared.MustDecimal("0.1"), MinNotional: shared.MustDecimal("1"),
		Status: "TRADING",
	}
	if market == exchange.MarketTypeSwap {
		instrument.Linear = true
		instrument.ContractValue = shared.MustDecimal("0.1")
		instrument.ContractValueAsset = "BTC"
		instrument.ExchangeQuantityStep = shared.MustDecimal("1")
		instrument.MinExchangeQuantity = shared.MustDecimal("1")
	}
	return instrument
}

func testSpec(referenceAt time.Time) orderdomain.OrderSpec {
	return orderdomain.OrderSpec{
		ExchangeAccountID: "account-1", ClientOrderID: "client-1",
		Symbol: "BTC-USDT", OrderType: exchange.OrderTypeMarket,
		Side: exchange.SideBuy, Quantity: shared.MustDecimal("1"),
		ReferencePrice: shared.MustDecimal("100"), ReferencePriceAt: referenceAt,
		Source: "test",
	}
}

func decimalPointer(raw string) *shared.Decimal {
	value := shared.MustDecimal(raw)
	return &value
}
