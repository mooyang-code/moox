package rpc

import (
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
)

// 引擎明细的仓位系数经存储行转到 proto 的 scale 字段；没写系数时为空。
func TestItemProtosCarryScale(t *testing.T) {
	rows := itemProtos(trigger.ResultItems([]engine.Item{
		{RuleID: "r", InstrumentID: "A", Stage: engine.StageWeighted, Weight: "0.25", Scale: "0.5"},
		{RuleID: "r", InstrumentID: "B", Stage: engine.StageWeighted, Weight: "0.5"},
	}))
	if len(rows) != 2 || rows[0].Scale != "0.5" || rows[0].Weight != "0.25" || rows[1].Scale != "" {
		t.Fatalf("proto 明细的系数不符：%+v", rows)
	}
}
