package command

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/mooyang-code/moox/modules/cli/internal/testfixture"
)

func collectorTestClient(server *httptest.Server) *adminclient.Client {
	return &adminclient.Client{Gateway: testfixture.HandlerGateway{Handler: server.Config.Handler}}
}

func newControlFixtureServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	previous := openCommandGateway
	openCommandGateway = func(context.Context, string, *setupconfig.Snapshot) (commandGateway, error) {
		return testfixture.HandlerGateway{Handler: handler}, nil
	}
	t.Cleanup(func() { openCommandGateway = previous })
	return server
}
