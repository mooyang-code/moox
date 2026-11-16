package bootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/codec"
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

// hostKeyFile 是本机的主机签名私钥：MOOX_ADMIN_HOST_KEY_FILE；没设时用 Admin 主密钥同目录下的 host.key。
func hostKeyFile() string {
	if file := strings.TrimSpace(os.Getenv("MOOX_ADMIN_HOST_KEY_FILE")); file != "" {
		return file
	}
	if master := strings.TrimSpace(os.Getenv("MOOX_ADMIN_ENCRYPTION_KEY_FILE")); master != "" {
		return filepath.Join(filepath.Dir(master), "host.key")
	}
	return filepath.Join("..", "secrets", "host.key")
}

// newConsoleGateway 以 console 的身份、用控制机的主机密钥签名，代浏览器调用各业务服务。
func newConsoleGateway(db *gorm.DB, hostID, keyFile string) (*gatewayclient.Client, error) {
	topology, err := sysdeploy.NewTopologyDAO(db, hostID)
	if err != nil {
		return nil, err
	}
	key, err := gatewayauth.ReadSigningKey(keyFile)
	if err != nil {
		return nil, fmt.Errorf("读控制机的主机签名私钥 %s: %w", keyFile, err)
	}
	return gatewayclient.New(gatewayclient.Config{Serialization: codec.SerializationTypePB,
		Mode: gatewayclient.Internal, Credentials: gatewayauth.Credentials{Caller: servicecatalog.OperatorConsole, Host: hostID, Key: key}, LocalHostID: hostID,
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
