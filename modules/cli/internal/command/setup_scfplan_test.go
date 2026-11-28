package command

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"

	"github.com/stretchr/testify/require"
)

func TestDefaultSetupEnsureSCFPlanIsANoopWhenDisabled(t *testing.T) {
	summary, err := defaultSetupEnsureSCFPlan(context.Background(), &setupSnapshot{Manifest: &topology.Config{}}, "")
	require.NoError(t, err)
	require.Nil(t, summary)
}

type stubSCFPlanPackages struct {
	latestCalls []string
	uploadCalls []string
}

func (s *stubSCFPlanPackages) Latest(_ context.Context, name string) (string, bool, error) {
	s.latestCalls = append(s.latestCalls, name)
	return "", false, nil
}

func (s *stubSCFPlanPackages) Upload(_ context.Context, name string) (string, error) {
	s.uploadCalls = append(s.uploadCalls, name)
	return "pkg-" + name, nil
}

// 所有空间共享同一个全局代码包名时，初始化只查询和上传一次。
func TestEnsureSCFPlanPackagesUploadsSharedPackageOnce(t *testing.T) {
	manifest := &topology.Config{
		SCFPlan: topology.SCFPlan{
			Enabled: true,
			Spaces: []topology.SCFPlanSpace{
				{SpaceID: "crypto"},
				{SpaceID: "stockcn"},
				{SpaceID: "stockhk"},
			},
		},
	}
	manifest.SCFPlan.Normalize()
	stub := &stubSCFPlanPackages{}
	summary := ensureSCFPlanPackages(context.Background(), manifest, stub)
	require.Equal(t, "ready", summary.Status)
	require.Equal(t, []string{topology.DefaultSCFPackageName}, stub.latestCalls)
	require.Equal(t, []string{topology.DefaultSCFPackageName}, stub.uploadCalls)
	require.Len(t, summary.Spaces, 3)
	for _, space := range summary.Spaces {
		require.Equal(t, topology.DefaultSCFPackageName, space.PackageName)
		require.Equal(t, "pkg-"+topology.DefaultSCFPackageName, space.PackageID)
	}
}
