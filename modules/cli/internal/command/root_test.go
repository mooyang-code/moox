package command

import (
	"context"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"testing"
)

func TestGetVersionInfoDefaultsToDev(t *testing.T) {
	old := Version
	Version = ""
	t.Cleanup(func() { Version = old })
	assert.Equal(t, "moox CLI dev", GetVersionInfo())
	assert.Equal(t, "moox CLI dev", GetFullVersionInfo())
}

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
			Name: "Spot Kline", DataKind: "TIME_SERIES", Freqs: []string{"1m"},
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

func TestDefaultMetadataImportURL(t *testing.T) {
	t.Setenv("MOOX_METADATA_URL", "")
	assert.Equal(t, "http://127.0.0.1:20200", defaultMetadataImportURL(""))
	assert.Equal(t, "http://meta:20200", defaultMetadataImportURL("meta:20200"))
	t.Setenv("MOOX_METADATA_URL", "http://env-meta:20200")
	assert.Equal(t, "http://env-meta:20200", defaultMetadataImportURL(""))
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
	summary, err := runMetadataImport(context.Background(), "http://unused", nil, false)
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

func TestParseCollectorOverridesAndSetDefaultEnv(t *testing.T) {
	got := parseCollectorOverrides([]string{" A = 1 ", "bad", "B=2"})
	assert.Equal(t, map[string]string{"A": "1", "B": "2"}, got)
	env := map[string]string{}
	setDefaultEnv(env, "K", "v")
	setDefaultEnv(env, "EMPTY", " ")
	assert.Equal(t, "v", env["K"])
}

func TestCollectorFunctionEnvironmentOmitsEmptyCA(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_CA_FILE", "")
	t.Setenv("MOOX_GATEWAY_CA_PEM_B64", "")
	t.Setenv("TENCENTCLOUD_SECRET_ID", "test-cls-id")
	t.Setenv("TENCENTCLOUD_SECRET_KEY", "test-cls-key")
	env, err := collectorFunctionEnvironment(collectorPublishOptions{})
	require.NoError(t, err)
	assert.NotContains(t, env, "MOOX_GATEWAY_CA_FILE")
	assert.NotContains(t, env, "MOOX_GATEWAY_CA_PEM_B64")
}

func TestNewControlClientSetsServiceAuth(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_NODE_ID", "gateway-gz-122")
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "ak")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "sk")
	client := newControlClient("http://control", "", "", "", "crypto")
	require.NotNil(t, client.ServiceAuth)
	assert.Equal(t, "ak", client.ServiceAuth.AccessKey)
	assert.Equal(t, "gateway-gz-122", client.ServiceAuth.TargetNode)
}

func TestDeployCollectorFunctionValidatesRequiredFields(t *testing.T) {
	_, err := deployCollectorFunction(context.Background(), collectorDeployOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "control-url")
}

func TestRunStorageImportDryRunPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.csv")
	content := "meta\nignore\nopen_time,close\n2026-01-02T03:04:05Z,1.25\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	meta := fakeStorageImportMeta{
		dataset: &pb.Dataset{DatasetId: "kline", Freqs: []string{"1m"}, Status: "active"},
		subject: &pb.Subject{SubjectId: "BTC", Status: "active"},
		columns: []*pb.DatasetColumn{
			{ColumnName: "open_time", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_TIME, Required: true, Status: "active"},
			{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Required: true, Status: "active"},
		},
	}
	summary, err := runStorageImport(context.Background(), storageImportOptions{
		Format: "csv", File: path, MetadataURL: "http://meta", AccessURL: "http://access",
		SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1m", TimeColumn: "open_time",
		DryRun: true,
	}, meta, fakeStorageWriter{})
	require.NoError(t, err)
	assert.Equal(t, "dry_run", summary.Status)
	assert.Equal(t, 1, summary.ValidatedRows)
}

type fakeStorageImportMeta struct {
	dataset *pb.Dataset
	subject *pb.Subject
	columns []*pb.DatasetColumn
}

func (f fakeStorageImportMeta) GetDataset(context.Context, string, string) (*pb.Dataset, error) {
	return f.dataset, nil
}
func (f fakeStorageImportMeta) GetView(context.Context, string, string) (*pb.View, error) {
	return nil, nil
}
func (f fakeStorageImportMeta) GetSubject(context.Context, string, string) (*pb.Subject, error) {
	return f.subject, nil
}
func (f fakeStorageImportMeta) ListDatasetColumns(context.Context, string, string) ([]*pb.DatasetColumn, error) {
	return f.columns, nil
}
func (f fakeStorageImportMeta) ListDatasetSubjects(context.Context, string, string, string) ([]*pb.DatasetSubject, error) {
	return nil, nil
}
func (f fakeStorageImportMeta) BindDatasetSubject(context.Context, *pb.DatasetSubject) error {
	return nil
}

type fakeStorageWriter struct{}

func (fakeStorageWriter) MergeTimeSeriesRows(context.Context, *pb.MergeTimeSeriesRowsReq) error {
	return nil
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
