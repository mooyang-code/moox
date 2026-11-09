package recalc

import (
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

// 指定部分标的补算时，含 observed 成员就拒绝；只有 current 成员时照常。
func TestRecalcSubsetRejectsObservedMembers(t *testing.T) {
	observed := domain.MustDefinition("obs", "obs", "cross_section_history: observed\noutput: cs_rank(close)\n")
	current := domain.MustDefinition("cur", "cur", "output: cs_rank(close)\n")
	require.NoError(t, rejectObservedSubset(domain.AsMembers(current)))
	err := rejectObservedSubset(domain.AsMembers(current, observed))
	require.ErrorContains(t, err, "成员 obs")
	require.ErrorContains(t, err, "补算全部标的")
}
