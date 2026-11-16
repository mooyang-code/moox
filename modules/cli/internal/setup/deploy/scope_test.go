package deploy

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

// 只部署个别组件时带了 --rotate-host-key：按整台部署处理，本机组件都按新指纹重启并记进 version.json，
// 下一次整台部署不会把没选中的组件再重启一遍。
func TestRotatingHostKeyDeploysTheWholeHost(t *testing.T) {
	all := []string{"host-gateway", "host-agent", "storage-primary", "storage-view"}

	selected, full, err := Options{}.scope("storage", all)
	require.NoError(t, err)
	require.True(t, full)
	require.Equal(t, all, selected)

	selected, full, err = Options{Components: []string{"storage-view"}}.scope("storage", all)
	require.NoError(t, err)
	require.False(t, full)
	require.Equal(t, []string{"storage-view"}, selected)

	var log bytes.Buffer
	selected, full, err = Options{Components: []string{"storage-view"}, RotateHostKey: true, Log: &log}.scope("storage", all)
	require.NoError(t, err)
	require.True(t, full)
	require.Equal(t, all, selected)
	require.Contains(t, log.String(), "按整台部署处理")

	_, _, err = Options{Components: []string{"trade"}, RotateHostKey: true}.scope("storage", all)
	require.ErrorContains(t, err, "component_not_on_host")
}

// 全新主机带 --component 部署时必然签发新私钥，但不升级为整台部署：首次安装第一步只给控制机装 admin、host-gateway、host-agent，
// 升级成整台会把 Collector 等提前装上，它们连不上还没装的 Storage。已经装过的主机换了私钥才要整台重装。
func TestFreshHostWithSelectedComponentsIsNotPromotedToWholeHost(t *testing.T) {
	require.False(t, redeployWholeHost(true, false, false), "全新主机：按选中的组件装")
	require.True(t, redeployWholeHost(true, false, true), "已装过的主机换了私钥：整台部署")
	require.False(t, redeployWholeHost(true, true, true), "本来就是整台部署")
	require.False(t, redeployWholeHost(false, false, true), "私钥没换")
}
