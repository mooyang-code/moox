package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewbuilder/eventconsumer"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	trpc "trpc.group/trpc-go/trpc-go"
)

func main() {
	if err := runDataNodeRole(); err != nil {
		log.Fatal(err)
	}
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
