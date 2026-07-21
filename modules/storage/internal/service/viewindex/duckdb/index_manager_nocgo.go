//go:build !cgo

package duckdb

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

var errCGODisabled = errors.New("duckdb view indexes require CGO")

type IndexManagerOptions struct{ Root string }
type IndexManager struct{}

func OpenIndexManager(IndexManagerOptions) (*IndexManager, error) { return nil, errCGODisabled }
func (*IndexManager) Engine() string                              { return "duckdb" }
func (*IndexManager) Prepare(context.Context, string, viewindex.ViewIndexSchema) error {
	return errCGODisabled
}
func (*IndexManager) Write(context.Context, string, viewindex.ViewIndexWriteBatch) error {
	return errCGODisabled
}
func (*IndexManager) Query(context.Context, string, viewindex.QuerySpec) ([]*pb.RowFieldValues, int64, error) {
	return nil, 0, errCGODisabled
}
func (*IndexManager) Stat(context.Context, string) (viewindex.ViewIndexStats, error) {
	return viewindex.ViewIndexStats{}, errCGODisabled
}
func (*IndexManager) Remove(context.Context, string) error { return errCGODisabled }
func (*IndexManager) Close() error                         { return nil }
