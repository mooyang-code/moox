//go:build !cgo

package duckdb

import (
	"context"
	"errors"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// Options 保存 DuckDB 视图存储打开配置。
type Options struct {
	Path string
}

// ViewStore 是 no-cgo 构建下的占位类型；真实 DuckDB 视图存储必须启用 CGO。
type ViewStore struct{}

var errDuckDBRequiresCGO = errors.New("duckdb view storage requires CGO_ENABLED=1; rebuild moox-storage with cgo to use disk-backed DuckDB")

func Open(opts Options) (*ViewStore, error) {
	if opts.Path == "" {
		return nil, errors.New("duckdb path is required")
	}
	return nil, errDuckDBRequiresCGO
}

func (s *ViewStore) Close() error {
	return nil
}

func (s *ViewStore) CreateResultTable(ctx context.Context, tableName string, columns []*pb.ViewColumn) error {
	return errDuckDBRequiresCGO
}

func (s *ViewStore) InsertRows(ctx context.Context, tableName string, rows []*pb.TimeSeriesRow) error {
	return errDuckDBRequiresCGO
}

func (s *ViewStore) ListResultTables(ctx context.Context) ([]string, error) {
	return nil, errDuckDBRequiresCGO
}

func (s *ViewStore) DropResultTable(ctx context.Context, tableName string) error {
	return errDuckDBRequiresCGO
}

func (s *ViewStore) QueryTimeSeriesRows(ctx context.Context, tableName string, req *pb.QueryTimeSeriesRowsReq) ([]*pb.ResultColumn, []*pb.TimeSeriesRow, *pb.PageResult, error) {
	return nil, nil, nil, errDuckDBRequiresCGO
}
