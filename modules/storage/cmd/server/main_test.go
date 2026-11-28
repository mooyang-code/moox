package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/dataformat"
	"github.com/mooyang-code/moox/packages/storagepolicy"
	"github.com/prometheus/client_golang/prometheus"
	trpc "trpc.group/trpc-go/trpc-go"
)

type cleanupDatasetReader struct{}

func (cleanupDatasetReader) ListDatasets(context.Context, metadata.DatasetQuery) ([]*pb.Dataset, *pb.PageResult, error) {
	dataset := func(spaceID, datasetID, freq string) *pb.Dataset {
		return &pb.Dataset{SpaceId: spaceID, DatasetId: datasetID, DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: freq, Status: "active"}
	}
	return []*pb.Dataset{
		dataset("crypto", "prices_1m", "1m"),
		dataset("crypto", "prices_1d", "1d"),
		dataset("stockcn", "equity_1m", "1m"),
	}, &pb.PageResult{Page: 1, Size: 1000}, nil
}

// recordingCleaner 记录每个数据集的清理截止时间。
type recordingCleaner struct {
	cutoffs         map[string]time.Time
	changeLogCutoff time.Time
}

func (c *recordingCleaner) CleanupChangeLog(_ context.Context, cutoff time.Time) (int, error) {
	c.changeLogCutoff = cutoff
	return 0, nil
}

func (c *recordingCleaner) ChangeLogRetention() time.Duration { return 7 * 24 * time.Hour }

func (c *recordingCleaner) CleanupExpiredBuckets(_ context.Context, spaceID, datasetID string, before time.Time) (uint64, error) {
	c.cutoffs[spaceID+"/"+datasetID] = before
	return 3, nil
}

func TestDatasetCleanupUsesPolicyRetentionBySpaceAndFrequency(t *testing.T) {
	cleaner := &recordingCleaner{cutoffs: map[string]time.Time{}}
	metrics, err := observability.NewCleanupMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	retention := storagepolicy.Default().Retention
	retention.Spaces = map[string]map[string]string{"stockcn": {"1m": "60d"}}
	cleanup := datasetCleanup{reader: cleanupDatasetReader{}, store: cleaner, retention: retention, metrics: metrics}
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	if err := cleanup.run(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	want := map[string]time.Time{
		// crypto 1m keeps 35 days（默认），stockcn 1m 60 days（空间覆盖）; whole day buckets.
		"crypto/prices_1m":  time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
		"stockcn/equity_1m": time.Date(2026, 5, 21, 0, 0, 0, 0, time.UTC),
	}
	if len(cleaner.cutoffs) != len(want) {
		t.Fatalf("cleanup cutoffs = %v; a forever (1d) Dataset must not be cleaned", cleaner.cutoffs)
	}
	for key, cutoff := range want {
		if got := cleaner.cutoffs[key]; !got.Equal(cutoff) {
			t.Fatalf("%s cutoff = %s, want %s", key, got, cutoff)
		}
	}
	if want := now.Add(-7 * 24 * time.Hour); !cleaner.changeLogCutoff.Equal(want) {
		t.Fatalf("变更日志清理截止时间 = %s, want %s", cleaner.changeLogCutoff, want)
	}
}

func TestStorageEventBusConfigLoadsCredentialFromExplicitEnv(t *testing.T) {
	credentialFile := filepath.Join(t.TempDir(), "storage-eventbus.yaml")
	if err := os.WriteFile(credentialFile, []byte("version: 1\nusername: storage-eventbus\ntoken: storage-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOOX_STORAGE_EVENTBUS_CREDENTIAL_FILE", credentialFile)
	t.Setenv("MOOX_STORAGE_CONFIG", "")
	t.Setenv("MOOX_EVENTBUS_RECONNECT_BUFFER_BYTES", "")

	loaded, err := loadRuntimeStorageConfig()
	if err != nil {
		t.Fatal(err)
	}
	got, err := storageEventBusConfig(loaded, []string{"nats://127.0.0.1:4222"}, "storage-view")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.URLs) != 1 || got.URLs[0] != "nats://127.0.0.1:4222" {
		t.Fatalf("credential file replaced role URL: %v", got.URLs)
	}
	if got.Username != "storage-eventbus" || got.Password != "storage-secret" {
		t.Fatalf("credential config = username %q/password %q", got.Username, got.Password)
	}
	if got.ReconnectBufferBytes != storageEventBusReconnectBufferBytes {
		t.Fatalf("reconnect buffer = %d, want %d", got.ReconnectBufferBytes, storageEventBusReconnectBufferBytes)
	}
}

func TestStorageEventBusConfigHonorsExplicitReconnectBuffer(t *testing.T) {
	t.Setenv("MOOX_STORAGE_EVENTBUS_CREDENTIAL_FILE", "")
	t.Setenv("MOOX_STORAGE_CONFIG", "")
	t.Setenv("MOOX_EVENTBUS_RECONNECT_BUFFER_BYTES", "0")

	loaded, err := loadRuntimeStorageConfig()
	if err != nil {
		t.Fatal(err)
	}
	got, err := storageEventBusConfig(loaded, []string{"nats://127.0.0.1:4222"}, "storage-view")
	if err != nil {
		t.Fatal(err)
	}
	if got.ReconnectBufferBytes != 0 {
		t.Fatalf("reconnect buffer = %d, want explicit 0", got.ReconnectBufferBytes)
	}
}

func TestIsTransientStorageViewEventBusError(t *testing.T) {
	for _, test := range []struct {
		name string
		err  string
		want bool
	}{
		{name: "reconnecting", err: "nats: no servers available for connection", want: true},
		{name: "timeout", err: "tls handshake timeout", want: true},
		{name: "auth", err: "nats: authorization violation", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := isTransientStorageViewEventBusError(fmt.Errorf("%s", test.err)); got != test.want {
				t.Fatalf("transient=%t, want %t", got, test.want)
			}
		})
	}
}

func TestLoadStoragePolicyFromThePackageRoot(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "storage", "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "storage.yaml")
	t.Setenv("MOOX_STORAGE_CONFIG", configPath)

	if err := os.WriteFile(configPath, []byte("storage:\n  root: ./var/storage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadRuntimeStorageConfig()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.LoadPolicy(); err == nil || !strings.Contains(err.Error(), "storage.policy_file is required") {
		t.Fatalf("missing policy_file error = %v", err)
	}

	if err := os.WriteFile(configPath, []byte("storage:\n  policy_file: ../config/storage-policy.json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy := storagepolicy.Default()
	policy.Retention.Defaults = map[string]string{}
	for frequency, retention := range storagepolicy.Default().Retention.Defaults {
		policy.Retention.Defaults[frequency] = retention
	}
	policy.Retention.Defaults["1m"] = "14d"
	raw, err := policy.Encode()
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(root, "config", "storage-policy.json")
	if err := os.WriteFile(policyPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = loadRuntimeStorageConfig()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := cfg.LoadPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Retention.Defaults["1m"]; got != "336h" {
		t.Fatalf("loaded 1m retention = %q, want the file's 336h", got)
	}

	if err := os.WriteFile(policyPath, []byte(`{"retention":{"defaults":{"1m":"7d"}},"view":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.LoadPolicy(); err == nil {
		t.Fatal("an incomplete policy file was accepted")
	}
}

func TestLoadStoragePolicyDefaultsWithoutStorageConfig(t *testing.T) {
	t.Setenv("MOOX_STORAGE_CONFIG", "")
	cfg, err := loadRuntimeStorageConfig()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := cfg.LoadPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if policy.View.Bars != 10000 || policy.Retention.Defaults["1m"] != "840h" {
		t.Fatalf("default policy = %#v", policy)
	}
}

func TestStorageViewMaintenanceDisabled(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE", "yes"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("MOOX_STORAGE_VIEW_MAINTENANCE_DISABLED", value)
			if !storageViewMaintenanceDisabled() {
				t.Fatalf("value %q did not disable maintenance", value)
			}
		})
	}
	for _, value := range []string{"", "0", "false", "no"} {
		t.Run("enabled_"+value, func(t *testing.T) {
			t.Setenv("MOOX_STORAGE_VIEW_MAINTENANCE_DISABLED", value)
			if storageViewMaintenanceDisabled() {
				t.Fatalf("value %q unexpectedly disabled maintenance", value)
			}
		})
	}
}

func TestStorageEventBusConfigRejectsInvalidReconnectBuffer(t *testing.T) {
	t.Setenv("MOOX_STORAGE_EVENTBUS_CREDENTIAL_FILE", "")
	t.Setenv("MOOX_STORAGE_CONFIG", "")
	loaded, err := loadRuntimeStorageConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"garbage", "-1"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("MOOX_EVENTBUS_RECONNECT_BUFFER_BYTES", value)
			if _, err := storageEventBusConfig(loaded, []string{"nats://127.0.0.1:4222"}, "storage-view"); err == nil {
				t.Fatalf("reconnect buffer %q was accepted", value)
			}
		})
	}
}

func TestStorageEventBusConfigLoadsCredentialFromStorageConfig(t *testing.T) {
	dir := t.TempDir()
	credentialFile := filepath.Join(dir, "storage-eventbus.yaml")
	if err := os.WriteFile(credentialFile, []byte("version: 1\nusername: storage-eventbus\ntoken: storage-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(dir, "storage.yaml")
	if err := os.WriteFile(configFile, []byte("storage:\n  eventbus:\n    credential_file: "+credentialFile+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOOX_STORAGE_EVENTBUS_CREDENTIAL_FILE", "")
	t.Setenv("MOOX_STORAGE_CONFIG", configFile)

	loaded, err := loadRuntimeStorageConfig()
	if err != nil {
		t.Fatal(err)
	}
	got, err := storageEventBusConfig(loaded, []string{"nats://127.0.0.1:4222"}, "storage-primary")
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "storage-eventbus" || got.Password != "storage-secret" {
		t.Fatalf("credential config = username %q/password %q", got.Username, got.Password)
	}
}

func TestStorageViewDeleteOldestBatchSizeDefaultsAndBounds(t *testing.T) {
	t.Setenv("MOOX_STORAGE_CONFIG", "")
	loaded, err := loadRuntimeStorageConfig()
	if err != nil {
		t.Fatal(err)
	}
	if vs, err := loaded.ViewSettings(); err != nil || vs.DeleteOldestBatchSize != 2000 {
		t.Fatalf("没有配置文件时应取默认 2000，实际 %+v，错误 %v", vs, err)
	}
	for _, c := range []struct {
		yaml    string
		want    int
		wantErr bool
	}{
		{yaml: "storage:\n  view:\n    backfill_page_size: 500\n", want: 2000},
		{yaml: "storage:\n  view:\n    delete_oldest_batch_size: 500\n", want: 500},
		{yaml: "storage:\n  view:\n    delete_oldest_batch_size: 20001\n", wantErr: true},
		{yaml: "storage:\n  view:\n    delete_oldest_batch_size: -1\n", wantErr: true},
	} {
		path := filepath.Join(t.TempDir(), "storage.yaml")
		if err := os.WriteFile(path, []byte(c.yaml), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("MOOX_STORAGE_CONFIG", path)
		loaded, err := loadRuntimeStorageConfig()
		if err != nil {
			t.Fatal(err)
		}
		vs, err := loaded.ViewSettings()
		if c.wantErr {
			if err == nil {
				t.Fatalf("%q 应报越界错误，实际取到 %d", c.yaml, vs.DeleteOldestBatchSize)
			}
			continue
		}
		if err != nil || vs.DeleteOldestBatchSize != c.want {
			t.Fatalf("%q 应取到 %d，实际 %d，错误 %v", c.yaml, c.want, vs.DeleteOldestBatchSize, err)
		}
	}
}

// 验证 nativewire 将 tRPC 帧上限抬升至不低于 servicecatalog/catalog.yaml 声明的 32 MiB max_body_bytes。
func TestTRPCMaxFrameSizeCoversCatalogMaxBodyBytes(t *testing.T) {
	const catalogMaxBodyBytes = 33554432 // 32 MiB
	if trpc.DefaultMaxFrameSize < catalogMaxBodyBytes {
		t.Fatalf("trpc.DefaultMaxFrameSize=%d 小于 catalog.yaml 声明的 max_body_bytes=%d", trpc.DefaultMaxFrameSize, catalogMaxBodyBytes)
	}
}

func TestRecordStorageViewFormatVerifiesExistingData(t *testing.T) {
	dir := t.TempDir()
	indexRoot := filepath.Join(dir, "view_index")
	if err := recordStorageViewFormat(dir, indexRoot, false); err != nil {
		t.Fatalf("首次写入 storage-view 格式文件失败：%v", err)
	}
	stale, err := dataformat.Build("storage-view", map[string]string{
		dataformat.PartStorageViewIndex: strconv.Itoa(dataformat.StorageViewIndexVersion + 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := dataformat.WriteDir(dir, stale); err != nil {
		t.Fatal(err)
	}
	// 已有数据时格式不同必须拒绝启动。
	err = recordStorageViewFormat(dir, indexRoot, true)
	if !errors.Is(err, dataformat.ErrIncompatibleFormat) || !strings.Contains(err.Error(), "setup install --wipe") {
		t.Fatalf("期望格式不符拒启，实际 err = %v", err)
	}
	// 尚无数据时照常覆盖写入。
	if err := recordStorageViewFormat(dir, indexRoot, false); err != nil {
		t.Fatalf("尚无数据时应照常写入：%v", err)
	}
}
