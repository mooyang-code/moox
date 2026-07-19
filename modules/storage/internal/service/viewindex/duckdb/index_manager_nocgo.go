//go:build legacy_viewindex && !cgo

package duckdb

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

var ErrIndexClosing = errors.New("view index is closing")

type IndexManagerOptions struct {
	Root string
}

type IndexManager struct{}

func OpenIndexManager(opts IndexManagerOptions) (*IndexManager, error) {
	if opts.Root == "" {
		return nil, errors.New("view index root is required")
	}
	return nil, errDuckDBRequiresCGO
}

func (m *IndexManager) Engine() string { return "duckdb" }

func (m *IndexManager) Prepare(context.Context, string, viewindex.ViewIndexSchema) error {
	return errDuckDBRequiresCGO
}

func (m *IndexManager) Write(context.Context, string, viewindex.BatchWrite) error {
	return errDuckDBRequiresCGO
}

func (m *IndexManager) Apply(context.Context, string, viewindex.ViewIndexApplyBatch) error {
	return errDuckDBRequiresCGO
}

func (m *IndexManager) DeleteTimeSeriesRows(context.Context, string, []*pb.TimeSeriesRow) error {
	return errDuckDBRequiresCGO
}

func (m *IndexManager) Stat(context.Context, string) (viewindex.ViewIndexStats, error) {
	return viewindex.ViewIndexStats{}, errDuckDBRequiresCGO
}

func (m *IndexManager) Remove(context.Context, string) error { return errDuckDBRequiresCGO }

func (m *IndexManager) List(context.Context) ([]string, error) { return nil, errDuckDBRequiresCGO }

func (m *IndexManager) QueryTimeSeriesRows(context.Context, string, *pb.QueryTimeSeriesRowsReq) ([]*pb.ResultColumn, []*pb.TimeSeriesRow, *pb.PageResult, error) {
	return nil, nil, nil, errDuckDBRequiresCGO
}

func (m *IndexManager) Close() error { return nil }
