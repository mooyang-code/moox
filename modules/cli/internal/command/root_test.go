package command

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestGetFullVersionInfoDefaultsToDev(t *testing.T) {
	old := Version
	Version = ""
	t.Cleanup(func() { Version = old })
	assert.Equal(t, "moox CLI dev", GetFullVersionInfo())
}

func TestPadLineAndDisplayWidth(t *testing.T) {
	assert.Equal(t, 5, calculateDisplayWidth("hello"))
	padded := padLine("x", 5)
	assert.Equal(t, 5, calculateDisplayWidth(padded))
}

func TestCalculateDisplayWidthCountsWideChars(t *testing.T) {
	assert.Equal(t, 4, calculateDisplayWidth("测试"))
}

func TestBuildMetadataImportCallsFromSeed(t *testing.T) {
	seed := metadataSeed{
		Spaces: []seedSpace{{SpaceID: "crypto", Name: "Crypto", seedCommon: seedCommon{Status: "active"}}},
		DataSources: []seedDataSource{{
			SpaceID: "crypto", DataSourceID: "binance", Name: "Binance", Kind: "exchange",
			seedCommon: seedCommon{Status: "active"},
		}},
		Datasets: []seedDataset{{
			SpaceID: "crypto", DatasetID: "spot_kline", DataSourceID: "binance",
			Name: "Spot Kline", DataKind: "TIME_SERIES", DataNodeID: "storage-node-0", Freq: "1m",
		}},
	}
	calls, err := buildMetadataImportCalls(seed)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(calls), 3)
	assert.Equal(t, "spaces", calls[0].Resource)
	assert.Equal(t, "CreateSpace", calls[0].Method)
}

func TestLoadMetadataSeed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seed.yaml")
	content := "spaces:\n  - space_id: crypto\n    name: Crypto\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	seed, err := loadMetadataSeed(path)
	require.NoError(t, err)
	require.Len(t, seed.Spaces, 1)
	assert.Equal(t, "crypto", seed.Spaces[0].SpaceID)
}

func TestMetadataNotFound(t *testing.T) {
	assert.True(t, metadataNotFound(&pb.RetInfo{Code: pb.ErrorCode_DATASET_NOT_FOUND}))
	assert.True(t, metadataNotFound(&pb.RetInfo{Code: pb.ErrorCode_INVALID_PARAM, Msg: "dataset not found"}))
	assert.False(t, metadataNotFound(&pb.RetInfo{Code: pb.ErrorCode_SUCCESS}))
}

func TestCountMetadataCalls(t *testing.T) {
	calls := []metadataImportCall{{Resource: "spaces"}, {Resource: "spaces"}, {Resource: "datasets"}}
	got := countMetadataCalls(calls)
	assert.Equal(t, 2, got["spaces"])
	assert.Equal(t, 1, got["datasets"])
}

func TestMetadataContractsEqual(t *testing.T) {
	a := &pb.Space{SpaceId: "crypto", Name: "Crypto", Status: "active"}
	b := &pb.Space{SpaceId: "crypto", Name: "Crypto", Status: "active"}
	assert.True(t, metadataContractsEqual("spaces", a, b))
	b.Name = "Other"
	assert.False(t, metadataContractsEqual("spaces", a, b))
}

func TestApplyProbeResultForSpace(t *testing.T) {
	space := &pb.Space{SpaceId: "crypto", Name: "Crypto"}
	probe := &metadataExistsProbe{
		Response: &pb.GetSpaceRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Space: space},
	}
	ok, actual := applyProbeResult("spaces", probe, &pb.CreateSpaceReq{Space: space})
	assert.True(t, ok)
	assert.Equal(t, space, actual)
}

func TestVerifyMetadataResource(t *testing.T) {
	space := &pb.Space{SpaceId: "crypto", Name: "Crypto", Status: "active"}
	require.NoError(t, verifyMetadataResource("spaces", &pb.CreateSpaceReq{Space: space}, space))
	err := verifyMetadataResource("spaces", &pb.CreateSpaceReq{Space: &pb.Space{SpaceId: "crypto", Name: "X", Status: "active"}}, space)
	require.Error(t, err)
}

func TestSeedToPBHelpers(t *testing.T) {
	space := (seedSpace{SpaceID: "crypto", Name: "Crypto"}).toPB()
	assert.Equal(t, "crypto", space.GetSpaceId())
	assert.Equal(t, "active", space.GetStatus())
	dataset, err := (seedDataset{SpaceID: "crypto", DatasetID: "kline", DataKind: "TIME_SERIES"}).toPB()
	require.NoError(t, err)
	assert.Equal(t, pb.DataKind_DATA_KIND_TIME_SERIES, dataset.GetDataKind())
}

func TestRunMetadataImportWithNoCalls(t *testing.T) {
	summary, err := runMetadataImport(context.Background(), nil, nil, false)
	require.NoError(t, err)
	assert.Equal(t, 0, summary.Applied)
}

func TestBuildFirewallAddPreviewDryRun(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetOut(&bytesBuffer{})
	preview, err := buildFirewallAddPreview(lighthouseFirewallAddOptions{
		InstanceID: "lhins-1", Ports: "20201", Protocol: "TCP", Cidr: "0.0.0.0/0",
	})
	require.NoError(t, err)
	assert.Equal(t, "lhins-1", preview["instance_id"])
}

func TestFirstNonEmpty(t *testing.T) {
	assert.Equal(t, "a", firstNonEmpty("", " a ", "b"))
	assert.Equal(t, "", firstNonEmpty("", "  "))
}

type bytesBuffer struct{ b []byte }

func (b *bytesBuffer) Write(p []byte) (int, error) {
	b.b = append(b.b, p...)
	return len(p), nil
}

func TestMetadataSeedYAMLRoundTrip(t *testing.T) {
	seed := metadataSeed{Spaces: []seedSpace{{SpaceID: "crypto", Name: "Crypto"}}}
	raw, err := yaml.Marshal(seed)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "seed.yaml")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	loaded, err := loadMetadataSeed(path)
	require.NoError(t, err)
	assert.Equal(t, seed.Spaces[0].SpaceID, loaded.Spaces[0].SpaceID)
}
