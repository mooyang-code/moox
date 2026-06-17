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

type LocalOptions struct {
	Pebble device.FactStore
}

type Local struct {
	pebble device.FactStore
}

func NewLocal(opts LocalOptions) *Local {
	return &Local{pebble: opts.Pebble}
}

func (l *Local) WriteRows(ctx context.Context, ref *pb.PrimaryTarget, rows []*pb.DataRow, mode pb.WriteMode) error {
	switch ref.GetEngine() {
	case "", "pebble":
		return l.pebble.WriteRows(ctx, rows, mode)
	default:
		return fmt.Errorf("unsupported write engine %s", ref.GetEngine())
	}
}

func (l *Local) ReadRows(ctx context.Context, ref *pb.PrimaryTarget, req *pb.ReadRowsReq) ([]*pb.DataRow, *pb.PageResult, error) {
	switch ref.GetEngine() {
	case "", "pebble":
		return l.pebble.ReadRows(ctx, req.GetScope(), req.GetReadMode(), req.GetTimeRange(), req.GetSnapshotTime(), req.GetRowIds(), req.GetColumnNames(), req.GetPage())
	default:
		return nil, nil, fmt.Errorf("unsupported read engine %s", ref.GetEngine())
	}
}

type LocalClientOptions struct {
	Root       string
	PebblePath string
	Pebble     device.FactStore
}

type LocalClient struct {
	pebblePath string
	pebble     device.FactStore
}

var pebbleStores sync.Map

func NewLocalClient(opts LocalClientOptions) *LocalClient {
	return &LocalClient{pebblePath: localPebblePath(opts.Root, opts.PebblePath), pebble: opts.Pebble}
}

func (c *LocalClient) WriteRows(ctx context.Context, target *pb.PrimaryTarget, rows []*pb.DataRow, mode pb.WriteMode) error {
	switch target.GetEngine() {
	case "", "pebble":
		store, err := c.factStore()
		if err != nil {
			return err
		}
		return store.WriteRows(ctx, rows, mode)
	default:
		return fmt.Errorf("unsupported write engine %s", target.GetEngine())
	}
}

func (c *LocalClient) ReadRows(ctx context.Context, target *pb.PrimaryTarget, req *pb.ReadRowsReq) ([]*pb.DataRow, *pb.PageResult, error) {
	switch target.GetEngine() {
	case "", "pebble":
		store, err := c.factStore()
		if err != nil {
			return nil, nil, err
		}
		return store.ReadRows(ctx, req.GetScope(), req.GetReadMode(), req.GetTimeRange(), req.GetSnapshotTime(), req.GetRowIds(), req.GetColumnNames(), req.GetPage())
	default:
		return nil, nil, fmt.Errorf("unsupported read engine %s", target.GetEngine())
	}
}

func (c *LocalClient) factStore() (device.FactStore, error) {
	if c.pebble != nil {
		return c.pebble, nil
	}
	if value, ok := pebbleStores.Load(c.pebblePath); ok {
		return value.(*devicepebble.Store), nil
	}
	opened, err := devicepebble.Open(devicepebble.Options{Path: c.pebblePath})
	if err != nil {
		return nil, err
	}
	actual, loaded := pebbleStores.LoadOrStore(c.pebblePath, opened)
	if loaded {
		_ = opened.Close()
	}
	return actual.(*devicepebble.Store), nil
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
