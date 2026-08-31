package markets

import (
	"context"
	"net/url"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	markethttp "github.com/mooyang-code/moox/modules/collector/internal/sources/markethttp/eastmoney"
	"github.com/stretchr/testify/require"
)

type compositionGetter struct{}

func (compositionGetter) Get(context.Context, string, string, url.Values, interface{}) error {
	return nil
}

func TestNewCompositionRegistersCanonicalHTTPSources(t *testing.T) {
	composition, err := NewComposition(compositionGetter{}, nil, false)
	require.NoError(t, err)
	for _, key := range []marketdata.SourceKey{
		{ProviderID: "eastmoney", SourceID: "stock_cn_http"},
		{ProviderID: "eastmoney", SourceID: "stock_hk_http"},
		{ProviderID: "eastmoney", SourceID: "stock_us_http"},
		{ProviderID: "eastmoney", SourceID: "index_http"},
		{ProviderID: "eastmoney", SourceID: "convertible_bond_http"},
	} {
		if _, ok := composition.Registry.Lookup(key); !ok {
			t.Fatalf("source %s was not registered", key)
		}
	}
	if _, ok := composition.Catalog.Lookup("stock_cn", "index"); !ok {
		t.Fatal("stock_cn index manifest was not registered")
	}
}

var _ markethttp.Getter = compositionGetter{}
