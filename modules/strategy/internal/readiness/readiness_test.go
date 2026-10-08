package readiness

import (
	"reflect"
	"testing"

	"github.com/mooyang-code/moox/packages/storagepb"
)

func factorEvent(states ...*storagepb.FactorPeriodState) *storagepb.ViewDataReady {
	status := "complete"
	for _, state := range states {
		if state.GetStatus() != "complete" {
			status = "degraded"
		}
	}
	return &storagepb.ViewDataReady{ViewId: "view_1", Status: status, Factors: states}
}

func state(id, status, hash string, failed ...string) *storagepb.FactorPeriodState {
	return &storagepb.FactorPeriodState{FactorId: id, Status: status, DefinitionHash: hash, FailedSubjects: failed}
}

// 设计文档 3.3 表格逐行覆盖。
func TestCheckFollowsReadinessTable(t *testing.T) {
	binding := Binding{Factors: map[string]string{"ma": "h1", "mom": "h2"}}
	previousBinding := Binding{Factors: map[string]string{"ma": "h1", "mom": "h2"}, UsesPreviousBar: true}
	adjacent := &Record{Factors: map[string]string{"ma": "h1", "mom": "h2"}}
	cases := []struct {
		name     string
		binding  Binding
		adjacent *Record
		event    *storagepb.ViewDataReady
		reason   string
		failed   map[string][]string
		subjects []string
	}{
		{name: "因子不在 factors 中", binding: binding, event: factorEvent(state("ma", "complete", "h1")), reason: ReasonFactorMissing},
		{name: "definition_hash 为空", binding: binding, event: factorEvent(state("ma", "complete", "h1"), state("mom", "complete", "")), reason: ReasonFactorMissing},
		{name: "指纹与固化值不同", binding: binding, event: factorEvent(state("ma", "complete", "h1"), state("mom", "complete", "h9")), reason: ReasonFactorChanged},
		{name: "bars[-1] 无相邻记录", binding: previousBinding, event: factorEvent(state("ma", "complete", "h1"), state("mom", "complete", "h2")), reason: ReasonPreviousVersionUnknown},
		{name: "bars[-1] 相邻记录指纹不同", binding: previousBinding, adjacent: &Record{Factors: map[string]string{"ma": "h0", "mom": "h2"}}, event: factorEvent(state("ma", "complete", "h1"), state("mom", "complete", "h2")), reason: ReasonFactorChanged},
		{name: "bars[-1] 相邻记录缺因子", binding: previousBinding, adjacent: &Record{Factors: map[string]string{"ma": "h1"}}, event: factorEvent(state("ma", "complete", "h1"), state("mom", "complete", "h2")), reason: ReasonPreviousVersionUnknown},
		{name: "bars[-1] 相邻记录一致", binding: previousBinding, adjacent: adjacent, event: factorEvent(state("ma", "complete", "h1"), state("mom", "complete", "h2"))},
		{name: "因子 skipped", binding: binding, event: factorEvent(state("ma", "skipped", "h1"), state("mom", "complete", "h2")), reason: ReasonFactorSkipped},
		{name: "因子 degraded 只记失败标的", binding: binding, event: factorEvent(state("ma", "degraded", "h1", "ETH-USDT", "SOL-USDT", "ETH-USDT"), state("mom", "complete", "h2")), failed: map[string][]string{"ma": {"ETH-USDT", "SOL-USDT"}}},
		{name: "全部 complete", binding: binding, event: factorEvent(state("ma", "complete", "h1"), state("mom", "complete", "h2"))},
		{name: "未引用的因子 skipped 不影响", binding: Binding{Factors: map[string]string{"ma": "h1"}}, event: factorEvent(state("ma", "complete", "h1"), state("mom", "skipped", "h2"))},
		{name: "K 线 View degraded 解析失败标的", binding: Binding{}, event: &storagepb.ViewDataReady{Status: "degraded", FailedScopeRef: "SOL-USDT,ETH-USDT"}, subjects: []string{"ETH-USDT", "SOL-USDT"}},
		{name: "K 线 View degraded 无具体标的", binding: Binding{}, event: &storagepb.ViewDataReady{Status: "degraded", FailedScopeRef: "degraded"}},
		{name: "K 线 View complete", binding: Binding{UsesPreviousBar: true}, event: &storagepb.ViewDataReady{Status: "complete"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := Check(tc.binding, tc.adjacent, tc.event)
			if result.Reason != tc.reason {
				t.Fatalf("原因不符：got %q(%s) want %q", result.Reason, result.Detail, tc.reason)
			}
			if tc.reason != "" {
				return
			}
			if tc.failed == nil {
				tc.failed = map[string][]string{}
			}
			if !reflect.DeepEqual(result.FailedByFactor, tc.failed) {
				t.Fatalf("失败标的不符：got %v want %v", result.FailedByFactor, tc.failed)
			}
			if !reflect.DeepEqual(result.FailedSubjects, tc.subjects) {
				t.Fatalf("View 级失败标的不符：got %v want %v", result.FailedSubjects, tc.subjects)
			}
		})
	}
}

// S13：事件指纹与固化值不同，或 bars[-1] 的相邻记录指纹与本期不同，都记 factor_changed。
func TestS13FactorChangedFromEventOrAdjacentRecord(t *testing.T) {
	binding := Binding{Factors: map[string]string{"ma": "h1"}, UsesPreviousBar: true}
	event := factorEvent(state("ma", "complete", "h2"))
	if result := Check(binding, &Record{Factors: map[string]string{"ma": "h1"}}, event); result.Reason != ReasonFactorChanged {
		t.Fatalf("事件指纹变化应为 factor_changed：%+v", result)
	}
	same := factorEvent(state("ma", "complete", "h1"))
	if result := Check(binding, &Record{Factors: map[string]string{"ma": "h0"}}, same); result.Reason != ReasonFactorChanged {
		t.Fatalf("相邻记录指纹不同应为 factor_changed：%+v", result)
	}
}

// S18：因子 H1 → H2 后重新启用（固化 H2），DSL 用了 bars[-1]：
// 旧会话留下 H1 记录时首期 factor_changed，没有记录时 previous_version_unknown；两者都记录本期指纹供次期使用。
func TestS18ReenableAfterFactorChange(t *testing.T) {
	binding := Binding{Factors: map[string]string{"ma": "h2"}, UsesPreviousBar: true}
	event := factorEvent(state("ma", "complete", "h2"))
	withOld := Check(binding, &Record{Factors: map[string]string{"ma": "h1"}}, event)
	if withOld.Reason != ReasonFactorChanged || withOld.Hashes["ma"] != "h2" {
		t.Fatalf("旧会话记录 H1 时应为 factor_changed 且记录本期指纹：%+v", withOld)
	}
	withoutRecord := Check(binding, nil, event)
	if withoutRecord.Reason != ReasonPreviousVersionUnknown || withoutRecord.Hashes["ma"] != "h2" {
		t.Fatalf("无记录时应为 previous_version_unknown 且记录本期指纹：%+v", withoutRecord)
	}
	// 次期：相邻记录（上一期的 skipped 记录）指纹为 H2，正常求值。
	next := Check(binding, &Record{Factors: withoutRecord.Hashes}, event)
	if next.Reason != "" {
		t.Fatalf("次期应正常求值：%+v", next)
	}
}

// S19：停机漏掉一根 bar 后恢复，缺少相邻记录的那一期记 previous_version_unknown；不用 bars[-1] 的实例不受影响。
func TestS19MissingAdjacentRecordOnlyMattersWithPreviousBar(t *testing.T) {
	event := factorEvent(state("ma", "complete", "h1"))
	if result := Check(Binding{Factors: map[string]string{"ma": "h1"}, UsesPreviousBar: true}, nil, event); result.Reason != ReasonPreviousVersionUnknown {
		t.Fatalf("应为 previous_version_unknown：%+v", result)
	}
	if result := Check(Binding{Factors: map[string]string{"ma": "h1"}}, nil, event); result.Reason != "" {
		t.Fatalf("不用 bars[-1] 时不应要求相邻记录：%+v", result)
	}
}

func TestCheckIgnoresNilAndBlankFactorStates(t *testing.T) {
	event := &storagepb.ViewDataReady{Status: "complete", Factors: []*storagepb.FactorPeriodState{nil, {FactorId: " ", Status: "complete"}, state("ma", "complete", "h1")}}
	if result := Check(Binding{Factors: map[string]string{"ma": "h1"}}, nil, event); result.Reason != "" {
		t.Fatalf("空状态应被忽略：%+v", result)
	}
}
