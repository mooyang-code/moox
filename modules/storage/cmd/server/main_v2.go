package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/sqlite"
	primarystorev2 "github.com/mooyang-code/moox/modules/storage/internal/service/primarystorev2"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewbuilder/eventconsumer"
	viewv2 "github.com/mooyang-code/moox/modules/storage/internal/service/viewv2"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/proto"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/client"
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
	secret := os.Getenv("MOOX_STORAGE_NODE_AUTH_SECRET")
	if secret == "" {
		return errors.New("MOOX_STORAGE_NODE_AUTH_SECRET is required for primary role")
	}
	primarySecret := os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET")
	if primarySecret == "" {
		return errors.New("MOOX_STORAGE_PRIMARY_AUTH_SECRET is required for primary role")
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
		dataset, err := meta.GetDataset(ctx, spaceID, datasetID)
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
	svc, err := primarystorev2.New(primarystorev2.Options{Resolver: resolver, Validator: primarystorev2.NewMetadataValidator(meta), Authorizer: func(auth *pb.AuthInfo) error {
		if auth == nil || auth.GetAppId() == "" || auth.GetAppKey() != datanode.ServiceAuthKey(primarySecret, auth.GetAppId()) {
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
	s := trpc.NewServer()
	listener := s.Service("trpc.moox.storage.PrimaryStore")
	if listener == nil {
		return errors.New("PrimaryStore listener is not configured")
	}
	pb.RegisterPrimaryStoreService(listener, svc)
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

func runViewRole() error {
	root := os.Getenv("MOOX_STORAGE_HOME")
	if root == "" {
		root = "./var/storage"
	}
	svc := viewv2.New(filepath.Join(root, "view-indexes"))
	var stopConsumer func()
	rawURL := os.Getenv("MOOX_STORAGE_EVENTBUS_URL")
	if rawURL == "" {
		return errors.New("MOOX_STORAGE_EVENTBUS_URL is required for view role")
	}
	{
		client, err := jetstream.Connect(trpc.BackgroundContext(), jetstream.ConfigFromEnv([]string{rawURL}, "storage-view"))
		if err != nil {
			return err
		}
		defer client.Close()
		stopConsumer, err = svc.StartEventConsumer(trpc.BackgroundContext(), client)
		if err != nil {
			return err
		}
		defer stopConsumer()
	}
	s := trpc.NewServer()
	indexListener := s.Service("trpc.moox.storage.ViewIndex")
	if indexListener == nil {
		return errors.New("ViewIndex listener is not configured")
	}
	viewListener := s.Service("trpc.moox.storage.DataView")
	if viewListener == nil {
		return errors.New("DataView listener is not configured")
	}
	pb.RegisterViewIndexService(indexListener, svc)
	pb.RegisterDataViewService(viewListener, svc)
	return s.Serve()
}

type dataNodeProxyAdapter struct{ proxy pb.DataNodeClientProxy }

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
	return s.Serve()
}
