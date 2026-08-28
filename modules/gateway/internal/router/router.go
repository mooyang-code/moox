// Package router serves the standalone gateway's authenticated service API.
package router

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayproxy"
)

const serviceNonceNamespace = "gateway-service"

type nonceConsumer interface {
	Consume(context.Context, string, string, time.Duration) (bool, error)
}

type credentialVerifier interface {
	Verify(gatewayauth.Request, http.Header, time.Time) (gatewayauth.Claims, error)
}

type routeTable interface {
	Resolve(string) (gatewayproxy.Route, bool)
	ResolveMethod(string, string) (gatewayproxy.Route, bool)
}

type Metrics interface {
	AuthFailed()
	ReplayFailed()
	UpstreamFailed(string)
	ObserveRequest(string, string, int, time.Duration)
}

type Options struct {
	NodeID             string
	Credentials        gatewayauth.Credentials
	CredentialRegistry credentialVerifier
	MaxBodyBytes       int64
	Table              routeTable
	Nonces             nonceConsumer
	Disabled           func() bool
	Now                func() time.Time
	Metrics            Metrics
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
	started := time.Now()
	serviceID, method := "unauthenticated", "unauthenticated"
	requestedService, requestedMethod := request.PathValue("service"), request.PathValue("method")
	status := http.StatusInternalServerError
	if handler.options.Metrics != nil {
		defer func() { handler.options.Metrics.ObserveRequest(serviceID, method, status, time.Since(started)) }()
	}
	body, err := readBoundedBody(request.Body, handler.options.MaxBodyBytes)
	if err != nil {
		status = http.StatusRequestEntityTooLarge
		writeError(response, http.StatusRequestEntityTooLarge)
		return
	}
	claims, err := handler.verify(gatewayauth.Request{
		Method: request.Method, Path: request.URL.EscapedPath(), TargetNode: handler.options.NodeID, Body: body,
	}, request.Header)
	if err != nil {
		status = http.StatusUnauthorized
		if handler.options.Metrics != nil {
			handler.options.Metrics.AuthFailed()
		}
		writeError(response, http.StatusUnauthorized)
		return
	}
	serviceID, method = "authenticated", "unresolved"
	consumed, err := handler.options.Nonces.Consume(request.Context(), serviceNonceNamespace, claims.Nonce, claims.TTL)
	if err != nil {
		status = http.StatusInternalServerError
		writeError(response, http.StatusInternalServerError)
		return
	}
	if !consumed {
		status = http.StatusUnauthorized
		if handler.options.Metrics != nil {
			handler.options.Metrics.ReplayFailed()
		}
		writeError(response, http.StatusUnauthorized)
		return
	}
	if handler.options.Disabled() {
		status = http.StatusServiceUnavailable
		writeError(response, http.StatusServiceUnavailable)
		return
	}
	route, ok := handler.options.Table.ResolveMethod(requestedService, requestedMethod)
	if !ok {
		status = http.StatusNotFound
		writeError(response, http.StatusNotFound)
		return
	}
	serviceID = route.ServiceID
	if !serviceCallerPolicyAllows(claims.Caller) {
		status = http.StatusForbidden
		writeError(response, http.StatusForbidden)
		return
	}
	if len(route.AllowedCallers) > 0 && !route.AllowsCaller(claims.Caller) {
		status = http.StatusForbidden
		writeError(response, http.StatusForbidden)
		return
	}
	if int64(len(body)) > route.MaxBodyBytes && route.MaxBodyBytes > 0 {
		status = http.StatusRequestEntityTooLarge
		writeError(response, http.StatusRequestEntityTooLarge)
		return
	}
	upstream, err := gatewayproxy.Forward(request.Context(), nil, route, requestedMethod, body, request.Header)
	if err != nil {
		switch {
		case errors.Is(err, gatewayproxy.ErrMethodNotAllowed):
			method = "rejected"
			status = http.StatusMethodNotAllowed
			writeError(response, http.StatusMethodNotAllowed)
		default:
			if handler.isUpstreamNetworkFailure(err) {
				method = requestedMethod
			}
			status = http.StatusBadGateway
			handler.recordUpstreamFailure(err)
			writeError(response, http.StatusBadGateway)
		}
		return
	}
	method = requestedMethod
	status = upstream.StatusCode
	for name, values := range upstream.Header {
		for _, value := range values {
			response.Header().Add(name, value)
		}
	}
	response.WriteHeader(upstream.StatusCode)
	_, _ = response.Write(upstream.Body)
}

func (handler *Handler) verify(request gatewayauth.Request, header http.Header) (gatewayauth.Claims, error) {
	if handler.options.CredentialRegistry != nil {
		return handler.options.CredentialRegistry.Verify(request, header, handler.options.Now())
	}
	return gatewayauth.Verify(handler.options.Credentials, request, header, handler.options.Now())
}

func (handler *Handler) recordUpstreamFailure(err error) {
	if handler.options.Metrics == nil {
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		handler.options.Metrics.UpstreamFailed("timeout")
		return
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		if networkError.Timeout() {
			handler.options.Metrics.UpstreamFailed("timeout")
		} else {
			handler.options.Metrics.UpstreamFailed("connection")
		}
	}
}

func (handler *Handler) isUpstreamNetworkFailure(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError)
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
