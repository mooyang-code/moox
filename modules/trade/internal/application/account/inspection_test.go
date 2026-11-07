package account

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

func liveProductionAccount() tradingaccount.Account {
	value := validAccount()
	value.ExecutionMode = exchange.ExecutionModeLive
	value.Environment = exchange.AccountEnvironmentProduction
	value.Live = &tradingaccount.LiveConfig{Environment: exchange.AccountEnvironmentProduction, CredentialSecretID: "secret-1"}
	return value
}

func inspectingService(store *memoryStore, inspector *fakeInspector, secret ExchangeSecret) *Service {
	return &Service{
		Store: store, Secrets: &fakeSecrets{secret: secret}, Inspector: inspector,
		LiveTradingEnabled: true,
		Now:                func() time.Time { return time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC) },
	}
}

func TestCreateLiveAccountWritesInspectedModeAndResult(t *testing.T) {
	store, inspector := newMemoryStore(), &fakeInspector{}
	created, err := inspectingService(store, inspector, validSecret()).Create(context.Background(), liveProductionAccount())
	require.NoError(t, err)
	require.Equal(t, exchange.AccountModeBinanceStandard, created.AccountMode)
	require.NotEmpty(t, created.InspectionJSON)
	inspection, err := exchange.ParseInspection(store.accounts[created.ID].InspectionJSON)
	require.NoError(t, err)
	require.Equal(t, exchange.AccountModeBinanceStandard, inspection.AccountMode)
	require.Len(t, inspector.calls, 1)
	require.Equal(t, []exchange.MarketType{exchange.MarketTypeSpot}, inspector.calls[0].Markets)
}

func TestCreateLiveAccountWithBlockersIsRejectedWithoutWrite(t *testing.T) {
	store := newMemoryStore()
	inspector := &fakeInspector{result: &exchange.Inspection{
		Exchange: exchange.ExchangeBinance, AccountMode: exchange.AccountModeBinanceStandard,
		CanWithdraw: true, CanTradeSpot: true, InspectedAt: time.Now().UTC(),
	}}
	_, err := inspectingService(store, inspector, validSecret()).Create(context.Background(), liveProductionAccount())
	require.ErrorIs(t, err, ErrInspectionBlocked)
	require.Contains(t, err.Error(), "提现")
	require.Zero(t, store.createCalls)
}

func TestCreateLiveAccountWhenInspectionIsInconclusiveIsRejectedWithoutWrite(t *testing.T) {
	store := newMemoryStore()
	inspector := &fakeInspector{err: errors.Join(exchange.ErrInspectionInconclusive, errors.New("timeout"))}
	_, err := inspectingService(store, inspector, validSecret()).Create(context.Background(), liveProductionAccount())
	require.ErrorIs(t, err, ErrInspectionUnavailable)
	require.Zero(t, store.createCalls)
}

func TestCreateLiveAccountRejectsReadOnlyCredential(t *testing.T) {
	store := newMemoryStore()
	secret := validSecret()
	secret.ExtraConfig = `{"readonly":"true"}`
	_, err := inspectingService(store, &fakeInspector{}, secret).Create(context.Background(), liveProductionAccount())
	require.ErrorIs(t, err, ErrReadOnlyCredential)
	require.Zero(t, store.createCalls)
}

func TestCreateLiveAccountWithoutInspectorIsRejected(t *testing.T) {
	store := newMemoryStore()
	service := inspectingService(store, nil, validSecret())
	service.Inspector = nil
	_, err := service.Create(context.Background(), liveProductionAccount())
	require.ErrorIs(t, err, ErrServiceNotConfigured)
}

func TestCreatePaperAccountSkipsInspection(t *testing.T) {
	store, inspector := newMemoryStore(), &fakeInspector{}
	_, err := inspectingService(store, inspector, validSecret()).Create(context.Background(), validAccount())
	require.NoError(t, err)
	require.Empty(t, inspector.calls)
}

func TestUpdateCredentialRequiresPassingInspectionAndStoresResult(t *testing.T) {
	store := newMemoryStore()
	value := liveProductionAccount()
	value.AccountMode = exchange.AccountModeBinanceStandard
	store.accounts[value.ID] = value
	next := "secret-2"
	secret := validSecret()
	secret.SecretID = next

	inspector := &fakeInspector{result: &exchange.Inspection{
		Exchange: exchange.ExchangeBinance, AccountMode: exchange.AccountModeBinancePortfolioMargin,
		CanTradeSpot: true, CanTradeSwap: true, InspectedAt: time.Now().UTC(),
	}}
	service := inspectingService(store, inspector, secret)
	updated, err := service.Update(context.Background(), UpdateCommand{TradingAccountID: value.ID, CredentialSecretID: &next})
	require.NoError(t, err)
	require.Equal(t, exchange.AccountModeBinancePortfolioMargin, updated.AccountMode)
	require.NotEmpty(t, updated.InspectionJSON)

	// 新密钥检测发现阻断项：不保存。
	blocked := &fakeInspector{result: &exchange.Inspection{
		Exchange: exchange.ExchangeBinance, AccountMode: exchange.AccountModeBinanceStandard,
		CanWithdraw: true, CanTradeSpot: true, InspectedAt: time.Now().UTC(),
	}}
	before := store.updateCalls
	third := "secret-3"
	secret.SecretID = third
	_, err = inspectingService(store, blocked, secret).Update(context.Background(), UpdateCommand{TradingAccountID: value.ID, CredentialSecretID: &third})
	require.ErrorIs(t, err, ErrInspectionBlocked)
	require.Equal(t, before, store.updateCalls)
}

func TestReinspectKeepsLastResultOnTransportErrorAndCountsFailures(t *testing.T) {
	store := newMemoryStore()
	value := liveProductionAccount()
	value.AccountMode = exchange.AccountModeBinanceStandard
	good := exchange.Inspection{
		Exchange: exchange.ExchangeBinance, AccountMode: exchange.AccountModeBinanceStandard,
		CanTradeSpot: true, InspectedAt: time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC),
	}
	encoded, err := good.Encode()
	require.NoError(t, err)
	value.InspectionJSON = encoded
	store.accounts[value.ID] = value

	failing := &fakeInspector{err: errors.Join(exchange.ErrInspectionInconclusive, errors.New("HTTP 503"))}
	service := inspectingService(store, failing, validSecret())
	for i := 1; i <= 6; i++ {
		result, err := service.Reinspect(context.Background(), value.ID)
		require.NoError(t, err)
		require.True(t, result.Inconclusive)
		require.False(t, result.Blocked, "传输错误不触发阻断")
		require.False(t, result.ModeChanged)
		require.Equal(t, i, result.Inspection.ConsecutiveFailures)
	}
	kept, err := exchange.ParseInspection(store.accounts[value.ID].InspectionJSON)
	require.NoError(t, err)
	require.Equal(t, 6, kept.ConsecutiveFailures)
	require.Equal(t, exchange.AccountModeBinanceStandard, kept.AccountMode, "保留上次的账户类型")
	require.True(t, kept.InspectedAt.Equal(good.InspectedAt), "保留上次的检测时间")
	require.Equal(t, exchange.AccountModeBinanceStandard, store.accounts[value.ID].AccountMode)

	// 检测成功后失败计数清零。
	recovered := inspectingService(store, &fakeInspector{}, validSecret())
	result, err := recovered.Reinspect(context.Background(), value.ID)
	require.NoError(t, err)
	require.False(t, result.Inconclusive)
	require.Zero(t, result.Inspection.ConsecutiveFailures)
	after, err := exchange.ParseInspection(store.accounts[value.ID].InspectionJSON)
	require.NoError(t, err)
	require.Zero(t, after.ConsecutiveFailures)
}

func TestReinspectReportsModeChangeAndBlockers(t *testing.T) {
	store := newMemoryStore()
	value := liveProductionAccount()
	value.AccountMode = exchange.AccountModeBinanceStandard
	store.accounts[value.ID] = value

	changed := &fakeInspector{result: &exchange.Inspection{
		Exchange: exchange.ExchangeBinance, AccountMode: exchange.AccountModeBinancePortfolioMargin,
		CanTradeSpot: true, InspectedAt: time.Now().UTC(),
	}}
	result, err := inspectingService(store, changed, validSecret()).Reinspect(context.Background(), value.ID)
	require.NoError(t, err)
	require.True(t, result.ModeChanged)
	require.Equal(t, exchange.AccountModeBinanceStandard, result.PreviousMode)
	require.Equal(t, exchange.AccountModeBinancePortfolioMargin, store.accounts[value.ID].AccountMode)

	withdraw := &fakeInspector{result: &exchange.Inspection{
		Exchange: exchange.ExchangeBinance, AccountMode: exchange.AccountModeBinancePortfolioMargin,
		CanWithdraw: true, CanTradeSpot: true, InspectedAt: time.Now().UTC(),
	}}
	result, err = inspectingService(store, withdraw, validSecret()).Reinspect(context.Background(), value.ID)
	require.NoError(t, err)
	require.True(t, result.Blocked)
	require.False(t, result.ModeChanged)
	// 阻断项写进账户后，账户不再满足下单条件。
	account := store.accounts[value.ID]
	account.Ready = true
	require.ErrorIs(t, account.ExecutionEligibility(), tradingaccount.ErrAccountNotExecutable)
}

func TestReinspectRejectsPaperAccount(t *testing.T) {
	store := newMemoryStore()
	value := validAccount()
	store.accounts[value.ID] = value
	_, err := inspectingService(store, &fakeInspector{}, validSecret()).Reinspect(context.Background(), value.ID)
	require.ErrorIs(t, err, tradingaccount.ErrInvalidAccount)
}

// 交易所拒绝了密钥（鉴权类错误）时没能读出账户类型：已有账户的类型沿用，不能被清空，也不算类型变化；阻断项照常生效。
func TestReinspectKeepsAccountModeWhenCredentialIsRejected(t *testing.T) {
	store := newMemoryStore()
	value := liveProductionAccount()
	value.AccountMode = exchange.AccountModeBinancePortfolioMargin
	store.accounts[value.ID] = value

	rejected := &fakeInspector{result: &exchange.Inspection{
		Exchange: exchange.ExchangeBinance, CredentialRejected: "交易所返回鉴权类错误 -2015", InspectedAt: time.Now().UTC(),
	}}
	result, err := inspectingService(store, rejected, validSecret()).Reinspect(context.Background(), value.ID)
	require.NoError(t, err)
	require.True(t, result.Blocked, "密钥被拒绝是阻断项")
	require.False(t, result.ModeChanged)
	require.Equal(t, exchange.AccountModeBinancePortfolioMargin, store.accounts[value.ID].AccountMode)
	stored, err := exchange.ParseInspection(store.accounts[value.ID].InspectionJSON)
	require.NoError(t, err)
	require.Equal(t, exchange.AccountModeBinancePortfolioMargin, stored.AccountMode, "检测结果里的类型也要沿用")

	// 已有统一账户在 papi、fapi 都被拒绝时会被判成“疑似统一账户旧密钥”（类型记作普通账户）：同样沿用原类型。
	suspected := &fakeInspector{result: &exchange.Inspection{
		Exchange: exchange.ExchangeBinance, AccountMode: exchange.AccountModeBinanceStandard,
		PortfolioMarginSuspected: true, CanTradeSpot: true, CanTradeSwap: true, InspectedAt: time.Now().UTC(),
	}}
	result, err = inspectingService(store, suspected, validSecret()).Reinspect(context.Background(), value.ID)
	require.NoError(t, err)
	require.True(t, result.Blocked)
	require.False(t, result.ModeChanged)
	require.Equal(t, exchange.AccountModeBinancePortfolioMargin, store.accounts[value.ID].AccountMode)

	// 第一次检测（账户还没有类型）：照常写入检测出的类型，不是“变化”。
	fresh := liveProductionAccount()
	fresh.ID = "account-2"
	store.accounts[fresh.ID] = fresh
	result, err = inspectingService(store, &fakeInspector{}, validSecret()).Reinspect(context.Background(), fresh.ID)
	require.NoError(t, err)
	require.False(t, result.ModeChanged)
	require.Equal(t, exchange.AccountModeBinanceStandard, store.accounts[fresh.ID].AccountMode)
}
