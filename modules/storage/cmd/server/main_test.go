package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	storagepebble "github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	primarystore "github.com/mooyang-code/moox/modules/storage/internal/service/primarystore"
	viewservice "github.com/mooyang-code/moox/modules/storage/internal/service/view"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/storagepolicy"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
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
func (*cleanupNode) ReadFields(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
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
		// crypto 1m keeps 7 days, stockcn 1m 45 days; whole day buckets.
		"crypto/prices_1m":  "2026-07-13T00:00:00.000000000Z",
		"stockcn/equity_1m": "2026-06-05T00:00:00.000000000Z",
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
	if policy.View.Bars != 5000 || policy.View.TrimBars != 6000 || policy.Retention.Defaults["1m"] != "168h" {
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
	if len(opts.PartitionConfigs) != 3 {
		t.Fatalf("consumer partitions = %+v", opts.PartitionConfigs)
	}
	if opts.PartitionConfigs[0].PartitionID != "factor" || opts.PartitionConfigs[0].Consumer != "storage_view_factor" || len(opts.PartitionConfigs[0].FilterSubjects) != 0 || opts.PartitionConfigs[0].FetchBatch != 1 || opts.PartitionConfigs[0].MaxWorkers != 1 || opts.PartitionConfigs[0].MaxAckPending != 1 || len(opts.PartitionConfigs[0].DatasetRoutes) != 0 {
		t.Fatalf("factor consumer options = %+v", opts.PartitionConfigs[0])
	}
	if opts.PartitionConfigs[1].PartitionID != "system_metrics" || opts.PartitionConfigs[1].Consumer != "storage_view_metrics" || len(opts.PartitionConfigs[1].FilterSubjects) != 4 || opts.PartitionConfigs[1].AckWaitMS != 120000 || opts.PartitionConfigs[1].Ordering != "dataset" || opts.PartitionConfigs[1].DeliverPolicy != "new" {
		t.Fatalf("metrics consumer options = %+v", opts.PartitionConfigs[1])
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
		{PartitionID: "system_metrics", Consumer: "storage_view_metrics"},
		{PartitionID: "misc", Consumer: "storage_view_misc"},
	}}
	stripMiscConsumerPartition(&opts)
	if len(opts.PartitionConfigs) != 1 || opts.PartitionConfigs[0].Consumer != "storage_view_metrics" {
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
func (r *resolverRuntime) ReadFields(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
	r.reads++
	return &pb.ReadFieldsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}, nil
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
	if proxy.proxy == nil || proxy.adminProxy == nil || proxy.markerProxy == nil || proxy.periodProxy == nil || proxy.historyProxy == nil {
		t.Fatalf("data node proxy adapter is missing a runtime proxy: %+v", proxy)
	}
}

func TestDataNodeProxyAdapterPeriodMethodsAreNilSafe(t *testing.T) {
	var nilAdapter *dataNodeProxyAdapter
	if _, err := nilAdapter.GetDatasetPeriodStatus(context.Background(), nil); err == nil {
		t.Fatal("nil adapter GetDatasetPeriodStatus returned no error")
	}
	if _, err := nilAdapter.RecordDatasetPeriodFailures(context.Background(), nil); err == nil {
		t.Fatal("nil adapter RecordDatasetPeriodFailures returned no error")
	}
	if _, err := (&dataNodeProxyAdapter{}).GetDatasetPeriodStatus(context.Background(), nil); err == nil {
		t.Fatal("missing period proxy GetDatasetPeriodStatus returned no error")
	}
	if _, err := (&dataNodeProxyAdapter{}).RecordDatasetPeriodFailures(context.Background(), nil); err == nil {
		t.Fatal("missing period proxy RecordDatasetPeriodFailures returned no error")
	}
}

type periodProxyForwardingStub struct {
	ctx       context.Context
	req       any
	err       error
	recordRsp *pb.RecordDatasetPeriodFailuresRsp
	statusRsp *pb.GetDatasetPeriodStatusRsp
}

func (*periodProxyForwardingStub) EnsureDatasetPeriod(context.Context, *pb.EnsureDatasetPeriodReq, ...client.Option) (*pb.EnsureDatasetPeriodRsp, error) {
	return nil, nil
}

func (*periodProxyForwardingStub) CommitTimeSeriesBatch(context.Context, *pb.CommitTimeSeriesBatchReq, ...client.Option) (*pb.CommitTimeSeriesBatchRsp, error) {
	return nil, nil
}

func (p *periodProxyForwardingStub) RecordDatasetPeriodFailures(ctx context.Context, req *pb.RecordDatasetPeriodFailuresReq, _ ...client.Option) (*pb.RecordDatasetPeriodFailuresRsp, error) {
	p.ctx, p.req = ctx, req
	return p.recordRsp, p.err
}

func (p *periodProxyForwardingStub) GetDatasetPeriodStatus(ctx context.Context, req *pb.GetDatasetPeriodStatusReq, _ ...client.Option) (*pb.GetDatasetPeriodStatusRsp, error) {
	p.ctx, p.req = ctx, req
	return p.statusRsp, p.err
}

func TestDataNodeProxyAdapterForwardsPeriodStatusAndFailureCalls(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "trace-id")
	wantErr := errors.New("period proxy failed")
	statusReq := &pb.GetDatasetPeriodStatusReq{Expectation: &pb.DatasetPeriodExpectation{SpaceId: "space"}}
	statusRsp := &pb.GetDatasetPeriodStatusRsp{Status: "waiting"}
	statusProxy := &periodProxyForwardingStub{err: wantErr, statusRsp: statusRsp}
	gotStatus, gotErr := (&dataNodeProxyAdapter{periodProxy: statusProxy, nodeID: "node-a"}).GetDatasetPeriodStatus(ctx, statusReq)
	statusExpected := proto.Clone(statusReq).(*pb.GetDatasetPeriodStatusReq)
	statusExpected.NodeId = "node-a"
	if gotStatus != statusRsp || gotErr != wantErr || statusProxy.ctx != ctx || !proto.Equal(statusExpected, statusProxy.req.(proto.Message)) || statusReq.GetNodeId() != "" || statusProxy.ctx.Value(contextKey{}) != "trace-id" {
		t.Fatalf("GetDatasetPeriodStatus forwarding: rsp=%p err=%v ctx=%v req=%p", gotStatus, gotErr, statusProxy.ctx, statusProxy.req)
	}

	recordReq := &pb.RecordDatasetPeriodFailuresReq{SeriesIndexes: []uint32{1, 3}}
	recordRsp := &pb.RecordDatasetPeriodFailuresRsp{PeriodStatus: "degraded"}
	recordProxy := &periodProxyForwardingStub{err: wantErr, recordRsp: recordRsp}
	gotRecord, gotErr := (&dataNodeProxyAdapter{periodProxy: recordProxy, nodeID: "node-a"}).RecordDatasetPeriodFailures(ctx, recordReq)
	recordExpected := proto.Clone(recordReq).(*pb.RecordDatasetPeriodFailuresReq)
	recordExpected.NodeId = "node-a"
	if gotRecord != recordRsp || gotErr != wantErr || recordProxy.ctx != ctx || !proto.Equal(recordExpected, recordProxy.req.(proto.Message)) || recordReq.GetNodeId() != "" || recordProxy.ctx.Value(contextKey{}) != "trace-id" {
		t.Fatalf("RecordDatasetPeriodFailures forwarding: rsp=%p err=%v ctx=%v req=%p", gotRecord, gotErr, recordProxy.ctx, recordProxy.req)
	}
}

type periodRPCContractService struct {
	ensure *pb.EnsureDatasetPeriodReq
	status *pb.GetDatasetPeriodStatusReq
	commit *pb.CommitTimeSeriesBatchReq
	record *pb.RecordDatasetPeriodFailuresReq
}

func (s *periodRPCContractService) EnsureDatasetPeriod(_ context.Context, req *pb.EnsureDatasetPeriodReq) (*pb.EnsureDatasetPeriodRsp, error) {
	s.ensure = req
	return &pb.EnsureDatasetPeriodRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Status: "waiting", DeadlineAt: 400}, nil
}

func (s *periodRPCContractService) GetDatasetPeriodStatus(_ context.Context, req *pb.GetDatasetPeriodStatusReq) (*pb.GetDatasetPeriodStatusRsp, error) {
	s.status = req
	return &pb.GetDatasetPeriodStatusRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Status: "complete", SeriesHash: "returned-hash", ExpectedCount: 2, DeadlineAt: 401}, nil
}

func (s *periodRPCContractService) CommitTimeSeriesBatch(_ context.Context, req *pb.CommitTimeSeriesBatchReq) (*pb.CommitTimeSeriesBatchRsp, error) {
	s.commit = req
	return &pb.CommitTimeSeriesBatchRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Keys: []*pb.RowKey{req.GetItems()[0].GetRow().GetKey()}, PeriodStatus: "complete"}, nil
}

func (s *periodRPCContractService) RecordDatasetPeriodFailures(_ context.Context, req *pb.RecordDatasetPeriodFailuresReq) (*pb.RecordDatasetPeriodFailuresRsp, error) {
	s.record = req
	return &pb.RecordDatasetPeriodFailuresRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, PeriodStatus: "degraded"}, nil
}

func TestDataNodeResolverPeriodRPCContract(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()

	dn := server.New(
		server.WithTransport(transport.NewServerTransport()),
		server.WithServiceName("trpc.moox.storage.DataNodePeriodRuntime"),
		server.WithProtocol("trpc"),
		server.WithNetwork("tcp"),
		server.WithListener(listener),
		server.WithServerAsync(false),
	)
	backend := &periodRPCContractService{}
	pb.RegisterDataNodePeriodRuntimeService(dn, backend)
	serveErr := make(chan error, 1)
	go func() { serveErr <- dn.Serve() }()
	t.Cleanup(func() {
		done := make(chan struct{}, 1)
		if err := dn.Close(done); err != nil {
			t.Errorf("close DataNode test service: %v", err)
		}
		<-done
		if err := <-serveErr; err != nil {
			t.Errorf("serve DataNode test service: %v", err)
		}
	})

	snapshot := resolverSnapshot{
		dataset: &pb.Dataset{SpaceId: "space", DatasetId: "dataset", DataNodeId: "node-a", Status: "active"},
		node:    &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "ip://" + address},
	}
	resolver := newDataNodeResolver(func() metadata.RequestSnapshot { return snapshot }, nil)
	runtime, err := resolver(context.Background(), "space", "dataset")
	if err != nil {
		t.Fatal(err)
	}
	periodRuntime, ok := runtime.(pb.DataNodePeriodRuntimeService)
	if !ok {
		t.Fatalf("resolved runtime %T does not implement DataNodePeriodRuntimeService", runtime)
	}

	auth := &pb.AuthInfo{AppId: "collector", AppKey: "signed-key"}
	expectation := &pb.DatasetPeriodExpectation{
		SpaceId: "space", DatasetId: "dataset", Frequency: "1h", PeriodTime: 123,
		SeriesHash: "expected-hash", ExpectedCount: 1,
		SeriesSnapshot: []*pb.DatasetPeriodSeries{{SeriesIndex: 0, SubjectId: "BTC-USDT", SeriesTag: "venue:binance"}},
	}
	row := &pb.RowFieldUpsert{
		Key:    &pb.RowKey{SpaceId: "space", DatasetId: "dataset", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1h", DataTime: "2026-09-30T00:00:00Z", SeriesTag: "venue:binance"}}},
		Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 101.25}}}},
	}
	ensureReq := &pb.EnsureDatasetPeriodReq{AuthInfo: auth, Expectation: expectation}
	statusReq := &pb.GetDatasetPeriodStatusReq{AuthInfo: auth, Expectation: expectation}
	commitReq := &pb.CommitTimeSeriesBatchReq{
		AuthInfo: auth, Expectation: expectation,
		Items: []*pb.TimeSeriesBatchRow{{SeriesIndex: 0, Row: row}}, SourceEventId: "event-1", WriteSource: "collector",
	}
	recordReq := &pb.RecordDatasetPeriodFailuresReq{AuthInfo: auth, Expectation: expectation, SeriesIndexes: []uint32{0, 2}}

	ensured, err := periodRuntime.EnsureDatasetPeriod(context.Background(), ensureReq)
	if err != nil || ensured.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || ensured.GetStatus() != "waiting" || ensured.GetDeadlineAt() != 400 {
		t.Fatalf("EnsureDatasetPeriod response=%v err=%v", ensured, err)
	}
	status, err := periodRuntime.GetDatasetPeriodStatus(context.Background(), statusReq)
	if err != nil || status.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || status.GetStatus() != "complete" || status.GetSeriesHash() != "returned-hash" || status.GetExpectedCount() != 2 || status.GetDeadlineAt() != 401 {
		t.Fatalf("GetDatasetPeriodStatus response=%v err=%v", status, err)
	}
	committed, err := periodRuntime.CommitTimeSeriesBatch(context.Background(), commitReq)
	if err != nil || committed.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || committed.GetPeriodStatus() != "complete" || len(committed.GetKeys()) != 1 || !proto.Equal(committed.GetKeys()[0], row.GetKey()) {
		t.Fatalf("CommitTimeSeriesBatch response=%v err=%v", committed, err)
	}
	recorded, err := periodRuntime.RecordDatasetPeriodFailures(context.Background(), recordReq)
	if err != nil || recorded.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || recorded.GetPeriodStatus() != "degraded" {
		t.Fatalf("RecordDatasetPeriodFailures response=%v err=%v", recorded, err)
	}

	boundEnsure := proto.Clone(ensureReq).(*pb.EnsureDatasetPeriodReq)
	boundEnsure.NodeId = "node-a"
	boundStatus := proto.Clone(statusReq).(*pb.GetDatasetPeriodStatusReq)
	boundStatus.NodeId = "node-a"
	boundCommit := proto.Clone(commitReq).(*pb.CommitTimeSeriesBatchReq)
	boundCommit.NodeId = "node-a"
	boundRecord := proto.Clone(recordReq).(*pb.RecordDatasetPeriodFailuresReq)
	boundRecord.NodeId = "node-a"
	for name, pair := range map[string][2]proto.Message{
		"ensure": {boundEnsure, backend.ensure},
		"status": {boundStatus, backend.status},
		"commit": {boundCommit, backend.commit},
		"record": {boundRecord, backend.record},
	} {
		if pair[1] == nil || !proto.Equal(pair[0], pair[1]) {
			t.Errorf("%s request forwarded as %v, want %v", name, pair[1], pair[0])
		}
	}
}

func TestPrimaryPeriodWrongDataNodeTargetFailsClosed(t *testing.T) {
	const secret = "period-routing-secret"
	nodeA, addressA := startPeriodRuntimeNode(t, "node-a", secret)
	nodeB, addressB := startPeriodRuntimeNode(t, "node-b", secret)
	if addressA == addressB {
		t.Fatal("test DataNodes must have distinct targets")
	}
	period := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	expectation := &pb.DatasetPeriodExpectation{
		SpaceId: "space", DatasetId: "dataset", Frequency: "1m", PeriodTime: period.Unix(),
		SeriesHash: "hash", ExpectedCount: 1, DeadlineAt: period.Add(time.Hour).Unix(),
		SeriesSnapshot: []*pb.DatasetPeriodSeries{{SeriesIndex: 0, SubjectId: "BTC-USDT", SeriesTag: "venue:binance"}},
	}
	row := &pb.RowFieldUpsert{
		Key:    &pb.RowKey{SpaceId: "space", DatasetId: "dataset", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: period.Format(time.RFC3339Nano), SeriesTag: "venue:binance"}}},
		Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}}}},
	}
	// Metadata says node-a, while the registered target is actually node-b.
	snapshot := resolverSnapshot{
		dataset: &pb.Dataset{
			SpaceId: "space", DatasetId: "dataset", DataNodeId: "node-a", Status: "active",
			DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1m",
			Attributes: map[string]string{"owner_module": "collector", "dataset_role": "raw_collection"},
		},
		node: &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "ip://" + addressB},
	}
	resolver := newDataNodeResolver(func() metadata.RequestSnapshot { return snapshot }, nil)
	primary, err := primarystore.New(primarystore.Options{
		Resolver: resolver,
		Snapshot: func() metadata.RequestSnapshot { return snapshot },
		AuthSigner: func(*pb.AuthInfo) (*pb.AuthInfo, error) {
			return &pb.AuthInfo{AppId: "primary", AppKey: datanode.ServiceAuthKey(secret, "primary")}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	auth := &pb.AuthInfo{AppId: "collector", AppKey: "caller-key"}
	ensure, err := primary.EnsureDatasetPeriod(context.Background(), &pb.PrimaryEnsureDatasetPeriodReq{AuthInfo: auth, Expectation: expectation})
	if err != nil || ensure.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM || !strings.Contains(ensure.GetRetInfo().GetMsg(), "node_id does not match") {
		t.Fatalf("wrong-target Ensure response=%v err=%v", ensure, err)
	}
	commit, err := primary.CommitTimeSeriesBatch(context.Background(), &pb.PrimaryCommitTimeSeriesBatchReq{
		AuthInfo: auth, Expectation: expectation, Items: []*pb.TimeSeriesBatchRow{{SeriesIndex: 0, Row: row}}, WriteSource: "collector",
	})
	if err != nil || commit.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM || !strings.Contains(commit.GetRetInfo().GetMsg(), "node_id does not match") {
		t.Fatalf("wrong-target Commit response=%v err=%v", commit, err)
	}
	status, err := primary.GetDatasetPeriodStatus(context.Background(), &pb.PrimaryGetDatasetPeriodStatusReq{AuthInfo: auth, Expectation: expectation})
	if err != nil || status.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM || !strings.Contains(status.GetRetInfo().GetMsg(), "node_id does not match") {
		t.Fatalf("wrong-target GetStatus response=%v err=%v", status, err)
	}
	failure, err := primary.RecordDatasetPeriodFailures(context.Background(), &pb.PrimaryRecordDatasetPeriodFailuresReq{AuthInfo: auth, Expectation: expectation, SeriesIndexes: []uint32{0}})
	if err != nil || failure.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM || !strings.Contains(failure.GetRetInfo().GetMsg(), "node_id does not match") {
		t.Fatalf("wrong-target RecordFailures response=%v err=%v", failure, err)
	}

	storageExpectation := storagepebble.DatasetPeriodExpectation{
		SpaceID: "space", DatasetID: "dataset", Frequency: "1m", PeriodTime: expectation.GetPeriodTime(),
		SeriesHash: "hash", ExpectedCount: 1, DeadlineAt: expectation.GetDeadlineAt(),
		SeriesSnapshot: []storagepebble.DatasetPeriodSeries{{SeriesIndex: 0, SubjectID: "BTC-USDT", SeriesTag: "venue:binance"}},
	}
	for nodeID, node := range map[string]*datanode.Service{"node-a": nodeA, "node-b": nodeB} {
		_, err := node.Store().GetDatasetPeriodStatus(context.Background(), storageExpectation)
		if !errors.Is(err, cpebble.ErrNotFound) {
			t.Errorf("node %s period state error=%v, want not found", nodeID, err)
		}
		rows, err := node.Store().ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{
			SpaceId: "space", DatasetId: "dataset", Page: &pb.Page{Page: 1, Size: 10},
		})
		if err != nil || len(rows.GetRows()) != 0 {
			t.Errorf("node %s rows=%v err=%v, want no writes", nodeID, rows.GetRows(), err)
		}
		outbox, err := node.Store().ListOutbox(context.Background(), 0, 100)
		if err != nil || len(outbox) != 0 {
			t.Errorf("node %s outbox=%d err=%v, want no writes", nodeID, len(outbox), err)
		}
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
	svc, err := primarystore.New(primarystore.Options{Resolver: resolver})
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
	read, err := svc.ReadFields(context.Background(), &pb.PrimaryReadFieldsReq{AuthInfo: &pb.AuthInfo{AppId: "caller"}, Keys: []*pb.RowKey{key}})
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
