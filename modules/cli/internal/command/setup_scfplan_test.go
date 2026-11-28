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

// 多个空间共享默认代码包名时，初始化只查询和上传一次。
func TestEnsureSCFPlanPackagesDeduplicatesSharedPackageName(t *testing.T) {
	manifest := &topology.Config{
		SCFPlan: topology.SCFPlan{
			Enabled: true,
			Spaces: []topology.SCFPlanSpace{
				{SpaceID: "crypto"},
				{SpaceID: "stockcn"},
				{SpaceID: "stockhk", PackageName: "moox-collector-hk"},
			},
		},
	}
	manifest.SCFPlan.Normalize()
	stub := &stubSCFPlanPackages{}
	summary := ensureSCFPlanPackages(context.Background(), manifest, stub)
	require.Equal(t, "ready", summary.Status)
	require.Equal(t, []string{topology.DefaultSCFPackageName, "moox-collector-hk"}, stub.latestCalls)
	require.Equal(t, []string{topology.DefaultSCFPackageName, "moox-collector-hk"}, stub.uploadCalls)
	require.Len(t, summary.Spaces, 3)
	require.Equal(t, "pkg-"+topology.DefaultSCFPackageName, summary.Spaces[0].PackageID)
	require.Equal(t, "pkg-"+topology.DefaultSCFPackageName, summary.Spaces[1].PackageID)
	require.Equal(t, "pkg-moox-collector-hk", summary.Spaces[2].PackageID)
}
