package command

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"

	"github.com/mooyang-code/moox/modules/cli/internal/rpcclient"
	"github.com/mooyang-code/moox/modules/cli/internal/testfixture"
)

func collectorTestClient(server *httptest.Server) *rpcclient.Client {
	return &rpcclient.Client{Gateway: testfixture.HandlerGateway{Handler: server.Config.Handler}}
}

func newControlFixtureServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	previous := openCommandGateway
	openCommandGateway = func(context.Context, string, *topology.Config) (commandGateway, error) {
		return testfixture.HandlerGateway{Handler: handler}, nil
	}
	t.Cleanup(func() { openCommandGateway = previous })
	return server
}
