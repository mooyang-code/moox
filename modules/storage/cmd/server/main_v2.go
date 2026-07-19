package main

import (
	"fmt"
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
	var err error
	switch os.Getenv("MOOX_STORAGE_ROLE") {
	case "", "node":
		err = runDataNodeRole()
	case "primary", "view":
		// Primary and View are independently deployable roles. Their RPC
		// surfaces are registered by the corresponding process bootstrap; this
		// entrypoint keeps role selection explicit rather than silently starting
		// a mixed legacy process.
		err = runEmptyRole()
	default:
		err = fmt.Errorf("unknown storage role %q", os.Getenv("MOOX_STORAGE_ROLE"))
	}
	if err != nil {
		log.Fatal(err)
	}
}

func runEmptyRole() error { return nil }

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
