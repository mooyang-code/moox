package duckdb

import (
	"context"
	"errors"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type IndexManagerOptions struct{ Root string }
type IndexManager struct{ core *viewindex.MemoryEngine }

func OpenIndexManager(opts IndexManagerOptions) (*IndexManager, error) {
	if strings.TrimSpace(opts.Root) == "" {
		return nil, errors.New("view index root is required")
	}
	return &IndexManager{core: viewindex.NewMemoryEngine("duckdb", opts.Root)}, nil
}
func (m *IndexManager) Engine() string { return "duckdb" }
func (m *IndexManager) Prepare(c context.Context, id string, s viewindex.ViewIndexSchema) error {
	return m.core.Prepare(c, id, s)
}
func (m *IndexManager) Apply(c context.Context, id string, b viewindex.ViewIndexApplyBatch) error {
	return m.core.Apply(c, id, b)
}
func (m *IndexManager) Write(c context.Context, id string, b viewindex.BatchWrite) error {
	return m.core.Write(c, id, b)
}
func (m *IndexManager) Stat(c context.Context, id string) (viewindex.ViewIndexStats, error) {
	return m.core.Stat(c, id)
}
func (m *IndexManager) Remove(c context.Context, id string) error { return m.core.Remove(c, id) }
func (m *IndexManager) List(c context.Context) ([]string, error)  { return m.core.List(c) }
func (m *IndexManager) Query(ctx context.Context, id string, keys []*pb.RowKey, fields []string) ([]*pb.RowFieldValues, error) {
	return m.core.Query(ctx, id, keys, fields)
}
func (m *IndexManager) Close() error { return nil }
