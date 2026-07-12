package jobqueue

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestStructToMap_NilReturnsEmptyMap(t *testing.T) {
	assert.Empty(t, structToMap(nil))
}

func TestStructToMap_ConvertsValues(t *testing.T) {
	st, err := structpb.NewStruct(map[string]any{"k": "v", "n": float64(1)})
	require.NoError(t, err)
	got := structToMap(st)
	assert.Equal(t, "v", got["k"])
	assert.Equal(t, float64(1), got["n"])
}

func TestMapToStruct_InvalidValuesReturnEmptyStruct(t *testing.T) {
	st := mapToStruct(map[string]any{"fn": func() {}})
	require.NotNil(t, st)
	assert.Empty(t, st.GetFields())
}

func TestJobItemMessage_ToPolledJobItem(t *testing.T) {
	msg := JobItemMessage{
		SpaceID: "crypto", JobID: "job-1", JobItemID: "item-1",
		JobType: "collect.kline", CodePackageID: "pkg-1",
		Params: map[string]any{"symbol": "BTCUSDT"}, Priority: 3,
		SubmittedAt: time.Unix(100, 0).UTC(),
	}
	item := msg.ToPolledJobItem(2)
	assert.Equal(t, "crypto", item.GetSpaceId())
	assert.Equal(t, "job-1", item.GetJobId())
	assert.Equal(t, "item-1", item.GetJobItemId())
	assert.Equal(t, int32(2), item.GetAttemptNo())
	assert.Equal(t, "BTCUSDT", item.GetParams().GetFields()["symbol"].GetStringValue())
}
