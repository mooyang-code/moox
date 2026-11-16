package unitbootstrap

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/failure"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitinstall"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
	"github.com/stretchr/testify/require"
)

// 已经装好完整控制单元的控制机上，再跑只含 admin 的 bootstrap 不能把其余组件缩掉；
// 用伪造的“已安装的上一版”核对规则，不需要真实软件包（Linux 门禁里只装 admin，那里构造不出这个场景）。
func TestBootstrapRefusesToNarrowAnInstalledControlUnit(t *testing.T) {
	original := readInstalled
	t.Cleanup(func() { readInstalled = original })
	var asked string
	readInstalled = func(_ context.Context, directory string) (unitinstall.Prepared, error) {
		asked = directory
		return unitinstall.Prepared{Components: []string{"admin", "monitor", "collector"}}, nil
	}
	control := hostbundle.Host{HostID: "control", Components: []string{"admin", "monitor", "collector", "eventbus"}}

	err := refuseNarrowing(context.Background(), "/deploy/prod/releases/previous", control, []string{"admin"})
	var coded *failure.Coded
	require.True(t, errors.As(err, &coded), "%v", err)
	require.Equal(t, failure.TargetSubsetInstalled, coded.Code)
	require.ErrorContains(t, err, "cannot narrow")
	require.Equal(t, "/deploy/prod/releases/previous", asked)

	require.NoError(t, refuseNarrowing(context.Background(), "/p", control, []string{"admin", "monitor", "collector"}), "同样的组件可以重跑")
	require.NoError(t, refuseNarrowing(context.Background(), "/p", control, []string{"admin", "monitor", "collector", "strategy"}), "可以扩大")
	moved := hostbundle.Host{HostID: "control", Components: []string{"admin", "collector"}}
	require.NoError(t, refuseNarrowing(context.Background(), "/p", moved, []string{"admin", "collector"}), "拓扑里已经不在控制机上的组件不算缩小")

	readInstalled = func(context.Context, string) (unitinstall.Prepared, error) {
		return unitinstall.Prepared{}, errors.New("receipt broken")
	}
	require.ErrorContains(t, refuseNarrowing(context.Background(), "/p", control, []string{"admin"}), "receipt broken")
}
