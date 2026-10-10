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

	directorypb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"trpc.group/trpc-go/trpc-go/client"
)

type DirectoryUpdate struct {
	Changed   bool
	Directory servicecatalog.Directory
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
	d := servicecatalog.Directory{Version: rsp.GetVersion()}
	if rsp.GetChanged() {
		d.Hosts = make(map[string]servicecatalog.DirectoryHost, len(rsp.GetHosts()))
		d.Services = make(map[string][]string, len(rsp.GetServices()))
		for id, host := range rsp.GetHosts() {
			if host == nil {
				return DirectoryUpdate{}, fmt.Errorf("empty directory host")
			}
			d.Hosts[id] = servicecatalog.DirectoryHost{Address: host.GetAddress(), PrivateAddress: host.GetPrivateAddress(), Region: host.GetRegion()}
		}
		for path, hosts := range rsp.GetServices() {
			if hosts == nil {
				return DirectoryUpdate{}, fmt.Errorf("empty directory service")
			}
			d.Services[path] = append([]string{}, hosts.GetHostIds()...)
		}
	}
	return DirectoryUpdate{Changed: rsp.GetChanged(), Directory: d}, nil
}

func readCache(filename string) (servicecatalog.Directory, error) {
	file, err := os.Open(filename)
	if err != nil {
		return servicecatalog.Directory{}, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	if err != nil {
		return servicecatalog.Directory{}, err
	}
	if len(raw) > 4<<20 {
		return servicecatalog.Directory{}, fmt.Errorf("directory cache exceeds 4 MiB")
	}
	var directory servicecatalog.Directory
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&directory); err != nil {
		return servicecatalog.Directory{}, fmt.Errorf("decode directory cache: %w", err)
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		return servicecatalog.Directory{}, fmt.Errorf("directory cache must contain one document")
	}
	return directory, directory.Validate()
}

func writeCache(filename string, directory servicecatalog.Directory) error {
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
