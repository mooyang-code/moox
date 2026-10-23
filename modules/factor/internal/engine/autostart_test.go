package engine

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/packages/lifecycle"
)

// 只有策略实例的私有因子集（有拥有者）随创建自动 init、start；共享的计算任务仍由用户按流程执行。
func TestAutoStartSetOnlyForPrivateSets(t *testing.T) {
	cache, _ := newTestCache(t)
	private := engineSet("fset_private", "dataset_a", true, "bias")
	private.Set.OwnerInstanceID = "inst-1"
	shared := engineSet("fset_shared", "dataset_a", true, "bias")
	_, err := cache.Apply(CatalogSnapshot{Hash: "h", Sets: []domain.EngineSet{uncompiled(private), uncompiled(shared)}})
	require.NoError(t, err)
	r := &Runtime{catalog: cache}

	require.True(t, r.autoStartSet(setRef("fset_private")))
	require.False(t, r.autoStartSet(setRef("fset_shared")))
	require.False(t, r.autoStartSet(setRef("fset_missing")), "不在目录里的集合不自动推进")
	require.False(t, r.autoStartSet(lifecycle.Ref{}), "模块自身不是集合")
}
