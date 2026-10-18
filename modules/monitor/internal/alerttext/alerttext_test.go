package alerttext

import (
	"testing"

	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
)

func TestServiceNamesComeOnlyFromComponentCatalog(t *testing.T) {
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	for _, component := range catalog.Components {
		require.Equal(t, component.Name, Service(component.ID))
	}
	for _, id := range []string{"moox-collector", "web_host", "random-factor-service"} {
		require.Equal(t, id, Service(id))
	}
}
