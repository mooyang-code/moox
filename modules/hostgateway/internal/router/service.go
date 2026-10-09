package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
)

type nonceConsumer interface {
	Consume(context.Context, string, string, time.Duration) (bool, error)
}
type Metrics interface {
	AuthFailed()
	ReplayFailed()
	UpstreamFailed(string)
	ObserveRequest(string, string, int, time.Duration)
}

type Forwarder interface {
	Forward(context.Context, servicecatalog.Route, int, []byte, codec.MetaData) ([]byte, error)
}

type ServiceOptions struct {
	State     *snapshot.State
	Nonces    nonceConsumer
	Metrics   Metrics
	Now       func() time.Time
	Forwarder Forwarder
}

type Proxy struct {
	options ServiceOptions
	catalog servicecatalog.Catalog
	owned   *Upstream
}

func NewService(options ServiceOptions) (*Proxy, error) {
	if options.State == nil || options.Nonces == nil {
		return nil, errors.New("host gateway requires snapshot and durable nonce store")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	p := &Proxy{options: options, catalog: catalog}
	if p.options.Forwarder == nil {
		p.owned = NewUpstream()
		p.options.Forwarder = p.owned
	}
	return p, nil
}

func (p *Proxy) Close() error {
	if p.owned != nil {
		return p.owned.Close()
	}
	return nil
}

func (p *Proxy) Register(service server.Service) error {
	return service.Register(&server.ServiceDesc{ServiceName: "trpc.moox.hostgateway.ServiceGateway", HandlerType: ((*interface{})(nil)),
		Methods: []server.Method{{Name: "*", Func: p.handle}},
	}, p)
}

func (p *Proxy) handle(_ interface{}, ctx context.Context, decode server.FilterFunc) (interface{}, error) {
	raw := &codec.Body{}
	filters, err := decode(raw)
	if err != nil {
		return nil, err
	}
	return filters.Filter(ctx, raw, func(ctx context.Context, input interface{}) (result interface{}, resultErr error) {
		started := p.options.Now()
		labelService, labelMethod, status := "unknown", "unknown", http.StatusBadRequest
		defer func() {
			if p.options.Metrics != nil {
				p.options.Metrics.ObserveRequest(labelService, labelMethod, status, p.options.Now().Sub(started))
			}
		}()
		message := codec.Message(ctx)
		serialization := message.SerializationType()
		body := input.(*codec.Body).Data
		if serialization != codec.SerializationTypePB && serialization != codec.SerializationTypeJSON || len(body) > 64<<20 {
			return nil, errors.New("host gateway requires bounded PB or JSON body")
		}
		path := message.ServerRPCName()
		service, method, ok := strings.Cut(strings.TrimPrefix(path, "/"), "/")
		if !ok || path != "/"+service+"/"+method || service == "" || method == "" || strings.Contains(method, "/") {
			return nil, errors.New("invalid host gateway RPC path")
		}
		view := p.options.State.Load()
		if view == nil {
			return nil, errors.New("host gateway snapshot not initialized")
		}
		headers := http.Header{}
		for key, value := range message.ServerMetaData() {
			headers.Add(key, string(value))
		}
		claims, err := view.Verify(gatewayauth.Request{Method: "POST", Path: path, TargetNode: view.HostID(), Callee: service, Func: method, Body: body}, headers, p.options.Now())
		if err != nil {
			status = http.StatusUnauthorized
			if p.options.Metrics != nil {
				p.options.Metrics.AuthFailed()
			}
			return nil, err
		}
		if view.Disabled() {
			status = http.StatusServiceUnavailable
			return nil, errors.New("host gateway is disabled")
		}
		route, ok := view.Resolve(service, method)
		if !ok {
			status = http.StatusNotFound
			return nil, errors.New("service/method is not deployed on this host")
		}
		labelService, labelMethod = route.ComponentID, method
		if !p.catalog.Allowed(claims.Caller, service, method) || !slices.Contains(route.Callers, claims.Caller) {
			status = http.StatusForbidden
			return nil, errors.New("caller is not allowed for service/method")
		}
		if int64(len(body)) > route.MaxBodyBytes {
			status = http.StatusRequestEntityTooLarge
			return nil, errors.New("host gateway request exceeds route body limit")
		}
		consumed, err := p.options.Nonces.Consume(ctx, "host-gateway:"+claims.KeyID, claims.Nonce, claims.TTL)
		if err != nil {
			status = http.StatusServiceUnavailable
			return nil, errors.New("host gateway replay store unavailable")
		}
		if !consumed {
			status = http.StatusUnauthorized
			if p.options.Metrics != nil {
				p.options.Metrics.ReplayFailed()
			}
			return nil, errors.New("host gateway request replayed")
		}
		upstreamCtx, cancel := context.WithTimeout(ctx, time.Duration(route.TimeoutMS)*time.Millisecond)
		defer cancel()
		response, err := p.options.Forwarder.Forward(upstreamCtx, route, serialization, body, message.ServerMetaData())
		if err != nil {
			status = http.StatusBadGateway
			if p.options.Metrics != nil {
				p.options.Metrics.UpstreamFailed(failureKind(err))
			}
			return nil, routeError(route, err)
		}
		if int64(len(response)) > route.MaxBodyBytes {
			status = http.StatusBadGateway
			return nil, errors.New("host gateway response exceeds route body limit")
		}
		status = http.StatusOK
		return &codec.Body{Data: response}, nil
	})
}

func (p *Proxy) String() string {
	return fmt.Sprintf("HostGatewayProxy{initialized=%t}", p.options.State.Load() != nil)
}
