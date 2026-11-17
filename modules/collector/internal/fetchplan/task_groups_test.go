package fetchplan

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

// flakyMetadata 在 failing 指定的那一步读元数据失败，其余照 planSymbolsStub。
type flakyMetadata struct {
	*planSymbolsStub
	failing string
}

func (m *flakyMetadata) GetDataset(ctx context.Context, space, dataset string) (storagesource.DatasetInfo, error) {
	if m.failing == "dataset" {
		return storagesource.DatasetInfo{}, errors.New("metadata unavailable")
	}
	return m.planSymbolsStub.GetDataset(ctx, space, dataset)
}

func (m *flakyMetadata) GetTag(ctx context.Context, space, tagID string) (*storagepb.Tag, error) {
	if m.failing == "tag" {
		return nil, errors.New("metadata unavailable")
	}
	return m.planSymbolsStub.GetTag(ctx, space, tagID)
}

// 元数据暂时读不到时沿用上一次的采集组，但采集方式取任务的现值：切换不会因为元数据读不到而推迟。
func TestCachedGroupsTakeTheCurrentIngest(t *testing.T) {
	ctx := context.Background()
	tasks := &planTasksStub{tasks: []domain.CollectionTask{{
		SpaceID: "crypto", TaskID: "spot-1m", Enabled: true, DataType: "kline", Ingest: ingestSCF, TagIDs: []string{"binance_spot"},
		CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"ds_spot","frequency":"1m"}`,
	}}}
	metadata := &flakyMetadata{planSymbolsStub: &planSymbolsStub{subjects: []string{"BTC-USDT", "ETH-USDT"}}}
	builder := &GroupBuilder{Tasks: tasks, Symbols: metadata}
	fresh, err := builder.groups(ctx, "crypto")
	require.NoError(t, err)
	require.Len(t, fresh, 1)
	require.Equal(t, ingestSCF, fresh[0].Ingest)

	tasks.tasks[0].Ingest = ingestStream
	for _, failing := range []string{"dataset", "tag"} {
		metadata.failing = failing
		cached, err := builder.groups(ctx, "crypto")
		require.NoError(t, err)
		require.Len(t, cached, 1, failing)
		require.Equal(t, ingestStream, cached[0].Ingest, "读不到 %s 时采集方式也取现值", failing)
		require.Equal(t, fresh[0].Subjects, cached[0].Subjects, "标的沿用缓存")
	}
	require.Equal(t, ingestSCF, builder.lastGroups["spot-1m"][0].Ingest, "缓存本身不被改写")
}
