package primary

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/device"
	devicepebble "github.com/mooyang-code/moox/modules/storage/internal/infra/device/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

const defaultRoot = "var/storage"

// LocalClientOptions 保存本地 PrimaryStore 客户端配置。
type LocalClientOptions struct {
	Root       string
	PebblePath string
	Pebble     device.FactStore
}

// LocalClient 在进程内直接调用 PrimaryStore 服务实现。
type LocalClient struct {
	pebblePath string
	pebble     device.FactStore
	opened     sync.Map
}

// sharedPebbleStore 保存进程内共享 Pebble Store 及其引用计数。
type sharedPebbleStore struct {
	store device.FactStore
	refs  int
}

var pebbleStores = struct {
	sync.Mutex
	items map[string]*sharedPebbleStore
}{items: make(map[string]*sharedPebbleStore)}

func NewLocalClient(opts LocalClientOptions) *LocalClient {
	return &LocalClient{pebblePath: localPebblePath(opts.Root, opts.PebblePath), pebble: opts.Pebble}
}

func (c *LocalClient) WriteRows(ctx context.Context, target *pb.PrimaryStoreTarget, rows []*pb.PrimaryStoreRow) error {
	switch target.GetEngine() {
	case "", "pebble":
		store, err := c.factStore()
		if err != nil {
			return err
		}
		return store.WriteRows(ctx, rows)
	default:
		return fmt.Errorf("unsupported write engine %s", target.GetEngine())
	}
}

func (c *LocalClient) ReadRows(ctx context.Context, target *pb.PrimaryStoreTarget, req *pb.ReadPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	switch target.GetEngine() {
	case "", "pebble":
		store, err := c.factStore()
		if err != nil {
			return nil, nil, err
		}
		return store.ReadRows(ctx, req.GetKeys(), req.GetVersionRange(), req.GetOrder(), req.GetColumnNames(), req.GetPage())
	default:
		return nil, nil, fmt.Errorf("unsupported read engine %s", target.GetEngine())
	}
}

func (c *LocalClient) ScanRows(ctx context.Context, target *pb.PrimaryStoreTarget, req *pb.ScanPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	switch target.GetEngine() {
	case "", "pebble":
		store, err := c.factStore()
		if err != nil {
			return nil, nil, err
		}
		return store.ScanRows(ctx, target, req.GetDataKind(), req.GetVersionRange(), req.GetOrder(), req.GetColumnNames(), req.GetPage())
	default:
		return nil, nil, fmt.Errorf("unsupported scan engine %s", target.GetEngine())
	}
}

func (c *LocalClient) factStore() (device.FactStore, error) {
	if c.pebble != nil {
		return c.pebble, nil
	}
	if _, ok := c.opened.Load(c.pebblePath); ok {
		return getPebbleStore(c.pebblePath)
	}
	store, err := acquirePebbleStore(c.pebblePath)
	if err != nil {
		return nil, err
	}
	c.opened.Store(c.pebblePath, struct{}{})
	return store, nil
}

func (c *LocalClient) Close() error {
	if c == nil || c.pebble != nil {
		return nil
	}
	var firstErr error
	c.opened.Range(func(key, _ any) bool {
		path, _ := key.(string)
		if err := releasePebbleStore(path); err != nil && firstErr == nil {
			firstErr = err
		}
		c.opened.Delete(key)
		return true
	})
	return firstErr
}

func acquirePebbleStore(path string) (device.FactStore, error) {
	pebbleStores.Lock()
	if shared := pebbleStores.items[path]; shared != nil {
		shared.refs++
		store := shared.store
		pebbleStores.Unlock()
		return store, nil
	}
	pebbleStores.Unlock()

	opened, err := devicepebble.Open(devicepebble.Options{Path: path})
	if err != nil {
		return nil, err
	}

	pebbleStores.Lock()
	defer pebbleStores.Unlock()
	if shared := pebbleStores.items[path]; shared != nil {
		shared.refs++
		_ = opened.Close()
		return shared.store, nil
	}
	pebbleStores.items[path] = &sharedPebbleStore{store: opened, refs: 1}
	return opened, nil
}

func getPebbleStore(path string) (device.FactStore, error) {
	pebbleStores.Lock()
	if shared := pebbleStores.items[path]; shared != nil {
		pebbleStores.Unlock()
		return shared.store, nil
	}
	pebbleStores.Unlock()
	return acquirePebbleStore(path)
}

func releasePebbleStore(path string) error {
	pebbleStores.Lock()
	shared := pebbleStores.items[path]
	if shared == nil {
		pebbleStores.Unlock()
		return nil
	}
	shared.refs--
	if shared.refs > 0 {
		pebbleStores.Unlock()
		return nil
	}
	delete(pebbleStores.items, path)
	pebbleStores.Unlock()
	if closer, ok := shared.store.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func localPebblePath(root string, pebblePath string) string {
	if pebblePath != "" {
		return filepath.Join(pebblePath, "main")
	}
	if root == "" {
		root = os.Getenv("MOOX_STORAGE_HOME")
	}
	if root == "" {
		root = defaultRoot
	}
	return filepath.Join(root, "pebble", "main")
}
