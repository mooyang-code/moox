package deploy

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

// 只部署个别组件时带了 --rotate-host-key：按整台部署处理，本机组件都按新指纹重启并记进 version.json，
// 下一次整台部署不会把没选中的组件再重启一遍。
func TestRotatingHostKeyDeploysTheWholeHost(t *testing.T) {
	all := []string{"host-gateway", "host-agent", "storage-primary", "access"}

	selected, full, err := Options{}.scope("storage", all)
	require.NoError(t, err)
	require.True(t, full)
	require.Equal(t, all, selected)

	selected, full, err = Options{Components: []string{"access"}}.scope("storage", all)
	require.NoError(t, err)
	require.False(t, full)
	require.Equal(t, []string{"access"}, selected)

	var log bytes.Buffer
	selected, full, err = Options{Components: []string{"access"}, RotateHostKey: true, Log: &log}.scope("storage", all)
	require.NoError(t, err)
	require.True(t, full)
	require.Equal(t, all, selected)
	require.Contains(t, log.String(), "按整台部署处理")

	_, _, err = Options{Components: []string{"trade"}, RotateHostKey: true}.scope("storage", all)
	require.ErrorContains(t, err, "component_not_on_host")
}
