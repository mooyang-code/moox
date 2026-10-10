package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/mooyang-code/moox/packages/storagepolicy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTencentSCFLimitsNormalizeDefaults(t *testing.T) {
	limits := TencentSCFLimits{}
	limits.normalize()

	assert.Equal(t, DefaultSCFMaxNamespacesPerRegion, limits.MaxNamespacesPerRegion)
	assert.Equal(t, DefaultSCFMaxFunctionsPerNamespace, limits.MaxFunctionsPerNamespace)
	assert.Equal(t, DefaultSCFMaxBurstConcurrencyPerMinute, limits.MaxBurstConcurrencyPerMinute)
	assert.Equal(t, 128000, limits.TotalConcurrencyMemoryMBByRegion["ap-guangzhou"])
	assert.Equal(t, 64000, limits.TotalConcurrencyMemoryMBByRegion["ap-singapore"])
	assert.Equal(t, "https://cloud.tencent.com/document/product/583/11637", limits.ReferenceURL)
	for _, region := range tencent.SCFRegions() {
		item, ok := limits.RegionLimits[region.Code]
		require.True(t, ok, region.Code)
		assert.Equal(t, DefaultSCFMaxNamespacesPerRegion, item.MaxNamespacesPerRegion, region.Code)
		assert.Equal(t, DefaultSCFMaxFunctionsPerNamespace, item.MaxFunctionsPerNamespace, region.Code)
		assert.Positive(t, item.TotalConcurrencyMemoryMB)
	}
}

func TestTencentSCFLimitsNormalizeMergesRegionOverrides(t *testing.T) {
	limits := TencentSCFLimits{TotalConcurrencyMemoryMBByRegion: map[string]int{
		" AP-NANJING ": 96000,
		"ap-guangzhou": 256000,
	}}
	limits.normalize()

	assert.Equal(t, 96000, limits.TotalConcurrencyMemoryMBByRegion["ap-nanjing"])
	assert.Equal(t, 256000, limits.TotalConcurrencyMemoryMBByRegion["ap-guangzhou"])
	assert.Equal(t, 64000, limits.TotalConcurrencyMemoryMBByRegion["ap-tokyo"])
}

func TestTencentSCFRegionLimitOverrideIsUsedForFunctionCapacity(t *testing.T) {
	limits := TencentSCFLimits{RegionLimits: map[string]TencentSCFRegionLimit{
		"ap-nanjing": {MaxNamespacesPerRegion: 3, MaxFunctionsPerNamespace: 8, TotalConcurrencyMemoryMB: 32000},
	}}
	limits.normalize()
	assert.Equal(t, 8, limits.ForRegion("ap-nanjing").MaxFunctionsPerNamespace)
	assert.Equal(t, 24, limits.ForRegion("ap-nanjing").MaxFunctionsPerRegion())
	assert.Equal(t, 23, limits.ForRegion("ap-nanjing").TimerCapacity(0))

	cfg := &SCFFetcher{Spaces: []SCFFetcherSpace{{
		SpaceID: "crypto", Namespace: "moox-crypto",
		Regions: []SCFFetcherRegion{{Region: "ap-nanjing", Enabled: true, FunctionCount: 8}},
	}}}
	limits.MaxFunctionsPerNamespace = DefaultSCFMaxFunctionsPerNamespace
	require.NoError(t, ValidateSCFCapacities(cfg, limits))

	cfg.Spaces[0].Regions[0].FunctionCount = 25
	require.ErrorContains(t, ValidateSCFCapacities(cfg, limits), "above max_namespaces_per_region 3")
}

func TestValidateSCFCapacitiesUsesRegionalNamespaceLimit(t *testing.T) {
	limits := defaultTencentSCFLimits()
	limits.RegionLimits["ap-nanjing"] = TencentSCFRegionLimit{
		MaxNamespacesPerRegion: 1, MaxFunctionsPerNamespace: 50, TotalConcurrencyMemoryMB: 64000,
	}
	cfg := &SCFFetcher{Spaces: []SCFFetcherSpace{
		{SpaceID: "crypto", Namespace: "moox-crypto", Regions: []SCFFetcherRegion{{Region: "ap-nanjing", Enabled: true, FunctionCount: 1}}},
		{SpaceID: "stockcn", Namespace: "moox-stockcn", Regions: []SCFFetcherRegion{{Region: "ap-nanjing", Enabled: true, FunctionCount: 1}}},
	}}
	require.ErrorContains(t, ValidateSCFCapacities(cfg, limits), "above max_namespaces_per_region 1")
}

func TestValidateSCFNamespaceUsesSpaceIdentity(t *testing.T) {
	space := SCFFetcherSpace{SpaceID: "stockcn", Namespace: "default"}
	err := validateSCFNamespace(&space, "scf_fetcher.spaces[0]")
	require.ErrorContains(t, err, "must be")

	space.Namespace = ""
	require.NoError(t, validateSCFNamespace(&space, "scf_fetcher.spaces[0]"))
	assert.Equal(t, "moox-stockcn", space.Namespace)

	space.Namespace = "moox-crypto"
	require.ErrorContains(t, validateSCFNamespace(&space, "scf_fetcher.spaces[0]"), "moox-stockcn")
}

func TestRebalanceSCFTimerFunctionCountsFillsStorageRegionFirst(t *testing.T) {
	limits := defaultTencentSCFLimits()
	cfg := SCFFetcherSpace{
		SpaceID: "crypto", TimerFunctionCount: 60,
		Regions: []SCFFetcherRegion{
			{Region: "ap-nanjing", Enabled: true, AutoFunctionCount: true},
			{Region: "ap-singapore", Enabled: true, AutoFunctionCount: true},
		},
	}
	require.NoError(t, RebalanceSCFTimerFunctionCounts(&cfg, "ap-nanjing", limits))
	assert.Equal(t, 59, cfg.Regions[0].FunctionCount)
	assert.Equal(t, 1, cfg.Regions[1].FunctionCount)
}

func TestTencentSCFLimitsRejectInvalidRegionMemory(t *testing.T) {
	limits := TencentSCFLimits{
		MaxNamespacesPerRegion:       5,
		MaxFunctionsPerNamespace:     50,
		MaxBurstConcurrencyPerMinute: 500,
		TotalConcurrencyMemoryMBByRegion: map[string]int{
			"ap-unknown": 64000,
		},
	}
	require.ErrorContains(t, limits.validate("scf_fetcher.tencent_limits"), "unsupported region")
}

func TestResolveSCFTimerFunctionCountsUsesConfiguredFunctionLimit(t *testing.T) {
	cfg := SCFFetcherSpace{
		SpaceID: "crypto", TimerFunctionCount: 3,
		Regions: []SCFFetcherRegion{
			{Region: "ap-guangzhou", Enabled: true},
			{Region: "ap-singapore", Enabled: true},
		},
	}
	require.NoError(t, resolveSCFTimerFunctionCountsWithLimit(&cfg, "scf", 2))
	assert.Equal(t, 1, cfg.Regions[0].FunctionCount)
	assert.Equal(t, 2, cfg.Regions[1].FunctionCount)

	cfg.TimerFunctionCount = 7
	cfg.Regions[0].FunctionCount = 0
	cfg.Regions[1].FunctionCount = 0
	require.ErrorContains(t, resolveSCFTimerFunctionCountsWithLimit(&cfg, "scf", 2), "available Timer capacity")
}

func TestResolveSCFFunctionCountsReserveCryptoReleaseCanaryCapacity(t *testing.T) {
	cfg := SCFFetcherSpace{
		SpaceID: "crypto", TimerFunctionCount: 60,
		Regions: []SCFFetcherRegion{
			{Region: "ap-guangzhou", Enabled: true},
			{Region: "ap-singapore", Enabled: true},
		},
	}
	require.NoError(t, resolveSCFTimerFunctionCountsWithCapacities(&cfg, "scf", 50, nil))
	assert.Equal(t, 11, cfg.Regions[0].FunctionCount)
	assert.Equal(t, 49, cfg.Regions[1].FunctionCount)
}

func TestValidateSCFCapacitiesReservesPublisherAuxiliaries(t *testing.T) {
	cfg := &SCFFetcher{Spaces: []SCFFetcherSpace{{
		SpaceID: "crypto", Namespace: "default",
		Regions: []SCFFetcherRegion{{Region: "ap-guangzhou", Enabled: true, FunctionCount: 50}},
	}}}
	limits := defaultTencentSCFLimits()
	require.NoError(t, ValidateSCFCapacities(cfg, limits))

	cfg.Spaces[0].Regions[0].FunctionCount = 49
	require.NoError(t, ValidateSCFCapacities(cfg, limits))

	cfg.Spaces = append(cfg.Spaces, SCFFetcherSpace{
		SpaceID: "other", Namespace: "default",
		Regions: []SCFFetcherRegion{{Region: "ap-guangzhou", Enabled: true, FunctionCount: 1}},
	})
	require.ErrorContains(t, ValidateSCFCapacities(cfg, limits), "above max_functions_per_namespace")
}

func TestValidateSCFCapacitiesAllowsOverflowNamespacesInOneRegion(t *testing.T) {
	limits := defaultTencentSCFLimits()
	cfg := &SCFFetcher{Spaces: []SCFFetcherSpace{{
		SpaceID: "crypto", Namespace: "moox-crypto",
		Regions: []SCFFetcherRegion{{Region: "ap-nanjing", Enabled: true, FunctionCount: 54}},
	}}}
	require.NoError(t, ValidateSCFCapacities(cfg, limits))

	shards := SpaceRegionNamespaceShards(cfg.Spaces[0], cfg.Spaces[0].Regions[0], limits)
	require.Len(t, shards, 2)
	assert.Equal(t, "moox-crypto", shards[0].Namespace)
	assert.Equal(t, 0, shards[0].Timers)
	assert.Equal(t, 50, shards[0].Invokes)
	assert.Equal(t, "moox-crypto-ns2", shards[1].Namespace)
	assert.Equal(t, 0, shards[1].Timers)
	assert.Equal(t, 4, shards[1].Invokes)
}

func TestSpaceRegionReleaseCanaryNamespaceUsesReservedCapacity(t *testing.T) {
	limits := TencentSCFLimits{RegionLimits: map[string]TencentSCFRegionLimit{
		"ap-nanjing": {MaxNamespacesPerRegion: 3, MaxFunctionsPerNamespace: 2},
	}}
	space := SCFFetcherSpace{SpaceID: "crypto", Namespace: "moox-crypto"}
	region := SCFFetcherRegion{Region: "ap-nanjing", Enabled: true, FunctionCount: 1}
	namespace, err := SpaceRegionReleaseCanaryNamespace(space, region, limits)
	require.NoError(t, err)
	assert.Equal(t, "moox-crypto", namespace, "use an existing namespace with spare per-namespace quota")

	region.FunctionCount = 2
	namespace, err = SpaceRegionReleaseCanaryNamespace(space, region, limits)
	require.NoError(t, err)
	assert.Equal(t, "moox-crypto-ns2", namespace, "overflow into a new namespace only when every production namespace is full")

	limits.RegionLimits["ap-nanjing"] = TencentSCFRegionLimit{MaxNamespacesPerRegion: 1, MaxFunctionsPerNamespace: 2}
	_, err = SpaceRegionReleaseCanaryNamespace(space, region, limits)
	require.ErrorContains(t, err, "no namespace capacity")
}

func TestValidateSCFCapacitiesIncludesCryptoReleaseCanary(t *testing.T) {
	limits := TencentSCFLimits{RegionLimits: map[string]TencentSCFRegionLimit{
		"ap-nanjing": {MaxNamespacesPerRegion: 2, MaxFunctionsPerNamespace: 2},
	}}
	cfg := &SCFFetcher{Spaces: []SCFFetcherSpace{{
		SpaceID: "crypto", Namespace: "moox-crypto",
		Regions: []SCFFetcherRegion{{Region: "ap-nanjing", Enabled: true, FunctionCount: 3}},
	}}}
	require.NoError(t, ValidateSCFCapacities(cfg, limits), "the release canary occupies one slot in the second namespace")

	cfg.Spaces[0].Regions[0].FunctionCount = 4
	// Four production functions fill both namespaces and leave no namespace for
	// the extra release canary; the validator must fail before publication.
	require.ErrorContains(t, ValidateSCFCapacities(cfg, limits), "release canary")
}

func TestValidateSCFCapacitiesIncludesStockCNReleaseCanary(t *testing.T) {
	limits := TencentSCFLimits{RegionLimits: map[string]TencentSCFRegionLimit{
		"ap-guangzhou": {MaxNamespacesPerRegion: 1, MaxFunctionsPerNamespace: 2},
	}}
	cfg := &SCFFetcher{Spaces: []SCFFetcherSpace{{
		SpaceID: "stockcn", Namespace: "moox-stockcn",
		Regions: []SCFFetcherRegion{{Region: "ap-guangzhou", Enabled: true, FunctionCount: 1}},
	}}}
	require.ErrorContains(t, ValidateSCFCapacities(cfg, limits), "release canary")

	limits.RegionLimits["ap-guangzhou"] = TencentSCFRegionLimit{MaxNamespacesPerRegion: 1, MaxFunctionsPerNamespace: 3}
	require.NoError(t, ValidateSCFCapacities(cfg, limits), "Timer, production Invoke, and isolated release canary fit exactly")
}

func TestValidateSCFCapacitiesRejectsOverflowBeyondRegionalNamespaces(t *testing.T) {
	limits := defaultTencentSCFLimits()
	cfg := &SCFFetcher{Spaces: []SCFFetcherSpace{{
		SpaceID: "crypto", Namespace: "moox-crypto",
		Regions: []SCFFetcherRegion{{Region: "ap-nanjing", Enabled: true, FunctionCount: 251}},
	}}}
	require.ErrorContains(t, ValidateSCFCapacities(cfg, limits), "above max_namespaces_per_region")
}

func TestValidateSCFCapacitiesCountsUsedNamespaces(t *testing.T) {
	limits := defaultTencentSCFLimits()
	cfg := &SCFFetcher{}
	for index := 0; index < 5; index++ {
		cfg.Spaces = append(cfg.Spaces, SCFFetcherSpace{
			SpaceID: fmt.Sprintf("space-%d", index), Namespace: fmt.Sprintf("custom-%d", index),
			Regions: []SCFFetcherRegion{{Region: "ap-guangzhou", Enabled: true, FunctionCount: 1}},
		})
	}
	require.NoError(t, ValidateSCFCapacities(cfg, limits))
	cfg.Spaces = append(cfg.Spaces, SCFFetcherSpace{
		SpaceID: "space-5", Namespace: "custom-5",
		Regions: []SCFFetcherRegion{{Region: "ap-guangzhou", Enabled: true, FunctionCount: 1}},
	})
	require.ErrorContains(t, ValidateSCFCapacities(cfg, limits), "above max_namespaces_per_region")
}

func TestPlanSCFNamespaceShardsKeepsCanaryInPrimaryNamespace(t *testing.T) {
	shards := PlanSCFNamespaceShards("moox-crypto", 54, 1, 50)
	require.Len(t, shards, 2)
	assert.Equal(t, SCFNamespaceShard{Namespace: "moox-crypto", Timers: 49, Invokes: 1}, shards[0])
	assert.Equal(t, SCFNamespaceShard{Namespace: "moox-crypto-ns2", Timers: 5}, shards[1])
	assert.Equal(t, "moox-crypto-ns3", OverflowSCFNamespace("moox-crypto", 2))
}

func TestValidateSCFRejectsSharedNamespaceAutoAllocation(t *testing.T) {
	cfg := &SCFFetcher{
		Enabled:      true,
		CloudAccount: SCFFetcherCloudAccount{AccountID: "a", AccountName: "a", CredentialSecretID: "s", AppID: "app", COSRegion: "ap-guangzhou", COSBucket: "bucket"},
		Spaces: []SCFFetcherSpace{
			{SpaceID: "crypto", Namespace: "shared", Regions: []SCFFetcherRegion{{Region: "ap-singapore", Enabled: true, FunctionCount: 0}}},
			{SpaceID: "stockcn", Namespace: "shared", Regions: []SCFFetcherRegion{{Region: "ap-singapore", Enabled: true, FunctionCount: 1}}},
		},
	}
	require.ErrorContains(t, validateSCFSharedNamespaceAutoAllocation(cfg), "shared by multiple Spaces")
}

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
				AccessAddress:       "106.53.107.122:11004",
				MaxInflightRequests: 32, RequestTimeoutMS: 1500,
				HTTPMaxAttempts: 4, Regions: []SCFFetcherRegion{{Region: "ap-guangzhou", Enabled: true, FunctionCount: 1}},
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

	t.Run("rejects shared namespace before per-space auto allocation", func(t *testing.T) {
		cfg := base()
		cfg.Spaces[0].Regions[0].FunctionCount = 0
		cfg.Spaces = append(cfg.Spaces, SCFFetcherSpace{
			SpaceID: "stockcn", Namespace: cfg.Spaces[0].Namespace,
			Regions: []SCFFetcherRegion{{Region: "ap-singapore", Enabled: true, FunctionCount: 1}},
		})
		require.ErrorContains(t, validateSCFFetcher(&cfg), "shared by multiple Spaces")
	})

	t.Run("caps invocation concurrency at the executor bound", func(t *testing.T) {
		cfg := base()
		cfg.Spaces[0].MaxInflightRequests = 65
		err := validateSCFFetcher(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "between 1 and 64")
	})

	t.Run("requires the fixed HTTP attempt budget", func(t *testing.T) {
		cfg := base()
		cfg.Spaces[0].HTTPMaxAttempts = 3
		err := validateSCFFetcher(&cfg)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "request timeout or HTTP attempts")
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
		cfg.Spaces[0].RequestTimeoutMS = 11000
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
		SpaceID: "stockcn", Entrypoint: "market_data", TimerFunctionCount: 1, MeasuredSafeGroupSize: 40, AccessAddress: "106.53.107.122:11004",
		MemorySize: 64, TimeoutSeconds: 15, RealtimeBatchSize: 1, MaxInflightRequests: 10, RequestTimeoutMS: 1000, HTTPMaxAttempts: 4, Regions: []SCFFetcherRegion{{Region: "ap-guangzhou", Enabled: true, FunctionCount: 1, CloudAccountID: "tencent-scf-guangzhou"}},
	}}}
	err := validateSCFFetcher(&cfg)
	require.Error(t, err)
	cfg.Spaces[0].MarketID = "stockcn"
	cfg.Spaces[0].InstrumentType = "equity"
	cfg.Spaces[0].ProviderID = "eastmoney"
	cfg.Spaces[0].SourceID = "stockcn_http"
	require.NoError(t, validateSCFFetcher(&cfg))
}

func TestValidateSCFAccessInstanceIDs(t *testing.T) {
	for _, id := range []string{"access@storage", "access@regional-sg"} {
		require.True(t, validAccessInstanceID(id))
	}
	for _, id := range []string{"storage", "access@bad_node", "access@Bad", "access@"} {
		require.False(t, validAccessInstanceID(id))
	}
}

func TestValidateSCFFetcherNormalizesMarketPublicNetworkStatus(t *testing.T) {
	cfg := SCFFetcher{Enabled: true, CloudAccount: SCFFetcherCloudAccount{
		AccountID: "tencent-scf", AccountName: "Tencent SCF", CredentialSecretID: "tencent-default",
		AppID: "1255382561", COSRegion: "ap-guangzhou", COSBucket: "moox-scf-guangzhou-1255382561",
	}, Spaces: []SCFFetcherSpace{{
		SpaceID: "crypto", Entrypoint: "market_data", MarketID: "crypto", InstrumentType: "spot",
		ProviderID: "binance", SourceID: "spot_http", PublicNetStatus: "enable",
		AccessAddress: "106.53.107.122:11004", MemorySize: 64, TimeoutSeconds: 15,
		RealtimeBatchSize: 1, MaxInflightRequests: 1, RequestTimeoutMS: 1000, HTTPMaxAttempts: 4, Regions: []SCFFetcherRegion{{Region: "ap-singapore", Enabled: true, FunctionCount: 1, CloudAccountID: "tencent-scf"}},
	}}}

	require.NoError(t, validateSCFFetcher(&cfg))
	assert.Equal(t, "ENABLE", cfg.Spaces[0].PublicNetStatus)
	assert.Equal(t, DefaultCryptoInvokeTimeoutSeconds, cfg.Spaces[0].InvokeTimeoutSeconds)

	cfg.Spaces[0].PublicNetStatus = "unknown"
	require.ErrorContains(t, validateSCFFetcher(&cfg), "public_net_status must be ENABLE or DISABLE")
}

func TestValidateSCFFetcherRequiresTDXEndpointConfiguration(t *testing.T) {
	cfg := SCFFetcher{Enabled: true, CloudAccount: SCFFetcherCloudAccount{
		AccountID: "tencent-scf", AccountName: "Tencent SCF", CredentialSecretID: "tencent-default",
		AppID: "1255382561", COSRegion: "ap-guangzhou", COSBucket: "moox-scf-guangzhou-1255382561",
	}, Spaces: []SCFFetcherSpace{{
		SpaceID: "stockcn", Entrypoint: "market_data", TimerFunctionCount: 1, MeasuredSafeGroupSize: 40, MarketID: "stockcn", InstrumentType: "equity",
		ProviderID: "tdx", SourceID: "normal_7709", AccessAddress: "106.53.107.122:11004",
		MemorySize: 64, TimeoutSeconds: 15, RealtimeBatchSize: 1, MaxInflightRequests: 10, RequestTimeoutMS: 1000, HTTPMaxAttempts: 4, Regions: []SCFFetcherRegion{{Region: "ap-guangzhou", Enabled: true, FunctionCount: 1, CloudAccountID: "tencent-scf-guangzhou"}},
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
			SpaceID: "crypto", AccessAddress: "106.53.107.122:11004",
			MemorySize: 64, TimeoutSeconds: 15, RealtimeBatchSize: 1, MaxInflightRequests: 1, RequestTimeoutMS: 1000,
			HTTPMaxAttempts: 4, Regions: []SCFFetcherRegion{
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

func TestResolveSCFTimerFunctionCountsPrioritizesOverseasCryptoRegions(t *testing.T) {
	cfg := SCFFetcherSpace{
		SpaceID: "crypto",
		Regions: []SCFFetcherRegion{
			{Region: "ap-guangzhou", Enabled: true, FunctionCount: 0, CloudAccountID: "gz"},
			{Region: "ap-singapore", Enabled: true, FunctionCount: 0, CloudAccountID: "sg"},
		},
	}

	require.NoError(t, resolveSCFTimerFunctionCounts(&cfg, "scf_fetcher.spaces[0]"))
	assert.Equal(t, 10, cfg.Regions[0].FunctionCount)
	assert.Equal(t, 50, cfg.Regions[1].FunctionCount)
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
		SpaceID: "stockcn", TimerFunctionCount: 192, MemorySize: 64, TimeoutSeconds: 15,
		MeasuredSafeGroupSize: 30, AccessAddress: "106.53.107.122:11004",
		RealtimeBatchSize: 10, MaxInflightRequests: 10, RequestTimeoutMS: 2000,
		HTTPMaxAttempts: 4, Regions: []SCFFetcherRegion{{Region: "ap-guangzhou", Enabled: true, FunctionCount: 48, CloudAccountID: "gz"}, {Region: "ap-shanghai", Enabled: true, FunctionCount: 48, CloudAccountID: "sh"}, {Region: "ap-beijing", Enabled: true, FunctionCount: 48, CloudAccountID: "bj"}, {Region: "ap-chengdu", Enabled: true, FunctionCount: 48, CloudAccountID: "cd"}},
	}
	require.NoError(t, validateSCFFetcherSpace(&base, "scf_fetcher.spaces[0]"))

	base.MeasuredSafeGroupSize = 0
	require.ErrorContains(t, validateSCFFetcherSpace(&base, "scf_fetcher.spaces[0]"), "measured_safe_group_size must be between 1 and 40")
	base.MeasuredSafeGroupSize = -1
	require.ErrorContains(t, validateSCFFetcherSpace(&base, "scf_fetcher.spaces[0]"), "measured_safe_group_size must be between 1 and 40")
	base.MeasuredSafeGroupSize = StockCNMaxRealtimeItems + 1
	require.ErrorContains(t, validateSCFFetcherSpace(&base, "scf_fetcher.spaces[0]"), "measured_safe_group_size must be between 1 and 40")
}

func TestValidateSCFFetcherRejectsUnsafeStockStaggerRate(t *testing.T) {
	base := SCFFetcherSpace{SpaceID: "stockcn", TimerFunctionCount: 200, MemorySize: 64, TimeoutSeconds: 15,
		MeasuredSafeGroupSize: 30, StaggerStartSecond: 5, StaggerWindowSeconds: 35, StaggerMaxStartsPerSecond: 5,
		AccessAddress: "106.53.107.122:11004", RealtimeBatchSize: 10, RealtimeBarLimit: 10,
		CatchupBatchSize: 1, CatchupBarLimit: 1000, MaxInflightRequests: 10, RequestTimeoutMS: 2000,
		HTTPMaxAttempts: 4, StorageTimeoutMS: 5000, MaxRetryAttempts: 3,
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
	assert.Equal(t, "1m", manifest.StorageView.MaintenanceCheckInterval)
	assert.Equal(t, "1h", manifest.StorageView.CapacityCheckInterval)
	assert.Equal(t, "1h", manifest.StorageView.CapacityCheckJitter)
	// The example spells out the recommended policy, so it renders the same
	// storage-policy.json as an omitted [storage_retention].
	examplePolicy, err := manifest.StoragePolicy().Encode()
	require.NoError(t, err)
	recommended, err := storagepolicy.Default().Encode()
	require.NoError(t, err)
	assert.Equal(t, string(recommended), string(examplePolicy))
	manifest.SCFFetcher.Enabled = true
	require.NoError(t, resolveManifestReferences(&manifest))
	require.NoError(t, validateSCFFetcher(&manifest.SCFFetcher))

	for _, space := range manifest.SCFFetcher.Spaces {
		if space.SpaceID != "stockcn" {
			continue
		}
		assert.Equal(t, DefaultStockCNMarketTimerFunctionCount, space.TimerFunctionCount)
		assert.Equal(t, DefaultStockCNInvokeTimeoutSeconds, space.InvokeTimeoutSeconds)
		require.Len(t, space.Regions, 4)
		for _, region := range space.Regions {
			assert.True(t, region.Enabled)
			assert.Contains(t, []int{42, 43}, region.FunctionCount)
		}
		return
	}
	t.Fatal("stockcn scf_fetcher config is missing")
}

func TestSCFFetcherSpaceParsesCanaryTaskID(t *testing.T) {
	var manifest Manifest
	_, err := toml.Decode(`[scf_fetcher]
enabled = true
[[scf_fetcher.spaces]]
space_id = "crypto"
canary_task_id = "task-canary-1"
`, &manifest)
	require.NoError(t, err)
	require.Len(t, manifest.SCFFetcher.Spaces, 1)
	assert.Equal(t, "task-canary-1", manifest.SCFFetcher.Spaces[0].CanaryTaskID)
}

func TestValidateSCFFetcherDefaultsAndBoundsStockCNInvokeTimeout(t *testing.T) {
	base := SCFFetcherSpace{
		SpaceID: "stockcn", MemorySize: 64, TimeoutSeconds: 15,
		TimerFunctionCount:    192,
		MeasuredSafeGroupSize: 30,
		AccessAddress:         "106.53.107.122:11004",
		RealtimeBatchSize:     10, MaxInflightRequests: 10, RequestTimeoutMS: 2000,
		HTTPMaxAttempts: 4, Regions: []SCFFetcherRegion{
			{Region: "ap-guangzhou", Enabled: true, FunctionCount: 48, CloudAccountID: "gz"},
			{Region: "ap-shanghai", Enabled: true, FunctionCount: 48, CloudAccountID: "sh"},
			{Region: "ap-beijing", Enabled: true, FunctionCount: 48, CloudAccountID: "bj"},
			{Region: "ap-chengdu", Enabled: true, FunctionCount: 48, CloudAccountID: "cd"},
		},
	}
	require.NoError(t, validateSCFFetcherSpace(&base, "scf_fetcher.spaces[0]"))
	assert.Equal(t, DefaultSCFTimerTimeoutSeconds, base.TimeoutSeconds)
	assert.Equal(t, DefaultStockCNInvokeTimeoutSeconds, base.InvokeTimeoutSeconds)

	base.InvokeTimeoutSeconds = 59
	err := validateSCFFetcherSpace(&base, "scf_fetcher.spaces[0]")
	require.ErrorContains(t, err, "invoke_timeout_seconds")
}

func TestValidateSCFTimerClaimCompletionBudget(t *testing.T) {
	base := SCFFetcherSpace{SpaceID: "crypto", MemorySize: 64, TimeoutSeconds: 60,
		AccessAddress: "storage.example:11004", RealtimeBatchSize: 30, MaxInflightRequests: 10, RequestTimeoutMS: 1000,
		HTTPMaxAttempts: 4, StorageTimeoutMS: 5000,
		Regions: []SCFFetcherRegion{{Region: "ap-singapore", Enabled: true, FunctionCount: 1, CloudAccountID: "sg"}}}
	require.NoError(t, validateSCFFetcherSpace(&base, "scf_fetcher.spaces[0]"))
	base.RequestTimeoutMS = 4000
	// 3 waves * 4 attempts * 4 seconds + Claim + Storage + Completion +
	// CLS/metrics/final reserves exceeds 60 seconds.
	require.ErrorContains(t, validateSCFFetcherSpace(&base, "scf_fetcher.spaces[0]"), "reserves")
}

func TestValidateSCFAccessAddress(t *testing.T) {
	require.NoError(t, validateAccessAddress("storage.example:11004", "scf"))
	for _, value := range []string{"ip://storage.example:11004", "https://storage.example", "storage.example:0", "storage.example:11004/path"} {
		require.Error(t, validateAccessAddress(value, "scf"))
	}
}

const validManifest = `[admin]
username = "admin"
password = "admin-password"

[tencent_cloud]
secret_id = "secret-id"
secret_key = "secret-key"

[eventbus]
port = 4222
tls_enabled = true

[hosts.control]
address = "192.0.2.10"
[hosts.control.ssh]
port = 22
username = "ubuntu"
password = "control-password"

[notification]
channel_type = "wecom"
webhook_url = ""

[placements]
control = ["admin", "console-proxy", "web-host", "eventbus"]
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
	assert.Equal(t, "192.0.2.10", snapshot.Manifest.EventBus.PublicAddress)
	assert.Equal(t, 4222, snapshot.Manifest.EventBus.Port)
	assert.True(t, snapshot.Manifest.EventBus.TLSEnabled)
	assert.Equal(t, "ap-guangzhou", snapshot.Manifest.TencentCloud.Region)
	assert.Equal(t, DefaultDeployRoot, snapshot.Manifest.Paths.DeployRoot)
	assert.Equal(t, DefaultControlRoot, snapshot.Manifest.Paths.ControlRoot)
	assert.Equal(t, DefaultStorageRoot, snapshot.Manifest.Paths.StorageRoot)
	assert.Equal(t, "1m", snapshot.Manifest.StorageView.MaintenanceCheckInterval)
	assert.Equal(t, "1h", snapshot.Manifest.StorageView.CapacityCheckInterval)
	assert.Equal(t, "1h0m0s", snapshot.Manifest.StorageView.CapacityCheckJitter)
	assert.Equal(t, int64(1<<30), snapshot.Manifest.StorageView.MaxViewFileBytes)
	policy := snapshot.Manifest.StoragePolicy()
	assert.Equal(t, storagepolicy.Default().Retention, policy.Retention, "an omitted [storage_retention] uses the recommended retention")
	assert.Equal(t, uint64(5000), policy.View.Bars)
	assert.Equal(t, uint64(6000), policy.View.TrimBars)
	require.NoError(t, policy.Validate())
	assert.Equal(t, 50, snapshot.Manifest.LocalLogs.MaxSizeMB)
	assert.Equal(t, 5, snapshot.Manifest.LocalLogs.BackupCount)
	assert.Equal(t, 22, snapshot.Manifest.ControlHost().Port)
	assert.Empty(t, snapshot.Manifest.Notification.WebhookURL)
	assert.Empty(t, snapshot.Manifest.OtherHosts())
	require.NoError(t, snapshot.VerifyUnchanged())
}

func TestLoadRoleSpecificStorageAndViewHosts(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
storage = ["storage-primary", "storage-node", "storage-view"]
[hosts.storage]
address = "192.0.2.20"
provider = "Tencent"
ssh = { username = "ubuntu", password = "storage-password" }
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	require.Equal(t, "storage", snapshot.Manifest.StorageHost().Name)
	require.Equal(t, "storage", snapshot.Manifest.ViewHost().Name)
	require.Equal(t, snapshot.Manifest.StorageHost(), snapshot.Manifest.ViewHost())
	assert.Equal(t, "ubuntu", snapshot.Manifest.StorageHost().Username)
	assert.Equal(t, "storage-password", snapshot.Manifest.ViewHost().Password)
	assert.Equal(t, "tencent", snapshot.Manifest.StorageHost().Provider)
	assert.Len(t, snapshot.Manifest.Hosts(), 2)
}

func TestLoadDerivesAccessFromPlacement(t *testing.T) {
	root := t.TempDir()
	body := strings.Replace(validManifest, `"web-host", "eventbus"`, `"web-host", "eventbus", "access"`, 1) + `
[[scf_fetcher.spaces]]
space_id = "crypto"
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	require.Len(t, snapshot.Manifest.SCFFetcher.Spaces, 1)
	space := snapshot.Manifest.SCFFetcher.Spaces[0]
	assert.Equal(t, "control", space.AccessHost)
	assert.Equal(t, "access@control", space.AccessID)
	assert.Equal(t, "192.0.2.10:11004", space.AccessAddress)
	assert.Empty(t, space.AccessPrivateAddress)
}

func TestLoadDerivesRegionalAccessTargets(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
storage = ["storage-primary", "access"]
compute = ["access"]
[hosts.storage]
address = "192.0.2.20"
region = "AP-NANJING"
ssh = { username = "ubuntu" }
[hosts.compute]
address = "192.0.2.21"
region = "ap-hongkong"
ssh = { username = "ubuntu" }
[[scf_fetcher.spaces]]
space_id = "crypto"
[[scf_fetcher.spaces.regions]]
region = "ap-nanjing"
[[scf_fetcher.spaces.regions]]
region = "ap-hongkong"
[[scf_fetcher.spaces.regions]]
region = "ap-guangzhou"
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	space := snapshot.Manifest.SCFFetcher.Spaces[0]
	assert.Equal(t, "storage", space.AccessHost)
	assert.Equal(t, "192.0.2.20:11004", space.AccessAddressForRegion("ap-nanjing"))
	assert.Equal(t, "192.0.2.21:11004", space.AccessAddressForRegion("ap-hongkong"))
	assert.Equal(t, "192.0.2.20:11004", space.AccessAddressForRegion("ap-guangzhou"))
	assert.Equal(t, "access@storage", space.AccessIDForRegion("ap-nanjing"))
	assert.Equal(t, "access@compute", space.AccessIDForRegion("ap-hongkong"))
}

func TestLoadKeepsPrivateAccessCandidateUntilNetworkVerification(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
storage = ["storage-primary", "access"]
[hosts.storage]
address = "192.0.2.20"
private_address = "10.206.0.5"
region = "ap-nanjing"
ssh = { username = "ubuntu" }
[[scf_fetcher.spaces]]
space_id = "crypto"
[[scf_fetcher.spaces.regions]]
region = "ap-nanjing"
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	space := snapshot.Manifest.SCFFetcher.Spaces[0]
	assert.Equal(t, "10.206.0.5:11004", space.AccessPrivateAddress)
	assert.Equal(t, "192.0.2.20:11004", space.AccessAddressForRegion("ap-nanjing"))
}

func TestLoadSupportsIPv6HostsAndAccess(t *testing.T) {
	root := t.TempDir()
	body := strings.ReplaceAll(validManifest, "192.0.2.10", "2001:db8::10")
	body = strings.Replace(body, `"web-host", "eventbus"`, `"web-host", "eventbus", "access"`, 1) + `
[[scf_fetcher.spaces]]
space_id = "crypto"
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.Equal(t, "2001:db8::10", snapshot.Manifest.EventBus.PublicAddress)
	assert.Equal(t, "[2001:db8::10]:11004", snapshot.Manifest.SCFFetcher.Spaces[0].AccessAddress)
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

func TestLoadStorageRetentionFromManifest(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[storage_retention]
view_bars = 777

[storage_retention.spaces.crypto]
"1m" = "14d"
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	policy := snapshot.Manifest.StoragePolicy()
	assert.Equal(t, uint64(777), policy.View.Bars)
	assert.Equal(t, uint64(933), policy.View.TrimBars)
	period, source, err := policy.Retention.Resolve("crypto", "1m")
	require.NoError(t, err)
	assert.Equal(t, "336h", period.String())
	assert.Equal(t, storagepolicy.SourceSpace, source)
	// The omitted defaults table is the recommended one; a given spaces table
	// replaces the recommended space overrides instead of merging with them.
	period, source, err = policy.Retention.Resolve("stockcn", "1m")
	require.NoError(t, err)
	assert.Equal(t, "168h", period.String())
	assert.Equal(t, storagepolicy.SourceDefault, source)
}

func TestLoadCollectorRetentionDefaultsAndOverrides(t *testing.T) {
	root := t.TempDir()
	snapshot, err := Load(writeManifest(t, root, validManifest, 0o600), root)
	require.NoError(t, err)
	assert.Equal(t, "1m", snapshot.Manifest.CollectorRetention.MaintenanceInterval)
	assert.Equal(t, "35s", snapshot.Manifest.CollectorRetention.MaintenanceOffset)
	assert.Equal(t, "20s", snapshot.Manifest.CollectorRetention.MaintenanceTimeout)
	assert.Equal(t, 50000, snapshot.Manifest.CollectorRetention.MaxRowsPerPass)
	assert.Equal(t, "6h", snapshot.Manifest.CollectorRetention.ExecutionDetailRetention)
	assert.Equal(t, "720h", snapshot.Manifest.CollectorRetention.ScheduledRunSummaryRetention)
	assert.Equal(t, "168h", snapshot.Manifest.CollectorRetention.TerminalRetryRetention)
	assert.Equal(t, "720h", snapshot.Manifest.CollectorRetention.PeriodSnapshotRetention)

	root = t.TempDir()
	body := validManifest + `
[collector_retention]
maintenance_interval = "2m"
maintenance_offset = "10s"
maintenance_timeout = "30s"
max_rows_per_pass = 25000
terminal_retry_retention = "336h"
`
	snapshot, err = Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.Equal(t, "2m", snapshot.Manifest.CollectorRetention.MaintenanceInterval)
	assert.Equal(t, "10s", snapshot.Manifest.CollectorRetention.MaintenanceOffset)
	assert.Equal(t, "30s", snapshot.Manifest.CollectorRetention.MaintenanceTimeout)
	assert.Equal(t, 25000, snapshot.Manifest.CollectorRetention.MaxRowsPerPass)
	assert.Equal(t, "336h", snapshot.Manifest.CollectorRetention.TerminalRetryRetention)
}

func TestLoadRejectsInvalidCollectorRetention(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "zero row budget", body: "max_rows_per_pass = 0", want: "max_rows_per_pass"},
		{name: "row budget below category minimum", body: "max_rows_per_pass = 8", want: "max_rows_per_pass"},
		{name: "excessive row budget", body: "max_rows_per_pass = 50001", want: "max_rows_per_pass"},
		{name: "timeout exceeds interval", body: `maintenance_interval = "1m"
maintenance_timeout = "2m"`, want: "maintenance_timeout"},
		{name: "pass crosses the next interval boundary", body: `maintenance_offset = "35s"
maintenance_timeout = "30s"`, want: "maintenance_timeout"},
		{name: "offset outside interval", body: `maintenance_offset = "1m"`, want: "maintenance_offset"},
		{name: "retention over one year", body: `execution_detail_retention = "9000h"`, want: "execution_detail_retention"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			_, err := Load(writeManifest(t, root, validManifest+"\n[collector_retention]\n"+tt.body+"\n", 0o600), root)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestLoadAcceptsMinimumCollectorRetentionBudget(t *testing.T) {
	root := t.TempDir()
	_, err := Load(writeManifest(t, root, validManifest+"\n[collector_retention]\nmax_rows_per_pass = 9\n", 0o600), root)
	require.NoError(t, err)
}

func TestLoadRejectsInvalidStorageRetention(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "no bars", body: "[storage_retention]\nview_bars = 0", want: "view.bars"},
		{name: "incomplete defaults", body: "[storage_retention.defaults]\n\"1m\" = \"7d\"", want: "must cover every frequency"},
		{name: "alias frequency", body: "[storage_retention.spaces.crypto]\n\"1H\" = \"7d\"", want: "not a canonical frequency"},
		{name: "invalid period", body: "[storage_retention.spaces.crypto]\n\"1m\" = \"7 days\"", want: "retention"},
		{name: "retention shorter than view bars", body: "[storage_retention.spaces.crypto]\n\"1m\" = \"3d\"", want: "fewer than view.bars"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			_, err := Load(writeManifest(t, root, validManifest+"\n"+tt.body+"\n", 0o600), root)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestLoadRejectsInvalidStorageViewCapacitySchedule(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "nonpositive interval", body: "capacity_check_interval = \"0s\"", want: "capacity_check_interval"},
		{name: "interval over one day", body: "capacity_check_interval = \"25h\"", want: "capacity_check_interval"},
		{name: "zero jitter", body: "capacity_check_jitter = \"0s\"", want: "capacity_check_jitter"},
		{name: "nonpositive jitter", body: "capacity_check_jitter = \"-1s\"", want: "capacity_check_jitter"},
		{name: "jitter exceeds interval", body: "capacity_check_interval = \"30m\"\ncapacity_check_jitter = \"1h\"", want: "capacity_check_jitter"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			body := validManifest + "\n[storage_view]\n" + tt.body + "\n"
			_, err := Load(writeManifest(t, root, body, 0o600), root)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestLoadStorageViewDefaultsJitterToShorterConfiguredInterval(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[storage_view]
capacity_check_interval = "30m"
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	require.Equal(t, "30m", snapshot.Manifest.StorageView.CapacityCheckInterval)
	require.Equal(t, "30m0s", snapshot.Manifest.StorageView.CapacityCheckJitter)
}

func TestFactorSetupParsesDefinitionsAndMembers(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[factors]
enabled = true
source_dir = "./examples/factors"

[[factors.sets]]
space_id = "crypto"
source_dataset_id = "dataset_crypto_kline_1m"
freq = "1m"

[[factors.definitions]]
factor_id = "bias"
factor_type = "timeseries"
file = "timeseries/bias.py"
input_columns = ["close"]
outputs = ["bias_5"]
params_json = '{"windows":[5]}'
lookback_periods = 5

[[factors.members]]
source_dataset_id = "dataset_crypto_kline_1m"
freq = "1m"
factor_id = "bias"
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.True(t, snapshot.Manifest.Factors.Enabled)
	assert.Equal(t, "examples/factors", snapshot.Manifest.Factors.SourceDir)
	require.Len(t, snapshot.Manifest.Factors.Definitions, 1)
	require.Len(t, snapshot.Manifest.Factors.Sets, 1)
	require.Len(t, snapshot.Manifest.Factors.Members, 1)
	assert.Equal(t, "bias", snapshot.Manifest.Factors.Definitions[0].FactorID)
	assert.Equal(t, "all", snapshot.Manifest.Factors.Sets[0].SubjectMode)
	assert.Equal(t, "enabled", snapshot.Manifest.Factors.Members[0].Status)
}

func TestFactorSetupRejectsMemberWithUnknownFactor(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[factors]
enabled = true
source_dir = "./examples/factors"

[[factors.sets]]
space_id = "crypto"
source_dataset_id = "dataset_crypto_kline_1m"
freq = "1m"

[[factors.members]]
source_dataset_id = "dataset_crypto_kline_1m"
freq = "1m"
factor_id = "ghost"
`
	_, err := Load(writeManifest(t, root, body, 0o600), root)
	require.ErrorContains(t, err, "unknown factor_id")
}

func TestFactorSetupRejectsMemberWithUnknownSet(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[factors]
enabled = true
source_dir = "./examples/factors"

[[factors.sets]]
space_id = "crypto"
source_dataset_id = "dataset_crypto_kline_1m"
freq = "1m"

[[factors.definitions]]
factor_id = "Bias"
factor_type = "timeseries"
file = "Bias.py"
lookback_periods = 20

[[factors.members]]
source_dataset_id = "dataset_other"
freq = "1m"
factor_id = "Bias"
`
	_, err := Load(writeManifest(t, root, body, 0o600), root)
	require.ErrorContains(t, err, "no matching factor set")
}

func TestFactorSetupRejectsLegacyItemsKeyWithMigrationHint(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[factors]
enabled = true
source_dir = "./examples/factors"

[[factors.sets]]
space_id = "crypto"
source_dataset_id = "dataset_crypto_kline_1m"
freq = "1m"

[[factors.items]]
factor_id = "Bias"
factor_type = "timeseries"
file = "Bias.py"
source_dataset_id = "dataset_crypto_kline_1m"
freq = "1m"
lookback_periods = 20
`
	_, err := Load(writeManifest(t, root, body, 0o600), root)
	require.ErrorContains(t, err, "factors.items is no longer supported")
	require.ErrorContains(t, err, "[[factors.members]]")
}

func TestFactorSetupRejectsDuplicateMember(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[factors]
enabled = true
source_dir = "./examples/factors"

[[factors.sets]]
space_id = "crypto"
source_dataset_id = "dataset_crypto_kline_1m"
freq = "1m"

[[factors.definitions]]
factor_id = "Bias"
factor_type = "timeseries"
file = "Bias.py"
lookback_periods = 20

[[factors.members]]
source_dataset_id = "dataset_crypto_kline_1m"
freq = "1m"
factor_id = "Bias"

[[factors.members]]
source_dataset_id = "dataset_crypto_kline_1m"
freq = "1m"
factor_id = "Bias"
status = "disabled"
`
	_, err := Load(writeManifest(t, root, body, 0o600), root)
	require.ErrorContains(t, err, "more than once")
}

func TestLoadDefaultsDisableFactors(t *testing.T) {
	root := t.TempDir()
	snapshot, err := Load(writeManifest(t, root, validManifest, 0o600), root)
	require.NoError(t, err)
	assert.False(t, snapshot.Manifest.Factors.Enabled)
}

func TestLoadRejectsInvalidFactorTypeBeforeSetup(t *testing.T) {
	for _, declaration := range []string{"", "factor_type = \"unknown\""} {
		root := t.TempDir()
		body := validManifest + `
[factors]
enabled = true
source_dir = "examples/factors"
[[factors.definitions]]
factor_id = "Bias"
file = "Bias.py"
lookback_periods = 20
` + declaration + "\n"
		_, err := Load(writeManifest(t, root, body, 0o600), root)
		require.ErrorContains(t, err, "factor_type")
	}
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

func TestLoadDefaultsHostPortsAndAllowsSSHKeyAuthentication(t *testing.T) {
	root := t.TempDir()
	body := strings.Replace(validManifest, `password = "control-password"`, "", 1) + `
[hosts.compute]
address = "192.0.2.11"
ssh = { username = "ubuntu" }
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.Equal(t, 22, snapshot.Manifest.ControlHost().Port)
	assert.Empty(t, snapshot.Manifest.ControlHost().Password)
	require.Len(t, snapshot.Manifest.OtherHosts(), 1)
	assert.Equal(t, 22, snapshot.Manifest.OtherHosts()[0].Port)
}

func TestLoadEgressProxyConfiguration(t *testing.T) {
	root := t.TempDir()
	snapshot, err := Load(writeManifest(t, root, validManifest+`
[egress_proxy]
http_domains = ["*.binance.com", "data-api.binance.vision"]
[egress_proxy.dns]
domains = ["FAPI.BINANCE.COM.", "api.binance.com"]
`, 0o600), root)
	require.NoError(t, err)
	require.Equal(t, []string{"fapi.binance.com", "api.binance.com"}, snapshot.Manifest.EgressProxy.DNS.Domains)
	for name, mutate := range map[string]func(*EgressProxy){
		"duplicate domain": func(c *EgressProxy) { c.DNS.Domains = []string{"api.binance.com", "API.BINANCE.COM."} },
		"wildcard DNS":     func(c *EgressProxy) { c.DNS.Domains = []string{"*.binance.com"} },
		"invalid interval": func(c *EgressProxy) { c.DNS.RequestTimeoutMS = 0 },
		"invalid port":     func(c *EgressProxy) { c.DNS.ProbePort = 70000 },
		"invalid cap":      func(c *EgressProxy) { c.DNS.MaxIPsPerDomain = 5 },
		"too many domains": func(c *EgressProxy) { c.DNS.Domains = make([]string, 17) },
		"invalid HTTP":     func(c *EgressProxy) { c.HTTPDomains = []string{"https://binance.com"} },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := snapshot.Manifest.EgressProxy
			mutate(&cfg)
			require.Error(t, validateEgressProxy(&cfg))
		})
	}
}
func TestPlacementsRejectAmbiguousOrInvalidComponents(t *testing.T) {
	m := Manifest{HostCatalog: map[string]HostDefinition{
		"control": {Address: "192.0.2.10"}, "compute": {Address: "192.0.2.11"},
	}}
	for _, placements := range []map[string][]string{
		{"missing": {"trade"}}, {"compute": {"missing"}}, {"compute": {"trade", "trade"}},
		{"control": {"admin", "console-proxy", "web-host", "trade"}, "compute": {"trade"}},
		{"compute": {"host-gateway"}}, {"compute": {"admin"}}, {"compute": {"web-host"}},
	} {
		if _, ok := placements["control"]; !ok {
			placements["control"] = []string{"admin", "console-proxy", "web-host", "eventbus"}
		}
		m.Placements = placements
		require.Error(t, validatePlacements(&m))
	}
	m.Placements = map[string][]string{"control": {"admin", "console-proxy", "web-host", "eventbus"}, "compute": {"trade", "egress-proxy"}}
	require.NoError(t, validatePlacements(&m))
	require.Equal(t, "compute", m.PlacementHost("trade"))
}

func TestLoadOptionalCompileHost(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
[hosts.compile]
address = "192.0.2.20"
ssh = { username = "builder" }
[compile_host]
host = "compile"
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.True(t, snapshot.Manifest.HasCompileHost())
	assert.Equal(t, "compile", snapshot.Manifest.CompileHost().Name)
	assert.Equal(t, 22, snapshot.Manifest.CompileHost().Port)
	assert.Len(t, snapshot.Manifest.Hosts(), 1)
	topology, err := snapshot.Manifest.Topology()
	require.NoError(t, err)
	assert.Len(t, topology.Hosts, 1)
	// An explicit empty placement includes a build host in the deployment.
	body = strings.Replace(body, "[hosts.compile]", "compile = []\n[hosts.compile]", 1)
	snapshot, err = Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.Len(t, snapshot.Manifest.Hosts(), 2)
}

func TestLoadOptionalStrategyHost(t *testing.T) {
	root := t.TempDir()
	body := validManifest + `
strategy = ["strategy"]
[hosts.strategy]
address = "192.0.2.21"
ssh = { username = "ubuntu" }
`
	snapshot, err := Load(writeManifest(t, root, body, 0o600), root)
	require.NoError(t, err)
	assert.True(t, snapshot.Manifest.HasStrategyHost())
	assert.Equal(t, "strategy", snapshot.Manifest.StrategyHost().Name)
	assert.Equal(t, 22, snapshot.Manifest.StrategyHost().Port)
	require.Len(t, snapshot.Manifest.Hosts(), 2)
	assert.Equal(t, "strategy", snapshot.Manifest.Hosts()[1].Name)
}

func TestLoadRejectsInvalidManifest(t *testing.T) {
	tests := []struct{ name, body, want string }{
		{"missing admin password", strings.Replace(validManifest, `password = "admin-password"`, `password = ""`, 1), "admin.password"},
		{"bcrypt password too long", strings.Replace(validManifest, "admin-password", strings.Repeat("x", 73), 1), "72 bytes"},
		{"missing secret id", strings.Replace(validManifest, `secret_id = "secret-id"`, `secret_id = ""`, 1), "tencent_cloud.secret_id"},
		{"missing secret key", strings.Replace(validManifest, `secret_key = "secret-key"`, `secret_key = ""`, 1), "tencent_cloud.secret_key"},
		{"empty region", strings.Replace(validManifest, `secret_key = "secret-key"`, "secret_key = \"secret-key\"\nregion = \" \"", 1), "tencent_cloud.region"},
		{"missing address", strings.Replace(validManifest, `address = "192.0.2.10"`, `address = ""`, 1), "hosts.control.address"},
		{"address with scheme", strings.ReplaceAll(validManifest, "192.0.2.10", "tls://192.0.2.10"), "hosts.control.address"},
		{"address with path", strings.ReplaceAll(validManifest, "192.0.2.10", "192.0.2.10/nats"), "hosts.control.address"},
		{"address with port", strings.ReplaceAll(validManifest, "192.0.2.10", "192.0.2.10:4222"), "hosts.control.address"},
		{"invalid private address", strings.Replace(validManifest, "[hosts.control]", "[hosts.control]\nprivate_address = \"http://private\"", 1), "hosts.control.private_address"},
		{"invalid host id", strings.ReplaceAll(validManifest, "hosts.control", "hosts.Uppercase"), "canonical host IDs"},
		{"missing SSH username", strings.Replace(validManifest, `username = "ubuntu"`, `username = ""`, 1), "hosts.control.ssh.username"},
		{"explicit zero SSH port", strings.Replace(validManifest, "port = 22", "port = 0", 1), "hosts.control.ssh.port"},
		{"invalid SSH port", strings.Replace(validManifest, "port = 22", "port = 70000", 1), "hosts.control.ssh.port"},
		{"invalid TLS mode", strings.Replace(validManifest, "[hosts.control]", "[hosts.control]\ntls_mode = \"bad\"", 1), "hosts.control.tls_mode"},
		{"eventbus explicit zero port", strings.Replace(validManifest, "port = 4222", "port = 0", 1), "eventbus.port"},
		{"eventbus invalid port", strings.Replace(validManifest, "port = 4222", "port = 70000", 1), "eventbus.port"},
		{"eventbus tls disabled", strings.Replace(validManifest, "tls_enabled = true", "tls_enabled = false", 1), "eventbus.tls_enabled"},
		{"missing eventbus placement", strings.Replace(validManifest, `, "eventbus"`, "", 1), "placements must include eventbus"},
		{"protected component missing", strings.Replace(validManifest, `"console-proxy", `, "", 1), "protected component"},
		{"unknown placement", validManifest + "unknown = [\"trade\"]\n", "placements host ID"},
		{"automatic component", strings.Replace(validManifest, `"eventbus"`, `"eventbus", "host-gateway"`, 1), "automatic component"},
		{"unknown field", "unexpected = true\n" + validManifest, "unknown field"},
		{"unknown compile host", validManifest + "[compile_host]\nhost = \"missing\"\n", "compile_host.host"},
		{"duplicate address", validManifest + "[hosts.compute]\naddress = \"192.0.2.10\"\nssh = { username = \"ubuntu\" }\n", "duplicates hosts."},
	}
	for _, hook := range []string{"http://example.test/hook", "not-a-url", "https://open.feishu.cn/hook", "https://example.invalid/hook"} {
		tests = append(tests, struct{ name, body, want string }{"invalid webhook " + hook, strings.Replace(validManifest, `webhook_url = ""`, `webhook_url = "`+hook+`"`, 1), "notification.webhook_url"})
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

func TestLoadRejectsRetiredDeploymentKeys(t *testing.T) {
	for _, section := range []string{"control_host", "storage_host", "view_host", "strategy_host", "other_hosts"} {
		t.Run(section, func(t *testing.T) {
			root := t.TempDir()
			_, err := Load(writeManifest(t, root, validManifest+"["+section+"]\nname = \"control\"\n", 0o600), root)
			require.ErrorContains(t, err, "[hosts.<host-id>]")
			require.ErrorContains(t, err, "[placements]")
		})
	}
	for _, field := range []string{"access_id", "access_host", "access_private_host", "access_addresses", "access_ids", "storage_gateway_host", "storage_access_targets", "storage_access_target_nodes", "collector_rpc_gateway_target", "collector_gateway_target_node"} {
		t.Run(field, func(t *testing.T) {
			root := t.TempDir()
			body := validManifest + "[[scf_fetcher.spaces]]\nspace_id = \"crypto\"\n" + field + " = \"secret-retired-value\"\n"
			_, err := Load(writeManifest(t, root, body, 0o600), root)
			require.ErrorContains(t, err, "derived")
			assert.NotContains(t, err.Error(), "secret-retired-value")
		})
	}
	for name, body := range map[string]string{
		"IP catalog key":  strings.ReplaceAll(validManifest, "hosts.control", `hosts."192.0.2.10"`),
		"flat SSH fields": validManifest + "[hosts.compute]\naddress = \"192.0.2.11\"\nusername = \"secret-retired-value\"\n",
		"eventbus host":   strings.Replace(validManifest, "[eventbus]", "[eventbus]\nhost = \"secret-retired-value\"", 1),
		"compile name":    validManifest + "[compile_host]\nname = \"secret-retired-value\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			_, err := Load(writeManifest(t, root, body, 0o600), root)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "secret-retired-value")
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

// resolveSCFTimerFunctionCountsWithCapacities allocates Timer functions while
// reserving the publisher-created Invoke canary for each region.
func resolveSCFTimerFunctionCountsWithCapacities(cfg *SCFFetcherSpace, path string, maxFunctionsPerNamespace int, reservedByRegion map[string]int) error {
	auxiliary := 1
	return resolveSCFTimerFunctionCountsWithCapacityFunc(cfg, path, func(region string) int {
		return maxFunctionsPerNamespace - auxiliary - reservedByRegion[strings.ToLower(strings.TrimSpace(region))]
	})
}

func resolveSCFTimerFunctionCountsWithLimit(cfg *SCFFetcherSpace, path string, maxFunctionsPerRegion int) error {
	// Tests use a flat per-region limit; manifest validation uses the
	// regional capacity-aware allocator instead.
	return resolveSCFTimerFunctionCountsWithCapacities(cfg, path, maxFunctionsPerRegion+1, nil)
}

func resolveSCFTimerFunctionCounts(cfg *SCFFetcherSpace, path string) error {
	return resolveSCFTimerFunctionCountsWithLimit(cfg, path, DefaultSCFMaxFunctionsPerNamespace)
}

func validateSCFFetcherSpace(cfg *SCFFetcherSpace, path string) error {
	limits := defaultTencentSCFLimits()
	return validateSCFFetcherSpaceWithLimits(cfg, path, limits)
}
