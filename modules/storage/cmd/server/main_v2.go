package main

import (
	"context"
	"crypto/hmac"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	storagehealth "github.com/mooyang-code/moox/modules/storage/internal/health"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/sqlite"
	metadataservice "github.com/mooyang-code/moox/modules/storage/internal/service/primarystore"
	primarystorev2 "github.com/mooyang-code/moox/modules/storage/internal/service/primarystorev2"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewbuilder/eventconsumer"
	viewv2 "github.com/mooyang-code/moox/modules/storage/internal/service/viewv2"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/proto"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/server"
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
	targets := parseNodeTargets(os.Getenv("MOOX_STORAGE_NODE_TARGETS"))
	if len(targets) == 0 {
		if target := os.Getenv("MOOX_STORAGE_NODE_TARGET"); target != "" {
			nodeID := os.Getenv("MOOX_STORAGE_NODE_ID")
			if nodeID == "" {
				nodeID = "storage-node-0"
			}
			targets[nodeID] = target
		}
	}
	proxies := make(map[string]pb.DataNodeService, len(targets))
	resolver := func(ctx context.Context, spaceID, datasetID string) (pb.DataNodeService, error) {
		dataset, err := cached.GetDataset(ctx, spaceID, datasetID)
		if err != nil {
			return nil, err
		}
		if dataset == nil || dataset.GetDataNodeId() == "" {
			return nil, fmt.Errorf("dataset %s/%s has no data_node_id", spaceID, datasetID)
		}
		nodeID := dataset.GetDataNodeId()
		target := targets[nodeID]
		if target == "" {
			return nil, fmt.Errorf("data node %q has no configured target", nodeID)
		}
		if proxies[nodeID] == nil {
			proxy := pb.NewDataNodeClientProxy(client.WithTarget(target), client.WithNetwork("tcp"), client.WithProtocol("trpc"))
			proxies[nodeID] = &dataNodeProxyAdapter{proxy: proxy}
		}
		return proxies[nodeID], nil
	}
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
	svc, err := primarystorev2.New(primarystorev2.Options{Resolver: resolver, View: viewResolver, Validator: primarystorev2.NewMetadataValidator(cached), Authorizer: func(auth *pb.AuthInfo) error {
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
	pb.RegisterPrimaryStoreService(s, svc)
	pb.RegisterMetadataService(s, metadataSvc)
	if err := registerRoleHealth(s, "storage-primary"); err != nil {
		return err
	}
	return s.Serve()
}

func parseNodeTargets(raw string) map[string]string {
	result := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		parts := strings.SplitN(strings.TrimSpace(pair), "=", 2)
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			result[parts[0]] = parts[1]
		}
	}
	return result
}

type datasetReader interface {
	ListDatasets(context.Context, string, string, pb.DataKind, string, *pb.Page) ([]*pb.Dataset, *pb.PageResult, error)
}

func runCleanupLoop(ctx context.Context, reader datasetReader, resolver primarystorev2.NodeResolver, auth *pb.AuthInfo, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	_ = cleanupDatasets(ctx, reader, resolver, auth, time.Now().UTC())
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			_ = cleanupDatasets(ctx, reader, resolver, auth, now.UTC())
		}
	}
}

func cleanupDatasets(ctx context.Context, reader datasetReader, resolver primarystorev2.NodeResolver, auth *pb.AuthInfo, now time.Time) error {
	var result error
	for pageNo := uint32(1); ; pageNo++ {
		datasets, page, err := reader.ListDatasets(ctx, "", "", pb.DataKind_DATA_KIND_TIME_SERIES, "", &pb.Page{Page: pageNo, Size: 1000})
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
			before := now.UTC().Add(-keep).Truncate(24 * time.Hour).Format("2006-01-02T15:04:05.000000000Z")
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

func runViewRole() error {
	root := os.Getenv("MOOX_STORAGE_HOME")
	if root == "" {
		root = "./var/storage"
	}
	viewSecret := os.Getenv("MOOX_STORAGE_VIEW_AUTH_SECRET")
	if viewSecret == "" {
		return errors.New("MOOX_STORAGE_VIEW_AUTH_SECRET is required for view role")
	}
	svc, err := viewv2.New(filepath.Join(root, "view-indexes"), viewSecret)
	if err != nil {
		return err
	}
	rawURL := os.Getenv("MOOX_STORAGE_EVENTBUS_URL")
	if rawURL == "" {
		return errors.New("MOOX_STORAGE_EVENTBUS_URL is required for view role")
	}
	metadataTarget := os.Getenv("MOOX_STORAGE_METADATA_TARGET")
	if metadataTarget == "" {
		metadataTarget = "ip://127.0.0.1:20100"
	}
	metadataProxy := pb.NewMetadataClientProxy(client.WithTarget(metadataTarget), client.WithNetwork("tcp"), client.WithProtocol("trpc"))
	primaryTarget := os.Getenv("MOOX_STORAGE_PRIMARY_TARGET")
	if primaryTarget == "" {
		primaryTarget = "ip://127.0.0.1:20101"
	}
	primarySecret := os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET")
	if primarySecret == "" {
		return errors.New("MOOX_STORAGE_PRIMARY_AUTH_SECRET is required for view role")
	}
	primaryProxy := pb.NewPrimaryStoreClientProxy(client.WithTarget(primaryTarget), client.WithNetwork("tcp"), client.WithProtocol("trpc"))
	svc.SetPrimaryAuth(&pb.AuthInfo{AppId: "storage-view", AppKey: datanode.ServiceAuthKey(primarySecret, "storage-view")})
	stopReconciler, err := svc.StartReconciler(trpc.BackgroundContext(), viewv2.ReconcilerOptions{
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
	eventClient, err := jetstream.Connect(trpc.BackgroundContext(), jetstream.ConfigFromEnv([]string{rawURL}, "storage-view"))
	if err != nil {
		return err
	}
	defer eventClient.Close()
	stopConsumer, err := svc.StartEventConsumer(trpc.BackgroundContext(), eventClient)
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
	pb.RegisterDataViewService(s, svc)
	if err := registerRoleHealth(s, "storage-view"); err != nil {
		return err
	}
	return s.Serve()
}

type dataNodeProxyAdapter struct{ proxy pb.DataNodeClientProxy }
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
	svc, err := datanode.NewService(datanode.Options{NodeID: nodeID, AuthSecret: authSecret, Pebble: pebble.Options{NodeID: nodeID, Path: filepath.Join(root, "pebble", nodeID)}})
	if err != nil {
		return err
	}
	defer svc.Close()
	var relay *datanode.OutboxRelay
	if rawURL := os.Getenv("MOOX_STORAGE_EVENTBUS_URL"); rawURL != "" {
		client, err := jetstream.Connect(trpc.BackgroundContext(), jetstream.ConfigFromEnv([]string{rawURL}, "storage-node"))
		if err != nil {
			return err
		}
		defer client.Close()
		publisher := eventconsumer.NewDatasetPublisher(client, nodeID)
		relay, err = datanode.NewOutboxRelay(svc.Store(), publisher, datanode.OutboxRelayOptions{})
		if err != nil {
			return err
		}
		relay.Start(trpc.BackgroundContext())
		defer relay.Close()
	}
	s := trpc.NewServer()
	listener := s.Service("trpc.moox.storage.DataNode")
	if listener == nil {
		return errors.New("DataNode listener is not configured")
	}
	pb.RegisterDataNodeService(listener, svc)
	if err := registerRoleHealth(s, "storage-node"); err != nil {
		return err
	}
	return s.Serve()
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
