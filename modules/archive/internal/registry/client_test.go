package registry

import (
	"testing"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
)

func TestArchiveFileUsesStableIdentity(t *testing.T) {
	key := domain.PartitionKey{SpaceID: "crypto_binance", DatasetID: "spot_kline", SubjectID: "BTC-USDT", Freq: "1m", Month: "202606"}
	manifest := domain.Manifest{Path: "/data/archive/crypto_binance/spot_kline/1m/BTC-USDT/crypto_binance__spot_kline__BTC-USDT__1m__202606.parquet", Generation: 7, SHA256: "hash", Size: 10, RowCount: 1, Columns: []string{"close"}}
	first := BuildArchiveFile("parquet-local", key, manifest, false, domain.COSState{})
	manifest.Generation++
	second := BuildArchiveFile("parquet-local", key, manifest, false, domain.COSState{})
	if first.GetArchiveFileId() != second.GetArchiveFileId() {
		t.Fatalf("stable id changed: %s != %s", first.GetArchiveFileId(), second.GetArchiveFileId())
	}
	if first.GetPartitionKey() != "1m/BTC-USDT/202606" || first.GetFileFormat() != "parquet" {
		t.Fatalf("unexpected ArchiveFile: %#v", first)
	}
}

func TestStableArchiveFileID(t *testing.T) {
	key := domain.PartitionKey{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1m", Month: "202601"}
	id := StableArchiveFileID(key)
	if id == "" {
		t.Fatal("stable archive file id is empty")
	}
}
