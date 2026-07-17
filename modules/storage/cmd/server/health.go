package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	"github.com/mooyang-code/moox/modules/storage/internal/health"
	"github.com/mooyang-code/moox/packages/healthz"
	"trpc.group/trpc-go/trpc-go/server"
)

func registerStorageHealth(s *server.Server, storage storageconfig.StorageConfig) error {
	serviceName := storageServiceName(storage)
	state := health.New("storage", serviceName, "", "")
	state.SnapshotFunc = storageHealthSnapshot(storage, state)
	if s == nil {
		return fmt.Errorf("storage health service is unavailable")
	}
	if err := health.Register(s.Service("trpc.moox.storage.Health"), state); err != nil {
		return fmt.Errorf("storage health server failed to start: %w", err)
	}
	return nil
}

func storageHealthSnapshot(storage storageconfig.StorageConfig, state *health.State) healthz.SnapshotFunc {
	return func(ctx context.Context) healthz.Response {
		serviceName := storageServiceName(storage)
		roleSummary := storageRoleSummary(storage)
		rootReady := storage.Root != "" && pathExists(storage.Root)
		metadataRequired := roleSummary != "view_index"
		metadataReady := !metadataRequired || (storage.Metadata.Path != "" && pathExists(storage.Metadata.Path))
		ready := rootReady && metadataReady
		state.SetReady(ready)
		rsp := healthz.Base("storage", serviceName, "", "", storageStartedAt, ready)
		rsp.Service = serviceName
		rsp.Details = map[string]any{
			"service":          serviceName,
			"role":             roleSummary,
			"roles":            roleSummary,
			"root":             storage.Root,
			"eventbus_type":    storage.EventBus.Type,
			"metadata_path":    storage.Metadata.Path,
			"view_max_workers": storage.View.MaxWorkers,
			"primary_service":  storage.Primary.ServiceName,
			"root_ready":       rootReady,
			"metadata_ready":   metadataReady,
		}
		return rsp
	}
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func storageServiceName(storage storageconfig.StorageConfig) string {
	roleSummary := storageRoleSummary(storage)
	switch roleSummary {
	case "view_query":
		return "storage-view-query"
	case "view_builder":
		return "storage-view-builder"
	case "view_index":
		return "storage-view-index"
	case "access":
		return "storage-access"
	case "access,view", "view":
		return "storage"
	default:
		if roleSummary == "" {
			return "storage"
		}
		return "storage-" + strings.ReplaceAll(strings.ReplaceAll(roleSummary, "_", "-"), ",", "-")
	}
}

func storageRoleSummary(storage storageconfig.StorageConfig) string {
	return strings.Join(storage.Roles, ",")
}
