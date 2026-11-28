package account

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func openTestAccountStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func accountCount(t *testing.T, db *store.Store) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.DBForTest().Table("t_trading_accounts").Count(&count).Error)
	return count
}

func seedAccount(t *testing.T, db *store.Store, value store.TradingAccountRecord) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
		return tx.CreateTradingAccount(value)
	}))
	if value.Ready {
		require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
			return tx.UpdateTradingAccountReadiness(value.SpaceID, value.TradingAccountID, true, 1, "")
		}))
	}
}

func TestCreateValidatesAggregateAndCredential(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*store.TradingAccountRecord)
		secret     ExchangeSecret
		want       error
		wantWrites int64
	}{
		{
			name: "valid SPOT paper account without credential",
			mutate: func(value *store.TradingAccountRecord) {
				value.CredentialSecretID = ""
			},
			wantWrites: 1,
		},
		{
			name:   "missing ID",
			mutate: func(value *store.TradingAccountRecord) { value.TradingAccountID = "" },
			secret: validSecret(),
			want:   tradingaccount.ErrInvalidAccount,
		},
		{
			name:   "missing SpaceID",
			mutate: func(value *store.TradingAccountRecord) { value.SpaceID = "" },
			secret: validSecret(),
			want:   tradingaccount.ErrInvalidAccount,
		},
		{
			name:   "missing name",
			mutate: func(value *store.TradingAccountRecord) { value.Name = "" },
			secret: validSecret(),
			want:   tradingaccount.ErrInvalidAccount,
		},
		{
			name:   "missing Exchange",
			mutate: func(value *store.TradingAccountRecord) { value.Exchange = "" },
			secret: validSecret(),
			want:   tradingaccount.ErrInvalidAccount,
		},
		{
			name:   "missing market",
			mutate: func(value *store.TradingAccountRecord) { value.Markets = nil },
			secret: validSecret(),
			want:   tradingaccount.ErrInvalidAccount,
		},
		{
			name: "missing execution mode",
			mutate: func(value *store.TradingAccountRecord) {
				value.ExecutionMode = ""
			},
			secret: validSecret(),
			want:   tradingaccount.ErrInvalidAccount,
		},
		{
			name: "missing credential secret ID",
			mutate: func(value *store.TradingAccountRecord) {
				setLive(value, exchange.AccountEnvironmentTestnet, "")
			},
			secret: validSecret(),
			want:   tradingaccount.ErrInvalidAccount,
		},
		{
			name: "missing settlement asset",
			mutate: func(value *store.TradingAccountRecord) {
				value.SettlementAsset = ""
			},
			secret: validSecret(),
			want:   tradingaccount.ErrInvalidAccount,
		},
		{
			name: "wrong credential category",
			mutate: func(value *store.TradingAccountRecord) {
				setLive(value, exchange.AccountEnvironmentTestnet, "secret-1")
			},
			secret: ExchangeSecret{
				SecretID: "secret-1", Category: "cloud",
				Exchange: exchange.ExchangeBinance, Status: "active",
			},
			want: ErrInvalidCredential,
		},
		{
			name: "wrong credential Exchange",
			mutate: func(value *store.TradingAccountRecord) {
				setLive(value, exchange.AccountEnvironmentTestnet, "secret-1")
			},
			secret: ExchangeSecret{
				SecretID: "secret-1", Category: "exchange",
				Exchange: exchange.ExchangeOKX, Status: "active",
			},
			want: ErrInvalidCredential,
		},
		{
			name: "inactive credential",
			mutate: func(value *store.TradingAccountRecord) {
				setLive(value, exchange.AccountEnvironmentTestnet, "secret-1")
			},
			secret: ExchangeSecret{
				SecretID: "secret-1", Category: "exchange",
				Exchange: exchange.ExchangeBinance, Status: "disabled",
			},
			want: ErrInvalidCredential,
		},
		{
			name:   "SPOT rejects margin",
			mutate: func(value *store.TradingAccountRecord) { value.MarginMode = string(exchange.MarginModeCross) },
			secret: validSecret(),
			want:   tradingaccount.ErrInvalidAccount,
		},
		{
			name: "SPOT rejects leverage",
			mutate: func(value *store.TradingAccountRecord) {
				value.LeverageSettings = store.LeverageSettings{"BTC-USDT": "2"}
			},
			secret: validSecret(),
			want:   tradingaccount.ErrInvalidAccount,
		},
		{
			name: "SWAP requires CROSS",
			mutate: func(value *store.TradingAccountRecord) {
				value.Markets = []string{string(exchange.MarketTypeSwap)}
			},
			secret: validSecret(),
			want:   tradingaccount.ErrInvalidAccount,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := validAccount()
			if tt.mutate != nil {
				tt.mutate(&value)
			}
			db := openTestAccountStore(t)
			secrets := &fakeSecrets{secret: tt.secret}
			service := Service{Store: db, Secrets: secrets, Inspector: okInspector}

			_, err := service.Create(context.Background(), value)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Create() error = %v, want %v", err, tt.want)
			}
			if got := accountCount(t, db); got != tt.wantWrites {
				t.Fatalf("Create() rows = %d, want %d", got, tt.wantWrites)
			}
		})
	}
}

func TestCreateDoesNotAliasSameNameAcrossExecutionModes(t *testing.T) {
	db := openTestAccountStore(t)
	paper := validAccount()
	seedAccount(t, db, paper)

	live := paper
	live.TradingAccountID = "account-live"
	setLive(&live, exchange.AccountEnvironmentTestnet, "secret-1")
	secrets := &fakeSecrets{secret: validSecret()}
	service := Service{Store: db, Secrets: secrets, Inspector: okInspector}

	_, err := service.Create(context.Background(), live)
	require.ErrorIs(t, err, store.ErrNameTaken)
	got, err := db.GetTradingAccountByID(context.Background(), paper.TradingAccountID)
	require.NoError(t, err)
	require.Equal(t, string(exchange.ExecutionModePaper), got.ExecutionMode)
	require.Equal(t, int64(1), accountCount(t, db))
}

func TestCreateProductionAccountIsRejectedWhileLiveTradingIsDisabled(t *testing.T) {
	db := openTestAccountStore(t)
	value := validAccount()
	setLive(&value, exchange.AccountEnvironmentProduction, "secret-1")
	secrets := &fakeSecrets{secret: validSecret()}
	service := Service{Store: db, Secrets: secrets, Inspector: okInspector}

	_, err := service.Create(context.Background(), value)
	require.ErrorIs(t, err, ErrLiveTradingDisabled)
	require.Zero(t, secrets.getCalls)
	require.Zero(t, accountCount(t, db))
}

func TestCreateProductionAccountWhenLiveTradingIsEnabled(t *testing.T) {
	db := openTestAccountStore(t)
	value := validAccount()
	setLive(&value, exchange.AccountEnvironmentProduction, "secret-1")
	service := Service{
		Inspector:          okInspector,
		Store:              db,
		Secrets:            &fakeSecrets{secret: validSecret()},
		LiveTradingEnabled: true,
	}

	_, err := service.Create(context.Background(), value)
	require.NoError(t, err)
	require.Equal(t, int64(1), accountCount(t, db))
}

func TestCreatePaperAccountDoesNotRequireSecretSource(t *testing.T) {
	db := openTestAccountStore(t)
	value := validAccount()
	value.CredentialSecretID = ""
	service := Service{Store: db}

	got, err := service.Create(context.Background(), value)
	require.NoError(t, err)
	require.Empty(t, got.CredentialSecretID)
	require.Equal(t, int64(1), accountCount(t, db))
}

func TestUpdateChangesOnlyExplicitMutableFields(t *testing.T) {
	db := openTestAccountStore(t)
	before := validSwapAccount()
	setLive(&before, exchange.AccountEnvironmentTestnet, "secret-1")
	seedAccount(t, db, before)
	service := Service{Store: db, Secrets: &fakeSecrets{secret: validSecret()}, Inspector: okInspector}
	name := "renamed"

	got, err := service.Update(context.Background(), UpdateCommand{
		TradingAccountID: before.TradingAccountID,
		Name:             &name,
	})
	require.NoError(t, err)
	require.Equal(t, name, got.Name)
	if got.Exchange != before.Exchange ||
		!slices.Equal(got.Markets, before.Markets) ||
		got.ExecutionMode != before.ExecutionMode ||
		got.CredentialSecretID != before.CredentialSecretID ||
		got.SettlementAsset != before.SettlementAsset ||
		got.MarginMode != before.MarginMode {
		t.Fatalf("Update() changed immutable or unspecified fields: before=%+v after=%+v", before, got)
	}
}

func TestUpdateEnablingProductionAccountIsRejectedWhileLiveTradingIsDisabled(t *testing.T) {
	db := openTestAccountStore(t)
	value := validAccount()
	setLive(&value, exchange.AccountEnvironmentProduction, "secret-1")
	value.Status = string(exchange.AccountStatusDisabled)
	seedAccount(t, db, value)
	status := exchange.AccountStatusEnabled
	service := Service{
		Store:   db,
		Secrets: &fakeSecrets{secret: validSecret()},
	}

	_, err := service.Update(context.Background(), UpdateCommand{
		TradingAccountID: value.TradingAccountID,
		Status:           &status,
	})
	require.ErrorIs(t, err, ErrLiveTradingDisabled)
	stored, err := db.GetTradingAccountByID(context.Background(), value.TradingAccountID)
	require.NoError(t, err)
	require.Equal(t, string(exchange.AccountStatusDisabled), stored.Status)
}

func TestUpdateDisablingProductionAccountIsAllowedWhileLiveTradingIsDisabled(t *testing.T) {
	db := openTestAccountStore(t)
	value := validAccount()
	setLive(&value, exchange.AccountEnvironmentProduction, "secret-1")
	value.Status = string(exchange.AccountStatusEnabled)
	seedAccount(t, db, value)
	status := exchange.AccountStatusDisabled
	service := Service{Store: db}

	got, err := service.Update(context.Background(), UpdateCommand{
		TradingAccountID: value.TradingAccountID,
		Status:           &status,
	})
	require.NoError(t, err)
	require.Equal(t, string(exchange.AccountStatusDisabled), got.Status)
}

func TestExecutionEligibilityUsesCurrentSessionState(t *testing.T) {
	tests := []struct {
		name    string
		status  exchange.AccountStatus
		ready   bool
		wantErr bool
	}{
		{name: "enabled ready", status: exchange.AccountStatusEnabled, ready: true},
		{name: "disabled", status: exchange.AccountStatusDisabled, ready: true, wantErr: true},
		{name: "not ready", status: exchange.AccountStatusEnabled, ready: false, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestAccountStore(t)
			value := validAccount()
			value.Status = string(tt.status)
			value.Ready = true // 持久化的同步状态就绪，由会话状态决定当前是否可交易
			seedAccount(t, db, value)
			service := Service{
				Store:        db,
				SessionState: fakeSessionState{ready: tt.ready},
			}

			_, err := service.ExecutionEligibility(context.Background(), value.TradingAccountID)
			if tt.wantErr {
				require.ErrorIs(t, err, tradingaccount.ErrAccountNotExecutable)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// 私有流的持仓/成交更新把持久化状态置为“等待完整同步”时，会话仍然就绪也不能放行：资金信息尚未同步。
func TestExecutionEligibilityRequiresPersistedSyncReadiness(t *testing.T) {
	db := openTestAccountStore(t)
	value := validAccount()
	value.Ready = false
	seedAccount(t, db, value)
	service := Service{Store: db, SessionState: fakeSessionState{ready: true}}

	_, err := service.ExecutionEligibility(context.Background(), value.TradingAccountID)
	require.ErrorIs(t, err, tradingaccount.ErrAccountNotExecutable)
}

func TestExecutionEligibilityRejectsMissingSessionState(t *testing.T) {
	db := openTestAccountStore(t)
	value := validAccount()
	value.Ready = true
	seedAccount(t, db, value)
	service := Service{Store: db}

	_, err := service.ExecutionEligibility(context.Background(), value.TradingAccountID)
	require.ErrorIs(t, err, ErrServiceNotConfigured)
}

func TestExecutionEligibilityRejectsProductionWhenLiveTradingDisabled(t *testing.T) {
	db := openTestAccountStore(t)
	value := validAccount()
	setLive(&value, exchange.AccountEnvironmentProduction, "secret-1")
	value.Ready = true
	seedAccount(t, db, value)
	service := Service{Store: db, SessionState: fakeSessionState{ready: true}}

	_, err := service.ExecutionEligibility(context.Background(), value.TradingAccountID)
	require.ErrorIs(t, err, ErrLiveTradingDisabled)

	service.LiveTradingEnabled = true
	_, err = service.ExecutionEligibility(context.Background(), value.TradingAccountID)
	require.NoError(t, err)
}

func TestSetLeverageAndPauseUseCommandSpecificWrites(t *testing.T) {
	db := openTestAccountStore(t)
	value := validSwapAccount()
	setLive(&value, exchange.AccountEnvironmentTestnet, "secret-1")
	seedAccount(t, db, value)
	service := Service{Store: db}

	err := service.SetLeverage(
		context.Background(),
		value.TradingAccountID,
		"ETH-USDT-SWAP",
		exchange.NewSymbolKey(exchange.MarketTypeSwap, "ETH-USDT"),
		shared.MustDecimal("3"),
	)
	require.NoError(t, err)
	stored, err := db.GetTradingAccountByID(context.Background(), value.TradingAccountID)
	require.NoError(t, err)
	require.Equal(t, "3", stored.LeverageSettings["ETH-USDT-SWAP"])
	require.Equal(t, "5", stored.LeverageSettings["BTC-USDT"])
}

func setLive(value *store.TradingAccountRecord, env exchange.AccountEnvironment, secretID string) {
	value.ExecutionMode = string(exchange.ExecutionModeLive)
	value.Environment = string(env)
	value.CredentialSecretID = secretID
	value.PaperConfig = nil
}

func validAccount() store.TradingAccountRecord {
	return store.TradingAccountRecord{
		TradingAccountID:   "account-1",
		SpaceID:            "space-1",
		Name:               "main",
		Exchange:           string(exchange.ExchangeBinance),
		Markets:            []string{string(exchange.MarketTypeSpot)},
		ExecutionMode:      string(exchange.ExecutionModePaper),
		Environment:        string(exchange.AccountEnvironmentPaper),
		CredentialSecretID: "secret-1",
		PaperConfig: &store.PaperAccountConfigRecord{
			SpaceID:            "space-1",
			TradingAccountID:   "account-1",
			InitialBalance:     "100000",
			MakerFeeRate:       "0",
			TakerFeeRate:       "0",
			SlippageBPS:        "0",
			OrderRetentionDays: tradingaccount.DefaultOrderRetentionDays,
		},
		SettlementAsset: "USDT",
		Status:          string(exchange.AccountStatusEnabled),
	}
}

func validSwapAccount() store.TradingAccountRecord {
	value := validAccount()
	value.Markets = []string{string(exchange.MarketTypeSwap)}
	value.MarginMode = string(exchange.MarginModeCross)
	value.LeverageSettings = store.LeverageSettings{
		"BTC-USDT": "5",
	}
	return value
}

func validSecret() ExchangeSecret {
	return ExchangeSecret{
		SecretID: "secret-1", Category: "exchange",
		Exchange: exchange.ExchangeBinance, Status: "active",
	}
}

// okInspector 对任何密钥都检测通过：币安记为普通账户、OKX 记为统一账户，两个市场都能开通。
var okInspector = &fakeInspector{}

type fakeInspector struct {
	// result 非空时返回它；err 非空时返回错误。
	result *exchange.Inspection
	err    error
	calls  []exchange.InspectRequest
}

func (f *fakeInspector) Inspect(_ context.Context, name exchange.Exchange, request exchange.InspectRequest) (exchange.Inspection, error) {
	f.calls = append(f.calls, request)
	if f.err != nil {
		return exchange.Inspection{}, f.err
	}
	if f.result != nil {
		return *f.result, nil
	}
	mode := exchange.AccountModeBinanceStandard
	if name == exchange.ExchangeOKX {
		mode = exchange.AccountModeOKXUnified
	}
	return exchange.Inspection{
		Exchange: name, Environment: request.Environment, AccountMode: mode,
		CanTradeSpot: true, CanTradeSwap: true, UniversalTransfer: true,
		AvailableMarkets: []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap},
		InspectedAt:      time.Now().UTC(),
	}, nil
}

type fakeSecrets struct {
	secret   ExchangeSecret
	err      error
	getCalls int
}

func (f *fakeSecrets) GetExchangeSecret(
	_ context.Context,
	_ string,
) (ExchangeSecret, error) {
	f.getCalls++
	return f.secret, f.err
}

type fakeSessionState struct{ ready bool }

func (f fakeSessionState) ReadyFor(store.TradingAccountRecord) bool { return f.ready }
func (f fakeSessionState) Invalidate(string)                        {}

func TestApplyUpdateReplacesCredential(t *testing.T) {
	value := validAccount()
	setLive(&value, exchange.AccountEnvironmentTestnet, "secret-a")
	next := "secret-b"
	applyUpdate(&value, UpdateCommand{CredentialSecretID: &next})
	require.Equal(t, "secret-b", value.CredentialSecretID)
}

func liveCredentialAccount(id string, name exchange.Exchange, market exchange.MarketType) store.TradingAccountRecord {
	value := validAccount()
	if market == exchange.MarketTypeSwap {
		value = validSwapAccount()
	}
	value.TradingAccountID, value.Name, value.Exchange = id, id, string(name)
	setLive(&value, exchange.AccountEnvironmentTestnet, "secret-1")
	value.Status = string(exchange.AccountStatusEnabled)
	return value
}

func TestRejectDuplicateCredentialIsUnconditional(t *testing.T) {
	ctx := context.Background()
	db := openTestAccountStore(t)
	service := Service{Store: db}

	for _, name := range []exchange.Exchange{exchange.ExchangeBinance, exchange.ExchangeOKX} {
		existing := liveCredentialAccount(string(name)+"-spot", name, exchange.MarketTypeSpot)
		seedAccount(t, db, existing)

		// 一把密钥只能对应一个执行账户：不管交易所、也不管市场是否相同。账户同时做现货和合约。
		for _, market := range []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap} {
			err := service.rejectDuplicateCredential(ctx, liveCredentialAccount(string(name)+"-second", name, market))
			require.ErrorIs(t, err, tradingaccount.ErrInvalidAccount, "%s %s", name, market)
			require.Contains(t, err.Error(), existing.Name)
		}
	}

	// 不同密钥、账户自身、不同环境都不算重复。
	binance, err := db.GetTradingAccountByID(ctx, "binance-spot")
	require.NoError(t, err)
	other := liveCredentialAccount("binance-other", exchange.ExchangeBinance, exchange.MarketTypeSpot)
	setLive(&other, exchange.AccountEnvironmentTestnet, "secret-2")
	require.NoError(t, service.rejectDuplicateCredential(ctx, other))
	require.NoError(t, service.rejectDuplicateCredential(ctx, binance))
	production := liveCredentialAccount("binance-production", exchange.ExchangeBinance, exchange.MarketTypeSpot)
	setLive(&production, exchange.AccountEnvironmentProduction, "secret-1")
	require.NoError(t, service.rejectDuplicateCredential(ctx, production))

	// 停用的账户不占用密钥，重新启用时才检查。
	require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateTradingAccountConfiguration(binance.SpaceID, binance.TradingAccountID, store.TradingAccountConfiguration{
			Name: binance.Name, CredentialSecretID: binance.CredentialSecretID,
			SettlementAsset: binance.SettlementAsset, MarginMode: binance.MarginMode, Status: "DISABLED",
		})
	}))
	require.NoError(t, service.rejectDuplicateCredential(ctx, liveCredentialAccount("binance-again", exchange.ExchangeBinance, exchange.MarketTypeSpot)))
}

func TestCreateAcceptsMultiMarketAccount(t *testing.T) {
	value := validAccount()
	value.Markets = []string{string(exchange.MarketTypeSpot), string(exchange.MarketTypeSwap)}
	value.MarginMode = string(exchange.MarginModeCross)
	value.LeverageSettings = store.LeverageSettings{"*": "1"}

	created, err := (&Service{Store: openTestAccountStore(t)}).Create(context.Background(), value)

	require.NoError(t, err)
	require.Equal(t, value.Markets, created.Markets)
}

func TestUpdateRejectsDisablingAccountStillInLogicalAccount(t *testing.T) {
	ctx := context.Background()
	db := openTestAccountStore(t)
	value := validAccount()
	setLive(&value, exchange.AccountEnvironmentProduction, "secret-1")
	value.Status = string(exchange.AccountStatusEnabled)
	seedAccount(t, db, value)
	require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: value.SpaceID, LogicalAccountID: "logical-1", Name: "组合一",
			Environment: "PRODUCTION", SettlementAsset: "USDT", PauseReason: "configure",
		}); err != nil {
			return err
		}
		return tx.PutLogicalAccountMember(store.LogicalAccountMemberRecord{
			SpaceID: value.SpaceID, LogicalAccountID: "logical-1", TradingAccountID: value.TradingAccountID, Enabled: true, Priority: 1,
		})
	}))
	status := exchange.AccountStatusDisabled
	service := Service{Store: db}

	_, err := service.Update(ctx, UpdateCommand{TradingAccountID: value.TradingAccountID, Status: &status})
	require.ErrorIs(t, err, ErrAccountInLogicalAccount)
	require.Contains(t, err.Error(), "组合一")
	stored, err := db.GetTradingAccountByID(ctx, value.TradingAccountID)
	require.NoError(t, err)
	require.Equal(t, "ENABLED", stored.Status)

	require.NoError(t, db.Transaction(ctx, func(tx *store.Tx) error {
		return tx.DeleteLogicalAccountMember(value.SpaceID, "logical-1", value.TradingAccountID)
	}))
	updated, err := service.Update(ctx, UpdateCommand{TradingAccountID: value.TradingAccountID, Status: &status})
	require.NoError(t, err)
	require.Equal(t, "DISABLED", updated.Status)
}
