package bootstrap

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/filter"
	"trpc.group/trpc-go/trpc-go/log"
)

// Read the authoritative Admin database directly. Admin must start before a
// gateway can pull its first snapshot, so its directory cannot come from 11002.
type adminDirectorySource struct {
	topology *sysdeploy.TopologyDAO
	hostID   string
}

func (s adminDirectorySource) Fetch(ctx context.Context, version string) (gatewayclient.DirectoryUpdate, error) {
	compiled, err := s.topology.Compile(ctx, s.hostID)
	if err != nil {
		return gatewayclient.DirectoryUpdate{}, err
	}
	return gatewayclient.DirectoryUpdate{Changed: compiled.Directory.Version != version, Directory: compiled.Directory}, nil
}

func newConsoleGateway(ctx context.Context, db *gorm.DB, hostID, master string) (*gatewayclient.Client, error) {
	topology, err := sysdeploy.NewTopologyDAO(db, hostID)
	if err != nil {
		return nil, err
	}
	store, err := keys.NewStore(db, master)
	if err != nil {
		return nil, err
	}
	key, err := store.Current(ctx, "console")
	if err != nil {
		return nil, fmt.Errorf("load provisioned console signing key: %w", err)
	}
	return gatewayclient.New(gatewayclient.Config{
		Mode: gatewayclient.Internal, Credentials: key.Credentials(), LocalHostID: hostID,
		CAFile:         filepath.Join(newCertificateWatchFromEnvironment(db).PKIDir, "ca.crt"),
		Source:         adminDirectorySource{topology: topology, hostID: hostID},
		OnRefreshError: func(err error) { log.Warnf("console directory refresh failed: %v", err) },
	})
}

// Preserve the configured RPC filters when calling a generated handler in
// process, including validation, response masking and observability.
func localServiceFilters(service string) ([]filter.ServerFilter, error) {
	cfg := trpc.GlobalConfig()
	names := append([]string(nil), cfg.Server.Filter...)
	for _, item := range cfg.Server.Service {
		if item.Name == service {
			names = append(names, item.Filter...)
		}
	}
	chain := make([]filter.ServerFilter, 0, len(names))
	for _, name := range names {
		f := filter.GetServer(name)
		if f == nil {
			return nil, fmt.Errorf("local RPC filter %q is not registered", name)
		}
		chain = append(chain, f)
	}
	return chain, nil
}
