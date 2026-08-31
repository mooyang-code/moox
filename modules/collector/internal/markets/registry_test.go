package markets

import (
	"context"
	"io"
	"net/url"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	markethttp "github.com/mooyang-code/moox/modules/collector/internal/sources/markethttp/eastmoney"
	"github.com/stretchr/testify/require"
)

type compositionGetter struct{}

func (compositionGetter) Get(context.Context, string, string, url.Values, interface{}) error {
	return nil
}

type rawCompositionGetter struct{ compositionGetter }

func (rawCompositionGetter) GetStream(_ context.Context, _ string, _ string, _ url.Values, consume func(io.Reader) error) error {
	return consume(strings.NewReader(`{"data":{}}`))
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

func TestNewCompositionRegistersTencentWhenRawHTTPIsAvailable(t *testing.T) {
	composition, err := NewComposition(rawCompositionGetter{}, nil, false)
	require.NoError(t, err)
	if _, ok := composition.Registry.Lookup(marketdata.SourceKey{ProviderID: "tencent", SourceID: "stock_cn_http"}); !ok {
		t.Fatal("tencent source was not registered for a raw HTTP getter")
	}
}

var _ markethttp.Getter = compositionGetter{}
