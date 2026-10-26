package readiness

import (
	"reflect"
	"testing"

	"github.com/mooyang-code/moox/packages/storagepb"
)

// 同一因子以两个成员（可变与固定）出现：按成员键分别判定，失败标的各归各的成员。
func TestCheckIndexesByMemberKey(t *testing.T) {
	fast := &storagepb.FactorPeriodState{MemberKey: "boll__var", MemberKind: "var", FactorId: "boll", Status: "degraded", DefinitionHash: "h1", FailedSubjects: []string{"BTC"}}
	slow := &storagepb.FactorPeriodState{MemberKey: "boll__30", MemberKind: "fixed", FactorId: "boll", Status: "complete", DefinitionHash: "h1"}
	result := Check(Binding{Factors: map[string]string{"boll__var": Contract("h1", "var"), "boll__30": Contract("h1", "fixed")}}, nil, factorEvent(fast, slow))
	if result.Reason != "" {
		t.Fatalf("不应跳过：%s %s", result.Reason, result.Detail)
	}
	if !reflect.DeepEqual(result.FailedByFactor, map[string][]string{"boll__var": {"BTC"}}) {
		t.Fatalf("失败标的应只归 fast：%v", result.FailedByFactor)
	}
	if !reflect.DeepEqual(result.Hashes, map[string]string{"boll__var": "h1|var", "boll__30": "h1|fixed"}) {
		t.Fatalf("契约按成员键保存：%v", result.Hashes)
	}
}

// 成员类型变了（固定 → 可变，成员键也会变；同键换类型的数据）按契约不一致处理。
func TestCheckDetectsMemberKindChange(t *testing.T) {
	state := &storagepb.FactorPeriodState{MemberKey: "boll", MemberKind: "var", FactorId: "boll", Status: "complete", DefinitionHash: "h1"}
	result := Check(Binding{Factors: map[string]string{"boll": Contract("h1", "fixed")}}, nil, factorEvent(state))
	if result.Reason != ReasonFactorChanged {
		t.Fatalf("成员类型变化应为 factor_changed：%+v", result)
	}
	missingKind := &storagepb.FactorPeriodState{MemberKey: "boll", FactorId: "boll", Status: "complete", DefinitionHash: "h1"}
	if result := Check(Binding{Factors: map[string]string{"boll": Contract("h1", "fixed")}}, nil, factorEvent(missingKind)); result.Reason != ReasonFactorMissing {
		t.Fatalf("事件没有成员类型应按缺失处理：%+v", result)
	}
}
