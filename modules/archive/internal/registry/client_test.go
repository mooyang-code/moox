package registry

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

type registryProxy struct {
	storagepb.MetadataClientProxy
	generations []string
}

func (p *registryProxy) RegisterArchiveFile(_ context.Context, req *storagepb.RegisterArchiveFileReq, _ ...client.Option) (*storagepb.RegisterArchiveFileRsp, error) {
	p.generations = append(p.generations, req.GetArchiveFile().GetAttributes()["generation"])
	return &storagepb.RegisterArchiveFileRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func TestArchiveFileUsesStableIdentity(t *testing.T) {
	key := domain.PartitionKey{SpaceID: "crypto", DatasetID: "spot_kline_1h", SubjectID: "BTC-USDT", Freq: "1h", SeriesTag: "venue:binance", Month: "202606"}
	manifest := domain.Manifest{Path: "/data/archive/crypto/spot_kline_1h/1h/BTC-USDT/series_tag=venue%3Abinance/crypto__spot_kline_1h__BTC-USDT__1h__series_tag=venue%3Abinance__202606.parquet", Generation: 7, SHA256: "hash", Size: 10, RowCount: 1, Columns: []string{"close"}}
	first := BuildArchiveFile("parquet-local", key, manifest, false, domain.COSState{})
	manifest.Generation++
	second := BuildArchiveFile("parquet-local", key, manifest, false, domain.COSState{})
	if first.GetArchiveFileId() != second.GetArchiveFileId() {
		t.Fatalf("stable id changed: %s != %s", first.GetArchiveFileId(), second.GetArchiveFileId())
	}
	if first.GetPartitionKey() != "1h/BTC-USDT/series_tag=venue%3Abinance/202606" || first.GetFileFormat() != "parquet" || first.GetAttributes()["manifest"] == "" || first.GetAttributes()["schema_version"] != "2" {
		t.Fatalf("unexpected ArchiveFile: %#v", first)
	}
	foundSeriesTag := false
	for _, column := range first.GetColumns() {
		foundSeriesTag = foundSeriesTag || column == "series_tag"
	}
	if !foundSeriesTag {
		t.Fatalf("ArchiveFile columns omit series_tag: %v", first.GetColumns())
	}
}

func TestStableArchiveFileIDIncludesSeriesTag(t *testing.T) {
	key := domain.PartitionKey{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1m", SeriesTag: "venue:binance", Month: "202601"}
	other := key
	other.SeriesTag = "venue:okx"
	if StableArchiveFileID(key) == StableArchiveFileID(other) {
		t.Fatal("different series tags must have distinct ArchiveFile IDs")
	}
}

func TestStableArchiveFileID(t *testing.T) {
	key := domain.PartitionKey{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1m", Month: "202601"}
	id := StableArchiveFileID(key)
	if id == "" {
		t.Fatal("stable archive file id is empty")
	}
}

func TestClientRefusesRegistryGenerationRollback(t *testing.T) {
	proxy := &registryProxy{}
	c := &Client{proxy: proxy}
	key := domain.PartitionKey{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1m", Month: "202601"}
	manifest := domain.Manifest{Path: "/tmp/archive.parquet", Generation: 2, SHA256: "hash", RowCount: 1}
	require.NoError(t, c.Register(t.Context(), BuildArchiveFile("device", key, manifest, false, domain.COSState{})))
	manifest.Generation = 1
	require.ErrorContains(t, c.Register(t.Context(), BuildArchiveFile("device", key, manifest, false, domain.COSState{})), "backward")
	require.Equal(t, []string{"2"}, proxy.generations)
}
