package bleve

import (
	"context"
	"errors"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type Options struct{ Path string }
type Index struct{ core *viewindex.MemoryEngine }

func Open(opts Options) (*Index, error) {
	if strings.TrimSpace(opts.Path) == "" {
		return nil, errors.New("bleve path is required")
	}
	return &Index{core: viewindex.NewMemoryEngine("bleve", opts.Path)}, nil
}
func OpenExisting(opts Options) (*Index, error) { return Open(opts) }
func (i *Index) Engine() string                 { return "bleve" }
func (i *Index) Prepare(c context.Context, id string, s viewindex.ViewIndexSchema) error {
	return i.core.Prepare(c, id, s)
}
func (i *Index) Apply(c context.Context, id string, b viewindex.ViewIndexApplyBatch) error {
	return i.core.Apply(c, id, b)
}
func (i *Index) Write(c context.Context, id string, b viewindex.BatchWrite) error {
	return i.core.Write(c, id, b)
}
func (i *Index) Stat(c context.Context, id string) (viewindex.ViewIndexStats, error) {
	return i.core.Stat(c, id)
}
func (i *Index) Remove(c context.Context, id string) error { return i.core.Remove(c, id) }
func (i *Index) List(c context.Context) ([]string, error)  { return i.core.List(c) }
func (i *Index) Query(ctx context.Context, id string, keys []*pb.RowKey, fields []string) ([]*pb.RowFieldValues, error) {
	return i.core.Query(ctx, id, keys, fields)
}
func (i *Index) Close() error { return nil }
