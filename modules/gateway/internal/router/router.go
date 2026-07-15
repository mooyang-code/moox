// Package router serves the standalone gateway's authenticated service API.
package router

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayproxy"
)

const serviceNonceNamespace = "gateway-service"

type nonceConsumer interface {
	Consume(context.Context, string, string, time.Duration) (bool, error)
}

type routeTable interface {
	Resolve(string) (gatewayproxy.Route, bool)
}

type Options struct {
	NodeID       string
	Credentials  gatewayauth.Credentials
	MaxBodyBytes int64
	Table        routeTable
	Nonces       nonceConsumer
	Disabled     func() bool
	Now          func() time.Time
}

type Handler struct {
	options Options
	mux     *http.ServeMux
}

func New(options Options) *Handler {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Disabled == nil {
		options.Disabled = func() bool { return false }
	}
	handler := &Handler{options: options, mux: http.NewServeMux()}
	handler.mux.HandleFunc("POST /api/service/{service}/{method}", handler.HandleService)
	return handler
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	handler.mux.ServeHTTP(response, request)
}

func (handler *Handler) HandleService(response http.ResponseWriter, request *http.Request) {
	body, err := readBoundedBody(request.Body, handler.options.MaxBodyBytes)
	if err != nil {
		writeError(response, http.StatusRequestEntityTooLarge)
		return
	}
	claims, err := gatewayauth.Verify(handler.options.Credentials, gatewayauth.Request{
		Method: request.Method, Path: request.URL.EscapedPath(), TargetNode: handler.options.NodeID, Body: body,
	}, request.Header, handler.options.Now())
	if err != nil {
		writeError(response, http.StatusUnauthorized)
		return
	}
	consumed, err := handler.options.Nonces.Consume(request.Context(), serviceNonceNamespace, claims.Nonce, claims.TTL)
	if err != nil {
		writeError(response, http.StatusInternalServerError)
		return
	}
	if !consumed {
		writeError(response, http.StatusUnauthorized)
		return
	}
	if handler.options.Disabled() {
		writeError(response, http.StatusServiceUnavailable)
		return
	}
	route, ok := handler.options.Table.Resolve(request.PathValue("service"))
	if !ok {
		writeError(response, http.StatusNotFound)
		return
	}
	if int64(len(body)) > route.MaxBodyBytes && route.MaxBodyBytes > 0 {
		writeError(response, http.StatusRequestEntityTooLarge)
		return
	}
	upstream, err := gatewayproxy.Forward(request.Context(), nil, route, request.PathValue("method"), body, request.Header)
	if err != nil {
		switch {
		case errors.Is(err, gatewayproxy.ErrMethodNotAllowed):
			writeError(response, http.StatusMethodNotAllowed)
		default:
			writeError(response, http.StatusBadGateway)
		}
		return
	}
	for name, values := range upstream.Header {
		for _, value := range values {
			response.Header().Add(name, value)
		}
	}
	response.WriteHeader(upstream.StatusCode)
	_, _ = response.Write(upstream.Body)
}

func readBoundedBody(body io.ReadCloser, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, errors.New("invalid body limit")
	}
	defer body.Close()
	value, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	if int64(len(value)) > limit {
		return nil, errors.New("request body exceeds limit")
	}
	return value, nil
}

func writeError(response http.ResponseWriter, status int) {
	http.Error(response, http.StatusText(status), status)
}
