package rpc

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestToPBTaskAndFromPBTask_ShouldRoundTripCoreFields(t *testing.T) {
	enabled := true
	params, err := structpb.NewStruct(map[string]any{"source": map[string]any{"kind": "none"}})
	require.NoError(t, err)
	in := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "rule-1", DataType: "kline", CollectParams: `{"source":{"kind":"none"}}`, Enabled: true,
		Creator: "tester", CreateTime: time.Unix(1, 0).UTC(), ModifyTime: time.Unix(2, 0).UTC(),
	}
	pbRule := toPBTask(in)
	assert.Equal(t, "crypto", pbRule.GetSpaceId())
	assert.Equal(t, "rule-1", pbRule.GetTaskId())
	assert.Equal(t, enabled, pbRule.GetEnabled())
	assert.Equal(t, "采集结果", pbRule.GetResult().GetResultName())

	out := fromPBTask(&pb.CollectionTask{
		SpaceId: "crypto", TaskId: "rule-2", DataType: "kline", CollectParams: params, Enabled: &enabled,
	})
	assert.Equal(t, "crypto", out.SpaceID)
	assert.Equal(t, "rule-2", out.TaskID)
}

func TestToPBTaskRedactsInternalDatasetIDsAndNamesResult(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-1", TaskName: "币安现货", DataType: "kline",
		CollectParams: `{"subject_tags":["binance_spot"],"source_dataset_id":"source","target_dataset_id":"target","frequency":"1h"}`,
		ResultViewID:  "view-1",
	}
	pbTask := toPBTask(task)
	assert.Equal(t, "币安现货 结果", pbTask.GetResult().GetResultName())
	params := pbTask.GetCollectParams().AsMap()
	assert.NotContains(t, params, "target_dataset_id")
	assert.NotContains(t, params, "subject_tags")
	assert.NotContains(t, params, "source_dataset_id")
	assert.Equal(t, "1h", params["frequency"])
}

func TestCollectionTaskProtoJSONRejectsLegacyRuleFields(t *testing.T) {
	for _, raw := range []string{
		`{"spaceId":"crypto","exchange":"binance"}`,
		`{"spaceId":"crypto","market":"spot"}`,
	} {
		var rule pb.CollectionTask
		err := protojson.Unmarshal([]byte(raw), &rule)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown field")
	}
}

func TestPageHelpers_ShouldNormalizeBounds(t *testing.T) {
	page, size := normalizePageParams(0, 0)
	assert.Equal(t, 1, page)
	assert.Equal(t, 50, size)
	page, size = normalizePageParams(2, 5000)
	assert.Equal(t, 2, page)
	assert.Equal(t, 1000, size)
	assert.Equal(t, uint32(0), uint32Total(0))
	assert.Equal(t, uint32(10), uint32Total(10))

	result := pageResult(1, 50, 120)
	assert.True(t, result.GetHasMore())
	assert.Equal(t, uint32(120), result.GetTotal())
}

func TestStatusAndJSONHelpers(t *testing.T) {
	assert.Equal(t, "", formatTime(time.Time{}))
	assert.Equal(t, "{}", jsonStringFromStruct(nil))
	st := structFromJSONString(`{"k":"v"}`)
	assert.Equal(t, "v", st.GetFields()["k"].GetStringValue())
	raw := structFromJSONString("not-json")
	assert.Equal(t, "not-json", raw.GetFields()["raw"].GetStringValue())
}
