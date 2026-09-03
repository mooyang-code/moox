package marketfetch

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTimerRequestFromEnv(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "crypto")
	t.Setenv("MOOX_MARKET_FETCH_PROVIDER", "binance")
	t.Setenv("MOOX_MARKET_FETCH_MARKET_TYPE", "spot")
	t.Setenv("MOOX_MARKET_FETCH_DATASET_ID", "bars")
	t.Setenv("MOOX_MARKET_FETCH_FREQUENCY", "1m")
	t.Setenv("MOOX_MARKET_FETCH_SUBJECTS", "ETH-USDT|BTC-USDT")
	t.Setenv("MOOX_MARKET_FETCH_SYMBOLS_JSON", `{"BTC-USDT":"BTCUSDT","ETH-USDT":"ETHUSDT"}`)
	t.Setenv("MOOX_MARKET_FETCH_ASSIGNMENT_HASH", "abc")
	t.Setenv("MOOX_MARKET_FETCH_DNS_ROUTES_JSON", `{"API.BINANCE.COM.":["203.0.113.1","203.0.113.1","bad"]}`)
	t.Setenv("MOOX_STORAGE_RPC_GATEWAY_TARGET", "ip://10.0.0.1:11003")
	t.Setenv("MOOX_FETCH_MAX_INFLIGHT_REQUESTS", "30")
	req, target, err := TimerRequestFromEnv("req", "node", time.Date(2026, 8, 4, 1, 2, 3, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, "ip://10.0.0.1:11003", target)
	require.Len(t, req.Items, 2)
	require.Equal(t, "node", req.FunctionName)
	require.Equal(t, 30, req.Concurrency)
	require.Equal(t, "BTCUSDT", req.Items[0].Symbol)
	require.Equal(t, "BTC-USDT", req.Items[0].SubjectID)
	require.Equal(t, []string{"203.0.113.1"}, req.DNSRoutes["api.binance.com"].IPs)
}

func TestTimerRequestFromEnvAllowsUnicodeSubjectNames(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "crypto")
	t.Setenv("MOOX_MARKET_FETCH_PROVIDER", "binance")
	t.Setenv("MOOX_MARKET_FETCH_MARKET_TYPE", "spot")
	t.Setenv("MOOX_MARKET_FETCH_DATASET_ID", "bars")
	t.Setenv("MOOX_MARKET_FETCH_FREQUENCY", "1m")
	t.Setenv("MOOX_MARKET_FETCH_SUBJECTS", "币安人生-USDT")
	t.Setenv("MOOX_MARKET_FETCH_SYMBOLS_JSON", `{"币安人生-USDT":"BINANCELIFEUSDT"}`)
	t.Setenv("MOOX_STORAGE_RPC_GATEWAY_TARGET", "ip://10.0.0.1:11003")

	req, _, err := TimerRequestFromEnv("req", "node", time.Date(2026, 8, 4, 1, 2, 3, 0, time.UTC))
	require.NoError(t, err)
	require.Len(t, req.Items, 1)
	if len(req.Items) == 1 {
		require.Equal(t, "币安人生-USDT", req.Items[0].SubjectID)
		require.Equal(t, "BINANCELIFEUSDT", req.Items[0].Symbol)
	}
}

func TestTimerRequestFromEnvAllowsConfiguredStockGroupAboveThirty(t *testing.T) {
	subjects := make([]string, 0, 40)
	symbols := make(map[string]string, 40)
	for index := 0; index < 40; index++ {
		subject := fmt.Sprintf("%06d.XSHG", 600000+index)
		subjects = append(subjects, subject)
		symbols[subject] = fmt.Sprintf("sh%06d", 600000+index)
	}
	rawSymbols, err := json.Marshal(symbols)
	require.NoError(t, err)
	t.Setenv("MOOX_SPACE_ID", StockCNSpaceID)
	t.Setenv("MOOX_MARKET_FETCH_PROVIDER", "sina")
	t.Setenv("MOOX_MARKET_FETCH_MARKET_TYPE", "equity")
	t.Setenv("MOOX_MARKET_FETCH_DATASET_ID", StockCNDatasetID)
	t.Setenv("MOOX_MARKET_FETCH_FREQUENCY", "1m")
	t.Setenv("MOOX_MARKET_FETCH_SUBJECTS", joinSubjects(subjects))
	t.Setenv("MOOX_MARKET_FETCH_SYMBOLS_JSON", string(rawSymbols))
	t.Setenv("MOOX_MARKET_FETCH_GROUP_ID", "3")
	t.Setenv("MOOX_MARKET_FETCH_GROUP_COUNT", "200")
	t.Setenv("MOOX_STORAGE_RPC_GATEWAY_TARGET", "ip://10.0.0.1:11003")

	req, _, err := TimerRequestFromEnv("req", "stock-node", time.Date(2026, 8, 4, 1, 2, 3, 0, time.UTC))
	require.NoError(t, err)
	require.Len(t, req.Items, 40)
}

func TestTimerRequestFromEnvDerivesStrictStockSymbolsAndAppliesOverrides(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", StockCNSpaceID)
	t.Setenv("MOOX_MARKET_FETCH_PROVIDER", "sina")
	t.Setenv("MOOX_MARKET_FETCH_MARKET_TYPE", "equity")
	t.Setenv("MOOX_MARKET_FETCH_DATASET_ID", StockCNDatasetID)
	t.Setenv("MOOX_MARKET_FETCH_FREQUENCY", "1m")
	t.Setenv("MOOX_MARKET_FETCH_SUBJECTS", "600000.XSHG|000001.XSHE")
	t.Setenv("MOOX_MARKET_FETCH_SYMBOLS_JSON", `{"600000.XSHG":"sh600000"}`)
	t.Setenv("MOOX_MARKET_FETCH_GROUP_ID", "3")
	t.Setenv("MOOX_MARKET_FETCH_GROUP_COUNT", "200")
	t.Setenv("MOOX_STORAGE_RPC_GATEWAY_TARGET", "ip://10.0.0.1:11003")

	req, _, err := TimerRequestFromEnv("req", "stock-node", time.Date(2026, 8, 4, 1, 2, 3, 0, time.UTC))
	require.NoError(t, err)
	require.Len(t, req.Items, 2)
	require.Equal(t, "sz000001", req.Items[0].Symbol)
	require.Equal(t, "sh600000", req.Items[1].Symbol)
}

func TestTimerRequestFromEnvRejectsStrictStockSymbolMismatch(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", StockCNSpaceID)
	t.Setenv("MOOX_MARKET_FETCH_PROVIDER", "sina")
	t.Setenv("MOOX_MARKET_FETCH_MARKET_TYPE", "equity")
	t.Setenv("MOOX_MARKET_FETCH_DATASET_ID", StockCNDatasetID)
	t.Setenv("MOOX_MARKET_FETCH_FREQUENCY", "1m")
	t.Setenv("MOOX_MARKET_FETCH_SUBJECTS", "600000.XSHG")
	t.Setenv("MOOX_MARKET_FETCH_SYMBOLS_JSON", `{"600000.XSHG":"sh600001"}`)
	t.Setenv("MOOX_MARKET_FETCH_GROUP_ID", "3")
	t.Setenv("MOOX_MARKET_FETCH_GROUP_COUNT", "200")
	t.Setenv("MOOX_STORAGE_RPC_GATEWAY_TARGET", "ip://10.0.0.1:11003")

	_, _, err := TimerRequestFromEnv("req", "stock-node", time.Date(2026, 8, 4, 1, 2, 3, 0, time.UTC))
	require.ErrorContains(t, err, "conflicts with strict symbol")
}

func TestTimerRequestFromEnvRejectsInvalidStockGroupIdentity(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", StockCNSpaceID)
	t.Setenv("MOOX_MARKET_FETCH_PROVIDER", "sina")
	t.Setenv("MOOX_MARKET_FETCH_MARKET_TYPE", "equity")
	t.Setenv("MOOX_MARKET_FETCH_DATASET_ID", StockCNDatasetID)
	t.Setenv("MOOX_MARKET_FETCH_FREQUENCY", "1m")
	t.Setenv("MOOX_MARKET_FETCH_SUBJECTS", "600000.XSHG")
	t.Setenv("MOOX_MARKET_FETCH_GROUP_ID", "200")
	t.Setenv("MOOX_MARKET_FETCH_GROUP_COUNT", "200")

	_, _, err := TimerRequestFromEnv("req", "stock-node", time.Date(2026, 8, 4, 1, 2, 3, 0, time.UTC))
	require.ErrorContains(t, err, "outside [0,200)")
}

func joinSubjects(subjects []string) string {
	result := ""
	for index, subject := range subjects {
		if index > 0 {
			result += "|"
		}
		result += subject
	}
	return result
}

func TestTimerRequestFromEnvMalformedDNSFallsBackToPlatformResolver(t *testing.T) {
	t.Setenv("MOOX_SPACE_ID", "crypto")
	t.Setenv("MOOX_MARKET_FETCH_PROVIDER", "binance")
	t.Setenv("MOOX_MARKET_FETCH_MARKET_TYPE", "spot")
	t.Setenv("MOOX_MARKET_FETCH_DATASET_ID", "bars")
	t.Setenv("MOOX_MARKET_FETCH_FREQUENCY", "1m")
	t.Setenv("MOOX_MARKET_FETCH_SUBJECTS", "BTC-USDT")
	t.Setenv("MOOX_MARKET_FETCH_SYMBOLS_JSON", `{"BTC-USDT":"BTCUSDT"}`)
	t.Setenv("MOOX_MARKET_FETCH_DNS_ROUTES_JSON", "not-json")
	t.Setenv("MOOX_STORAGE_RPC_GATEWAY_TARGET", "ip://10.0.0.1:11003")
	req, _, err := TimerRequestFromEnv("req", "node", time.Date(2026, 8, 4, 1, 2, 3, 0, time.UTC))
	require.NoError(t, err)
	require.Empty(t, req.DNSRoutes)
}
