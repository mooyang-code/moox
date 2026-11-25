package gatewayclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	directorypb "github.com/mooyang-code/moox/packages/gatewayclient/directorypb"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"trpc.group/trpc-go/trpc-go/client"
)

// DirectoryUpdate 是下发的目录：版本加登记的主机与部署。客户端按内嵌的服务目录把它展开成“每个服务在哪些主机上”。
// Changed 为假时只有 Version 有意义。落盘缓存存的也是这一份，升级后的进程按自己的服务目录重新展开。
type DirectoryUpdate struct {
	Changed     bool                        `json:"-"`
	Version     string                      `json:"version"`
	Hosts       []servicecatalog.Host       `json:"hosts"`
	Deployments []servicecatalog.Deployment `json:"deployments"`
}

// DirectorySource must honor ctx. It may fetch from a local listener or an SSH
// tunnel to control; it must never discover by querying a public plaintext port.
type DirectorySource interface {
	Fetch(ctx context.Context, currentVersion string) (DirectoryUpdate, error)
}

// LocalDirectorySource owns its connections. Call Close when the source is no
// longer used. A Client closes only a source it created itself.
type LocalDirectorySource struct {
	proxy directorypb.DirectoryClientProxy
	pool  *rpcPool
}

func NewLocalDirectorySource(address string, timeout time.Duration) (*LocalDirectorySource, error) {
	if !loopbackAddress(address) || timeout <= 0 {
		return nil, fmt.Errorf("directory requires a loopback address and positive timeout")
	}
	pool := newRPCPool()
	return &LocalDirectorySource{pool: pool, proxy: directorypb.NewDirectoryClientProxy(
		client.WithTarget("ip://"+address), client.WithNetwork("tcp"), client.WithProtocol("trpc"), client.WithTimeout(timeout), client.WithDialTimeout(timeout),
		client.WithTransport(pool.transport), client.WithPool(pool), client.WithMultiplexed(false),
	)}, nil
}

func (s *LocalDirectorySource) Close() error { return s.pool.Close() }

func (s *LocalDirectorySource) Fetch(ctx context.Context, version string) (DirectoryUpdate, error) {
	rsp, err := s.proxy.GetDirectory(ctx, &directorypb.GetDirectoryReq{CurrentVersion: version})
	if err != nil {
		return DirectoryUpdate{}, err
	}
	if rsp == nil {
		return DirectoryUpdate{}, fmt.Errorf("empty directory response")
	}
	update := DirectoryUpdate{Changed: rsp.GetChanged(), Version: rsp.GetVersion()}
	if !update.Changed {
		return update, nil
	}
	for _, host := range rsp.GetHosts() {
		reachability, ok := servicecatalog.ParseReachability(host.GetReachability())
		if !ok {
			return DirectoryUpdate{}, fmt.Errorf("directory host %s has an unknown reachability", host.GetHostId())
		}
		update.Hosts = append(update.Hosts, servicecatalog.Host{ID: host.GetHostId(), Address: host.GetAddress(), PrivateAddress: host.GetPrivateAddress(), Status: statusName(host.GetEnabled()), Reachability: reachability})
	}
	for _, deployment := range rsp.GetDeployments() {
		update.Deployments = append(update.Deployments, servicecatalog.Deployment{HostID: deployment.GetHostId(), ComponentID: deployment.GetComponentId(), Status: statusName(deployment.GetEnabled())})
	}
	return update, nil
}

func statusName(enabled bool) string {
	if enabled {
		return servicecatalog.Enabled
	}
	return servicecatalog.Disabled
}

// resolve 按内嵌的服务目录把主机与部署展开成目录；内嵌目录不认识的组件忽略。
func resolve(catalog servicecatalog.Catalog, update DirectoryUpdate) (servicecatalog.Directory, error) {
	if update.Version == "" || len(update.Hosts) == 0 {
		return servicecatalog.Directory{}, fmt.Errorf("directory has no version or no hosts")
	}
	directory, _ := catalog.Resolve(servicecatalog.NewTopology(update.Hosts, update.Deployments))
	directory.Version = update.Version
	return directory, directory.Validate()
}

func readCache(filename string) (DirectoryUpdate, error) {
	file, err := os.Open(filename)
	if err != nil {
		return DirectoryUpdate{}, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	if err != nil {
		return DirectoryUpdate{}, err
	}
	if len(raw) > 4<<20 {
		return DirectoryUpdate{}, fmt.Errorf("directory cache exceeds 4 MiB")
	}
	var directory DirectoryUpdate
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&directory); err != nil {
		return DirectoryUpdate{}, fmt.Errorf("decode directory cache: %w", err)
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		return DirectoryUpdate{}, fmt.Errorf("directory cache must contain one document")
	}
	return directory, nil
}

func writeCache(filename string, directory DirectoryUpdate) error {
	if filename == "" {
		return nil
	}
	raw, err := json.Marshal(directory)
	if err != nil {
		return err
	}
	if len(raw) > 4<<20 {
		return fmt.Errorf("directory cache exceeds 4 MiB")
	}
	parent := filepath.Dir(filename)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(parent, ".directory-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(raw); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filename)
}
