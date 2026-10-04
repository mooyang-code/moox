package command

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
)

const (
	collectorPublishLeaseRenewInterval = 30 * time.Second
	collectorPublishLeaseRequestLimit  = 10 * time.Second
	collectorPublishLeaseCloseLimit    = 5 * time.Second
)

var errCollectorBatchOutcomeUnknown = errors.New("collector batch outcome is unknown")
var errCollectorPublishFenceChanged = errors.New("collector publish fence changed")

type collectorPublishLeaseGuard struct {
	client *adminclient.Client
	lease  *adminclient.CollectorPublishLease
	parent context.Context
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func acquireCollectorPublishLease(ctx context.Context, client *adminclient.Client, spaceID string) (context.Context, *collectorPublishLeaseGuard, error) {
	return acquireCollectorPublishLeaseWithRequestContext(ctx, ctx, client, spaceID)
}

func acquireCollectorPublishLeaseWithRequestContext(lifetimeCtx, requestCtx context.Context, client *adminclient.Client, spaceID string) (context.Context, *collectorPublishLeaseGuard, error) {
	if lifetimeCtx == nil || requestCtx == nil || client == nil {
		return nil, nil, fmt.Errorf("collector publish lease requires context and control client")
	}
	holderID, err := newCollectorPublishHolderID()
	if err != nil {
		return nil, nil, err
	}
	lease, err := client.AcquireCollectorPublishLease(requestCtx, spaceID, holderID)
	if err != nil {
		return nil, nil, err
	}
	leaseCtx, cancel := context.WithCancel(lifetimeCtx)
	guard := &collectorPublishLeaseGuard{client: client, lease: lease, parent: lifetimeCtx, cancel: cancel, done: make(chan struct{})}
	client.SetCollectorPublishLease(lease)
	go guard.renewLoop(leaseCtx)
	return leaseCtx, guard, nil
}

func (g *collectorPublishLeaseGuard) renewLoop(ctx context.Context) {
	defer close(g.done)
	ticker := time.NewTicker(collectorPublishLeaseRenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			renewCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), collectorPublishLeaseRequestLimit)
			renewed, err := g.client.RenewCollectorPublishLease(renewCtx, g.lease)
			cancel()
			if err != nil {
				g.cancel()
				return
			}
			g.lease = renewed
			g.client.SetCollectorPublishLease(renewed)
		}
	}
}

func (g *collectorPublishLeaseGuard) Close() error {
	if g == nil {
		return nil
	}
	var releaseErr error
	g.once.Do(func() {
		g.cancel()
		<-g.done
		defer g.client.SetCollectorPublishLease(nil)
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(g.parent), collectorPublishLeaseCloseLimit)
		defer cancel()
		releaseErr = g.client.ReleaseCollectorPublishLease(releaseCtx, g.lease)
	})
	return releaseErr
}

func (g *collectorPublishLeaseGuard) Abandon() {
	if g == nil {
		return
	}
	g.once.Do(func() {
		g.cancel()
		<-g.done
		g.client.SetCollectorPublishLease(nil)
	})
}

func newCollectorPublishHolderID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate collector publish holder id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func isAmbiguousCollectorPublishOutcome(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return true
	}
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"connection reset", "connection refused", "broken pipe", "server closed idle connection",
		"control returned http 5", "code 5", "empty job_id", "missing ret_info", "empty response body",
		"operation:", "trpc-ret=",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
