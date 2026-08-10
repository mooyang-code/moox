package catalog

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateDatasetIDAllowsFiftyCharacters(t *testing.T) {
	require.NoError(t, validateDatasetID("a"+strings.Repeat("b", 49)))
	require.Error(t, validateDatasetID("a"+strings.Repeat("b", 50)))
}

func TestValidateViewIDRemainsThirtyCharacters(t *testing.T) {
	require.NoError(t, validateViewID("a"+strings.Repeat("b", 29)))
	require.Error(t, validateViewID("a"+strings.Repeat("b", 30)))
}
