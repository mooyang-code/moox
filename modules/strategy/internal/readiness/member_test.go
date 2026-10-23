package readiness

import (
	"reflect"
	"testing"

	"github.com/mooyang-code/moox/packages/storagepb"
)

// 同一定义以两个别名出现：按成员别名分别判定，失败标的各归各的成员。
func TestCheckIndexesByMemberAlias(t *testing.T) {
	fast := &storagepb.FactorPeriodState{MemberAlias: "fast", FactorId: "boll", Status: "degraded", DefinitionHash: "h1", FailedSubjects: []string{"BTC"}, ParamsSource: "requested"}
	slow := &storagepb.FactorPeriodState{MemberAlias: "slow", FactorId: "boll", Status: "complete", DefinitionHash: "h1", ParamsSource: "carried"}
	result := Check(Binding{Factors: map[string]string{"fast": "h1", "slow": "h1"}}, nil, factorEvent(fast, slow))
	if result.Reason != "" {
		t.Fatalf("不应跳过：%s %s", result.Reason, result.Detail)
	}
	if !reflect.DeepEqual(result.FailedByFactor, map[string][]string{"fast": {"BTC"}}) {
		t.Fatalf("失败标的应只归 fast：%v", result.FailedByFactor)
	}
	if !reflect.DeepEqual(result.Hashes, map[string]string{"fast": "h1", "slow": "h1"}) {
		t.Fatalf("指纹按别名保存：%v", result.Hashes)
	}
}
