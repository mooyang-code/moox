package bus

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

type RuntimeStore interface {
	OutboxStore
	PendingOutboxStats(context.Context) (domain.OutboxStats, error)
}

type Connector func(context.Context) (JetStreamClient, error)

type RuntimeConfig struct {
	Connector         Connector
	Store             RuntimeStore
	InstanceID        string
	RelayInterval     time.Duration
	ReconnectInterval time.Duration
	BatchSize         int
}

type Runtime struct {
	cfg       RuntimeConfig
	mu        sync.RWMutex
	client    JetStreamClient
	lastError error
	cancel    context.CancelFunc
	done      chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

func NewRuntime(cfg RuntimeConfig) (*Runtime, error) {
	if cfg.Connector == nil || cfg.Store == nil {
		return nil, errors.New("strategy EventBus runtime connector and store are required")
	}
	if cfg.RelayInterval <= 0 || cfg.ReconnectInterval <= 0 || cfg.BatchSize <= 0 {
		return nil, errors.New("strategy EventBus runtime intervals and batch size must be positive")
	}
	return &Runtime{cfg: cfg, done: make(chan struct{})}, nil
}

func (r *Runtime) Start(parent context.Context) {
	r.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(parent)
		r.mu.Lock()
		r.cancel = cancel
		r.mu.Unlock()
		go r.run(ctx)
	})
}

func (r *Runtime) run(ctx context.Context) {
	defer close(r.done)
	for {
		if ctx.Err() != nil {
			return
		}
		client := r.currentClient()
		if client == nil || !client.Ready() {
			r.dropClient(client)
			connected, err := r.cfg.Connector(ctx)
			if err != nil {
				r.setError(err)
				if !waitFor(ctx, r.cfg.ReconnectInterval) {
					return
				}
				continue
			}
			r.setClient(connected)
			client = connected
		}
		relay := &Relay{Store: r.cfg.Store, Publisher: &JetStreamPublisher{Client: client, InstanceID: r.cfg.InstanceID}}
		if err := relay.PublishPending(ctx, r.cfg.BatchSize); err != nil {
			r.setError(err)
			r.dropClient(client)
			if !waitFor(ctx, r.cfg.ReconnectInterval) {
				return
			}
			continue
		}
		r.setError(nil)
		if !waitFor(ctx, r.cfg.RelayInterval) {
			return
		}
	}
}

func (r *Runtime) Connected() bool {
	client := r.currentClient()
	return client != nil && client.Ready()
}

func (r *Runtime) LastError() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lastError
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	var closeErr error
	r.closeOnce.Do(func() {
		started := false
		r.mu.RLock()
		cancel := r.cancel
		started = cancel != nil
		r.mu.RUnlock()
		if cancel != nil {
			cancel()
		}
		if started {
			<-r.done
		}
		client := r.currentClient()
		if client != nil {
			closeErr = client.Close()
		}
	})
	return closeErr
}

func (r *Runtime) currentClient() JetStreamClient {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.client
}

func (r *Runtime) setClient(client JetStreamClient) {
	r.mu.Lock()
	old := r.client
	r.client = client
	r.mu.Unlock()
	if old != nil && old != client {
		_ = old.Close()
	}
}

func (r *Runtime) dropClient(expected JetStreamClient) {
	r.mu.Lock()
	client := r.client
	if expected == nil || client == expected {
		r.client = nil
	}
	r.mu.Unlock()
	if client != nil && (expected == nil || client == expected) {
		_ = client.Close()
	}
}

func (r *Runtime) setError(err error) {
	r.mu.Lock()
	r.lastError = err
	r.mu.Unlock()
}

func waitFor(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
