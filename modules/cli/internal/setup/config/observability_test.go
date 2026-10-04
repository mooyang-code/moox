package config

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestObservabilityDeliverPolicy(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{{"all", true}, {"new", true}, {"NEW", false}, {"", false}, {"latest", false}} {
		t.Run(tc.value, func(t *testing.T) {
			root := t.TempDir()
			snapshot, err := Load(writeManifest(t, root, validManifest+"\n[observability]\ndeliver_policy = \""+tc.value+"\"\n", 0600), root)
			if !tc.valid {
				require.ErrorContains(t, err, "observability.deliver_policy")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.value, snapshot.Manifest.Observability.DeliverPolicy)
			require.True(t, snapshot.Manifest.Observability.DeliverPolicyExplicit)
		})
	}
	root := t.TempDir()
	snapshot, err := Load(writeManifest(t, root, validManifest, 0600), root)
	require.NoError(t, err)
	require.Equal(t, "all", snapshot.Manifest.Observability.DeliverPolicy)
	require.False(t, snapshot.Manifest.Observability.DeliverPolicyExplicit)
}
