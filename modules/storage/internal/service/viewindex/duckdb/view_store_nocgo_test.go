//go:build !cgo

package duckdb

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenRequiresPath(t *testing.T) {
	_, err := Open(Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duckdb path is required")
}

func TestOpenReturnsCGORequiredError(t *testing.T) {
	_, err := Open(Options{Path: t.TempDir()})
	require.ErrorIs(t, err, errDuckDBRequiresCGO)
}

func TestViewStoreNoCGOMethodsReturnCGORequiredError(t *testing.T) {
	store := &ViewStore{}
	assert.Equal(t, "duckdb", store.Engine())
	require.NoError(t, store.Close())

	ctx := context.Background()
	schema := viewindex.ViewIndexSchema{}
	require.ErrorIs(t, store.Prepare(ctx, "idx-1", schema), errDuckDBRequiresCGO)
	require.ErrorIs(t, store.Write(ctx, "idx-1", viewindex.BatchWrite{}), errDuckDBRequiresCGO)
	_, err := store.Stat(ctx, "idx-1")
	require.ErrorIs(t, err, errDuckDBRequiresCGO)
	require.ErrorIs(t, store.Remove(ctx, "idx-1"), errDuckDBRequiresCGO)
	require.ErrorIs(t, store.CreateResultTable(ctx, "t1", nil), errDuckDBRequiresCGO)
	require.ErrorIs(t, store.InsertRows(ctx, "t1", nil), errDuckDBRequiresCGO)
	_, err = store.ListResultTables(ctx)
	require.ErrorIs(t, err, errDuckDBRequiresCGO)
	require.ErrorIs(t, store.DropResultTable(ctx, "t1"), errDuckDBRequiresCGO)
	_, _, _, err = store.QueryTimeSeriesRows(ctx, "t1", &pb.QueryTimeSeriesRowsReq{})
	require.ErrorIs(t, err, errDuckDBRequiresCGO)
}

func TestOpenIndexManagerRequiresRoot(t *testing.T) {
	_, err := OpenIndexManager(IndexManagerOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "view index root is required")
}

func TestIndexManagerNoCGOMethodsReturnCGORequiredError(t *testing.T) {
	_, err := OpenIndexManager(IndexManagerOptions{Root: t.TempDir()})
	require.ErrorIs(t, err, errDuckDBRequiresCGO)

	mgr := &IndexManager{}
	assert.Equal(t, "duckdb", mgr.Engine())
	require.NoError(t, mgr.Close())

	ctx := context.Background()
	require.ErrorIs(t, mgr.Prepare(ctx, "idx-1", viewindex.ViewIndexSchema{}), errDuckDBRequiresCGO)
	require.ErrorIs(t, mgr.Write(ctx, "idx-1", viewindex.BatchWrite{}), errDuckDBRequiresCGO)
	_, err = mgr.Stat(ctx, "idx-1")
	require.ErrorIs(t, err, errDuckDBRequiresCGO)
	require.ErrorIs(t, mgr.Remove(ctx, "idx-1"), errDuckDBRequiresCGO)
	_, err = mgr.List(ctx)
	require.ErrorIs(t, err, errDuckDBRequiresCGO)
	_, _, _, err = mgr.QueryTimeSeriesRows(ctx, "idx-1", &pb.QueryTimeSeriesRowsReq{})
	require.ErrorIs(t, err, errDuckDBRequiresCGO)
}
