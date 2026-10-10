package testfixture

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/mooyang-code/moox/packages/gatewayclient"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
)

// HandlerGateway reuses task workflow assertions in process. It does
// not call an HTTP listener; wire protocol coverage uses the real tRPC fixture.
type HandlerGateway struct {
	Handler http.Handler
}

func (f HandlerGateway) Forward(ctx context.Context, service, method string, serialization int, body []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	alias := map[string]string{"trpc.moox.collector.CollectMgr": "collectmgr", "trpc.moox.cloudnode.CloudNodeMgr": "cloudnode", "trpc.moox.ops.SecretMgr": "secret", "trpc.moox.admin.CollectorPublishLease": "publishlease", "trpc.moox.admin.Setup": "setup", "trpc.moox.ops.SysDeploy": "sysdeploy", "trpc.moox.trade.TradeConsoleService": "trade", "trpc.moox.storage.Metadata": "storage-metadata", "trpc.moox.storage.PrimaryStore": "storage-primary", "trpc.moox.storage.DataView": "storage-view"}[service]
	if alias == "" || strings.ContainsAny(method, "/?#") || serialization != codec.SerializationTypeJSON {
		return nil, fmt.Errorf("unexpected gateway fixture call")
	}
	path := "/api/admin/" + alias + "/" + method
	if alias == "setup" || alias == "sysdeploy" || alias == "trade" || strings.HasPrefix(alias, "storage-") {
		path = "/" + service + "/" + method
	}
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Space-Id", gatewayclient.CallMetadataFromContext(ctx).SpaceID)
	response := httptest.NewRecorder()
	f.Handler.ServeHTTP(response, request)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if response.Code >= 500 {
		return nil, errs.NewFrameError(errs.RetServerSystemErr, response.Body.String())
	}
	if response.Code != http.StatusOK {
		return nil, fmt.Errorf("gateway fixture returned %d: %s", response.Code, response.Body.String())
	}
	return response.Body.Bytes(), nil
}

func (f HandlerGateway) Invoke(ctx context.Context, service, method string, req, rsp any) error {
	body, err := protojson.Marshal(req.(proto.Message))
	if err != nil {
		return err
	}
	raw, err := f.Forward(ctx, service, method, codec.SerializationTypeJSON, body)
	if err != nil {
		return err
	}
	if err := protojson.Unmarshal(raw, rsp.(proto.Message)); err != nil {
		return errs.NewFrameError(errs.RetClientDecodeFail, "decode gateway response")
	}
	return nil
}
func (f HandlerGateway) Close() error { return nil }
