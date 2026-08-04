package rpc

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSCFEnvironmentBytesIsStable(t *testing.T) {
	values := map[string]string{"B": "2", "A": "1"}
	require.Equal(t, len("A=1\x00")+len("B=2\x00"), scfEnvironmentBytes(values))
}

func TestManagedEnvironmentRejectsUnknownKey(t *testing.T) {
	if _, ok := managedEnvironmentKeys["TENCENTCLOUD_SECRET_KEY"]; ok {
		t.Fatal("provider credentials must not be collector-managed")
	}
}
