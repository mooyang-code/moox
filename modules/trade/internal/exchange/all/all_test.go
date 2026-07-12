package all

import (
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBlankImport_ShouldRegisterBuiltInExchanges(t *testing.T) {
	for _, name := range []string{"binance", "okx"} {
		adapter, err := exchange.New(name)
		require.NoError(t, err, "exchange %s", name)
		require.NotNil(t, adapter)
		assert.Equal(t, name, adapter.Name())
	}
}
