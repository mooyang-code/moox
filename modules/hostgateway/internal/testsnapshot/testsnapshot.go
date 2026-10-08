// Package testsnapshot 为测试构造合法的主机网关快照，哈希的算法与网关控制一致。
package testsnapshot

import (
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// Build 由路由、校验密钥和服务目录构造一份快照。
func Build(hostID string, disabled bool, routes []gatewayroute.Route, keys []gatewayroute.VerificationKey, directory servicecatalog.Directory) (*adminpb.HostSnapshot, error) {
	normalized, err := gatewayroute.NormalizeAndHashState(hostID, disabled, routes)
	if err != nil {
		return nil, err
	}
	version, err := servicecatalog.DirectoryVersion(directory)
	if err != nil {
		return nil, err
	}
	directory.Version = version
	hash, err := gatewayroute.StateHash(normalized.RouteHash, version, keys)
	if err != nil {
		return nil, err
	}
	snapshot := &adminpb.HostSnapshot{
		HostId: hostID, Hash: hash, GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), Disabled: disabled,
		Directory: gatewayclient.DirectoryToProto(directory),
	}
	for _, route := range normalized.Routes {
		snapshot.Routes = append(snapshot.Routes, &adminpb.HostRoute{
			ComponentId: route.ServiceID, ServicePath: route.ServicePath, Address: route.Address,
			TimeoutMs: route.TimeoutMS, MaxBodyBytes: route.MaxBodyBytes, Methods: route.AllowedMethods, Callers: route.AllowedCallers,
		})
	}
	for _, key := range keys {
		snapshot.Keys = append(snapshot.Keys, &adminpb.VerificationKey{KeyId: key.KeyID, Caller: key.Caller, Secret: key.Secret})
	}
	return snapshot, nil
}

// Directory 返回只有一台主机、列出给定服务的目录。
func Directory(hostID string, services ...string) servicecatalog.Directory {
	directory := servicecatalog.Directory{Hosts: []servicecatalog.DirectoryHost{{ID: hostID, Address: "127.0.0.1"}}}
	for _, service := range services {
		directory.Services = append(directory.Services, servicecatalog.ServiceHosts{Path: service, HostIDs: []string{hostID}})
	}
	return directory
}
