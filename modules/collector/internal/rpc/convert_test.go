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

func TestToPBRuleAndFromPBRule_ShouldRoundTripCoreFields(t *testing.T) {
	enabled := true
	params, err := structpb.NewStruct(map[string]any{"source": map[string]any{"kind": "none"}})
	require.NoError(t, err)
	in := domain.TaskRule{
		SpaceID: "crypto", RuleID: "rule-1", DataType: "symbol", Exchange: "binance",
		CollectParams: `{"source":{"kind":"none"}}`, Enabled: true,
		Creator: "tester", CreateTime: time.Unix(1, 0).UTC(), ModifyTime: time.Unix(2, 0).UTC(),
	}
	pbRule := toPBRule(in)
	assert.Equal(t, "crypto", pbRule.GetSpaceId())
	assert.Equal(t, "rule-1", pbRule.GetRuleId())
	assert.Equal(t, enabled, pbRule.GetEnabled())

	out := fromPBRule(&pb.TaskRule{
		SpaceId: "crypto", RuleId: "rule-2", DataType: "kline", Exchange: "binance",
		CollectParams: params, Enabled: &enabled,
	})
	assert.Equal(t, "crypto", out.SpaceID)
	assert.Equal(t, "rule-2", out.RuleID)
	assert.Equal(t, "binance", out.Exchange)
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

func TestToPBInstance_ShouldMapStatus(t *testing.T) {
	now := time.Now().UTC()
	instance := toPBInstance(domain.TaskInstance{
		SpaceID: "crypto", TaskID: "task-1", RuleID: "rule-1", Exchange: "binance",
		Market: "spot", DataType: "symbol", LastExecStatus: domain.InstanceStatusSuccess,
		CreateTime: now, ModifyTime: now,
	})
	assert.Equal(t, "task-1", instance.GetTaskId())
	assert.Equal(t, pb.TaskInstanceStatus_TASK_INSTANCE_STATUS_SUCCESS, instance.GetLastExecStatus())
	encoded, err := protojson.Marshal(instance)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "plannedExecNode")
}

func TestStatusAndJSONHelpers(t *testing.T) {
	assert.Equal(t, pb.TaskInstanceStatus_TASK_INSTANCE_STATUS_PENDING, toPBStatus(domain.InstanceStatusPending))
	assert.Equal(t, pb.TaskInstanceStatus_TASK_INSTANCE_STATUS_SUCCESS, toPBStatus(domain.InstanceStatusSuccess))
	assert.Equal(t, pb.TaskInstanceStatus_TASK_INSTANCE_STATUS_FAILED, toPBStatus(domain.InstanceStatusFailed))
	assert.Equal(t, domain.InstanceStatusSuccess, fromPBStatus(pb.TaskInstanceStatus_TASK_INSTANCE_STATUS_SUCCESS))
	assert.Equal(t, 0, fromPBStatus(pb.TaskInstanceStatus_TASK_INSTANCE_STATUS_UNSPECIFIED))

	assert.Equal(t, "", formatTime(time.Time{}))
	assert.Equal(t, "", formatPtrTime(nil))
	assert.Equal(t, "[]", jsonStringFromStrings(nil))
	assert.Equal(t, `["a"]`, jsonStringFromStrings([]string{"a"}))
	assert.Nil(t, stringsFromJSONString(""))
	assert.Equal(t, []string{"a"}, stringsFromJSONString(`["a"]`))
	assert.Equal(t, "{}", jsonStringFromStruct(nil))
	st := structFromJSONString(`{"k":"v"}`)
	assert.Equal(t, "v", st.GetFields()["k"].GetStringValue())
	raw := structFromJSONString("not-json")
	assert.Equal(t, "not-json", raw.GetFields()["raw"].GetStringValue())
}
