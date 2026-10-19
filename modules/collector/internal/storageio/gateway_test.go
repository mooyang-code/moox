package storageio

import (
	"context"
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/wirecompress"
	"github.com/stretchr/testify/require"
)

type spaceRecorder struct{ space string }

func (r *spaceRecorder) Invoke(ctx context.Context, _, _ string, _, _ any) error {
	r.space = wirecompress.SpaceOf(ctx, nil)
	return nil
}

// 整分钟写入突发的请求顶层没有 space_id（空间在 Expectation 里），必须由调用封装显式带上，压缩才能按空间选字典。
func TestCommitTimeSeriesBatchCarriesTheExpectationSpace(t *testing.T) {
	recorder := &spaceRecorder{}
	client := NewGatewayClient(recorder)
	_, err := client.CommitTimeSeriesBatch(context.Background(), &storagepb.PrimaryCommitTimeSeriesBatchReq{
		Expectation: &storagepb.DatasetPeriodExpectation{SpaceId: "ashare"},
	})
	require.NoError(t, err)
	require.Equal(t, "ashare", recorder.space)

	// 没有期望信息时不设置空间，走默认字典。
	_, err = client.CommitTimeSeriesBatch(context.Background(), &storagepb.PrimaryCommitTimeSeriesBatchReq{})
	require.NoError(t, err)
	require.Equal(t, "", recorder.space)
}
