package command

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/verify"
)

type fakeVerifySource struct {
	reads int
	ready int
}

func (s *fakeVerifySource) read(_ context.Context, baseline map[string]int64) (verify.Input, error) {
	s.reads++
	in := verify.Input{Now: time.Now(), HasPlan: s.reads >= s.ready}
	in.Datasets = []verify.Dataset{{ID: "d", Registered: s.reads >= s.ready, Frequency: "1m", Expected: 10}}
	in.Factors = []verify.Factor{{TaskID: "f", Frequency: "1m", LastPeriod: int64(s.reads), Baseline: baseline["f"], SourceEnabled: true}}
	in.FactorDeployed = true
	return in, nil
}

func TestRunSetupVerifyWaitsUntilDataArrives(t *testing.T) {
	source := &fakeVerifySource{ready: 3}
	checks, err := runSetupVerify(context.Background(), source, time.Second, time.Millisecond)
	require.NoError(t, err)
	require.True(t, verify.Settled(checks))
	require.Equal(t, verify.Pass, checks[1].Status)
	require.Equal(t, verify.Pass, checks[2].Status)
}

func TestRunSetupVerifyFailsAfterTimeout(t *testing.T) {
	source := &fakeVerifySource{ready: 1 << 30}
	checks, err := runSetupVerify(context.Background(), source, 5*time.Millisecond, time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, verify.Fail, checks[0].Status)
}
