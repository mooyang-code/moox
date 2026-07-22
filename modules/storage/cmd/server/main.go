package main

import (
	"context"
	"crypto/hmac"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	storagehealth "github.com/mooyang-code/moox/modules/storage/internal/health"
	metadataservice "github.com/mooyang-code/moox/modules/storage/internal/service/catalog"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/sqlite"
	primarystore "github.com/mooyang-code/moox/modules/storage/internal/service/primarystore"
	viewservice "github.com/mooyang-code/moox/modules/storage/internal/service/view"
	"github.com/mooyang-code/moox/modules/storage/internal/service/view/eventconsumer"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcotel"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/proto"
	_ "trpc.group/trpc-go/trpc-database/timer"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/server"
	_ "trpc.group/trpc-go/trpc-log-cls"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"
)

func main() {
	var err error
	switch os.Getenv("MOOX_STORAGE_ROLE") {
	case "", "node":
		err = runDataNodeRole()
	case "primary":
		err = runPrimaryRole()
	case "view":
		err = runViewRole()
	default:
		err = fmt.Errorf("unknown storage role %q", os.Getenv("MOOX_STORAGE_ROLE"))
	}
	if err != nil {
		log.Fatal(err)
	}
}

func runPrimaryRole() error {
	root := os.Getenv("MOOX_STORAGE_HOME")
	if root == "" {
		root = "./var/storage"
	}
	metadataPath := os.Getenv("MOOX_STORAGE_METADATA_PATH")
	if metadataPath == "" {
		metadataPath = filepath.Join(root, "metadata", "storage_metadata.db")
	}
	meta, err := metasqlite.Open(trpc.BackgroundContext(), metasqlite.Options{Path: metadataPath})
	if err != nil {
		return err
	}
	defer meta.Close()
	if err := meta.ValidateSchemaVersion(trpc.BackgroundContext()); err != nil {
		return fmt.Errorf("metadata schema validation failed: %w", err)
	}
	cached, err := metacache.New(trpc.BackgroundContext(), meta, metacache.Options{})
	if err != nil {
		return fmt.Errorf("open metadata cache: %w", err)
	}
	defer cached.Close()
	secret := os.Getenv("MOOX_STORAGE_NODE_AUTH_SECRET")
	if secret == "" {
		return errors.New("MOOX_STORAGE_NODE_AUTH_SECRET is required for primary role")
	}
	primarySecret := os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET")
	if primarySecret == "" {
		return errors.New("MOOX_STORAGE_PRIMARY_AUTH_SECRET is required for primary role")
	}
	viewSecret := os.Getenv("MOOX_STORAGE_VIEW_AUTH_SECRET")
	if viewSecret == "" {
		return errors.New("MOOX_STORAGE_VIEW_AUTH_SECRET is required for primary role")
	}
	resolver := newDataNodeResolver(cached.RequestSnapshot, func(target string) pb.DataNodeRuntimeService {
		proxy := pb.NewDataNodeRuntimeClientProxy(client.WithTarget(target), client.WithNetwork("tcp"), client.WithProtocol("trpc"))
		return &dataNodeProxyAdapter{proxy: proxy}
	})
	viewTarget := os.Getenv("MOOX_STORAGE_VIEW_TARGET")
	if viewTarget == "" {
		viewTarget = "ip://127.0.0.1:20103"
	}
	viewProxy := &dataViewProxyAdapter{
		proxy: pb.NewDataViewClientProxy(client.WithTarget(viewTarget), client.WithNetwork("tcp"), client.WithProtocol("trpc")),
		auth:  &pb.AuthInfo{AppId: "storage-primary", AppKey: datanode.ServiceAuthKey(viewSecret, "storage-primary")},
	}
	viewResolver := func(ctx context.Context, spaceID, datasetID string) (pb.DataViewService, string, error) {
		views, err := cached.ListViewsByDataset(ctx, spaceID, datasetID)
		if err != nil {
			return nil, "", err
		}
		for _, view := range views {
			if view != nil && view.GetPrimaryDatasetId() == datasetID && view.GetActiveIndexId() != "" {
				return viewProxy, view.GetViewId(), nil
			}
		}
		return nil, "", fmt.Errorf("dataset %s/%s has no active view", spaceID, datasetID)
	}
	svc, err := primarystore.New(primarystore.Options{Resolver: resolver, View: viewResolver, Validator: primarystore.NewMetadataValidator(cached), Snapshot: cached.RequestSnapshot, Authorizer: func(auth *pb.AuthInfo) error {
		if auth == nil || auth.GetAppId() == "" ||
			!hmac.Equal([]byte(strings.ToLower(auth.GetAppKey())), []byte(datanode.ServiceAuthKey(primarySecret, auth.GetAppId()))) {
			return errors.New("invalid primary auth")
		}
		return nil
	}, AuthSigner: func(auth *pb.AuthInfo) (*pb.AuthInfo, error) {
		if auth == nil {
			return nil, errors.New("auth_info is required")
		}
		clone := proto.Clone(auth).(*pb.AuthInfo)
		clone.AppKey = datanode.ServiceAuthKey(secret, clone.GetAppId())
		return clone, nil
	}})
	if err != nil {
		return err
	}
	metadataSvc, err := metadataservice.NewMetadataService(meta, cached)
	if err != nil {
		return err
	}
	cleanupCtx, stopCleanup := context.WithCancel(trpc.BackgroundContext())
	defer stopCleanup()
	cleanupAuth := &pb.AuthInfo{AppId: "storage-primary", AppKey: datanode.ServiceAuthKey(secret, "storage-primary")}
	go runCleanupLoop(cleanupCtx, cached, resolver, cleanupAuth, time.Hour)
	s := trpc.NewServer()
	for _, name := range []string{"trpc.moox.storage.PrimaryStore", "trpc.moox.storage.PrimaryStore.trpc", "trpc.moox.storage.PrimaryStore.http"} {
		if listener := s.Service(name); listener != nil {
			pb.RegisterPrimaryStoreService(listener, svc)
		}
	}
	for _, name := range []string{"trpc.moox.storage.Metadata", "trpc.moox.storage.Metadata.trpc", "trpc.moox.storage.Metadata.http"} {
		if listener := s.Service(name); listener != nil {
			pb.RegisterMetadataService(listener, metadataSvc)
		}
	}
	if err := registerRoleHealth(s, "storage-primary"); err != nil {
		return err
	}
	return s.Serve()
}

type datasetReader interface {
	ListDatasets(context.Context, metadata.DatasetQuery) ([]*pb.Dataset, *pb.PageResult, error)
}

func runCleanupLoop(ctx context.Context, reader datasetReader, resolver primarystore.NodeResolver, auth *pb.AuthInfo, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	if err := cleanupDatasets(ctx, reader, resolver, auth, time.Now().UTC()); err != nil {
		log.Printf("storage cleanup initial run failed: %v", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := cleanupDatasets(ctx, reader, resolver, auth, now.UTC()); err != nil {
				log.Printf("storage cleanup run failed: %v", err)
			}
		}
	}
}

func cleanupDatasets(ctx context.Context, reader datasetReader, resolver primarystore.NodeResolver, auth *pb.AuthInfo, now time.Time) error {
	var result error
	for pageNo := uint32(1); ; pageNo++ {
		datasets, page, err := reader.ListDatasets(ctx, metadata.DatasetQuery{DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Page: &pb.Page{Page: pageNo, Size: 1000}})
		if err != nil {
			return err
		}
		for _, dataset := range datasets {
			if dataset == nil || (dataset.GetStatus() != "" && dataset.GetStatus() != "active") || dataset.GetKeepDuration() == "0" {
				continue
			}
			keep, err := time.ParseDuration(dataset.GetKeepDuration())
			if err != nil || keep <= 0 {
				result = errors.Join(result, fmt.Errorf("dataset %s/%s has invalid keep_duration %q", dataset.GetSpaceId(), dataset.GetDatasetId(), dataset.GetKeepDuration()))
				continue
			}
			node, err := resolver(ctx, dataset.GetSpaceId(), dataset.GetDatasetId())
			if err != nil {
				result = errors.Join(result, err)
				continue
			}
			before := now.UTC().Add(-keep).Truncate(cleanupBucketDuration()).Format("2006-01-02T15:04:05.000000000Z")
			rsp, err := node.CleanupExpiredBuckets(ctx, &pb.CleanupExpiredBucketsReq{
				AuthInfo: auth, SpaceId: dataset.GetSpaceId(), DatasetId: dataset.GetDatasetId(), BeforeBucketStart: before,
			})
			if err != nil {
				result = errors.Join(result, err)
				continue
			}
			if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
				result = errors.Join(result, errors.New(rsp.GetRetInfo().GetMsg()))
			}
		}
		if page == nil || !page.GetHasMore() || len(datasets) == 0 {
			return result
		}
	}
}

func cleanupBucketDuration() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("MOOX_STORAGE_BUCKET_DURATION")); raw != "" {
		if duration, err := time.ParseDuration(raw); err == nil && duration > 0 {
			return duration
		}
		log.Printf("invalid MOOX_STORAGE_BUCKET_DURATION=%q; using 24h", raw)
	}
	return 24 * time.Hour
}

func runViewRole() error {
	root := os.Getenv("MOOX_STORAGE_HOME")
	if root == "" {
		root = "./var/storage"
	}
	viewSecret := os.Getenv("MOOX_STORAGE_VIEW_AUTH_SECRET")
	if viewSecret == "" {
		return errors.New("MOOX_STORAGE_VIEW_AUTH_SECRET is required for view role")
	}
	svc, err := viewservice.New(filepath.Join(root, "view-indexes"), viewSecret)
	if err != nil {
		return err
	}
	if !svc.HasEngine("duckdb") {
		return errors.New("storage-view requires a CGO-enabled DuckDB engine")
	}
	rawURL := os.Getenv("MOOX_STORAGE_EVENTBUS_URL")
	if rawURL == "" {
		return errors.New("MOOX_STORAGE_EVENTBUS_URL is required for view role")
	}
	metadataTarget := envOrDefault("MOOX_STORAGE_METADATA_TARGET", "ip://127.0.0.1:20200")
	metadataNetwork := envOrDefault("MOOX_STORAGE_METADATA_NETWORK", "tcp")
	metadataProtocol := envOrDefault("MOOX_STORAGE_METADATA_PROTOCOL", "http")
	metadataProxy := pb.NewMetadataClientProxy(client.WithTarget(metadataTarget), client.WithNetwork(metadataNetwork), client.WithProtocol(metadataProtocol))
	primaryTarget := envOrDefault("MOOX_STORAGE_PRIMARY_TARGET", "ip://127.0.0.1:20201")
	primaryNetwork := envOrDefault("MOOX_STORAGE_PRIMARY_NETWORK", "tcp")
	primaryProtocol := envOrDefault("MOOX_STORAGE_PRIMARY_PROTOCOL", "http")
	primarySecret := os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET")
	if primarySecret == "" {
		return errors.New("MOOX_STORAGE_PRIMARY_AUTH_SECRET is required for view role")
	}
	primaryProxy := pb.NewPrimaryStoreClientProxy(client.WithTarget(primaryTarget), client.WithNetwork(primaryNetwork), client.WithProtocol(primaryProtocol))
	svc.SetPrimaryAuth(&pb.AuthInfo{AppId: "storage-view", AppKey: datanode.ServiceAuthKey(primarySecret, "storage-view")})
	svc.SetPrimaryReader(primaryProxy)
	stopReconciler, err := svc.StartReconciler(trpc.BackgroundContext(), viewservice.ReconcilerOptions{
		Metadata: metadataProxy,
		Primary:  primaryProxy,
		OwnerID:  "storage-view",
		Interval: 30 * time.Second,
		Grace:    time.Minute,
	})
	if err != nil {
		return err
	}
	defer stopReconciler()
	eventConfig, err := storageEventBusConfig([]string{rawURL}, "storage-view")
	if err != nil {
		return err
	}
	eventClient, err := jetstream.Connect(trpc.BackgroundContext(), eventConfig)
	if err != nil {
		return err
	}
	defer eventClient.Close()
	consumerOptions, err := storageViewConsumerOptions()
	if err != nil {
		return err
	}
	stopConsumer, err := svc.StartEventConsumer(trpc.BackgroundContext(), eventClient, consumerOptions)
	if err != nil {
		return err
	}
	defer stopConsumer()
	s := trpc.NewServer()
	indexListener := s.Service("trpc.moox.storage.ViewIndex")
	if indexListener == nil {
		return errors.New("ViewIndex listener is not configured")
	}
	pb.RegisterViewIndexService(indexListener, svc)
	for _, name := range []string{"trpc.moox.storage.DataView", "trpc.moox.storage.DataView.trpc", "trpc.moox.storage.DataView.http"} {
		if listener := s.Service(name); listener != nil {
			pb.RegisterDataViewService(listener, svc)
		}
	}
	if err := registerRoleHealth(s, "storage-view"); err != nil {
		return err
	}
	return s.Serve()
}

func storageViewConsumerOptions() (viewservice.EventConsumerOptions, error) {
	path := strings.TrimSpace(os.Getenv("MOOX_STORAGE_CONFIG"))
	if path == "" {
		return viewservice.EventConsumerOptions{}, nil
	}
	var runtimeConfig storageconfig.RuntimeConfig
	loader := storageconfig.NewConfigLoader(filepath.Dir(path))
	if err := loader.LoadConfigWithDefaults(filepath.Base(path), &runtimeConfig, runtimeConfig.ApplyDefaults); err != nil {
		return viewservice.EventConsumerOptions{}, fmt.Errorf("load storage view consumer config: %w", err)
	}
	return viewservice.EventConsumerOptions{
		Stream:        runtimeConfig.Storage.EventBus.StreamName,
		Durable:       runtimeConfig.Storage.EventBus.ConsumerName,
		AckWaitMS:     runtimeConfig.Storage.EventBus.AckWaitMS,
		FetchBatch:    runtimeConfig.Storage.View.FetchBatch,
		MaxWorkers:    runtimeConfig.Storage.View.MaxWorkers,
		MaxAckPending: runtimeConfig.Storage.EventBus.MaxAckPending,
		Ordering:      runtimeConfig.Storage.View.Ordering,
	}, nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

type dataNodeProxyKey struct {
	NodeID        string
	ServiceTarget string
}

func newDataNodeResolver(snapshotProvider func() metadata.RequestSnapshot, newProxy func(string) pb.DataNodeRuntimeService) primarystore.NodeResolver {
	if newProxy == nil {
		newProxy = func(target string) pb.DataNodeRuntimeService {
			proxy := pb.NewDataNodeRuntimeClientProxy(client.WithTarget(target), client.WithNetwork("tcp"), client.WithProtocol("trpc"))
			return &dataNodeProxyAdapter{proxy: proxy}
		}
	}
	proxies := make(map[dataNodeProxyKey]pb.DataNodeRuntimeService)
	var proxiesMu sync.Mutex
	return func(ctx context.Context, spaceID, datasetID string) (pb.DataNodeRuntimeService, error) {
		snapshot := metadata.RequestSnapshotFromContext(ctx)
		if snapshot == nil && snapshotProvider != nil {
			snapshot = snapshotProvider()
		}
		nodeID, target, err := resolveDataNodeFromSnapshot(snapshot, spaceID, datasetID)
		if err != nil {
			return nil, err
		}
		key := dataNodeProxyKey{NodeID: nodeID, ServiceTarget: target}
		proxiesMu.Lock()
		defer proxiesMu.Unlock()
		if proxy := proxies[key]; proxy != nil {
			return proxy, nil
		}
		for existing := range proxies {
			if existing.NodeID == nodeID {
				delete(proxies, existing)
			}
		}
		proxy := newProxy(target)
		if proxy == nil {
			return nil, errors.New("data node proxy is unavailable")
		}
		proxies[key] = proxy
		return proxy, nil
	}
}

func resolveDataNodeFromSnapshot(snapshot metadata.RequestSnapshot, spaceID, datasetID string) (string, string, error) {
	if snapshot == nil {
		return "", "", errors.New("metadata cache snapshot is unavailable")
	}
	dataset, ok := snapshot.GetDataset(spaceID, datasetID)
	if !ok || dataset == nil {
		return "", "", fmt.Errorf("dataset %s/%s is not found", spaceID, datasetID)
	}
	if dataset.GetStatus() != "active" {
		return "", "", fmt.Errorf("dataset %s/%s is not active", spaceID, datasetID)
	}
	nodeID := strings.TrimSpace(dataset.GetDataNodeId())
	if nodeID == "" {
		return "", "", fmt.Errorf("dataset %s/%s has no data_node_id", spaceID, datasetID)
	}
	node, ok := snapshot.GetDataNode(nodeID)
	if !ok || node == nil {
		return "", "", fmt.Errorf("data node %q is not found", nodeID)
	}
	if node.GetNodeId() != nodeID || node.GetStatus() != "active" {
		return "", "", fmt.Errorf("data node %q is not active", nodeID)
	}
	target, err := normalizeServiceTarget(node.GetServiceTarget())
	if err != nil {
		return "", "", fmt.Errorf("data node %q has invalid service_target: %w", nodeID, err)
	}
	return nodeID, target, nil
}

func normalizeServiceTarget(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "ip" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", errors.New("service_target must be an ip://host:port address")
	}
	host, portText, err := net.SplitHostPort(u.Host)
	if err != nil || host == "" {
		return "", errors.New("service_target must include host and port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("service_target port is invalid")
	}
	return "ip://" + net.JoinHostPort(host, strconv.Itoa(port)), nil
}

type dataNodeProxyAdapter struct{ proxy pb.DataNodeRuntimeClientProxy }
type dataViewProxyAdapter struct {
	proxy pb.DataViewClientProxy
	auth  *pb.AuthInfo
}

func (a *dataNodeProxyAdapter) WriteFields(ctx context.Context, req *pb.WriteFieldsReq) (*pb.WriteFieldsRsp, error) {
	return a.proxy.WriteFields(ctx, req)
}
func (a *dataNodeProxyAdapter) ReadFields(ctx context.Context, req *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
	return a.proxy.ReadFields(ctx, req)
}
func (a *dataNodeProxyAdapter) DeleteFields(ctx context.Context, req *pb.DeleteFieldsReq) (*pb.DeleteFieldsRsp, error) {
	return a.proxy.DeleteFields(ctx, req)
}
func (a *dataNodeProxyAdapter) GetNodeState(ctx context.Context, req *pb.GetNodeStateReq) (*pb.GetNodeStateRsp, error) {
	return a.proxy.GetNodeState(ctx, req)
}
func (a *dataNodeProxyAdapter) CleanupExpiredBuckets(ctx context.Context, req *pb.CleanupExpiredBucketsReq) (*pb.CleanupExpiredBucketsRsp, error) {
	return a.proxy.CleanupExpiredBuckets(ctx, req)
}
func (a *dataViewProxyAdapter) QueryTimeSeriesRows(ctx context.Context, req *pb.QueryTimeSeriesRowsReq) (*pb.QueryTimeSeriesRowsRsp, error) {
	clone := proto.Clone(req).(*pb.QueryTimeSeriesRowsReq)
	clone.AuthInfo = proto.Clone(a.auth).(*pb.AuthInfo)
	return a.proxy.QueryTimeSeriesRows(ctx, clone)
}

func (a *dataViewProxyAdapter) SearchRecordRows(ctx context.Context, req *pb.SearchRecordRowsReq) (*pb.SearchRecordRowsRsp, error) {
	clone := proto.Clone(req).(*pb.SearchRecordRowsReq)
	clone.AuthInfo = proto.Clone(a.auth).(*pb.AuthInfo)
	return a.proxy.SearchRecordRows(ctx, clone)
}

// This small role entrypoint intentionally keeps the DataNode process
// independent from PrimaryStore and View. Deployment selects the role through
// its tRPC listener configuration; one process owns exactly one Pebble node.
func runDataNodeRole() error {
	root := os.Getenv("MOOX_STORAGE_HOME")
	if root == "" {
		root = "./var/storage"
	}
	nodeID := os.Getenv("MOOX_STORAGE_NODE_ID")
	if nodeID == "" {
		nodeID = "storage-node-0"
	}
	authSecret := os.Getenv("MOOX_STORAGE_NODE_AUTH_SECRET")
	rawURL := os.Getenv("MOOX_STORAGE_EVENTBUS_URL")
	if rawURL == "" {
		return errors.New("MOOX_STORAGE_EVENTBUS_URL is required for node role")
	}
	eventConfig, err := storageEventBusConfig([]string{rawURL}, "storage-node")
	if err != nil {
		return err
	}
	svc, err := datanode.NewService(datanode.Options{
		NodeID: nodeID, AuthSecret: authSecret,
		Pebble: pebble.Options{
			NodeID: nodeID, Path: filepath.Join(root, "pebble", nodeID),
			MaxEventBytes: eventConfig.MaxPayloadBytes(),
		},
	})
	if err != nil {
		return err
	}
	defer svc.Close()
	client, err := jetstream.Connect(trpc.BackgroundContext(), eventConfig)
	if err != nil {
		return err
	}
	defer client.Close()
	publisher := eventconsumer.NewDatasetPublisher(client, nodeID)
	relay, err := datanode.NewOutboxRelay(svc.Store(), publisher, datanode.OutboxRelayOptions{})
	if err != nil {
		return err
	}
	relay.Start(trpc.BackgroundContext())
	defer relay.Close()
	s := trpc.NewServer()
	listener := s.Service("trpc.moox.storage.DataNodeRuntime")
	if listener == nil {
		return errors.New("DataNode listener is not configured")
	}
	pb.RegisterDataNodeRuntimeService(listener, svc)
	if err := registerRoleHealth(s, "storage-node"); err != nil {
		return err
	}
	return s.Serve()
}

func storageEventBusConfig(urls []string, name string) (jetstream.Config, error) {
	cfg := jetstream.ConfigFromEnv(urls, name)
	path, err := storageEventBusCredentialFile()
	if err != nil {
		return jetstream.Config{}, err
	}
	if path == "" {
		return cfg, nil
	}
	if cfg.Credentials != "" || cfg.Username != "" {
		return jetstream.Config{}, errors.New("storage eventbus credential file conflicts with EventBus credential environment")
	}
	if err := cfg.ApplyCredentialFile(path); err != nil {
		return jetstream.Config{}, fmt.Errorf("storage eventbus credential: %w", err)
	}
	return cfg, nil
}

func storageEventBusCredentialFile() (string, error) {
	if path := strings.TrimSpace(os.Getenv("MOOX_STORAGE_EVENTBUS_CREDENTIAL_FILE")); path != "" {
		return jetstream.ExpandCredentialPath(path), nil
	}
	configPath := strings.TrimSpace(os.Getenv("MOOX_STORAGE_CONFIG"))
	if configPath == "" {
		return "", nil
	}
	var runtimeConfig storageconfig.RuntimeConfig
	loader := storageconfig.NewConfigLoader(filepath.Dir(configPath))
	if err := loader.LoadConfig(filepath.Base(configPath), &runtimeConfig); err != nil {
		return "", fmt.Errorf("load storage eventbus config: %w", err)
	}
	return jetstream.ExpandCredentialPath(strings.TrimSpace(runtimeConfig.Storage.EventBus.CredentialFile)), nil
}

func registerRoleHealth(s *server.Server, instance string) error {
	if s == nil {
		return errors.New("storage health server is unavailable")
	}
	state := storagehealth.New("storage", instance, "", "")
	state.SetReady(true)
	if err := storagehealth.Register(s.Service("trpc.moox.storage.Health"), state); err != nil {
		return fmt.Errorf("register storage health: %w", err)
	}
	return nil
}
