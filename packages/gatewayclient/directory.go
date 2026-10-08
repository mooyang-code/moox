package gatewayclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayroute/proto/directorypb"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/pool/connpool"
)

// DefaultRefreshInterval 是比对服务目录版本的间隔，与主机网关拉取快照的间隔一致。
const DefaultRefreshInterval = 15 * time.Second

const directoryCacheFile = "directory.json"

// View 是客户端看到的服务目录：本机主机 ID（隧道方式没有本机）与全局目录。
type View struct {
	LocalHostID string                   `json:"local_host_id"`
	Directory   servicecatalog.Directory `json:"directory"`
}

// Source 提供服务目录。currentVersion 与最新版本相同时返回 changed=false。
type Source interface {
	Fetch(ctx context.Context, currentVersion string) (View, bool, error)
}

// SourceFunc 让普通函数实现 Source，主要用于测试。
type SourceFunc func(ctx context.Context, currentVersion string) (View, bool, error)

// Fetch 调用函数本身。
func (f SourceFunc) Fetch(ctx context.Context, currentVersion string) (View, bool, error) {
	return f(ctx, currentVersion)
}

// rpcSource 通过主机网关的 Directory 服务取目录：内部方式连本机网关，隧道方式经隧道连 control。
type rpcSource struct {
	address func(context.Context) (string, error)
	timeout time.Duration
	pool    connpool.Pool
}

func (s rpcSource) Fetch(ctx context.Context, currentVersion string) (View, bool, error) {
	address, err := s.address(ctx)
	if err != nil {
		return View{}, false, err
	}
	options := []client.Option{client.WithTarget("ip://" + address), client.WithNetwork("tcp"), client.WithProtocol("trpc"), client.WithTimeout(s.timeout)}
	if s.pool != nil {
		options = append(options, client.WithPool(s.pool))
	}
	proxy := directorypb.NewDirectoryClientProxy(options...)
	rsp, err := proxy.GetDirectory(ctx, &directorypb.GetDirectoryReq{CurrentVersion: currentVersion})
	if err != nil {
		return View{}, false, fmt.Errorf("从 %s 查询服务目录失败: %w", address, err)
	}
	if code := rsp.GetRetInfo().GetCode(); code != commonpb.ErrorCode_SUCCESS {
		return View{}, false, fmt.Errorf("从 %s 查询服务目录失败: %s", address, rsp.GetRetInfo().GetMsg())
	}
	if !rsp.GetChanged() {
		return View{LocalHostID: rsp.GetHostId()}, false, nil
	}
	if rsp.GetDirectory() == nil || rsp.GetDirectory().GetVersion() == "" {
		return View{}, false, fmt.Errorf("%s 返回的服务目录为空", address)
	}
	return View{LocalHostID: rsp.GetHostId(), Directory: DirectoryFromProto(rsp.GetDirectory())}, true, nil
}

// directoryState 持有当前目录、落盘缓存和后台刷新。
type directoryState struct {
	source   Source
	cacheDir string
	interval time.Duration
	tunnel   bool

	mu         sync.RWMutex
	view       View
	loaded     bool
	lastErr    error
	refreshing chan struct{}

	stop     chan struct{}
	stopOnce sync.Once
}

func newDirectoryState(source Source, cacheDir string, interval time.Duration, tunnel bool) *directoryState {
	if interval <= 0 {
		interval = DefaultRefreshInterval
	}
	state := &directoryState{source: source, cacheDir: cacheDir, interval: interval, tunnel: tunnel, stop: make(chan struct{})}
	if cached, err := state.loadCache(); err == nil {
		state.view, state.loaded = cached, true
	}
	return state
}

// start 在后台定期比对目录版本。
func (s *directoryState) start() {
	go func() {
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), s.interval)
				_ = s.refresh(ctx)
				cancel()
			}
		}
	}()
}

func (s *directoryState) close() { s.stopOnce.Do(func() { close(s.stop) }) }

// current 返回当前目录；还没有任何目录时同步拉取一次。
func (s *directoryState) current(ctx context.Context) (View, error) {
	s.mu.RLock()
	view, loaded := s.view, s.loaded
	s.mu.RUnlock()
	if loaded {
		return view, nil
	}
	if err := s.refresh(ctx); err != nil {
		return View{}, fmt.Errorf("服务目录不可用: %w", err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.view, nil
}

// refresh 拉取一次目录；并发调用共享同一次拉取。拉取失败时保留原目录。
func (s *directoryState) refresh(ctx context.Context) error {
	s.mu.Lock()
	if wait := s.refreshing; wait != nil {
		s.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.lastErr
	}
	done := make(chan struct{})
	s.refreshing = done
	version := ""
	if s.loaded {
		version = s.view.Directory.Version
	}
	s.mu.Unlock()

	view, changed, err := s.source.Fetch(ctx, version)

	s.mu.Lock()
	defer func() {
		s.refreshing = nil
		close(done)
		s.mu.Unlock()
	}()
	s.lastErr = err
	if err != nil {
		return err
	}
	if !changed {
		if !s.loaded {
			s.lastErr = errors.New("主机网关报告目录未变化，但本地没有目录")
			return s.lastErr
		}
		if view.LocalHostID != "" {
			s.view.LocalHostID = view.LocalHostID
		}
		return nil
	}
	if s.tunnel {
		// 隧道方式下操作员机器不是任何一台 MooX 主机。
		view.LocalHostID = ""
	}
	s.view, s.loaded = view, true
	// 落盘失败不影响内存中的目录，下次目录变化时再写。
	_ = s.saveCache(view)
	return nil
}

func (s *directoryState) loadCache() (View, error) {
	if s.cacheDir == "" {
		return View{}, errors.New("没有配置目录缓存")
	}
	raw, err := os.ReadFile(filepath.Join(s.cacheDir, directoryCacheFile))
	if err != nil {
		return View{}, err
	}
	var view View
	if err := json.Unmarshal(raw, &view); err != nil {
		return View{}, fmt.Errorf("解析目录缓存: %w", err)
	}
	if view.Directory.Version == "" {
		return View{}, errors.New("目录缓存没有版本号")
	}
	return view, nil
}

func (s *directoryState) saveCache(view View) error {
	if s.cacheDir == "" {
		return nil
	}
	if err := os.MkdirAll(s.cacheDir, 0o700); err != nil {
		return err
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.cacheDir, directoryCacheFile+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(s.cacheDir, directoryCacheFile))
}

// DirectoryFromProto 把主机网关下发的目录转换为组件目录的结构。
func DirectoryFromProto(snapshot *directorypb.DirectorySnapshot) servicecatalog.Directory {
	if snapshot == nil {
		return servicecatalog.Directory{}
	}
	out := servicecatalog.Directory{Version: snapshot.GetVersion()}
	for _, service := range snapshot.GetServices() {
		out.Services = append(out.Services, servicecatalog.ServiceHosts{Path: service.GetPath(), HostIDs: append([]string(nil), service.GetHostIds()...)})
	}
	for _, component := range snapshot.GetComponents() {
		out.Components = append(out.Components, servicecatalog.ComponentHosts{ComponentID: component.GetComponentId(), HostIDs: append([]string(nil), component.GetHostIds()...)})
	}
	for _, host := range snapshot.GetHosts() {
		out.Hosts = append(out.Hosts, servicecatalog.DirectoryHost{ID: host.GetHostId(), Address: host.GetAddress(), PrivateAddress: host.GetPrivateAddress(), Region: host.GetRegion()})
	}
	return out
}

// DirectoryToProto 把编译出的目录转换为下发给主机网关的结构。
func DirectoryToProto(directory servicecatalog.Directory) *directorypb.DirectorySnapshot {
	out := &directorypb.DirectorySnapshot{Version: directory.Version}
	for _, service := range directory.Services {
		out.Services = append(out.Services, &directorypb.ServiceHosts{Path: service.Path, HostIds: append([]string(nil), service.HostIDs...)})
	}
	for _, component := range directory.Components {
		out.Components = append(out.Components, &directorypb.ComponentHosts{ComponentId: component.ComponentID, HostIds: append([]string(nil), component.HostIDs...)})
	}
	for _, host := range directory.Hosts {
		out.Hosts = append(out.Hosts, &directorypb.HostInfo{HostId: host.ID, Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region})
	}
	return out
}
