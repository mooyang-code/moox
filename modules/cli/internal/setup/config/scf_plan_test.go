package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 采集方式不在 moox.toml 里：旧的 default_ingest、default_executor 报中文错误并提示迁移，渲染出的配置里也没有这两项。
func TestSCFPlanRejectsRemovedIngestDefaults(t *testing.T) {
	for _, key := range []string{"default_ingest = \"stream\"", "default_executor = \"local\""} {
		var manifest Manifest
		err := decodeStrict([]byte("[[scf_plan.spaces]]\nspace_id = \"crypto\"\n"+key+"\n"), &manifest)
		require.ErrorContains(t, err, "已删除")
		require.ErrorContains(t, err, "删掉它")
	}
}
