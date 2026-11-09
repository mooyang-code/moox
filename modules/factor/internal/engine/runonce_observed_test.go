package engine

import (
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

// run-once 遇到 observed 成员直接拒绝：它直接读 Storage，历史截面口径与实时不同，又会写入结果数据集。
func TestRunOnceRejectsObservedMembers(t *testing.T) {
	observed := domain.MustDefinition("obs", "obs", "cross_section_history: observed\noutput: cs_rank(close)\n")
	current := domain.MustDefinition("cur", "cur", "output: cs_rank(close)\n")
	require.NoError(t, rejectObservedRunOnce(domain.AsMembers(current)))
	err := rejectObservedRunOnce(domain.AsMembers(current, observed))
	require.ErrorContains(t, err, "成员 obs")
	require.ErrorContains(t, err, "写入结果数据集")
}
