package resample

import (
	"context"
	"testing"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

type fakeViewGetter struct {
	views []*storagepb.View
	index int
}

func (f *fakeViewGetter) GetView(_ context.Context, _ *storagepb.GetViewReq, _ ...client.Option) (*storagepb.GetViewRsp, error) {
	if len(f.views) == 0 {
		return &storagepb.GetViewRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
	}
	view := f.views[f.index]
	if f.index < len(f.views)-1 {
		f.index++
	}
	return &storagepb.GetViewRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, View: view}, nil
}

func TestTargetViewRevisionReadyRequiresActiveIndexAndRevision(t *testing.T) {
	assert.False(t, targetViewRevisionReady(&storagepb.View{Status: "active", ActiveViewRevision: 2, ActiveIndexId: "idx"}, 3))
	assert.False(t, targetViewRevisionReady(&storagepb.View{Status: "active", ActiveViewRevision: 3}, 3))
	assert.False(t, targetViewRevisionReady(&storagepb.View{Status: "building", ActiveViewRevision: 3, ActiveIndexId: "idx"}, 3))
	assert.False(t, targetViewRevisionReady(&storagepb.View{Status: "active", DesiredViewRevision: 4, ActiveViewRevision: 3, ActiveIndexId: "idx"}, 3))
	assert.True(t, targetViewRevisionReady(&storagepb.View{Status: "active", ActiveViewRevision: 3, ActiveIndexId: "idx"}, 3))
}

func TestWaitTargetViewRevisionPollsUntilDesiredIndexIsActive(t *testing.T) {
	getter := &fakeViewGetter{views: []*storagepb.View{
		{Status: "active", DesiredViewRevision: 3, ActiveViewRevision: 2, ActiveIndexId: "idx-a"},
		{Status: "active", DesiredViewRevision: 3, ActiveViewRevision: 3, ActiveIndexId: "idx-b"},
	}}
	require.NoError(t, waitTargetViewRevision(context.Background(), getter, nil, "crypto", "view", 3, 500*time.Millisecond))
	assert.Equal(t, 1, getter.index)
}

func TestWaitTargetViewRevisionReturnsNotReadyWhenTimeoutExpires(t *testing.T) {
	getter := &fakeViewGetter{views: []*storagepb.View{{Status: "active", DesiredViewRevision: 2, ActiveViewRevision: 1, ActiveIndexId: "idx"}}}
	err := waitTargetViewRevision(context.Background(), getter, nil, "crypto", "view", 2, 20*time.Millisecond)
	assert.ErrorIs(t, err, ErrTargetViewNotReady)
}
