package primary

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

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
	Outbox     OutboxConfig
}

// LocalClient 在进程内直接调用 PrimaryStore 服务实现。
type LocalClient struct {
	pebblePath string
	pebble     device.FactStore
	outbox     OutboxConfig
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
	return &LocalClient{pebblePath: localPebblePath(opts.Root, opts.PebblePath), pebble: opts.Pebble, outbox: opts.Outbox}
}

func (c *LocalClient) WriteRows(ctx context.Context, target *pb.PrimaryStoreTarget, rows []*pb.PrimaryStoreRow) error {
	return c.writeRows(ctx, target, rows, nil)
}

func (c *LocalClient) WriteRowsWithMessage(ctx context.Context, target *pb.PrimaryStoreTarget, rows []*pb.PrimaryStoreRow, message []byte) error {
	return c.writeRows(ctx, target, rows, message)
}

func (c *LocalClient) writeRows(ctx context.Context, target *pb.PrimaryStoreTarget, rows []*pb.PrimaryStoreRow, message []byte) error {
	switch target.GetEngine() {
	case "", "pebble":
		store, err := c.factStore()
		if err != nil {
			return err
		}
		if len(message) > 0 {
			if err := rejectOutboxBacklog(ctx, store, c.outbox); err != nil {
				return err
			}
		}
		if messageStore, ok := store.(interface {
			WriteRowsWithOutbox(context.Context, []*pb.PrimaryStoreRow, *device.OutboxEntry) error
		}); ok && len(message) > 0 {
			return messageStore.WriteRowsWithOutbox(ctx, rows, &device.OutboxEntry{MessageID: "", Data: append([]byte(nil), message...), CreatedAt: time.Now().UTC()})
		}
		return store.WriteRows(ctx, rows)
	default:
		return fmt.Errorf("unsupported write engine %s", target.GetEngine())
	}
}

func rejectOutboxBacklog(ctx context.Context, store device.FactStore, cfg OutboxConfig) error {
	cfg = cfg.normalized()
	entries, err := store.ListOutbox(ctx, 0, cfg.MaxRows+1, cfg.MaxBytes+1)
	if err != nil {
		return err
	}
	if len(entries) > cfg.MaxRows {
		return fmt.Errorf("storage outbox row limit exceeded: %d", cfg.MaxRows)
	}
	var bytes int
	for _, entry := range entries {
		if entry != nil {
			bytes += len(entry.Data)
		}
	}
	if bytes >= cfg.MaxBytes {
		return fmt.Errorf("storage outbox byte limit exceeded: %d", cfg.MaxBytes)
	}
	if len(entries) > 0 && cfg.MaxAge > 0 && time.Since(entries[0].CreatedAt) > cfg.MaxAge {
		return fmt.Errorf("storage outbox oldest entry exceeds max age %s", cfg.MaxAge)
	}
	return nil
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

func (c *LocalClient) DeleteRows(ctx context.Context, target *pb.PrimaryStoreTarget, keys []*pb.PrimaryStoreKey) error {
	if target.GetEngine() != "" && target.GetEngine() != "pebble" {
		return fmt.Errorf("unsupported delete engine %s", target.GetEngine())
	}
	store, err := c.factStore()
	if err != nil {
		return err
	}
	deleter, ok := store.(device.FactDeleter)
	if !ok {
		return fmt.Errorf("primary store does not support row deletion")
	}
	return deleter.DeleteRows(ctx, keys)
}

func (c *LocalClient) ScanRows(ctx context.Context, target *pb.PrimaryStoreTarget, req *pb.ScanPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	switch target.GetEngine() {
	case "", "pebble":
		store, err := c.factStore()
		if err != nil {
			return nil, nil, err
		}
		if req.GetKeyPrefix() != "" {
			if scanner, ok := store.(device.FactPrefixScanner); ok {
				return scanner.ScanRowsWithPrefix(ctx, target, req.GetDataKind(), req.GetVersionRange(), req.GetOrder(), req.GetColumnNames(), req.GetPage(), req.GetKeyPrefix())
			}
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
