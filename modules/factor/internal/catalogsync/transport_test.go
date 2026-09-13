package catalogsync

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/jetstream/testkit"
	"github.com/stretchr/testify/require"
)

type snapshotReaderFunc func(context.Context) (*domain.CatalogSnapshot, error)

func (f snapshotReaderFunc) CatalogSnapshot(ctx context.Context) (*domain.CatalogSnapshot, error) {
	return f(ctx)
}

func TestSnapshotTransportAcrossSeparateConnections(t *testing.T) {
	server := testkit.Start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	control, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{server.URL()}, Name: "catalog-control"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, control.Close()) })
	engine, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{server.URL()}, Name: "catalog-engine"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, engine.Close()) })
	want := &domain.CatalogSnapshot{Revision: 7, Factors: []domain.FactorDef{{FactorID: "f", FactorType: "timeseries", SourceCode: "source"}}}
	sub, err := ServeSnapshots(ctx, control, snapshotReaderFunc(func(context.Context) (*domain.CatalogSnapshot, error) { return want, nil }))
	require.NoError(t, err)
	actual, err := FetchSnapshot(ctx, engine)
	require.NoError(t, err)
	require.Equal(t, want, actual)
	require.NoError(t, sub.Unsubscribe())
	require.NoError(t, control.FlushWithContext(ctx))
	_, err = ServeSnapshots(ctx, control, snapshotReaderFunc(func(context.Context) (*domain.CatalogSnapshot, error) { return nil, errors.New("secret local path") }))
	require.NoError(t, err)
	_, err = FetchSnapshot(ctx, engine)
	require.ErrorContains(t, err, "catalog_unavailable")
	require.NotContains(t, err.Error(), "secret")
}
