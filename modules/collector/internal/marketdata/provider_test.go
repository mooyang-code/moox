package marketdata

import (
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
