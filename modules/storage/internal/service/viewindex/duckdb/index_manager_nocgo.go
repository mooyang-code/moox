//go:build !cgo

package duckdb

import (
	"context"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type IndexManagerOptions struct{ Root string }
type IndexManager struct{}

func OpenIndexManager(IndexManagerOptions) (*IndexManager, error) { return nil, ErrUnavailable }
func (*IndexManager) Engine() string                              { return "duckdb" }
func (*IndexManager) Prepare(context.Context, string, viewindex.ViewIndexSchema) error {
	return ErrUnavailable
}
func (*IndexManager) Write(context.Context, string, viewindex.ViewIndexWriteBatch) error {
	return ErrUnavailable
}
func (*IndexManager) Query(context.Context, string, viewindex.QuerySpec) ([]*pb.RowFieldValues, int64, error) {
	return nil, 0, ErrUnavailable
}
func (*IndexManager) Stat(context.Context, string) (viewindex.ViewIndexStats, error) {
	return viewindex.ViewIndexStats{}, ErrUnavailable
}
func (*IndexManager) Remove(context.Context, string) error { return ErrUnavailable }
func (*IndexManager) Close() error                         { return nil }
