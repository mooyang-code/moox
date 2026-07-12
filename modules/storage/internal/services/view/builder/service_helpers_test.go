package builder

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetInfoError(t *testing.T) {
	assert.NoError(t, retInfoError(nil))
	assert.NoError(t, retInfoError(&pb.RetInfo{Code: pb.ErrorCode_SUCCESS}))
	err := retInfoError(&pb.RetInfo{Code: pb.ErrorCode_INVALID_PARAM, Msg: "bad"})
	require.Error(t, err)
	assert.Equal(t, "bad", err.Error())
}

func TestCompleteDeriveItem_SendsError(t *testing.T) {
	done := make(chan error, 1)
	completeDeriveItem(done, errors.New("boom"))
	assert.Equal(t, "boom", (<-done).Error())
	completeDeriveItem(nil, errors.New("ignored"))
}

func TestWaitDeriveResults_ReturnsFirstError(t *testing.T) {
	first := make(chan error, 1)
	second := make(chan error, 1)
	first <- errors.New("first")
	second <- nil
	err := waitDeriveResults(context.Background(), []chan error{first, second})
	require.Error(t, err)
	assert.Equal(t, "first", err.Error())
}

func TestWaitDeriveResults_HonorsContextCancel(t *testing.T) {
	pending := make(chan error)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := waitDeriveResults(ctx, []chan error{pending})
	require.Error(t, err)
}

func TestWritableIndexSet(t *testing.T) {
	active := viewindex.ViewIndexID("crypto", "kline_view", viewindex.SlotA)
	warming := viewindex.ViewIndexID("crypto", "kline_view", viewindex.SlotB)
	columns := []*pb.ViewColumn{{ColumnName: "close"}}
	schemaHash := viewindex.HashViewIndexSchema(viewindex.ViewIndexSchema{
		SpaceID: "crypto", ViewID: "kline_view", Engine: "duckdb", Columns: columns,
	})
	item := &pb.View{
		SpaceId: "crypto", ViewId: "kline_view", Engine: "duckdb",
		ViewVersion: 1, ActiveIndexId: active, Columns: columns,
		IndexBuild: &pb.ViewIndexBuild{
			IndexId: warming, TargetViewVersion: 1, State: pb.ViewIndexBuild_BUILDING,
			Engine: "duckdb", SchemaHash: schemaHash, Columns: columns,
		},
	}
	got := writableIndexSet(item)
	assert.True(t, got[active])
	assert.True(t, got[warming])
}
