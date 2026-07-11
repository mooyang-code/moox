package cosstore

import "testing"

func TestObjectKey(t *testing.T) {
	key, err := ObjectKey("/archive", "moox/archive", "/archive/crypto_binance/BTCUSDT/spot_BTCUSDT_1m_202606.parquet")
	if err != nil || key != "moox/archive/crypto_binance/BTCUSDT/spot_BTCUSDT_1m_202606.parquet" {
		t.Fatalf("key=%q err=%v", key, err)
	}
}
