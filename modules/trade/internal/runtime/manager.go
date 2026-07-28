package runtime

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

type AccountSource interface {
	ListEnabledLiveExchangeAccounts(context.Context) ([]store.ExchangeAccountRecord, error)
}

type ManagedSession interface {
	Run(context.Context) error
	Ready() bool
}

type SessionFactory func(store.ExchangeAccountRecord) (ManagedSession, error)

type SessionSnapshot struct {
	EnabledLive  int
	Ready        int
	ConfigErrors []string
}

type Manager struct {
	Accounts     AccountSource
	NewSession   SessionFactory
	PollInterval time.Duration
	RetryMin     time.Duration
	RetryMax     time.Duration

	mu           sync.RWMutex
	sessions     map[string]*managedEntry
	configErrors []string
}

type managedEntry struct {
	session  ManagedSession
	cancel   context.CancelFunc
	account  store.ExchangeAccountRecord
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
	if err := m.reconcile(ctx); err != nil {
		return err
	}
	interval := m.PollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer m.stopAll()
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

func (m *Manager) Ready(exchangeAccountID string) bool {
	m.mu.RLock()
	entry := m.sessions[exchangeAccountID]
	m.mu.RUnlock()
	return entry != nil && entry.session.Ready()
}

func (m *Manager) Snapshot() SessionSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := SessionSnapshot{
		EnabledLive:  len(m.sessions),
		ConfigErrors: append([]string(nil), m.configErrors...),
	}
	for _, entry := range m.sessions {
		if entry.session.Ready() {
			result.Ready++
		}
	}
	sort.Strings(result.ConfigErrors)
	return result
}

func (m *Manager) reconcile(ctx context.Context) error {
	accounts, err := m.Accounts.ListEnabledLiveExchangeAccounts(ctx)
	if err != nil {
		return err
	}
	wanted := make(map[string]store.ExchangeAccountRecord, len(accounts))
	configErrors := make([]string, 0)
	for _, account := range accounts {
		if _, duplicate := wanted[account.ExchangeAccountID]; duplicate {
			configErrors = append(
				configErrors,
				"duplicate enabled Exchange account ID "+account.ExchangeAccountID,
			)
			continue
		}
		wanted[account.ExchangeAccountID] = account
	}

	m.mu.Lock()
	if m.sessions == nil {
		m.sessions = make(map[string]*managedEntry)
	}
	stopping := make(map[string]*managedEntry)
	for id, entry := range m.sessions {
		account, found := wanted[id]
		if found && sameSessionConfig(entry.account, account) {
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
		}
	}
	for id, account := range wanted {
		if _, found := m.sessions[id]; found {
			continue
		}
		session, createErr := m.NewSession(account)
		if createErr != nil {
			configErrors = append(configErrors, createErr.Error())
			continue
		}
		sessionCtx, cancel := context.WithCancel(ctx)
		entry := &managedEntry{
			session: session, cancel: cancel, account: account,
			done: make(chan struct{}),
		}
		m.sessions[id] = entry
		go func() {
			defer close(entry.done)
			m.runSession(sessionCtx, session)
		}()
	}
	m.configErrors = configErrors
	m.mu.Unlock()
	return nil
}

func sameSessionConfig(
	left store.ExchangeAccountRecord,
	right store.ExchangeAccountRecord,
) bool {
	return left.SpaceID == right.SpaceID &&
		left.ExchangeAccountID == right.ExchangeAccountID &&
		left.Exchange == right.Exchange &&
		left.MarketType == right.MarketType &&
		left.ExecutionMode == right.ExecutionMode &&
		left.CredentialSecretID == right.CredentialSecretID &&
		left.SettlementAsset == right.SettlementAsset &&
		left.MarginMode == right.MarginMode &&
		left.Status == right.Status &&
		left.Paused == right.Paused &&
		reflect.DeepEqual(left.SyncSymbols, right.SyncSymbols) &&
		reflect.DeepEqual(left.LeverageSettings, right.LeverageSettings)
}

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
		err := session.Run(ctx)
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return
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
	m.mu.Unlock()
}

func (m *Manager) stopAll() {
	m.mu.Lock()
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
