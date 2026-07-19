package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
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
	target := os.Getenv("MOOX_STORAGE_NODE_TARGET")
	if target == "" {
		return errors.New("MOOX_STORAGE_NODE_TARGET is required for primary role")
	}
	secret := os.Getenv("MOOX_STORAGE_NODE_AUTH_SECRET")
	if secret == "" {
		return errors.New("MOOX_STORAGE_NODE_AUTH_SECRET is required for primary role")
	}
	proxy := pb.NewDataNodeClientProxy(client.WithTarget(target), client.WithNetwork("tcp"), client.WithProtocol("trpc"))
	svc, err := primarystorev2.New(primarystorev2.Options{Node: &dataNodeProxyAdapter{proxy: proxy}, AuthSigner: func(auth *pb.AuthInfo) (*pb.AuthInfo, error) {
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

func runViewRole() error {
	root := os.Getenv("MOOX_STORAGE_HOME")
	if root == "" {
		root = "./var/storage"
	}
	svc := viewv2.New(filepath.Join(root, "view-indexes"))
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
	svc, err := datanode.NewService(datanode.Options{NodeID: nodeID, Pebble: pebble.Options{NodeID: nodeID, Path: filepath.Join(root, "pebble", nodeID)}})
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
		return nil
	}
	pb.RegisterDataNodeService(listener, svc)
	return s.Serve()
}
