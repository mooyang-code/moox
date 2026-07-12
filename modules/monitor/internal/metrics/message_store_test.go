package metrics

import (
	"context"
	"testing"

	messagepb "github.com/mooyang-code/moox/packages/messagepb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetricMessageStoreNilGuards(t *testing.T) {
	var store *MetricMessageStore
	_, err := store.IsDuplicate(context.Background(), "msg")
	require.Error(t, err)
	_, err = store.CommitIngest(context.Background(), &messagepb.MooxMessage{MessageId: "m"}, nil)
	require.Error(t, err)

	empty := NewMetricMessageStore(nil)
	require.NotNil(t, empty)
	_, err = empty.IsDuplicate(context.Background(), "")
	require.Error(t, err)
	_, err = empty.IsDuplicate(context.Background(), "id")
	require.Error(t, err)
	_, err = empty.CommitIngest(context.Background(), nil, nil)
	require.Error(t, err)
	_, err = empty.CommitIngest(context.Background(), &messagepb.MooxMessage{}, nil)
	require.Error(t, err)
	assert.Equal(t, empty.DedupeRetention.Hours(), float64(7*24))
}
