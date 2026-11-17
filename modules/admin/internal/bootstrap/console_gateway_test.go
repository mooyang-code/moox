package bootstrap

import (
	"context"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayauth/authtest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/filter"
)

func TestConsoleGatewayStartsFromDatabaseWhileHostGatewayIsOffline(t *testing.T) {
	ctx := context.Background()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "admin.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	topology, err := sysdeploy.NewTopologyDAO(db, "control")
	require.NoError(t, err)
	require.NoError(t, topology.SyncHosts(ctx, []sysdeploy.HostSpec{
		{HostID: "control", Address: "control.example.test", Components: []string{"admin", "collector"}},
		{HostID: "storage", Address: "storage.example.test", Components: []string{"storage-primary", "storage-view"}},
	}))
	// 管理台用控制机的主机密钥、以 console 的身份签名。
	keyFile := filepath.Join(t.TempDir(), "host.key")
	require.NoError(t, os.WriteFile(keyFile, []byte(gatewayauth.EncodeSigningKey(authtest.Key("host/control"))+"\n"), 0o600))
	watch, _ := certificateWatchFixture(t, 365*24*time.Hour, 365*24*time.Hour)
	t.Setenv("MOOX_ADMIN_PKI_DIR", watch.PKIDir)
	client, err := newConsoleGateway(db, "control", keyFile)
	require.NoError(t, err, "startup must not contact 127.0.0.1:11002")
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	first := client.Directory()
	require.Equal(t, []string{"control"}, first.Services["trpc.moox.collector.CollectMgr"])
	require.Equal(t, []string{"storage"}, first.Services["trpc.moox.storage.Metadata"])
	require.NoError(t, topology.SetDeploymentStatus(ctx, "control", "collector", servicecatalog.Disabled))
	require.NoError(t, client.Refresh(ctx))
	require.NotEqual(t, first.Version, client.Directory().Version)
	require.NotContains(t, client.Directory().Services, "trpc.moox.collector.CollectMgr")
	// 目录来源给出的就是快照里的主机与部署，版本是快照哈希；版本没变时不带内容。
	source := adminDirectorySource{topology: topology}
	snapshot, err := topology.Snapshot(ctx)
	require.NoError(t, err)
	require.Equal(t, snapshot.Hash, client.Directory().Version)
	update, err := source.Fetch(ctx, client.Directory().Version)
	require.NoError(t, err)
	require.False(t, update.Changed)
	require.Equal(t, snapshot.Hash, update.Version)
	require.Empty(t, update.Hosts)
	require.Empty(t, update.Deployments)
	update, err = source.Fetch(ctx, "")
	require.NoError(t, err)
	require.True(t, update.Changed)
	require.Equal(t, snapshot.Hash, update.Version)
	require.Len(t, update.Hosts, 2)
	require.Len(t, update.Deployments, len(snapshot.Deployments))
	require.Contains(t, update.Hosts, servicecatalog.Host{ID: "storage", Address: "storage.example.test", Status: servicecatalog.Enabled})
	require.Contains(t, update.Deployments, servicecatalog.Deployment{HostID: "control", ComponentID: "collector", Status: servicecatalog.Disabled})
	require.Contains(t, update.Deployments, servicecatalog.Deployment{HostID: "control", ComponentID: "admin", Status: servicecatalog.Enabled})
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = source.Fetch(canceled, "")
	require.Error(t, err)
	_, err = newConsoleGateway(db, "control", filepath.Join(t.TempDir(), "missing.key"))
	require.Error(t, err, "没有主机私钥时不启动，也不会另造一个签名身份")
	t.Setenv("MOOX_ADMIN_HOST_KEY_FILE", "")
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY_FILE", "/data/moox/secrets/admin-master.key")
	require.Equal(t, "/data/moox/secrets/host.key", hostKeyFile())
	t.Setenv("MOOX_ADMIN_HOST_KEY_FILE", "/elsewhere/host.key")
	require.Equal(t, "/elsewhere/host.key", hostKeyFile())
}

func TestLocalServiceFiltersPreserveGlobalAndServiceOrder(t *testing.T) {
	const globalName, localName = "console-test-global", "console-test-local"
	var visited []string
	filter.Register(globalName, func(ctx context.Context, req any, next filter.ServerHandleFunc) (any, error) {
		visited = append(visited, "global")
		return next(ctx, req)
	}, nil)
	filter.Register(localName, func(ctx context.Context, req any, next filter.ServerHandleFunc) (any, error) {
		visited = append(visited, "service")
		return next(ctx, req)
	}, nil)
	cfg := trpc.GlobalConfig()
	previous := cfg.Server.Filter
	previousServices := cfg.Server.Service
	cfg.Server.Filter = []string{globalName}
	cfg.Server.Service = []*trpc.ServiceConfig{{Name: "trpc.moox.infra.Auth", Filter: []string{localName}}}
	t.Cleanup(func() { cfg.Server.Filter = previous; cfg.Server.Service = previousServices })
	chain, err := localServiceFilters("trpc.moox.infra.Auth")
	require.NoError(t, err)
	require.Len(t, chain, 2)
	_, err = filter.ServerChain(chain).Filter(context.Background(), struct{}{}, func(context.Context, any) (any, error) {
		visited = append(visited, "handler")
		return nil, nil
	})
	require.NoError(t, err)
	require.Equal(t, []string{"global", "service", "handler"}, visited)
	cfg.Server.Filter = []string{"unregistered-console-filter"}
	_, err = localServiceFilters("trpc.moox.infra.Auth")
	require.ErrorContains(t, err, "not registered")
}
