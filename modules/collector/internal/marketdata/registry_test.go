package marketdata

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testProvider struct {
	descriptor ProviderDescriptor
}

func (p *testProvider) Descriptor() ProviderDescriptor { return p.descriptor }

type testKlineProvider struct{ testProvider }

func (p *testKlineProvider) KlineSpec() KlineSpec { return KlineSpec{} }
func (p *testKlineProvider) FetchKlines(context.Context, KlineRequest) ([]NormalizedKline, error) {
	return nil, nil
}

type testInstrumentProvider struct{ testProvider }

func (p *testInstrumentProvider) InstrumentSpec() InstrumentSpec { return InstrumentSpec{} }
func (p *testInstrumentProvider) FetchInstrumentSnapshot(context.Context, InstrumentRequest) (InstrumentSnapshot, error) {
	return InstrumentSnapshot{}, nil
}

func TestProviderRegistryRegistersUniqueIDs(t *testing.T) {
	registry := NewRegistry()
	provider := &testProvider{descriptor: ProviderDescriptor{ID: "test", DisplayName: "Test", Hosts: []string{"api.test"}}}
	require.NoError(t, registry.Register(provider))
	assert.ErrorIs(t, registry.Register(provider), ErrProviderAlreadyRegistered)

	got, err := registry.Provider("test")
	require.NoError(t, err)
	assert.Same(t, provider, got)
	_, err = registry.Provider("unknown")
	assert.ErrorIs(t, err, ErrProviderNotFound)
}

func TestProviderRegistryReturnsStronglyTypedFetchers(t *testing.T) {
	registry := NewRegistry()
	kline := &testKlineProvider{testProvider{descriptor: ProviderDescriptor{ID: "kline", DisplayName: "Kline", Hosts: []string{"kline.test"}}}}
	instrument := &testInstrumentProvider{testProvider{descriptor: ProviderDescriptor{ID: "instrument", DisplayName: "Instrument", Hosts: []string{"instrument.test"}}}}
	require.NoError(t, registry.Register(kline))
	require.NoError(t, registry.Register(instrument))

	gotKline, err := registry.KlineFetcher("kline")
	require.NoError(t, err)
	assert.Same(t, kline, gotKline)
	_, err = registry.InstrumentFetcher("kline")
	assert.ErrorIs(t, err, ErrFetcherNotSupported)

	gotInstrument, err := registry.InstrumentFetcher("instrument")
	require.NoError(t, err)
	assert.Same(t, instrument, gotInstrument)
	_, err = registry.KlineFetcher("missing")
	assert.ErrorIs(t, err, ErrProviderNotFound)
}

func TestProviderRegistryRejectsInvalidDescriptor(t *testing.T) {
	registry := NewRegistry()
	err := registry.Register(&testProvider{descriptor: ProviderDescriptor{ID: "Bad ID", DisplayName: "Bad", Hosts: []string{"api.test"}}})
	assert.Error(t, err)
}

func TestProviderRegistryResolvesDistinctSourceKeys(t *testing.T) {
	registry := NewRegistry()
	first := &testKlineProvider{testProvider{descriptor: ProviderDescriptor{ID: "feed", SourceID: "source-a", DisplayName: "Feed A", Hosts: []string{"a.test"}}}}
	second := &testKlineProvider{testProvider{descriptor: ProviderDescriptor{ID: "feed", SourceID: "source-b", DisplayName: "Feed B", Hosts: []string{"b.test"}}}}
	require.NoError(t, registry.Register(first))
	require.NoError(t, registry.Register(second))

	got, err := registry.Source(SourceKey{ProviderID: "feed", SourceID: "source-a"})
	require.NoError(t, err)
	assert.Same(t, first, got)
	gotFetcher, err := registry.KlineFetcherBySource(SourceKey{ProviderID: "feed", SourceID: "source-b"})
	require.NoError(t, err)
	assert.Same(t, second, gotFetcher)
	_, err = registry.Provider("feed")
	assert.ErrorIs(t, err, ErrProviderAmbiguous)
}
