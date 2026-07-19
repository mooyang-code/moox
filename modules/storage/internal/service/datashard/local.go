package datashard

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	contracts "github.com/mooyang-code/moox/modules/storage/internal/service/datashard/contracts"
	devicepebble "github.com/mooyang-code/moox/modules/storage/internal/service/datashard/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

const defaultRoot = "var/storage"

// LocalClientOptions 保存本地 PrimaryStore 客户端配置。
type LocalClientOptions struct {
	Root       string
	PebblePath string
	ShardID    string
	Pebble     contracts.FactStore
	Outbox     OutboxConfig
}

// LocalClient 在进程内直接调用 PrimaryStore 服务实现。
type LocalClient struct {
	pebblePath string
	pebble     contracts.FactStore
	outbox     OutboxConfig
	shardID    string
	opened     sync.Map
}

// Ready performs the same lightweight store-open and shard-head probe used by
// the service readiness endpoint. A directory existing on disk is not enough
// to prove that Pebble is open and serving the configured shard.
func (c *LocalClient) Ready(ctx context.Context) bool {
	if c == nil {
		return false
	}
	store, err := c.factStore()
	if err != nil {
		return false
	}
	headReader, ok := store.(contracts.ShardHeadReader)
	if !ok {
		return false
	}
	_, err = headReader.HeadSequence(ctx)
	return err == nil
}

// sharedPebbleStore 保存进程内共享 Pebble Store 及其引用计数。
type sharedPebbleStore struct {
	store contracts.FactStore
	refs  int
}

var pebbleStores = struct {
	sync.Mutex
	items map[string]*sharedPebbleStore
}{items: make(map[string]*sharedPebbleStore)}

func NewLocalClient(opts LocalClientOptions) *LocalClient {
	return &LocalClient{pebblePath: localPebblePath(opts.Root, opts.PebblePath), pebble: opts.Pebble, outbox: opts.Outbox, shardID: opts.ShardID}
}

func (c *LocalClient) WriteRows(ctx context.Context, target *pb.ShardTarget, rows []*pb.ShardRow) error {
	return c.writeRows(ctx, target, rows, nil)
}

func (c *LocalClient) writeRows(ctx context.Context, target *pb.ShardTarget, rows []*pb.ShardRow, message []byte) error {
	switch target.GetEngine() {
	case "", "pebble":
		store, err := c.factStore()
		if err != nil {
			return err
		}
		if err := validateTargetShard(target, store); err != nil {
			return err
		}
		if err := validateTargetRows(target, rows); err != nil {
			return err
		}
		if len(message) > 0 {
			if err := rejectOutboxBacklog(ctx, store, c.outbox); err != nil {
				return err
			}
		}
		if messageStore, ok := store.(interface {
			WriteRowsWithOutbox(context.Context, []*pb.ShardRow, *contracts.OutboxEntry) error
		}); ok && len(message) > 0 {
			return messageStore.WriteRowsWithOutbox(ctx, rows, &contracts.OutboxEntry{MessageID: "", Data: append([]byte(nil), message...), CreatedAt: time.Now().UTC()})
		}
		if len(message) == 0 {
			if committed, ok := store.(contracts.CommittedWriter); ok {
				return committed.WriteRowsWithCommittedMessage(ctx, rows)
			}
		}
		return fmt.Errorf("DataShard store must implement committed writes")
	default:
		return fmt.Errorf("unsupported write engine %s", target.GetEngine())
	}
}

func rejectOutboxBacklog(ctx context.Context, store contracts.FactStore, cfg OutboxConfig) error {
	cfg = cfg.normalized()
	entries, err := store.ListOutbox(ctx, 0, cfg.MaxRows+1, cfg.MaxBytes+1)
	if err != nil {
		return err
	}
	if len(entries) >= cfg.MaxRows {
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

func (c *LocalClient) ReadRows(ctx context.Context, target *pb.ShardTarget, req *pb.ReadRowsReq) ([]*pb.ShardRow, *pb.PageResult, error) {
	switch target.GetEngine() {
	case "", "pebble":
		store, err := c.factStore()
		if err != nil {
			return nil, nil, err
		}
		if err := validateTargetShard(target, store); err != nil {
			return nil, nil, err
		}
		if err := validateTargetKeys(target, req.GetKeys()); err != nil {
			return nil, nil, err
		}
		return store.ReadRows(ctx, req.GetKeys(), req.GetVersionRange(), req.GetOrder(), req.GetColumnNames(), req.GetPage())
	default:
		return nil, nil, fmt.Errorf("unsupported read engine %s", target.GetEngine())
	}
}

func (c *LocalClient) DeleteRows(ctx context.Context, target *pb.ShardTarget, keys []*pb.ShardKey) error {
	if target.GetEngine() != "" && target.GetEngine() != "pebble" {
		return fmt.Errorf("unsupported delete engine %s", target.GetEngine())
	}
	store, err := c.factStore()
	if err != nil {
		return err
	}
	if err := validateTargetShard(target, store); err != nil {
		return err
	}
	if err := validateTargetKeys(target, keys); err != nil {
		return err
	}
	deleter, ok := store.(contracts.FactDeleter)
	if !ok {
		return fmt.Errorf("primary store does not support row deletion")
	}
	if committed, ok := store.(contracts.CommittedDeleter); ok {
		return committed.DeleteRowsWithCommittedMessage(ctx, keys)
	}
	return deleter.DeleteRows(ctx, keys)
}

func (c *LocalClient) ListOutbox(ctx context.Context, after uint64, maxItems, maxBytes int) ([]*contracts.OutboxEntry, error) {
	store, err := c.factStore()
	if err != nil {
		return nil, err
	}
	return store.ListOutbox(ctx, after, maxItems, maxBytes)
}

func (c *LocalClient) HeadSequence(ctx context.Context, target *pb.ShardTarget) (uint64, error) {
	store, err := c.factStore()
	if err != nil {
		return 0, err
	}
	if err := validateTargetShard(target, store); err != nil {
		return 0, err
	}
	headReader, ok := store.(contracts.ShardHeadReader)
	if !ok {
		return 0, fmt.Errorf("primary store does not expose shard head")
	}
	return headReader.HeadSequence(ctx)
}

func (c *LocalClient) ScanRows(ctx context.Context, target *pb.ShardTarget, req *pb.ScanRowsReq) ([]*pb.ShardRow, *pb.PageResult, error) {
	switch target.GetEngine() {
	case "", "pebble":
		store, err := c.factStore()
		if err != nil {
			return nil, nil, err
		}
		if err := validateTargetShard(target, store); err != nil {
			return nil, nil, err
		}
		if req.GetKeyPrefix() != "" {
			if scanner, ok := store.(contracts.FactPrefixScanner); ok {
				return scanner.ScanRowsWithPrefix(ctx, target, req.GetDataKind(), req.GetVersionRange(), req.GetOrder(), req.GetColumnNames(), req.GetPage(), req.GetKeyPrefix())
			}
		}
		return store.ScanRows(ctx, target, req.GetDataKind(), req.GetVersionRange(), req.GetOrder(), req.GetColumnNames(), req.GetPage())
	default:
		return nil, nil, fmt.Errorf("unsupported scan engine %s", target.GetEngine())
	}
}

func validateTargetShard(target *pb.ShardTarget, store contracts.FactStore) error {
	if target == nil {
		return fmt.Errorf("target is required")
	}
	identity, ok := store.(contracts.ShardIdentity)
	if !ok {
		// Lightweight test and remote adapters may not expose a local shard
		// identity; the concrete Pebble DataShard does and is checked below.
		return nil
	}
	identityTarget := target.GetShardId()
	if identityTarget == "" {
		return fmt.Errorf("target shard identity is required")
	}
	if identity.ShardID() == "" {
		return fmt.Errorf("DataShard identity is unavailable for target %q", target.GetNodeId())
	}
	if identityTarget != identity.ShardID() {
		return fmt.Errorf("target shard %q does not match DataShard %q", identityTarget, identity.ShardID())
	}
	return nil
}

func validateTargetRows(target *pb.ShardTarget, rows []*pb.ShardRow) error {
	if target == nil {
		return fmt.Errorf("target is required")
	}
	for _, row := range rows {
		if row == nil || row.GetKey() == nil {
			return fmt.Errorf("row and row key are required")
		}
		if target.GetSpaceId() != "" && row.GetKey().GetSpaceId() != target.GetSpaceId() {
			return fmt.Errorf("row space_id %q does not match target %q", row.GetKey().GetSpaceId(), target.GetSpaceId())
		}
		if target.GetDatasetId() != "" && row.GetKey().GetDatasetId() != target.GetDatasetId() {
			return fmt.Errorf("row dataset_id %q does not match target %q", row.GetKey().GetDatasetId(), target.GetDatasetId())
		}
	}
	return nil
}

func validateTargetKeys(target *pb.ShardTarget, keys []*pb.ShardKey) error {
	if target == nil {
		return fmt.Errorf("target is required")
	}
	for _, key := range keys {
		if key == nil {
			return fmt.Errorf("key is required")
		}
		if target.GetSpaceId() != "" && key.GetSpaceId() != target.GetSpaceId() {
			return fmt.Errorf("key space_id %q does not match target %q", key.GetSpaceId(), target.GetSpaceId())
		}
		if target.GetDatasetId() != "" && key.GetDatasetId() != target.GetDatasetId() {
			return fmt.Errorf("key dataset_id %q does not match target %q", key.GetDatasetId(), target.GetDatasetId())
		}
	}
	return nil
}

func (c *LocalClient) factStore() (contracts.FactStore, error) {
	if c.pebble != nil {
		return c.pebble, nil
	}
	key := pebbleStoreKey(c.pebblePath, c.shardID)
	if _, ok := c.opened.Load(key); ok {
		return getPebbleStore(c.pebblePath, c.shardID)
	}
	store, err := acquirePebbleStore(c.pebblePath, c.shardID)
	if err != nil {
		return nil, err
	}
	c.opened.Store(key, struct{}{})
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

func acquirePebbleStore(path, shardID string) (contracts.FactStore, error) {
	storeKey := pebbleStoreKey(path, shardID)
	pebbleStores.Lock()
	if shared := pebbleStores.items[storeKey]; shared != nil {
		shared.refs++
		store := shared.store
		pebbleStores.Unlock()
		return store, nil
	}
	pebbleStores.Unlock()

	shardID = normalizedShardID(shardID)
	opened, err := devicepebble.Open(devicepebble.Options{Path: path, ShardID: shardID})
	if err != nil {
		return nil, err
	}

	pebbleStores.Lock()
	defer pebbleStores.Unlock()
	if shared := pebbleStores.items[storeKey]; shared != nil {
		shared.refs++
		_ = opened.Close()
		return shared.store, nil
	}
	pebbleStores.items[storeKey] = &sharedPebbleStore{store: opened, refs: 1}
	return opened, nil
}

func getPebbleStore(path, shardID string) (contracts.FactStore, error) {
	storeKey := pebbleStoreKey(path, shardID)
	pebbleStores.Lock()
	if shared := pebbleStores.items[storeKey]; shared != nil {
		pebbleStores.Unlock()
		return shared.store, nil
	}
	pebbleStores.Unlock()
	return acquirePebbleStore(path, shardID)
}

func releasePebbleStore(storeKey string) error {
	pebbleStores.Lock()
	shared := pebbleStores.items[storeKey]
	if shared == nil {
		pebbleStores.Unlock()
		return nil
	}
	shared.refs--
	if shared.refs > 0 {
		pebbleStores.Unlock()
		return nil
	}
	delete(pebbleStores.items, storeKey)
	pebbleStores.Unlock()
	if closer, ok := shared.store.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func normalizedShardID(shardID string) string {
	return strings.TrimSpace(shardID)
}

func pebbleStoreKey(path, shardID string) string {
	return path + "\x00" + normalizedShardID(shardID)
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
