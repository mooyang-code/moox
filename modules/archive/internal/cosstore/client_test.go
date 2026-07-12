package cosstore

import (
	"context"
	"testing"
)

func TestObjectKey(t *testing.T) {
	key, err := ObjectKey("/archive", "moox/archive", "/archive/crypto_binance/BTCUSDT/spot_BTCUSDT_1m_202606.parquet")
	if err != nil || key != "moox/archive/crypto_binance/BTCUSDT/spot_BTCUSDT_1m_202606.parquet" {
		t.Fatalf("key=%q err=%v", key, err)
	}
}

func TestNew_RejectsMissingCredentials(t *testing.T) {
	_, err := New("ap-shanghai", "bucket", "/archive", "", "", "")
	if err == nil {
		t.Fatal("expected credentials error")
	}
}

func TestPut_RejectsNilClient(t *testing.T) {
	var c *Client
	err := c.Put(context.Background(), "key", "local")
	if err == nil {
		t.Fatal("expected nil client error")
	}
}
