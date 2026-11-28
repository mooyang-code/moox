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

// adminDirectorySource 直接读 Admin 自己的库：Admin 要先于网关启动（网关的第一份快照就来自它），目录不能从 11002 取。
// 给出的内容与网关目录相同：登记的主机与部署，版本用快照哈希；客户端按内嵌的服务目录展开，目标在本机的直连回环端口。
type adminDirectorySource struct {
	topology *sysdeploy.TopologyDAO
}

func (s adminDirectorySource) Fetch(ctx context.Context, version string) (gatewayclient.DirectoryUpdate, error) {
	snapshot, err := s.topology.Snapshot(ctx)
	if err != nil {
		return gatewayclient.DirectoryUpdate{}, err
	}
	update := gatewayclient.DirectoryUpdate{Changed: snapshot.Hash != version, Version: snapshot.Hash}
	if !update.Changed {
		return update, nil
	}
	enabled := map[bool]string{true: servicecatalog.Enabled, false: servicecatalog.Disabled}
	for _, host := range snapshot.Hosts {
		reachability, _ := servicecatalog.ParseReachability(host.Reachability)
		update.Hosts = append(update.Hosts, servicecatalog.Host{ID: host.HostId, Address: host.Address, PrivateAddress: host.PrivateAddress, Status: enabled[host.Enabled], Reachability: reachability})
	}
	for _, deployment := range snapshot.Deployments {
		update.Deployments = append(update.Deployments, servicecatalog.Deployment{HostID: deployment.HostId, ComponentID: deployment.ComponentId, Status: enabled[deployment.Enabled]})
	}
	return update, nil
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
func newConsoleGateway(db *gorm.DB, topology *sysdeploy.TopologyDAO, hostID, keyFile string) (*gatewayclient.Client, error) {
	if topology == nil {
		var err error
		topology, err = sysdeploy.NewTopologyDAO(db, hostID)
		if err != nil {
			return nil, err
		}
	}
	key, err := gatewayauth.ReadSigningKey(keyFile)
	if err != nil {
		return nil, fmt.Errorf("读控制机的主机签名私钥 %s: %w", keyFile, err)
	}
	return gatewayclient.New(gatewayclient.Config{Serialization: codec.SerializationTypePB,
		Mode: gatewayclient.Internal, Credentials: gatewayauth.Credentials{Caller: servicecatalog.OperatorConsole, Host: hostID, Key: key}, LocalHostID: hostID,
		CAFile:         filepath.Join(adminPKIDir(), "ca.crt"),
		Source:         adminDirectorySource{topology: topology},
		OnRefreshError: func(err error) { log.Warnf("console directory refresh failed: %v", err) },
	})
}

// Preserve the configured RPC filters when calling a generated handler in
// process, including validation and observability.
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
