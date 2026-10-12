package command

import (
	"context"
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

func TestDefaultSetupEnsureSCFPlanIsANoopWhenDisabled(t *testing.T) {
	summary, err := defaultSetupEnsureSCFPlan(context.Background(), &setupconfig.Snapshot{})
	require.NoError(t, err)
	require.Nil(t, summary)
}
