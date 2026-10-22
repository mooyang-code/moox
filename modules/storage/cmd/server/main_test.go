package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	storagepebble "github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	primarystore "github.com/mooyang-code/moox/modules/storage/internal/service/primarystore"
	viewservice "github.com/mooyang-code/moox/modules/storage/internal/service/view"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/storagepolicy"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

type cleanupDatasetReader struct{}

func (cleanupDatasetReader) ListDatasets(context.Context, metadata.DatasetQuery) ([]*pb.Dataset, *pb.PageResult, error) {
	dataset := func(spaceID, datasetID, freq string) *pb.Dataset {
		return &pb.Dataset{SpaceId: spaceID, DatasetId: datasetID, DataNodeId: "node-a", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: freq, Status: "active"}
	}
	return []*pb.Dataset{
		dataset("crypto", "prices_1m", "1m"),
		dataset("crypto", "prices_1d", "1d"),
		dataset("stockcn", "equity_1m", "1m"),
	}, &pb.PageResult{Page: 1, Size: 1000}, nil
}

type cleanupNode struct {
	requests map[string]*pb.CleanupExpiredBucketsReq
}

func (*cleanupNode) UpsertFields(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
	return nil, nil
}
func (*cleanupNode) ReadRows(context.Context, *pb.ReadRowsReq) (*pb.ReadRowsRsp, error) {
	return nil, nil
}
func (*cleanupNode) GetNodeState(context.Context, *pb.GetNodeStateReq) (*pb.GetNodeStateRsp, error) {
	return nil, nil
}
func (*cleanupNode) WriteFactorRows(context.Context, *pb.WriteFactorRowsReq) (*pb.WriteFactorRowsRsp, error) {
	return nil, nil
}
func (n *cleanupNode) CleanupExpiredBuckets(_ context.Context, req *pb.CleanupExpiredBucketsReq) (*pb.CleanupExpiredBucketsRsp, error) {
	n.requests[req.GetSpaceId()+"/"+req.GetDatasetId()] = req
	return &pb.CleanupExpiredBucketsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, DeletedRanges: 3}, nil
}

func TestDatasetCleanupUsesPolicyRetentionBySpaceAndFrequency(t *testing.T) {
	node := &cleanupNode{requests: map[string]*pb.CleanupExpiredBucketsReq{}}
	metrics, err := observability.NewCleanupMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	cleanup := datasetCleanup{
		reader: cleanupDatasetReader{},
		resolver: func(context.Context, string, string) (pb.DataNodeRuntimeService, error) {
			return node, nil
		},
		auth: &pb.AuthInfo{AppId: "primary", AppKey: "key"}, retention: storagepolicy.Default().Retention, metrics: metrics,
	}
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	if err := cleanup.run(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		// crypto 1m keeps 8 days, stockcn 1m 60 days; whole day buckets.
		"crypto/prices_1m":  "2026-07-12T00:00:00.000000000Z",
		"stockcn/equity_1m": "2026-05-21T00:00:00.000000000Z",
	}
	if len(node.requests) != len(want) {
		t.Fatalf("cleanup requests = %v; a forever (1d) Dataset must not be cleaned", node.requests)
	}
	for key, cutoff := range want {
		if got := node.requests[key].GetBeforeBucketStart(); got != cutoff {
			t.Fatalf("%s cutoff = %q, want %q", key, got, cutoff)
		}
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

	got, err := storageEventBusConfig([]string{"nats://127.0.0.1:4222"}, "storage-view")
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

	got, err := storageEventBusConfig([]string{"nats://127.0.0.1:4222"}, "storage-view")
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
	if _, err := loadStoragePolicy(); err == nil || !strings.Contains(err.Error(), "storage.policy_file is required") {
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
	loaded, err := loadStoragePolicy()
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Retention.Defaults["1m"]; got != "336h" {
		t.Fatalf("loaded 1m retention = %q, want the file's 336h", got)
	}

	if err := os.WriteFile(policyPath, []byte(`{"retention":{"defaults":{"1m":"7d"}},"view":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadStoragePolicy(); err == nil {
		t.Fatal("an incomplete policy file was accepted")
	}
}

func TestLoadStoragePolicyDefaultsWithoutStorageConfig(t *testing.T) {
	t.Setenv("MOOX_STORAGE_CONFIG", "")
	policy, err := loadStoragePolicy()
	if err != nil {
		t.Fatal(err)
	}
	if policy.View.Bars != 10000 || policy.View.TrimBars != 12000 || policy.Retention.Defaults["1m"] != "192h" {
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

func TestStorageViewRebuildSettingsUsesConfiguredValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage.yaml")
	if err := os.WriteFile(path, []byte("storage:\n  view:\n    rebuild_max_pending: 5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOOX_STORAGE_CONFIG", path)
	maxPending, idleChecks, maxPendingConfigured, idleChecksConfigured, err := storageViewRebuildSettings()
	if err != nil {
		t.Fatal(err)
	}
	if maxPending != 5 || idleChecks != 3 {
		t.Fatalf("gate settings = %d/%d", maxPending, idleChecks)
	}
	if !maxPendingConfigured || idleChecksConfigured {
		t.Fatal("configured flags do not match the file")
	}
}

func TestStorageViewRebuildSettingsAllowsExplicitZeroMaxPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage.yaml")
	if err := os.WriteFile(path, []byte("storage:\n  view:\n    rebuild_max_pending: 0\n    rebuild_idle_checks: 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOOX_STORAGE_CONFIG", path)
	if _, _, _, _, err := storageViewRebuildSettings(); err == nil {
		t.Fatal("accepted explicit zero idle checks")
	}
	if err := os.WriteFile(path, []byte("storage:\n  view:\n    rebuild_max_pending: 0\n    rebuild_idle_checks: 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	maxPending, idleChecks, maxPendingConfigured, idleChecksConfigured, err := storageViewRebuildSettings()
	if err != nil || maxPending != 0 || idleChecks != 3 || !maxPendingConfigured || !idleChecksConfigured {
		t.Fatalf("explicit zero max pending settings = %d/%d configured=%v/%v err=%v", maxPending, idleChecks, maxPendingConfigured, idleChecksConfigured, err)
	}
}

func TestStorageEventBusConfigRejectsInvalidReconnectBuffer(t *testing.T) {
	t.Setenv("MOOX_STORAGE_EVENTBUS_CREDENTIAL_FILE", "")
	t.Setenv("MOOX_STORAGE_CONFIG", "")
	for _, value := range []string{"garbage", "-1"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("MOOX_EVENTBUS_RECONNECT_BUFFER_BYTES", value)
			if _, err := storageEventBusConfig([]string{"nats://127.0.0.1:4222"}, "storage-view"); err == nil {
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

	got, err := storageEventBusConfig([]string{"nats://127.0.0.1:4222"}, "storage-node")
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "storage-eventbus" || got.Password != "storage-secret" {
		t.Fatalf("credential config = username %q/password %q", got.Username, got.Password)
	}
}

func TestStorageViewConsumerOptionsUseCodeOwnedDeliverySettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "storage.yaml")
	if err := os.WriteFile(path, []byte("storage:\n  eventbus:\n    credential_file: \"\"\n  view:\n    fetch_batch: 1\n    max_workers: 1\n    ordering: dataset\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOOX_STORAGE_CONFIG", path)
	opts, err := storageViewConsumerOptions()
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.PartitionConfigs) != 2 {
		t.Fatalf("consumer partitions = %+v", opts.PartitionConfigs)
	}
	if opts.PartitionConfigs[0].PartitionID != "factor" || opts.PartitionConfigs[0].Consumer != "storage_view_factor" || len(opts.PartitionConfigs[0].FilterSubjects) != 0 || opts.PartitionConfigs[0].FetchBatch != 1 || opts.PartitionConfigs[0].MaxWorkers != 1 || opts.PartitionConfigs[0].MaxAckPending != 1 || len(opts.PartitionConfigs[0].DatasetRoutes) != 0 {
		t.Fatalf("factor consumer options = %+v", opts.PartitionConfigs[0])
	}
	if opts.PartitionConfigs[1].PartitionID != "misc" || opts.PartitionConfigs[1].Consumer != "storage_view_misc" || opts.PartitionConfigs[1].Ordering != "dataset" || opts.PartitionConfigs[1].DeliverPolicy != "new" {
		t.Fatalf("misc consumer options = %+v", opts.PartitionConfigs[1])
	}
}

func TestStorageViewConsumerOptionsAllowExplicitDynamicSpaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "storage.yaml")
	if err := os.WriteFile(path, []byte("storage:\n  view:\n    consumer_partitions:\n      - id: misc\n        durable: storage_view_misc\n        routes:\n          - space_id: crypto\n            dataset_ids: [\"*\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOOX_STORAGE_CONFIG", path)
	t.Setenv("MOOX_STORAGE_VIEW_ALLOWED_DATASET_SPACES", " factor_e2e, crypto, factor_e2e ")
	opts, err := storageViewConsumerOptions()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := opts.AllowedDatasetSpaces, []string{"crypto", "factor_e2e"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("allowed dynamic spaces = %v, want %v", got, want)
	}
}

func TestStorageViewConfigFilesBindFactorResultsDynamically(t *testing.T) {
	for _, path := range []string{
		filepath.Join("..", "..", "config", "storage.yaml"),
		filepath.Join("..", "..", "config", "storage_view", "trpc_go.yaml"),
	} {
		t.Run(path, func(t *testing.T) {
			t.Setenv("MOOX_STORAGE_CONFIG", path)
			opts, err := storageViewConsumerOptions()
			if err != nil {
				t.Fatal(err)
			}

			stockRoutes := make(map[string]bool)
			for _, partition := range opts.PartitionConfigs {
				for _, route := range partition.DatasetRoutes {
					if route.SpaceID == "stockcn" {
						stockRoutes[route.DatasetID] = true
					}
				}
				if partition.PartitionID == "factor" && len(partition.DatasetRoutes) != 0 {
					t.Fatalf("factor results bind dynamically; static routes = %+v", partition.DatasetRoutes)
				}
				if partition.PartitionID == "factor" && (partition.FetchBatch != 1 || partition.MaxWorkers != 1 || partition.MaxAckPending != 1) {
					t.Fatalf("factor delivery budget = %+v, want serialized 1/1/1", partition)
				}
			}
			if len(stockRoutes) != 1 || !stockRoutes["*"] {
				t.Fatalf("stockcn routes = %+v", stockRoutes)
			}
		})
	}
}

func TestStripWildcardConsumerRoutesKeepsStaticMiscDurableStable(t *testing.T) {
	opts := viewservice.EventConsumerOptions{PartitionConfigs: []viewservice.EventConsumerOptions{
		{PartitionID: "misc", Consumer: "storage_view_misc", DatasetRoutes: []viewservice.DatasetRoute{
			{SpaceID: "crypto", DatasetID: "*"},
			{SpaceID: "stockcn", DatasetID: "dataset_stockcn_equity_kline_1m"},
		}},
	}}
	stripWildcardConsumerRoutes(&opts)
	if got := opts.PartitionConfigs[0].DatasetRoutes; len(got) != 1 || got[0].SpaceID != "stockcn" || got[0].DatasetID != "dataset_stockcn_equity_kline_1m" {
		t.Fatalf("static routes = %+v, want only exact route", got)
	}
}

func TestStripMiscConsumerPartitionLeavesExactConsumers(t *testing.T) {
	opts := viewservice.EventConsumerOptions{PartitionConfigs: []viewservice.EventConsumerOptions{
		{PartitionID: "factor", Consumer: "storage_view_factor"},
		{PartitionID: "misc", Consumer: "storage_view_misc"},
	}}
	stripMiscConsumerPartition(&opts)
	if len(opts.PartitionConfigs) != 1 || opts.PartitionConfigs[0].Consumer != "storage_view_factor" {
		t.Fatalf("static partitions = %+v, want misc removed", opts.PartitionConfigs)
	}
}

type resolverSnapshot struct {
	dataset *pb.Dataset
	node    *pb.DataNode
}

func (s resolverSnapshot) GetDataset(spaceID, datasetID string) (*pb.Dataset, bool) {
	if s.dataset == nil || s.dataset.GetSpaceId() != spaceID || s.dataset.GetDatasetId() != datasetID {
		return nil, false
	}
	return s.dataset, true
}

func (s resolverSnapshot) GetDataNode(nodeID string) (*pb.DataNode, bool) {
	if s.node == nil || s.node.GetNodeId() != nodeID {
		return nil, false
	}
	return s.node, true
}

func (resolverSnapshot) ListDatasetColumns(string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return nil, nil, nil
}

type resolverRuntime struct {
	target string
	writes int
	reads  int
}

func (r *resolverRuntime) UpsertFields(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
	r.writes++
	return &pb.UpsertFieldsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}, nil
}
func (r *resolverRuntime) ReadRows(context.Context, *pb.ReadRowsReq) (*pb.ReadRowsRsp, error) {
	r.reads++
	return &pb.ReadRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}, nil
}
func (*resolverRuntime) GetNodeState(context.Context, *pb.GetNodeStateReq) (*pb.GetNodeStateRsp, error) {
	return &pb.GetNodeStateRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}, nil
}
func (*resolverRuntime) CleanupExpiredBuckets(context.Context, *pb.CleanupExpiredBucketsReq) (*pb.CleanupExpiredBucketsRsp, error) {
	return &pb.CleanupExpiredBucketsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}, nil
}
func (*resolverRuntime) WriteFactorRows(context.Context, *pb.WriteFactorRowsReq) (*pb.WriteFactorRowsRsp, error) {
	return &pb.WriteFactorRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}, nil
}

func TestDataNodeClientTimeoutCoversMaintenanceRPCs(t *testing.T) {
	if dataNodeClientTimeout != 5*time.Minute {
		t.Fatalf("dataNodeClientTimeout = %s, want 5m", dataNodeClientTimeout)
	}
}

func TestDataNodeProxyAdapterIncludesPeriodRuntime(t *testing.T) {
	proxy := newDataNodeProxyAdapter()
	if proxy.proxy == nil || proxy.adminProxy == nil || proxy.markerProxy == nil || proxy.periodProxy == nil || proxy.rangeProxy == nil {
		t.Fatalf("data node proxy adapter is missing a runtime proxy: %+v", proxy)
	}
}

func startPeriodRuntimeNode(t *testing.T, nodeID, secret string) (*datanode.Service, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	node, err := datanode.NewService(datanode.Options{NodeID: nodeID, AuthSecret: secret, Pebble: storagepebble.Options{
		NodeID: nodeID, Path: filepath.Join(t.TempDir(), nodeID),
	}})
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	dn := server.New(
		server.WithTransport(transport.NewServerTransport()),
		server.WithServiceName("trpc.moox.storage.DataNodePeriodRuntime"),
		server.WithProtocol("trpc"),
		server.WithNetwork("tcp"),
		server.WithListener(listener),
		server.WithServerAsync(false),
	)
	pb.RegisterDataNodePeriodRuntimeService(dn, node)
	serveErr := make(chan error, 1)
	go func() { serveErr <- dn.Serve() }()
	t.Cleanup(func() {
		done := make(chan struct{}, 1)
		if err := dn.Close(done); err != nil {
			t.Errorf("close DataNode %s test service: %v", nodeID, err)
		}
		<-done
		if err := <-serveErr; err != nil {
			t.Errorf("serve DataNode %s test service: %v", nodeID, err)
		}
		if err := node.Close(); err != nil {
			t.Errorf("close DataNode %s store: %v", nodeID, err)
		}
	})
	return node, listener.Addr().String()
}

func TestResolveDataNodeUsesActiveDatasetNodeAndTargetOnly(t *testing.T) {
	base := resolverSnapshot{
		dataset: &pb.Dataset{SpaceId: "space", DatasetId: "dataset", DataNodeId: "node-a", Status: "active"},
		node:    &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "ip://127.0.0.1:20107"},
	}
	resolver := newDataNodeResolver(func() metadata.RequestSnapshot { return base }, func(target string) pb.DataNodeRuntimeService { return &resolverRuntime{target: target} })
	if _, err := resolver(context.Background(), "space", "dataset"); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		dataset *pb.Dataset
		node    *pb.DataNode
	}{
		{name: "unknown dataset", dataset: nil, node: base.node},
		{name: "disabled dataset", dataset: &pb.Dataset{SpaceId: "space", DatasetId: "dataset", DataNodeId: "node-a", Status: "disabled"}, node: base.node},
		{name: "missing node", dataset: base.dataset, node: nil},
		{name: "disabled node", dataset: base.dataset, node: &pb.DataNode{NodeId: "node-a", Status: "disabled", ServiceTarget: "ip://127.0.0.1:20107"}},
		{name: "empty target", dataset: base.dataset, node: &pb.DataNode{NodeId: "node-a", Status: "active"}},
		{name: "malformed target", dataset: base.dataset, node: &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "http://127.0.0.1:20107"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := resolveDataNodeFromSnapshot(resolverSnapshot{dataset: tc.dataset, node: tc.node}, "space", "dataset")
			if err == nil {
				t.Fatal("expected routing failure")
			}
		})
	}
}

func TestDataNodeResolverReplacesProxyWhenTargetChanges(t *testing.T) {
	first := resolverSnapshot{
		dataset: &pb.Dataset{SpaceId: "space", DatasetId: "dataset", DataNodeId: "node-a", Status: "active"},
		node:    &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "ip://127.0.0.1:20107"},
	}
	second := resolverSnapshot{dataset: first.dataset, node: &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "ip://127.0.0.1:20108"}}
	current := metadata.RequestSnapshot(first)
	var targets []string
	resolver := newDataNodeResolver(func() metadata.RequestSnapshot { return current }, func(target string) pb.DataNodeRuntimeService {
		targets = append(targets, target)
		return &resolverRuntime{target: target}
	})
	firstProxy, err := resolver(context.Background(), "space", "dataset")
	if err != nil {
		t.Fatal(err)
	}
	current = second
	secondProxy, err := resolver(context.Background(), "space", "dataset")
	if err != nil {
		t.Fatal(err)
	}
	firstRuntime := firstProxy.(*resolverRuntime)
	secondRuntime := secondProxy.(*resolverRuntime)
	if firstRuntime.target == secondRuntime.target || fmt.Sprint(targets) != "[ip://127.0.0.1:20107 ip://127.0.0.1:20108]" {
		t.Fatalf("proxy refresh failed: first=%p second=%p targets=%v", firstProxy, secondProxy, targets)
	}
}

func TestPrimaryReadWriteUsePublishedSnapshotAndFakeRuntime(t *testing.T) {
	snapshot := resolverSnapshot{
		dataset: &pb.Dataset{SpaceId: "space", DatasetId: "dataset", DataNodeId: "node-a", Status: "active"},
		node:    &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "ip://127.0.0.1:20107"},
	}
	var runtime *resolverRuntime
	resolver := newDataNodeResolver(func() metadata.RequestSnapshot { return snapshot }, func(target string) pb.DataNodeRuntimeService {
		runtime = &resolverRuntime{target: target}
		return runtime
	})
	svc, err := primarystore.New(primarystore.Options{Resolver: resolver.primaryStore()})
	if err != nil {
		t.Fatal(err)
	}
	key := &pb.RowKey{SpaceId: "space", DatasetId: "dataset", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "record", Version: "1"}}}
	write, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller"},
		Rows:     []*pb.RowFieldUpsert{{Key: key, Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "ok"}}}}}},
	})
	if err != nil || write.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("write=%v err=%v", write, err)
	}
	read, err := svc.ReadRows(context.Background(), &pb.PrimaryReadRowsReq{AuthInfo: &pb.AuthInfo{AppId: "caller"}, Keys: []*pb.RowKey{key}, ColumnNames: []string{"close"}})
	if err != nil || read.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("read=%v err=%v", read, err)
	}
	if runtime == nil || runtime.writes != 1 || runtime.reads != 1 || runtime.target != "ip://127.0.0.1:20107" {
		t.Fatalf("runtime=%+v", runtime)
	}
}

func TestStorageViewGatewayRequiresConfigurationAndItsOwnIdentity(t *testing.T) {
	t.Setenv("MOOX_STORAGE_CONFIG", "")
	if _, err := openStorageViewGateway(t.TempDir()); err == nil || !strings.Contains(err.Error(), "MOOX_STORAGE_CONFIG is required") {
		t.Fatalf("missing config error: %v", err)
	}
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte("gateway_client:\n  caller: console\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOOX_STORAGE_CONFIG", path)
	if _, err := openStorageViewGateway(t.TempDir()); err == nil || !strings.Contains(err.Error(), "caller must be storage-view") {
		t.Fatalf("wrong caller error: %v", err)
	}
}
