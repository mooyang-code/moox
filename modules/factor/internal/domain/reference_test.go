package domain

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateReferenceSubjects(t *testing.T) {
	member := MemberDef{Key: "rel", Factor: MustDefinition("rel", "rel", "output: close - cs_ref(close, \"BTC-USDT\") + cs_ref(close, \"ETH-USDT\")\n")}
	scope := func(subjects ...string) func(string) bool {
		known := map[string]bool{}
		for _, subject := range subjects {
			known[subject] = true
		}
		return func(subject string) bool { return known[subject] }
	}
	require.NoError(t, ValidateReferenceSubjects(member, scope("BTC-USDT", "ETH-USDT", "SOL-USDT")))
	require.ErrorContains(t, ValidateReferenceSubjects(member, scope("BTC-USDT")), "参考标的 ETH-USDT 不在任务的标的范围内")
	require.NoError(t, ValidateReferenceSubjects(member, nil), "没有标的来源时不校验")
	plain := MemberDef{Key: "m", Factor: MustDefinition("m", "m", "output: ts_mean(close, 3)\n")}
	require.NoError(t, ValidateReferenceSubjects(plain, scope()), "没有参考标的时不校验")
}
