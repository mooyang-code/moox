package outbox

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
	Probe             func(context.Context, JetStreamClient) error
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
	cancel    context.CancelFunc
	done      chan struct{}
	started   bool
	closed    bool
	validated bool
}

func NewRuntime(cfg RuntimeConfig) (*Runtime, error) {
	if cfg.Connector == nil || cfg.Probe == nil || cfg.Store == nil {
		return nil, errors.New("strategy EventBus runtime connector, probe, and store are required")
	}
	if cfg.RelayInterval <= 0 || cfg.ReconnectInterval <= 0 || cfg.BatchSize <= 0 {
		return nil, errors.New("strategy EventBus runtime intervals and batch size must be positive")
	}
	return &Runtime{cfg: cfg, done: make(chan struct{})}, nil
}

func (r *Runtime) Start(parent context.Context) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return errors.New("strategy EventBus runtime is closed")
	}
	if r.started {
		r.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	r.cancel = cancel
	r.started = true
	r.mu.Unlock()
	go r.run(ctx)
	return nil
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
				if !waitFor(ctx, r.cfg.ReconnectInterval) {
					return
				}
				continue
			}
			r.setClient(connected)
			client = connected
		}
		if !r.isValidated() {
			if err := r.cfg.Probe(ctx, client); err != nil {
				r.dropClient(client)
				if !waitFor(ctx, r.cfg.ReconnectInterval) {
					return
				}
				continue
			}
			r.setValidated(true)
		}
		eventPublisher := client.EventPublisher()
		if eventPublisher == nil {
			r.dropClient(client)
			if !waitFor(ctx, r.cfg.ReconnectInterval) {
				return
			}
			continue
		}
		relay := &Relay{Store: r.cfg.Store, Publisher: &JetStreamPublisher{Publisher: eventPublisher, InstanceID: r.cfg.InstanceID}}
		if err := relay.PublishPending(ctx, r.cfg.BatchSize); err != nil {
			r.dropClient(client)
			if !waitFor(ctx, r.cfg.ReconnectInterval) {
				return
			}
			continue
		}
		if !waitFor(ctx, r.cfg.RelayInterval) {
			return
		}
	}
}

func (r *Runtime) Connected() bool {
	client := r.currentClient()
	return client != nil && client.Ready() && r.isValidated()
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		started := r.started
		done := r.done
		r.mu.Unlock()
		if started {
			<-done
		}
		return nil
	}
	r.closed = true
	started := r.started
	cancel := r.cancel
	done := r.done
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if started {
		<-done
	}
	client := r.currentClient()
	if client != nil {
		return client.Close()
	}
	return nil
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
	r.validated = false
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
		r.validated = false
	}
	r.mu.Unlock()
	if client != nil && (expected == nil || client == expected) {
		_ = client.Close()
	}
}

func (r *Runtime) isValidated() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.validated
}

func (r *Runtime) setValidated(validated bool) {
	r.mu.Lock()
	r.validated = validated
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
