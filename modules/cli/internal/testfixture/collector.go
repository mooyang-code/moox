package testfixture

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"trpc.group/trpc-go/trpc-go/codec"
)

// CollectorHandlerGateway reuses task workflow assertions in process. It does
// not call an HTTP listener; wire protocol coverage uses the real tRPC fixture.
type CollectorHandlerGateway struct{ Handler http.Handler }

func (f CollectorHandlerGateway) Forward(ctx context.Context, service, method string, serialization int, body []byte) ([]byte, error) {
	if service != "trpc.moox.collector.CollectMgr" || strings.ContainsAny(method, "/?#") || serialization != codec.SerializationTypeJSON {
		return nil, fmt.Errorf("unexpected Collector gateway call")
	}
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/admin/collectmgr/"+method, bytes.NewReader(body))
	response := httptest.NewRecorder()
	f.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		return nil, fmt.Errorf("Collector fixture returned %d", response.Code)
	}
	return response.Body.Bytes(), nil
}
