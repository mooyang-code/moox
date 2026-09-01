package marketdata

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRegistryUsesProviderAndSourceKey(t *testing.T) {
	r := NewRegistry()
	descriptor := ProviderDescriptor{
		ProviderID: "TDX", SourceID: "normal_7709", ProtocolVariant: ProtocolTDXNormal,
		Transport: "tcp", Port: 7709, Markets: []string{"stock_cn"}, InstrumentTypes: []string{"equity"}, Frequencies: []string{"1d"},
	}
	require.NoError(t, r.Register(descriptor))
	require.Error(t, r.Register(descriptor))

	got, ok := r.Lookup(SourceKey{ProviderID: "tdx", SourceID: "normal_7709"})
	require.True(t, ok)
	require.Equal(t, "TDX", got.ProviderID)
}

func TestProviderErrorCategoriesAreDistinct(t *testing.T) {
	categories := []error{ErrTimeout, ErrRateLimited, ErrRemoteBusy, ErrTCP, ErrHTTPStatus, ErrProtocol, ErrNoClosedBar, ErrUnsupportedSymbol, ErrUnsupportedFrequency}
	seen := make(map[string]struct{}, len(categories))
	for _, category := range categories {
		require.NotNil(t, category)
		require.NotContains(t, seen, category.Error())
		seen[category.Error()] = struct{}{}
		require.ErrorIs(t, errors.Join(category, errors.New("detail")), category)
	}
}

func TestSourceSpecValidatesStaticSourceIdentity(t *testing.T) {
	spec := SourceSpec{
		Key: SourceKey{ProviderID: "tdx", SourceID: "normal_7709"}, Status: SourceEnabled,
		ProtocolVariant: ProtocolTDXNormal, Transport: "tcp", Port: 7709,
		Markets: []string{"stock_cn"}, Instruments: []string{"equity"}, Frequencies: []string{"1d"},
		TimestampMode: string(TimestampStartLabel), CompleteOHLCV: true, HasAmount: true,
	}
	require.NoError(t, spec.Validate())

	spec.Key.SourceID = ""
	require.Error(t, spec.Validate())
}

func TestProviderDescriptorSourceSpecCarriesKlineSemantics(t *testing.T) {
	descriptor := ProviderDescriptor{
		ProviderID: "sw", SourceID: "index_sw_http", Status: SourceCatalogOnly,
		ProtocolVariant: ProtocolHTTP, Transport: "https", Port: 443,
		Markets: []string{"stock_cn"}, InstrumentTypes: []string{"index"}, Frequencies: []string{"1d", "1M"},
	}
	spec := descriptor.SourceSpec(KlineSpec{
		TimestampMode: "start-label", CompleteOHLCV: true, HasAmount: true,
	})
	require.Equal(t, "start-label", spec.TimestampMode)
	require.True(t, spec.CompleteOHLCV)
	require.True(t, spec.HasAmount)
}

func TestRegistryDefensivelyCopiesDescriptor(t *testing.T) {
	r := NewRegistry()
	markets := []string{"stock_cn"}
	require.NoError(t, r.Register(ProviderDescriptor{
		ProviderID: "eastmoney", SourceID: "http", ProtocolVariant: ProtocolHTTP,
		Transport: "https", Port: 443, Markets: markets, InstrumentTypes: []string{"equity"}, Frequencies: []string{"1d"},
	}))
	markets[0] = "corrupted"
	got, ok := r.Lookup(SourceKey{ProviderID: "eastmoney", SourceID: "http"})
	require.True(t, ok)
	require.Equal(t, "stock_cn", got.Markets[0])
	got.Markets[0] = "corrupted"
	again, ok := r.Lookup(SourceKey{ProviderID: "eastmoney", SourceID: "http"})
	require.True(t, ok)
	require.Equal(t, "stock_cn", again.Markets[0])
}

func TestProviderDescriptorRejectsUnknownStatus(t *testing.T) {
	descriptor := ProviderDescriptor{
		ProviderID: "cni", SourceID: "index_http", Status: "experimental", ProtocolVariant: ProtocolHTTP,
		Transport: "https", Port: 443, Markets: []string{"stock_cn"}, InstrumentTypes: []string{"index"}, Frequencies: []string{"1d"},
	}
	require.Error(t, descriptor.Validate())
}

func TestCatalogOnlyProviderDescriptorIsNotEnabled(t *testing.T) {
	descriptor := ProviderDescriptor{Status: SourceCatalogOnly}
	require.False(t, descriptor.Status.IsEnabled())
}
