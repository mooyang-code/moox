package bootstrap

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/catalogsync"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/packages/jetstream/testkit"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

type blockingControlSnapshot struct{ entered, canceled, release chan struct{} }

func (r *blockingControlSnapshot) CatalogSnapshot(ctx context.Context) (*domain.CatalogSnapshot, error) {
	close(r.entered)
	<-ctx.Done()
	close(r.canceled)
	<-r.release
	return nil, ctx.Err()
}

func TestControlCatalogCloseCancelsAndDrainsReader(t *testing.T) {
	bus := testkit.Start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reader := &blockingControlSnapshot{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	defer close(reader.release)
	stop, err := StartControlCatalog(ctx, CatalogBusConfig{URLs: []string{bus.URL()}}, reader)
	require.NoError(t, err)
	nc, err := nats.Connect(bus.URL())
	require.NoError(t, err)
	defer nc.Close()
	requestDone := make(chan struct{})
	go func() { defer close(requestDone); _, _ = catalogsync.FetchSnapshot(ctx, nc) }()
	select {
	case <-reader.entered:
	case <-ctx.Done():
		t.Fatal("snapshot did not start")
	}
	closed := make(chan error, 1)
	go func() { closed <- stop() }()
	select {
	case <-reader.canceled:
	case <-closed:
		t.Fatal("catalog closed before draining its reader")
	case <-time.After(time.Second):
		t.Fatal("close did not cancel active snapshot")
	}
	select {
	case <-closed:
		t.Fatal("close returned while reader still runs")
	case <-time.After(20 * time.Millisecond):
	}
	reader.release <- struct{}{}
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("close did not drain")
	}
	require.NoError(t, stop())
	cancel()
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("request did not finish")
	}
}
