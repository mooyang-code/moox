package cosstore

import (
	"context"
	"testing"
)

func TestObjectKey(t *testing.T) {
	path := "/archive/crypto/dataset_spot_kline_1h/1h/BTC-USDT/series_tag=venue%3Abinance/crypto__spot_kline_1h__BTC-USDT__1h__series_tag=venue%3Abinance__202606.parquet"
	key, err := ObjectKey("/archive", "moox/archive", path)
	if err != nil || key != "moox/archive/crypto/dataset_spot_kline_1h/1h/BTC-USDT/series_tag=venue%3Abinance/crypto__spot_kline_1h__BTC-USDT__1h__series_tag=venue%3Abinance__202606.parquet" {
		t.Fatalf("key=%q err=%v", key, err)
	}
}

func TestObjectKeyRejectsMismatchedTagDirectory(t *testing.T) {
	path := "/archive/crypto/kline/1h/BTC/series_tag=venue%3Aokx/crypto__kline__BTC__1h__series_tag=venue%3Abinance__202606.parquet"
	if _, err := ObjectKey("/archive", "", path); err == nil {
		t.Fatal("expected tag mismatch rejection")
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
	err := c.Put(context.Background(), "key", "local", ObjectMetadata{})
	if err == nil {
		t.Fatal("expected nil client error")
	}
	_, err = c.Head(context.Background(), "key")
	if err == nil {
		t.Fatal("expected nil client HEAD error")
	}
}
