package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateSCFFetcherRejectsUnusableFleetAndUnsafeConcurrency(t *testing.T) {
	base := func() SCFFetcher {
		return SCFFetcher{
			Enabled: true,
			CloudAccount: SCFFetcherCloudAccount{
				AccountID: "tencent-scf", AccountName: "Tencent SCF", CredentialSecretID: "tencent-default",
				AppID: "1255382561", COSRegion: "ap-guangzhou", COSBucket: "moox-scf-guangzhou-1255382561",
			},
			Spaces: []SCFFetcherSpace{{
				SpaceID: "crypto", MemorySize: 64, TimeoutSeconds: 15,
				StorageRPCGatewayTarget: "ip://106.53.107.122:11003",
				MaxInflightRequests:     32, RequestTimeoutMS: 1500,
				HTTPMaxAttempts: 4, StorageMaxAttempts: 3,
				Regions: []SCFFetcherRegion{{Region: "ap-guangzhou", Enabled: true, FunctionCount: 1}},
			}},
		}
	}

	t.Run("requires an enabled region", func(t *testing.T) {
		cfg := base()
		cfg.Spaces[0].Regions[0].Enabled = false
		err := validateSCFFetcher(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "at least one enabled region")
	})

	t.Run("caps invocation concurrency at the executor bound", func(t *testing.T) {
		cfg := base()
		cfg.Spaces[0].MaxInflightRequests = 65
		err := validateSCFFetcher(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "between 1 and 64")
	})

	t.Run("caps aggregate Storage retries at three attempts", func(t *testing.T) {
		cfg := base()
		cfg.Spaces[0].StorageMaxAttempts = 4
		err := validateSCFFetcher(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "request/storage attempts")
	})

	t.Run("allows a 30-item realtime batch for fleet fanout", func(t *testing.T) {
		cfg := base()
		cfg.Spaces[0].RealtimeBatchSize = 30
		cfg.Spaces[0].Regions[0].CloudAccountID = "tencent-scf-guangzhou"
		require.NoError(t, validateSCFFetcher(&cfg))
	})

	t.Run("accepts the standard 10-item timer and invoke budget", func(t *testing.T) {
		cfg := base()
		cfg.Spaces[0].RealtimeBatchSize = 10
		cfg.Spaces[0].MaxInflightRequests = 10
		cfg.Spaces[0].RequestTimeoutMS = 2000
		cfg.Spaces[0].Regions[0].CloudAccountID = "tencent-scf-guangzhou"
		require.NoError(t, validateSCFFetcher(&cfg))
	})

	t.Run("rejects a realtime batch above the SCF request bound", func(t *testing.T) {
		cfg := base()
		cfg.Spaces[0].RealtimeBatchSize = 31
		cfg.Spaces[0].Regions[0].CloudAccountID = "tencent-scf-guangzhou"
		err := validateSCFFetcher(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "between 1 and 30")
	})

	t.Run("rejects a batch that cannot finish before the SCF deadline", func(t *testing.T) {
		cfg := base()
		cfg.Spaces[0].RealtimeBatchSize = 30
		cfg.Spaces[0].MaxInflightRequests = 1
		cfg.Spaces[0].RequestTimeoutMS = 7000
		cfg.Spaces[0].Regions[0].CloudAccountID = "tencent-scf-guangzhou"
		err := validateSCFFetcher(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "request waves")
	})

	t.Run("reserves the CLS flush window", func(t *testing.T) {
		cfg := base()
		cfg.Spaces[0].RealtimeBatchSize = 30
		cfg.Spaces[0].MaxInflightRequests = 32
		cfg.Spaces[0].RequestTimeoutMS = 5000
		cfg.Spaces[0].Regions[0].CloudAccountID = "tencent-scf-guangzhou"
		err := validateSCFFetcher(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "reserves")
	})
}

func TestValidateSCFFetcherRequiresMarketDataSourceIdentity(t *testing.T) {
	cfg := SCFFetcher{Enabled: true, CloudAccount: SCFFetcherCloudAccount{
		AccountID: "tencent-scf", AccountName: "Tencent SCF", CredentialSecretID: "tencent-default",
		AppID: "1255382561", COSRegion: "ap-guangzhou", COSBucket: "moox-scf-guangzhou-1255382561",
	}, Spaces: []SCFFetcherSpace{{
		SpaceID: "stockcn", Entrypoint: "market_data", TimerFunctionCount: 1, MeasuredSafeGroupSize: 40, StorageRPCGatewayTarget: "ip://106.53.107.122:11003",
		MemorySize: 64, TimeoutSeconds: 15, RealtimeBatchSize: 1, MaxInflightRequests: 1, RequestTimeoutMS: 1000, HTTPMaxAttempts: 4, StorageMaxAttempts: 1,
		Regions: []SCFFetcherRegion{{Region: "ap-guangzhou", Enabled: true, FunctionCount: 1, CloudAccountID: "tencent-scf-guangzhou"}},
	}}}
	err := validateSCFFetcher(&cfg)
	require.Error(t, err)
	cfg.Spaces[0].MarketID = "stockcn"
	cfg.Spaces[0].InstrumentType = "equity"
	cfg.Spaces[0].ProviderID = "eastmoney"
	cfg.Spaces[0].SourceID = "stockcn_http"
	require.NoError(t, validateSCFFetcher(&cfg))
}

func TestValidateSCFFetcherRequiresTDXEndpointConfiguration(t *testing.T) {
	cfg := SCFFetcher{Enabled: true, CloudAccount: SCFFetcherCloudAccount{
		AccountID: "tencent-scf", AccountName: "Tencent SCF", CredentialSecretID: "tencent-default",
		AppID: "1255382561", COSRegion: "ap-guangzhou", COSBucket: "moox-scf-guangzhou-1255382561",
	}, Spaces: []SCFFetcherSpace{{
		SpaceID: "stockcn", Entrypoint: "market_data", TimerFunctionCount: 1, MeasuredSafeGroupSize: 40, MarketID: "stockcn", InstrumentType: "equity",
		ProviderID: "tdx", SourceID: "normal_7709", StorageRPCGatewayTarget: "ip://106.53.107.122:11003",
		MemorySize: 64, TimeoutSeconds: 15, RealtimeBatchSize: 1, MaxInflightRequests: 1, RequestTimeoutMS: 1000, HTTPMaxAttempts: 4, StorageMaxAttempts: 1,
		Regions: []SCFFetcherRegion{{Region: "ap-guangzhou", Enabled: true, FunctionCount: 1, CloudAccountID: "tencent-scf-guangzhou"}},
	}}}
	err := validateSCFFetcher(&cfg)
	require.ErrorContains(t, err, "tdx_host")
	cfg.Spaces[0].TDXHost = "quotes.example"
	require.NoError(t, validateSCFFetcher(&cfg))
	assert.Equal(t, 7709, cfg.Spaces[0].TDXPort)
}

func TestValidateSCFFetcherCopiesOneCloudAccountToEveryRegion(t *testing.T) {
	cfg := SCFFetcher{
		Enabled: true,
		CloudAccount: SCFFetcherCloudAccount{
			AccountID: "tencent-scf", AccountName: "Tencent SCF", CredentialSecretID: "tencent-default",
			AppID: "1255382561", COSRegion: "ap-guangzhou", COSBucket: "moox-scf-guangzhou-1255382561",
		},
		Spaces: []SCFFetcherSpace{{
			SpaceID: "crypto", StorageRPCGatewayTarget: "ip://106.53.107.122:11003",
			MemorySize: 64, TimeoutSeconds: 15, RealtimeBatchSize: 1, MaxInflightRequests: 1, RequestTimeoutMS: 1000,
			HTTPMaxAttempts: 4, StorageMaxAttempts: 1,
			Regions: []SCFFetcherRegion{
				{Region: "ap-guangzhou", Enabled: true, FunctionCount: 1},
				{Region: "ap-shanghai", Enabled: true, FunctionCount: 1},
			},
		}},
	}
	require.NoError(t, validateSCFFetcher(&cfg))
	for _, region := range cfg.Spaces[0].Regions {
		assert.Equal(t, "tencent-scf", region.CloudAccountID)
		assert.Equal(t, "ap-guangzhou", region.COSRegion)
		assert.Equal(t, "moox-scf-guangzhou-1255382561", region.COSBucket)
	}
}

func TestResolveSCFTimerFunctionCountsUsesSpaceDefaults(t *testing.T) {
	crypto := SCFFetcherSpace{
		SpaceID: "crypto",
		Regions: []SCFFetcherRegion{
			{Region: "ap-guangzhou", Enabled: true, FunctionCount: 0, CloudAccountID: "gz"},
			{Region: "ap-shanghai", Enabled: true, FunctionCount: 0, CloudAccountID: "sh"},
		},
	}
	require.NoError(t, resolveSCFTimerFunctionCounts(&crypto, "scf_fetcher.spaces[0]"))
	assert.Equal(t, DefaultCryptoMarketTimerFunctionCount, crypto.TimerFunctionCount)
	assert.Equal(t, 30, crypto.Regions[0].FunctionCount)
	assert.Equal(t, 30, crypto.Regions[1].FunctionCount)

	stock := SCFFetcherSpace{
		SpaceID: "stockcn", TimerFunctionCount: 170, MeasuredSafeGroupSize: 40,
		Regions: []SCFFetcherRegion{
			{Region: "ap-guangzhou", Enabled: true, CloudAccountID: "gz"},
			{Region: "ap-shanghai", Enabled: true, CloudAccountID: "sh"},
			{Region: "ap-beijing", Enabled: true, CloudAccountID: "bj"},
			{Region: "ap-chengdu", Enabled: true, CloudAccountID: "cd"},
		},
	}
	require.NoError(t, resolveSCFTimerFunctionCounts(&stock, "scf_fetcher.spaces[1]"))
	assert.Equal(t, DefaultStockCNMarketTimerFunctionCount, stock.TimerFunctionCount)
	assert.Equal(t, DefaultStockCNStaggerStartSecond, stock.StaggerStartSecond)
	assert.Equal(t, DefaultStockCNStaggerWindowSeconds, stock.StaggerWindowSeconds)
	assert.Equal(t, DefaultStockCNStaggerMaxStartsPerSecond, stock.StaggerMaxStartsPerSecond)
	total := 0
	for _, region := range stock.Regions {
		assert.Contains(t, []int{42, 43}, region.FunctionCount)
		total += region.FunctionCount
	}
	assert.Equal(t, DefaultStockCNMarketTimerFunctionCount, total)
}

func TestResolveSCFTimerFunctionCountsRequiresExplicitStockN(t *testing.T) {
	stock := SCFFetcherSpace{
		SpaceID: "stockcn",
		Regions: []SCFFetcherRegion{{Region: "ap-guangzhou", Enabled: true, FunctionCount: 7, CloudAccountID: "gz"}},
	}
	err := resolveSCFTimerFunctionCounts(&stock, "scf_fetcher.spaces[0]")
	require.ErrorContains(t, err, "timer_function_count must be an explicit positive value")

	stock.TimerFunctionCount = 7
	require.NoError(t, resolveSCFTimerFunctionCounts(&stock, "scf_fetcher.spaces[0]"))
	assert.Equal(t, 7, stock.TimerFunctionCount)
}

func TestValidateSCFFetcherRequiresMeasuredSafeGroupSizeForStock(t *testing.T) {
	base := SCFFetcherSpace{
		SpaceID: "stockcn", TimerFunctionCount: 200, MemorySize: 64, TimeoutSeconds: 15,
		MeasuredSafeGroupSize: 30, StorageRPCGatewayTarget: "ip://106.53.107.122:11003",
		RealtimeBatchSize: 10, MaxInflightRequests: 10, RequestTimeoutMS: 2000,
		HTTPMaxAttempts: 4, StorageMaxAttempts: 1,
		Regions: []SCFFetcherRegion{{Region: "ap-guangzhou", Enabled: true, FunctionCount: 50, CloudAccountID: "gz"}, {Region: "ap-shanghai", Enabled: true, FunctionCount: 50, CloudAccountID: "sh"}, {Region: "ap-beijing", Enabled: true, FunctionCount: 50, CloudAccountID: "bj"}, {Region: "ap-chengdu", Enabled: true, FunctionCount: 50, CloudAccountID: "cd"}},
	}
	require.NoError(t, validateSCFFetcherSpace(&base, "scf_fetcher.spaces[0]"))

	base.MeasuredSafeGroupSize = 0
	require.ErrorContains(t, validateSCFFetcherSpace(&base, "scf_fetcher.spaces[0]"), "measured_safe_group_size must be a positive value")
	base.MeasuredSafeGroupSize = -1
	require.ErrorContains(t, validateSCFFetcherSpace(&base, "scf_fetcher.spaces[0]"), "measured_safe_group_size must be a positive value")
}

func TestValidateSCFFetcherRejectsUnsafeStockStaggerRate(t *testing.T) {
	base := SCFFetcherSpace{SpaceID: "stockcn", TimerFunctionCount: 200, MemorySize: 64, TimeoutSeconds: 15,
		MeasuredSafeGroupSize: 30, StaggerStartSecond: 5, StaggerWindowSeconds: 35, StaggerMaxStartsPerSecond: 5,
		StorageRPCGatewayTarget: "ip://106.53.107.122:11003", RealtimeBatchSize: 10, RealtimeBarLimit: 10,
		CatchupBatchSize: 1, CatchupBarLimit: 1000, MaxInflightRequests: 10, RequestTimeoutMS: 2000,
		HTTPMaxAttempts: 4, StorageMaxAttempts: 1, StorageTimeoutMS: 5000, MaxRetryAttempts: 3,
		Regions: []SCFFetcherRegion{{Region: "ap-guangzhou", Enabled: true, FunctionCount: 200, CloudAccountID: "gz"}}}
	err := validateSCFFetcherSpace(&base, "stockcn")
	require.ErrorContains(t, err, "requires up to 6 starts per second")
}

func TestResolveSCFTimerFunctionCountsRejectsMismatch(t *testing.T) {
	cfg := SCFFetcherSpace{
		SpaceID:            "crypto",
		TimerFunctionCount: 60,
		Regions:            []SCFFetcherRegion{{Region: "ap-guangzhou", Enabled: true, FunctionCount: 20, CloudAccountID: "gz"}},
	}
	err := resolveSCFTimerFunctionCounts(&cfg, "scf_fetcher.spaces[0]")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "regional function_count total")
}

func TestCustomExampleDefinesValidStockCN170FunctionFleet(t *testing.T) {
	var manifest Manifest
	_, err := toml.DecodeFile(filepath.Join("..", "..", "..", "..", "..", "moox.toml.example"), &manifest)
	require.NoError(t, err)
	manifest.SCFFetcher.Enabled = true
	require.NoError(t, validateSCFFetcher(&manifest.SCFFetcher))

	for _, space := range manifest.SCFFetcher.Spaces {
		if space.SpaceID != "stockcn" {
			continue
		}
		assert.Equal(t, DefaultStockCNMarketTimerFunctionCount, space.TimerFunctionCount)
		assert.Equal(t, DefaultStockCNInstrumentInvokeTimeoutSeconds, space.InstrumentInvokeTimeoutSeconds)
		require.Len(t, space.Regions, 4)
		for _, region := range space.Regions {
			assert.True(t, region.Enabled)
			assert.Contains(t, []int{42, 43}, region.FunctionCount)
		}
		return
	}
	t.Fatal("stockcn scf_fetcher config is missing")
}

func TestValidateSCFFetcherDefaultsAndBoundsStockCNInstrumentInvokeTimeout(t *testing.T) {
	base := SCFFetcherSpace{
		SpaceID: "stockcn", MemorySize: 64, TimeoutSeconds: 15,
		TimerFunctionCount:      200,
		MeasuredSafeGroupSize:   30,
		StorageRPCGatewayTarget: "ip://106.53.107.122:11003",
		RealtimeBatchSize:       10, MaxInflightRequests: 10, RequestTimeoutMS: 2000,
		HTTPMaxAttempts: 4, StorageMaxAttempts: 1,
		Regions: []SCFFetcherRegion{
			{Region: "ap-guangzhou", Enabled: true, FunctionCount: 50, CloudAccountID: "gz"},
			{Region: "ap-shanghai", Enabled: true, FunctionCount: 50, CloudAccountID: "sh"},
			{Region: "ap-beijing", Enabled: true, FunctionCount: 50, CloudAccountID: "bj"},
			{Region: "ap-chengdu", Enabled: true, FunctionCount: 50, CloudAccountID: "cd"},
		},
	}
	require.NoError(t, validateSCFFetcherSpace(&base, "scf_fetcher.spaces[0]"))
	assert.Equal(t, 15, base.TimeoutSeconds)
	assert.Equal(t, DefaultStockCNInstrumentInvokeTimeoutSeconds, base.InstrumentInvokeTimeoutSeconds)

	base.InstrumentInvokeTimeoutSeconds = 59
	err := validateSCFFetcherSpace(&base, "scf_fetcher.spaces[0]")
	require.ErrorContains(t, err, "instrument_invoke_timeout_seconds")
}

func TestValidateSCFFetcherDefaultsIndependentStockInstrumentTimer(t *testing.T) {
	base := SCFFetcherSpace{
		SpaceID: "stockcn", MemorySize: 64, TimeoutSeconds: 15,
		TimerFunctionCount: 200, MeasuredSafeGroupSize: 30,
		StorageRPCGatewayTarget: "ip://106.53.107.122:11003",
		RealtimeBatchSize:       10, MaxInflightRequests: 10, RequestTimeoutMS: 2000,
		HTTPMaxAttempts: 4, StorageMaxAttempts: 1, StorageTimeoutMS: 5000,
		Regions: []SCFFetcherRegion{
			{Region: "ap-shanghai", Enabled: true, FunctionCount: 50, CloudAccountID: "tencent-scf-shanghai"},
			{Region: "ap-guangzhou", Enabled: true, FunctionCount: 50, CloudAccountID: "tencent-scf-guangzhou"},
			{Region: "ap-beijing", Enabled: true, FunctionCount: 50, CloudAccountID: "tencent-scf-beijing"},
			{Region: "ap-chengdu", Enabled: true, FunctionCount: 50, CloudAccountID: "tencent-scf-chengdu"},
		},
	}

	require.NoError(t, validateSCFFetcherSpace(&base, "scf_fetcher.spaces[0]"))
	assert.Equal(t, "ap-shanghai", base.InstrumentSnapshotRegion)
	assert.Equal(t, "tencent-scf-shanghai", base.InstrumentSnapshotCloudAccountID)
	assert.Equal(t, "moox-fetcher-stockcn-instrument", base.InstrumentSnapshotFunctionPrefix)
	assert.Equal(t, "0 0 0 * * * *", base.InstrumentSnapshotTimerCron)
	assert.Equal(t, 300, base.InstrumentSnapshotTimeoutSeconds)
	assert.Equal(t, DefaultStockCNInstrumentSnapshotMemorySize, base.InstrumentSnapshotMemorySize)
}

const validManifest = `[admin]
username = "admin"
password = "admin-password"

[tencent_cloud]
secret_id = "secret-id"
secret_key = "secret-key"

[eventbus]
public_address = "eventbus.example.test"
port = 4222
tls_enabled = true

[notification]
channel_type = "wecom"
webhook_url = ""

[control_host]
name = "control"
address = "192.0.2.10"
port = 22
username = "ubuntu"
password = "control-password"
`

func writeManifest(t *testing.T, root, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(root, "moox.toml")
	require.NoError(t, os.WriteFile(path, []byte(body), mode))
	require.NoError(t, os.Chmod(path, mode))
	return path
}

func TestLoadValidManifest(t *testing.T) {
	root := t.TempDir()
	path := writeManifest(t, root, validManifest, 0o600)

	snapshot, err := Load(path, root)
	require.NoError(t, err)
	assert.Equal(t, "admin", snapshot.Manifest.Admin.Username)
	assert.Equal(t, "eventbus.example.test", snapshot.Manifest.EventBus.PublicAddress)
	assert.Equal(t, 4222, snapshot.Manifest.EventBus.Port)
	assert.True(t, snapshot.Manifest.EventBus.TLSEnabled)
	assert.Equal(t, "ap-guangzhou", snapshot.Manifest.TencentCloud.Region)
	assert.Equal(t, DefaultDeployRoot, snapshot.Manifest.Paths.DeployRoot)
	assert.Equal(t, DefaultControlRoot, snapshot.Manifest.Paths.ControlRoot)
	assert.Equal(t, DefaultStorageRoot, snapshot.Manifest.Paths.StorageRoot)
	assert.Equal(t, uint64(1000), snapshot.Manifest.StorageView.RebuildLookbackPeriods)
	assert.Equal(t, 50, snapshot.Manifest.LocalLogs.MaxSizeMB)
	assert.Equal(t, 5, snapshot.Manifest.LocalLogs.BackupCount)
	assert.Equal(t, 22, snapshot.Manifest.ControlHost.Port)
	assert.Empty(t, snapshot.Manifest.Notification.WebhookURL)
	assert.Empty(t, snapshot.Manifest.OtherHosts)
	require.NoError(t, snapshot.VerifyUnchanged())
}

func TestLoadRoleSpecificStorageAndViewHosts(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `

[storage_host]
name = "storage"
address = "192.0.2.20"
port = 22
username = "ubuntu"
password = "storage-password"

[view_host]
name = "view"
address = "192.0.2.20"
port = 22
username = "ubuntu"
password = "view-password"
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	require.Equal(t, "storage", snapshot.Manifest.StorageHost.Name)
	require.Equal(t, "view", snapshot.Manifest.ViewHost.Name)
	require.Equal(t, snapshot.Manifest.StorageHost.Address, snapshot.Manifest.ViewHost.Address)
}

func TestValidateRejectsStorageRootOverlapWithControl(t *testing.T) {
	paths := &Paths{
		DeployRoot:  "/data/moox",
		ControlRoot: "/data/moox/prod",
		StorageRoot: "/data/moox/prod/storage",
	}
	require.ErrorContains(t, validatePaths(paths), "must not overlap")
	paths.StorageRoot = "/data/moox"
	require.ErrorContains(t, validatePaths(paths), "must not overlap")
	paths.StorageRoot = "/data/moox/storage"
	require.NoError(t, validatePaths(paths))
}

func TestLoadLocalLogRotationFromManifest(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[local_logs]
max_size_mb = 128
backup_count = 7
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.Equal(t, 128, snapshot.Manifest.LocalLogs.MaxSizeMB)
	assert.Equal(t, 7, snapshot.Manifest.LocalLogs.BackupCount)
}

func TestLoadRejectsInvalidLocalLogRotation(t *testing.T) {
	for name, body := range map[string]string{
		"max size":     "[local_logs]\nmax_size_mb = 0\nbackup_count = 5\n",
		"backup count": "[local_logs]\nmax_size_mb = 50\nbackup_count = 0\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			_, err := Load(writeManifest(t, root, validManifest+"\n"+body, 0o600), root)
			require.Error(t, err)
			require.Contains(t, err.Error(), "local_logs")
		})
	}
}

func TestLoadPathsFromManifest(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[paths]
deploy_root = "/data/custom"
control_root = "/data/custom/control"
storage_root = "/data/custom/storage"
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.Equal(t, Paths{DeployRoot: "/data/custom", ControlRoot: "/data/custom/control", StorageRoot: "/data/custom/storage"}, snapshot.Manifest.Paths)
}

func TestLoadAllowsStorageRootOnSeparateHostMount(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[paths]
deploy_root = "/data/moox"
control_root = "/data/moox/prod"
storage_root = "/home/ubuntu/moox/storage"
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.Equal(t, "/home/ubuntu/moox/storage", snapshot.Manifest.Paths.StorageRoot)
}

func TestLoadRejectsPathsOutsideDeployRoot(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[paths]
deploy_root = "/data/moox"
control_root = "/var/lib/moox"
storage_root = "/data/moox/storage"
`
	_, err := Load(writeManifest(t, root, body, 0o600), root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "paths.control_root")
}

func TestLoadStorageViewRebuildLookbackPeriodsFromManifest(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[storage_view]
rebuild_lookback_periods = 777
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.Equal(t, uint64(777), snapshot.Manifest.StorageView.RebuildLookbackPeriods)
}

func TestLoadRejectsSystemMonitorIdentityOverride(t *testing.T) {
	body := validManifest + `
[storage_view.system_monitor]
space_id = "mooxsys"
view_id = "view_mooxsys_host_disk"
`
	root := t.TempDir()
	_, err := Load(writeManifest(t, root, body, 0o600), root)
	if err == nil || !strings.Contains(err.Error(), "system_monitor must not set space_id or view_id") {
		t.Fatalf("expected system monitor identity validation error, got %v", err)
	}
}

func TestLoadRejectsInvalidStorageViewRebuildLookbackPeriods(t *testing.T) {
	for _, value := range []string{"0", "1000001"} {
		t.Run(value, func(t *testing.T) {
			root := t.TempDir()
			body := validManifest + fmt.Sprintf("\n[storage_view]\nrebuild_lookback_periods = %s\n", value)
			_, err := Load(writeManifest(t, root, body, 0o600), root)
			require.Error(t, err)
			require.Contains(t, err.Error(), "storage_view.rebuild_lookback_periods")
		})
	}
}

func TestLoadFactorSetupDefaultsAndItems(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[factors]
enabled = true
source_dir = "./examples/factors"

[[factors.items]]
factor_id = "bias"
file = "timeseries/bias.py"
input_columns = ["close"]
outputs = ["bias_5"]
params_json = '{"windows":[5]}'
lookback_periods = 5
space_id = "crypto"
source_view_id = "view_crypto_spot_kline_1m"
freq = "1m"
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.True(t, snapshot.Manifest.Factors.Enabled)
	assert.Equal(t, "examples/factors", snapshot.Manifest.Factors.SourceDir)
	require.Len(t, snapshot.Manifest.Factors.Items, 1)
	assert.Equal(t, "bias", snapshot.Manifest.Factors.Items[0].FactorID)
	assert.Equal(t, "all", snapshot.Manifest.Factors.Items[0].SubjectMode)
	assert.Equal(t, "enabled", snapshot.Manifest.Factors.Items[0].Status)
}

func TestLoadDefaultsDisableFactors(t *testing.T) {
	root := t.TempDir()
	snapshot, err := Load(writeManifest(t, root, validManifest, 0o600), root)
	require.NoError(t, err)
	assert.False(t, snapshot.Manifest.Factors.Enabled)
}

func TestLoadNotificationWebhook(t *testing.T) {
	root := t.TempDir()
	body := strings.Replace(validManifest, `webhook_url = ""`, `webhook_url = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test"`, 1)

	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.Equal(t, "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test", snapshot.Manifest.Notification.WebhookURL)
}

func TestLoadDefaultsEventBusPort(t *testing.T) {
	root := t.TempDir()
	body := strings.Replace(validManifest, "port = 4222\n", "", 1)

	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.Equal(t, 4222, snapshot.Manifest.EventBus.Port)
}

func TestLoadTencentCloudRegion(t *testing.T) {
	root := t.TempDir()
	body := strings.Replace(validManifest, `secret_key = "secret-key"`, "secret_key = \"secret-key\"\nregion = \"ap-shanghai\"", 1)

	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.Equal(t, "ap-shanghai", snapshot.Manifest.TencentCloud.Region)
}

func TestLoadDefaultsHostPorts(t *testing.T) {
	root := t.TempDir()
	body := strings.Replace(validManifest, "port = 22\n", "", 1) + `
[[other_hosts]]
name = "compute"
address = "192.0.2.11"
username = "ubuntu"
password = "compute-password"
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.Equal(t, 22, snapshot.Manifest.ControlHost.Port)
	assert.Equal(t, 22, snapshot.Manifest.OtherHosts[0].Port)
}

func TestLoadDNSResolverConfiguration(t *testing.T) {
	valid := validManifest + `
[[other_hosts]]
name = "compute-1"
address = "43.132.204.177"
username = "ubuntu"
password = "compute-password"

[dns_resolver]
enabled = true
trade_node = "compute-1"
refresh_interval_seconds = 300
request_timeout_ms = 3000
lookup_timeout_ms = 1500
probe_timeout_ms = 500
probe_port = 443
cache_ttl_seconds = 300
max_ips_per_domain = 4
domains = ["FAPI.BINANCE.COM.", "api.binance.com"]
`
	root := t.TempDir()
	snapshot, err := Load(writeManifest(t, root, valid, 0o600), root)
	require.NoError(t, err)
	require.True(t, snapshot.Manifest.DNSResolver.Enabled)
	require.Equal(t, "compute-1", snapshot.Manifest.DNSResolver.TradeNode)
	require.Equal(t, []string{"fapi.binance.com", "api.binance.com"}, snapshot.Manifest.DNSResolver.Domains)

	for name, mutate := range map[string]func(*DNSResolver){
		"missing trade node": func(cfg *DNSResolver) { cfg.TradeNode = "missing" },
		"duplicate domain":   func(cfg *DNSResolver) { cfg.Domains = []string{"api.binance.com", "API.BINANCE.COM."} },
		"invalid interval":   func(cfg *DNSResolver) { cfg.RequestTimeoutMS = 0 },
		"invalid port":       func(cfg *DNSResolver) { cfg.ProbePort = 70000 },
		"invalid cap":        func(cfg *DNSResolver) { cfg.MaxIPsPerDomain = 5 },
		"too many domains":   func(cfg *DNSResolver) { cfg.Domains = make([]string, 17) },
	} {
		t.Run(name, func(t *testing.T) {
			manifest := snapshot.Manifest
			mutate(&manifest.DNSResolver)
			err := validateDNSResolver(&manifest.DNSResolver, &manifest)
			require.Error(t, err)
		})
	}
	unsafe := snapshot.Manifest
	unsafe.OtherHosts[0].Address = "127.0.0.1"
	require.ErrorContains(t, validateDNSResolver(&unsafe.DNSResolver, &unsafe), "public address")

	disabled := snapshot.Manifest
	disabled.DNSResolver = DNSResolver{Enabled: false, TradeNode: "missing"}
	require.NoError(t, validateDNSResolver(&disabled.DNSResolver, &disabled))
}

func TestLoadOptionalCompileHost(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[compile_host]
name = "compile"
address = "192.0.2.20"
username = "builder"
`

	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.True(t, snapshot.Manifest.HasCompileHost())
	assert.Equal(t, "compile", snapshot.Manifest.CompileHost.Name)
	assert.Equal(t, 22, snapshot.Manifest.CompileHost.Port)
	assert.Len(t, snapshot.Manifest.Hosts(), 1)
}

func TestLoadRejectsInvalidManifest(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "missing admin password", body: strings.Replace(validManifest, `password = "admin-password"`, `password = ""`, 1), want: "admin.password"},
		{name: "bcrypt password too long", body: strings.Replace(validManifest, "admin-password", strings.Repeat("x", 73), 1), want: "72 bytes"},
		{name: "missing secret id", body: strings.Replace(validManifest, `secret_id = "secret-id"`, `secret_id = ""`, 1), want: "tencent_cloud.secret_id"},
		{name: "missing secret key", body: strings.Replace(validManifest, `secret_key = "secret-key"`, `secret_key = ""`, 1), want: "tencent_cloud.secret_key"},
		{name: "empty region", body: strings.Replace(validManifest, `secret_key = "secret-key"`, "secret_key = \"secret-key\"\nregion = \" \"", 1), want: "tencent_cloud.region"},
		{name: "missing eventbus address", body: strings.Replace(validManifest, `public_address = "eventbus.example.test"`, `public_address = ""`, 1), want: "eventbus.public_address"},
		{name: "eventbus address with surrounding whitespace", body: strings.Replace(validManifest, `eventbus.example.test`, ` eventbus.example.test `, 1), want: "eventbus.public_address"},
		{name: "eventbus address with scheme", body: strings.Replace(validManifest, `eventbus.example.test`, `tls://eventbus.example.test`, 1), want: "eventbus.public_address"},
		{name: "eventbus address with path", body: strings.Replace(validManifest, `eventbus.example.test`, `eventbus.example.test/nats`, 1), want: "eventbus.public_address"},
		{name: "eventbus address with port", body: strings.Replace(validManifest, `eventbus.example.test`, `eventbus.example.test:4222`, 1), want: "eventbus.public_address"},
		{name: "eventbus ipv6 address", body: strings.Replace(validManifest, `eventbus.example.test`, `2001:db8::1`, 1), want: "eventbus.public_address"},
		{name: "eventbus explicit zero port", body: strings.Replace(validManifest, "port = 4222", "port = 0", 1), want: "eventbus.port"},
		{name: "eventbus invalid port", body: strings.Replace(validManifest, "port = 4222", "port = 70000", 1), want: "eventbus.port"},
		{name: "eventbus tls disabled", body: strings.Replace(validManifest, "tls_enabled = true", "tls_enabled = false", 1), want: "eventbus.tls_enabled"},
		{name: "notification webhook must use HTTPS", body: strings.Replace(validManifest, `webhook_url = ""`, `webhook_url = "http://example.test/hook"`, 1), want: "notification.webhook_url"},
		{name: "notification webhook must be a URL", body: strings.Replace(validManifest, `webhook_url = ""`, `webhook_url = "not-a-url"`, 1), want: "notification.webhook_url"},
		{name: "notification webhook must match channel host", body: strings.Replace(validManifest, `webhook_url = ""`, `webhook_url = "https://open.feishu.cn/hook"`, 1), want: "notification.webhook_url"},
		{name: "notification webhook must use approved platform host", body: strings.Replace(validManifest, `webhook_url = ""`, `webhook_url = "https://example.invalid/hook"`, 1), want: "notification.webhook_url"},
		{name: "missing host address", body: strings.Replace(validManifest, `address = "192.0.2.10"`, `address = ""`, 1), want: "control_host.address"},
		{name: "invalid host name", body: strings.Replace(validManifest, `name = "control"`, `name = "Storage A"`, 1), want: "control_host.name"},
		{name: "missing compile host address", body: validManifest + `
[compile_host]
name = "compile"
address = ""
username = "builder"
password = "password"
`, want: "compile_host.address"},
		{name: "unknown field", body: validManifest + "unexpected = true\n", want: "unknown field"},
		{name: "invalid port", body: strings.Replace(validManifest, "port = 22", "port = 70000", 1), want: "control_host.port"},
		{
			name: "duplicate host name",
			body: validManifest + `
[[other_hosts]]
name = "control"
address = "192.0.2.11"
username = "ubuntu"
password = "password"
`,
			want: "duplicate host name",
		},
		{
			name: "duplicate host address",
			body: validManifest + `
[[other_hosts]]
name = "compute"
address = "192.0.2.10"
username = "ubuntu"
password = "password"
`,
			want: "duplicate host address",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			_, err := Load(writeManifest(t, root, tt.body, 0o600), root)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.NotContains(t, err.Error(), "admin-password")
			assert.NotContains(t, err.Error(), "secret-key")
		})
	}
}

func TestLoadRejectsInsecureFile(t *testing.T) {
	t.Run("wrong mode", func(t *testing.T) {
		root := t.TempDir()
		_, err := Load(writeManifest(t, root, validManifest, 0o644), root)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "0600")
	})

	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		require.NoError(t, os.WriteFile(target, []byte(validManifest), 0o600))
		path := filepath.Join(root, "moox.toml")
		require.NoError(t, os.Symlink(target, path))
		_, err := Load(path, root)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "regular file")
	})

	t.Run("wrong basename", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "other.toml")
		require.NoError(t, os.WriteFile(path, []byte(validManifest), 0o600))
		_, err := Load(path, root)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "moox.toml")
	})

	t.Run("outside repository root", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		_, err := Load(writeManifest(t, outside, validManifest, 0o600), root)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "repository root")
	})
}

func TestSnapshotDetectsMutation(t *testing.T) {
	root := t.TempDir()
	path := writeManifest(t, root, validManifest, 0o600)
	snapshot, err := Load(path, root)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(path, []byte(validManifest+"\n"), 0o600))
	err = snapshot.VerifyUnchanged()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config_changed")
}

func TestSnapshotDetectsReplacement(t *testing.T) {
	root := t.TempDir()
	path := writeManifest(t, root, validManifest, 0o600)
	snapshot, err := Load(path, root)
	require.NoError(t, err)

	replacement := filepath.Join(root, "replacement")
	require.NoError(t, os.WriteFile(replacement, []byte(validManifest), 0o600))
	require.NoError(t, os.Rename(replacement, path))
	err = snapshot.VerifyUnchanged()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config_changed")
}
