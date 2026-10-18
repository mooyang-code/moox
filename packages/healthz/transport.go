package healthz

import (
	"context"
	"net/http"
	"time"

	thttp "trpc.group/trpc-go/trpc-go/http"
	"trpc.group/trpc-go/trpc-go/transport"
)

// The upstream HTTP transport only observes cancellation when reuse-port is
// enabled. Health listeners use exclusive ports, so own the HTTP server's
// shutdown while retaining the framework's codec, filters and listener setup.
func init() {
	transport.RegisterServerTransport("http_no_protocol", healthHTTPTransport{})
}

type healthHTTPTransport struct{}

func (healthHTTPTransport) ListenAndServe(ctx context.Context, options ...transport.ListenServeOption) error {
	var httpServer *http.Server
	base := thttp.NewServerTransport(func() *http.Server {
		httpServer = &http.Server{}
		return httpServer
	})
	if err := base.ListenAndServe(ctx, options...); err != nil {
		if httpServer != nil {
			_ = httpServer.Close()
		}
		return err
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdown); err != nil {
			_ = httpServer.Close()
		}
	}()
	return nil
}
