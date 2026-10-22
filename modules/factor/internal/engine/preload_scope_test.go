package engine

import (
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

// 集合的标的列表变化后，预载要重新做（窗口释放被移出的标的）；只是顺序不同不算变化。
func TestPreloaderRepreparesWhenSubjectListChanges(t *testing.T) {
	p := newPreloader(nil, nil, nil, nil, nil)
	set := domain.EngineSet{Set: domain.FactorSet{SetID: "s1", SubjectMode: domain.SubjectModeInclude, Subjects: []string{"A", "B"}}}
	require.False(t, p.upToDate(set))
	p.markPrepared(set)
	require.True(t, p.upToDate(set))

	reordered := set
	reordered.Set.Subjects = []string{"B", "A"}
	require.True(t, p.upToDate(reordered))

	removed := set
	removed.Set.Subjects = []string{"A"}
	require.False(t, p.upToDate(removed), "标的被移出后要重新预载")

	all := domain.EngineSet{Set: domain.FactorSet{SetID: "s2", SubjectMode: domain.SubjectModeAll}}
	p.markPrepared(all)
	require.True(t, p.upToDate(all))
}
