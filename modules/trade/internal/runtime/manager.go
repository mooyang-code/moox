package runtime

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

type AccountSource interface {
	ListEnabledTradingAccounts(context.Context) ([]store.TradingAccountRecord, error)
}

type ManagedSession interface {
	Run(context.Context) error
	Ready() bool
}

type SessionFactory func(store.TradingAccountRecord) (ManagedSession, error)

type SessionSnapshot struct {
	Enabled       int
	Ready         int
	Reconciled    bool
	ConfigErrors  []string
	AccountErrors map[string]string
}

type Manager struct {
	Accounts         AccountSource
	NewSession       SessionFactory
	PollInterval     time.Duration
	RetryMin         time.Duration
	RetryMax         time.Duration
	OnSessionRemoved func(string)

	mu            sync.RWMutex
	sessions      map[string]*managedEntry
	configErrors  []string
	accountErrors map[string]string
	enabled       int
	reconciled    bool
}

type managedEntry struct {
	session  ManagedSession
	cancel   context.CancelFunc
	account  store.TradingAccountRecord
	done     chan struct{}
	stopping bool
}

func (m *Manager) Run(ctx context.Context) error {
	if m == nil || m.Accounts == nil || m.NewSession == nil {
		return ErrSessionConfig
	}
	m.mu.Lock()
	if m.sessions == nil {
		m.sessions = make(map[string]*managedEntry)
	}
	m.mu.Unlock()
	interval := m.PollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	defer m.stopAll()
	for {
		if err := m.reconcile(ctx); err == nil {
			break
		} else {
			m.setConfigErrors([]string{err.Error()})
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := m.reconcile(ctx); err != nil {
				m.setConfigErrors([]string{err.Error()})
			}
		}
	}
}

func (m *Manager) Ready(tradingAccountID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry := m.sessions[tradingAccountID]
	return entry != nil && !entry.stopping && entry.session.Ready()
}

func (m *Manager) ReadyFor(account tradingaccount.Account) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry := m.sessions[account.ID]
	if entry == nil || entry.stopping || !entry.session.Ready() {
		return false
	}
	environment := string(account.Environment)
	if account.ExecutionMode == exchange.ExecutionModePaper {
		// Paper has no persisted live environment. Its market-data environment
		// is derived at bind time, so do not compare the domain sentinel PAPER
		// with the empty live-environment column.
		environment = ""
	}
	// 只填 sameSessionConfig 比较的字段：账户类型和检测指纹也在比较之列，漏填会让实盘账户永远不就绪。
	current := store.TradingAccountRecord{
		SpaceID: account.SpaceID, TradingAccountID: account.ID,
		Exchange: string(account.Exchange), Markets: marketStrings(account.Markets),
		ExecutionMode:      string(account.ExecutionMode),
		AccountMode:        string(account.AccountMode),
		Environment:        environment,
		CredentialSecretID: account.CredentialSecretID,
		InspectionJSON:     account.InspectionJSON,
		SettlementAsset:    account.SettlementAsset, MarginMode: string(account.MarginMode),
		Status: string(account.Status),
	}
	if account.Paper != nil {
		current.PaperConfig = &store.PaperAccountConfigRecord{
			SpaceID: account.SpaceID, TradingAccountID: account.ID,
			InitialBalance: account.Paper.InitialBalance.String(),
			MakerFeeRate:   account.Paper.MakerFeeRate.String(),
			TakerFeeRate:   account.Paper.TakerFeeRate.String(),
			SlippageBPS:    account.Paper.SlippageBPS.String(),
		}
	}
	return sameSessionConfig(entry.account, current)
}

// EnsureSymbolConfig 在首次开仓前把全仓和杠杆设置到交易所。
func (m *Manager) EnsureSymbolConfig(
	ctx context.Context,
	tradingAccountID string,
	symbol exchange.SymbolKey,
	leverage shared.Decimal,
) error {
	session, err := m.symbolConfigSession(tradingAccountID)
	if err != nil {
		return err
	}
	return session.EnsureSymbolConfig(ctx, symbol, leverage)
}

// RequestSync 请求账户会话做一次防抖轻量同步，会话不支持时返回 false。
func (m *Manager) RequestSync(tradingAccountID string) bool {
	m.mu.RLock()
	entry := m.sessions[tradingAccountID]
	if entry == nil || entry.stopping {
		m.mu.RUnlock()
		return false
	}
	session, ok := entry.session.(interface{ RequestSync() bool })
	m.mu.RUnlock()
	return ok && session.RequestSync()
}

// ForgetSymbolConfig 让标的下次开仓前重新设置。
func (m *Manager) ForgetSymbolConfig(tradingAccountID string, symbol exchange.SymbolKey) {
	if session, err := m.symbolConfigSession(tradingAccountID); err == nil {
		session.ForgetSymbolConfig(symbol)
	}
}

// ForgetAllSymbolConfigs 让账户的全部标的下次开仓前重新设置。
func (m *Manager) ForgetAllSymbolConfigs(tradingAccountID string) {
	if session, err := m.symbolConfigSession(tradingAccountID); err == nil {
		session.ForgetAllSymbolConfigs()
	}
}

type symbolConfigSession interface {
	EnsureSymbolConfig(ctx context.Context, symbol exchange.SymbolKey, leverage shared.Decimal) error
	ForgetSymbolConfig(symbol exchange.SymbolKey)
	ForgetAllSymbolConfigs()
}

func (m *Manager) symbolConfigSession(tradingAccountID string) (symbolConfigSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry := m.sessions[tradingAccountID]
	if entry == nil || entry.stopping {
		return nil, ErrSessionConfig
	}
	session, ok := entry.session.(symbolConfigSession)
	if !ok {
		return nil, ErrSessionConfig
	}
	return session, nil
}

func (m *Manager) Adapter(tradingAccountID string) (execution.ExecutionAdapter, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry := m.sessions[tradingAccountID]
	if entry == nil {
		return nil, ErrSessionConfig
	}
	if entry.stopping {
		return nil, ErrSessionConfig
	}
	source, ok := entry.session.(interface {
		ExecutionAdapter() execution.ExecutionAdapter
	})
	if !ok {
		return nil, ErrSessionConfig
	}
	adapter := source.ExecutionAdapter()
	if adapter == nil {
		return nil, ErrSessionConfig
	}
	return adapter, nil
}

func (m *Manager) Snapshot() SessionSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := SessionSnapshot{
		Enabled:      m.enabled,
		Reconciled:   m.reconciled,
		ConfigErrors: append([]string(nil), m.configErrors...),
	}
	if len(m.accountErrors) > 0 {
		result.AccountErrors = make(map[string]string, len(m.accountErrors))
		for id, message := range m.accountErrors {
			result.AccountErrors[id] = message
		}
	}
	for _, entry := range m.sessions {
		if !entry.stopping && entry.session.Ready() {
			result.Ready++
		}
	}
	sort.Strings(result.ConfigErrors)
	return result
}

func (m *Manager) reconcile(ctx context.Context) error {
	accounts, err := m.Accounts.ListEnabledTradingAccounts(ctx)
	if err != nil {
		m.setConfigErrors([]string{err.Error()})
		return err
	}
	wanted := make(map[string]store.TradingAccountRecord, len(accounts))
	configErrors := make([]string, 0)
	accountErrors := make(map[string]string)
	for _, account := range accounts {
		if _, duplicate := wanted[account.TradingAccountID]; duplicate {
			configErrors = append(
				configErrors,
				"duplicate enabled Exchange account ID "+account.TradingAccountID,
			)
			continue
		}
		wanted[account.TradingAccountID] = account
	}

	m.mu.Lock()
	m.enabled = len(accounts)
	if m.sessions == nil {
		m.sessions = make(map[string]*managedEntry)
	}
	stopping := make(map[string]*managedEntry)
	for id, entry := range m.sessions {
		account, found := wanted[id]
		if found && !entry.stopping && sameSessionConfig(entry.account, account) {
			continue
		}
		if !entry.stopping {
			entry.stopping = true
			entry.cancel()
		}
		stopping[id] = entry
	}
	m.mu.Unlock()

	for _, entry := range stopping {
		<-entry.done
	}

	m.mu.Lock()
	for id, entry := range stopping {
		if m.sessions[id] == entry {
			delete(m.sessions, id)
			if m.OnSessionRemoved != nil {
				m.OnSessionRemoved(id)
			}
		}
	}
	missing := make([]store.TradingAccountRecord, 0)
	for id, account := range wanted {
		if _, found := m.sessions[id]; !found {
			missing = append(missing, account)
		}
	}
	m.mu.Unlock()

	// 放锁后再建会话：实盘账户建会话要经网关取密钥（可能等好几秒），持锁建会话会挡住 Adapter、Ready、
	// RequestSync 等全部读者，连带下单与同步。reconcile 只由 Run 串行调用，m.sessions 只有它和 Run 退出时的
	// stopAll 会改，放锁期间不会有别人插入同一账户；账户列表是本轮开头读的快照，建会话期间配置变了由下一轮按
	// sameSessionConfig 重建。
	created := make(map[string]ManagedSession, len(missing))
	for _, account := range missing {
		session, createErr := m.NewSession(account)
		if createErr == nil && session == nil {
			createErr = ErrSessionConfig
		}
		if createErr != nil {
			accountErrors[account.TradingAccountID] = createErr.Error()
			continue
		}
		created[account.TradingAccountID] = session
	}

	m.mu.Lock()
	for id, session := range created {
		sessionCtx, cancel := context.WithCancel(ctx)
		entry := &managedEntry{
			session: session, cancel: cancel, account: wanted[id],
			done: make(chan struct{}),
		}
		m.sessions[id] = entry
		go func() {
			defer close(entry.done)
			m.runSession(sessionCtx, session)
		}()
	}
	m.configErrors = configErrors
	m.accountErrors = accountErrors
	m.reconciled = len(configErrors) == 0
	m.mu.Unlock()
	return nil
}

func marketStrings(markets []exchange.MarketType) []string {
	result := make([]string, len(markets))
	for i, market := range markets {
		result[i] = string(market)
	}
	return result
}

func sameSessionConfig(
	left store.TradingAccountRecord,
	right store.TradingAccountRecord,
) bool {
	return left.SpaceID == right.SpaceID &&
		left.TradingAccountID == right.TradingAccountID &&
		left.Exchange == right.Exchange &&
		slices.Equal(left.Markets, right.Markets) &&
		left.ExecutionMode == right.ExecutionMode &&
		left.AccountMode == right.AccountMode &&
		left.Environment == right.Environment &&
		left.CredentialSecretID == right.CredentialSecretID &&
		// 同一个密钥 ID 下改了密钥内容：重新检测会写入新的指纹，会话随之重建、绑定检测过的新密钥。
		inspectionFingerprint(left) == inspectionFingerprint(right) &&
		left.SettlementAsset == right.SettlementAsset &&
		left.MarginMode == right.MarginMode &&
		left.Status == right.Status &&
		reflect.DeepEqual(left.PaperConfig, right.PaperConfig)
}

// stableSessionRun 是会话被视为正常运行过的时长，超过后断开重连不再沿用翻倍后的退避。
var stableSessionRun = time.Minute

func (m *Manager) runSession(ctx context.Context, session ManagedSession) {
	delay := m.RetryMin
	if delay <= 0 {
		delay = time.Second
	}
	maxDelay := m.RetryMax
	if maxDelay <= 0 {
		maxDelay = 30 * time.Second
	}
	for {
		started := time.Now()
		err := session.Run(ctx)
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return
		}
		// 正常跑过一段时间才断开（如币安每 24 小时断一次）从最短退避重来；只有连续快速失败才翻倍。
		if time.Since(started) >= stableSessionRun {
			delay = m.RetryMin
			if delay <= 0 {
				delay = time.Second
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
}

func (m *Manager) setConfigErrors(values []string) {
	m.mu.Lock()
	m.configErrors = append([]string(nil), values...)
	m.reconciled = false
	m.mu.Unlock()
}

func (m *Manager) stopAll() {
	m.mu.Lock()
	m.reconciled = false
	entries := make([]*managedEntry, 0, len(m.sessions))
	for _, entry := range m.sessions {
		entry.stopping = true
		entry.cancel()
		entries = append(entries, entry)
	}
	m.mu.Unlock()
	for _, entry := range entries {
		<-entry.done
	}
	m.mu.Lock()
	for id := range m.sessions {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
}

// inspectionFingerprint 取账户检测结果里记录的密钥指纹；没有检测结果为空串。
func inspectionFingerprint(account store.TradingAccountRecord) string {
	inspection, err := exchange.ParseInspection(account.InspectionJSON)
	if err != nil {
		return ""
	}
	return inspection.CredentialFingerprint
}
