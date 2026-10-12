package command

import (
	"context"
	"errors"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/stretchr/testify/require"
)

type fakeSCFPlanPackages struct {
	existing  map[string]string
	queryErr  error
	uploadErr error
	uploads   []string
}

func (f *fakeSCFPlanPackages) Latest(_ context.Context, spaceID, name string) (string, bool, error) {
	if f.queryErr != nil {
		return "", false, f.queryErr
	}
	id, ok := f.existing[spaceID+"/"+name]
	return id, ok, nil
}

func (f *fakeSCFPlanPackages) Upload(_ context.Context, space setupconfig.SCFPlanSpace, account string) (string, error) {
	if f.uploadErr != nil {
		return "", f.uploadErr
	}
	f.uploads = append(f.uploads, space.SpaceID+"@"+account)
	return space.PackageName + "_init_1", nil
}

func scfPlanTestManifest() setupconfig.Manifest {
	return setupconfig.Manifest{
		SCFPlan: setupconfig.SCFPlan{
			Enabled:      true,
			CloudAccount: setupconfig.SCFFetcherCloudAccount{AccountID: "tencent-scf"},
			Spaces: []setupconfig.SCFPlanSpace{
				{SpaceID: "crypto", PackageName: "moox-collector-crypto-plan"},
				{SpaceID: "stockcn", PackageName: "moox-collector-stockcn-plan"},
			},
		},
	}
}

func TestEnsureSCFPlanPackagesUploadsOnlyWhatIsMissing(t *testing.T) {
	packages := &fakeSCFPlanPackages{existing: map[string]string{"crypto/moox-collector-crypto-plan": "pkg-crypto-v3"}}
	summary := ensureSCFPlanPackages(context.Background(), scfPlanTestManifest(), packages)
	require.Equal(t, "ready", summary.Status)
	require.Len(t, summary.Spaces, 2)
	require.Equal(t, setupSCFPlanSpaceResult{SpaceID: "crypto", PackageName: "moox-collector-crypto-plan", PackageID: "pkg-crypto-v3", Action: "exists"}, summary.Spaces[0])
	require.Equal(t, "uploaded", summary.Spaces[1].Action)
	require.Equal(t, []string{"stockcn@tencent-scf"}, packages.uploads, "已有可用版本的空间不重复上传")
}

func TestEnsureSCFPlanPackagesReportsFailuresWithoutBlockingInit(t *testing.T) {
	summary := ensureSCFPlanPackages(context.Background(), scfPlanTestManifest(), &fakeSCFPlanPackages{queryErr: errors.New("cloudnode unavailable")})
	require.Equal(t, "failed", summary.Status)
	require.Equal(t, "failed", summary.Spaces[0].Action)
	require.Contains(t, summary.Spaces[0].Warning, "cloudnode unavailable")

	summary = ensureSCFPlanPackages(context.Background(), scfPlanTestManifest(), &fakeSCFPlanPackages{uploadErr: errors.New("no source tree")})
	require.Equal(t, "failed", summary.Status)
	require.Contains(t, summary.Spaces[1].Warning, "no source tree")
}

func TestDefaultSetupEnsureSCFPlanIsANoopWhenDisabled(t *testing.T) {
	summary, err := defaultSetupEnsureSCFPlan(context.Background(), &setupconfig.Snapshot{})
	require.NoError(t, err)
	require.Nil(t, summary)
}
