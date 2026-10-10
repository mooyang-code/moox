package accessproxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
)

const AccessServiceName = "trpc.moox.access.Access"

// NonceStore persists inbound nonces across process restarts.
type NonceStore interface {
	Consume(context.Context, string, string, time.Duration) (bool, error)
}

// Forwarder borrows the process-owned internal gateway client. The client
// signs as access and selects the destination using the current directory.
type Forwarder interface {
	Forward(context.Context, string, string, int, []byte) ([]byte, error)
}

type Options struct {
	HostID      string
	Credentials *gatewayauth.CredentialRegistry
	Gateway     Forwarder
	Nonces      NonceStore
	Registerer  prometheus.Registerer
	Now         func() time.Time
}

type Proxy struct {
	options Options
	catalog servicecatalog.Catalog
	denials *prometheus.CounterVec
}

func New(options Options) (*Proxy, error) {
	if !servicecatalog.ValidHostID(options.HostID) || options.Credentials == nil || options.Gateway == nil || options.Nonces == nil {
		return nil, errors.New("access requires a canonical host ID, credential registry, internal gateway and durable nonce store")
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Registerer == nil {
		options.Registerer = prometheus.DefaultRegisterer
	}
	denials := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "moox_access_denials_total", Help: "External Access requests rejected before forwarding.",
	}, []string{"caller", "service", "method", "reason"})
	if err := options.Registerer.Register(denials); err != nil {
		return nil, fmt.Errorf("register access metrics: %w", err)
	}
	return &Proxy{options: options, catalog: catalog, denials: denials}, nil
}

func (p *Proxy) deny(caller, service, method, reason, message string) (*codec.Body, error) {
	known := false
	for _, principal := range p.catalog.Principals {
		known = known || principal.ID == caller
	}
	if !known {
		caller = "unknown"
	}
	spec, ok := p.catalog.Service(service)
	if !ok {
		service, method = "unknown", "unknown"
	} else {
		found := false
		for _, registered := range spec.Methods {
			found = found || registered == method
		}
		if !found {
			method = "unknown"
		}
	}
	p.denials.WithLabelValues(caller, service, method, reason).Inc()
	return nil, errors.New(message)
}

// Forward verifies the fixed access@host signature and catalog grant before
// passing the original PB/JSON bytes to the internal gateway client. External
// user IDs and roles are not trusted application authorization context.
func (p *Proxy) Forward(ctx context.Context, body *codec.Body) (*codec.Body, error) {
	if p == nil {
		return nil, errors.New("access proxy is unavailable")
	}
	message := codec.Message(ctx)
	if body == nil || message == nil {
		return p.deny("", "", "", "request", "access requires a tRPC message and body")
	}
	path := message.ServerRPCName()
	service, method, ok := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	if !ok || path != "/"+service+"/"+method || service == "" || method == "" || strings.Contains(method, "/") {
		return p.deny("", "", "", "request", "invalid access RPC path")
	}
	serialization := message.SerializationType()
	if serialization != codec.SerializationTypePB && serialization != codec.SerializationTypeJSON {
		return p.deny("", service, method, "serialization", "access requires PB or JSON serialization")
	}
	spec, exists := p.catalog.Service(service)
	limit := int64(servicecatalog.DefaultMaxBodyBytes)
	if exists && spec.MaxBodyBytes != 0 {
		limit = spec.MaxBodyBytes
	}
	if int64(len(body.Data)) > limit {
		return p.deny("", service, method, "request_limit", "access request exceeds service body limit")
	}
	headers := make(http.Header, len(message.ServerMetaData()))
	for key, value := range message.ServerMetaData() {
		headers.Add(key, string(value))
	}
	claims, err := p.options.Credentials.Verify(gatewayauth.Request{
		Method: http.MethodPost, Path: path, TargetNode: "access@" + p.options.HostID,
		Callee: service, Func: method, Body: body.Data,
	}, headers, p.options.Now())
	if err != nil {
		return p.deny("", service, method, "authentication", "access authentication failed")
	}
	if !p.catalog.PrincipalAllowed(claims.Caller, service, method) {
		return p.deny(claims.Caller, service, method, "permission", "access caller is not allowed for this method")
	}
	consumed, err := p.options.Nonces.Consume(ctx, "access:"+p.options.HostID+":"+claims.KeyID, claims.Nonce, claims.TTL)
	if err != nil {
		return p.deny(claims.Caller, service, method, "nonce_store", "access replay store unavailable")
	}
	if !consumed {
		return p.deny(claims.Caller, service, method, "replay", "access request replayed")
	}
	metadata := gatewayclient.CallMetadata{}
	for name, output := range map[string]*string{"X-Space-Id": &metadata.SpaceID, "X-Trace-Id": &metadata.TraceID} {
		values := headers.Values(name)
		if len(values) > 1 || len(values) == 1 && (len(values[0]) > 512 || strings.ContainsAny(values[0], "\x00\r\n")) {
			return p.deny(claims.Caller, service, method, "metadata", "invalid access application metadata")
		}
		if len(values) == 1 {
			*output = values[0]
		}
	}
	timeout := spec.TimeoutMS
	if timeout == 0 {
		timeout = servicecatalog.DefaultTimeoutMS
	}
	upstream, cancel := context.WithTimeout(gatewayclient.WithCallMetadata(ctx, metadata), time.Duration(timeout)*time.Millisecond)
	defer cancel()
	response, err := p.options.Gateway.Forward(upstream, service, method, serialization, body.Data)
	if err != nil {
		return nil, err
	}
	if int64(len(response)) > limit {
		return nil, errors.New("access response exceeds service body limit")
	}
	return &codec.Body{Data: response}, nil
}

// AccessServiceDesc exposes a wildcard service so the request protobuf is
// never decoded and re-encoded at the regional boundary.
var AccessServiceDesc = server.ServiceDesc{
	ServiceName: AccessServiceName,
	HandlerType: ((*AccessServer)(nil)),
	Methods:     []server.Method{{Name: "*", Func: accessForwardHandler}},
}

type AccessServer interface {
	Forward(context.Context, *codec.Body) (*codec.Body, error)
}

func RegisterAccessService(s server.Service, impl AccessServer) error {
	if s == nil {
		return errors.New("access service is unavailable")
	}
	return s.Register(&AccessServiceDesc, impl)
}

func accessForwardHandler(svr interface{}, ctx context.Context, f server.FilterFunc) (interface{}, error) {
	req := &codec.Body{}
	filters, err := f(req)
	if err != nil {
		return nil, err
	}
	handle := func(ctx context.Context, req interface{}) (interface{}, error) {
		body, ok := req.(*codec.Body)
		if !ok {
			return nil, errors.New("access request body is invalid")
		}
		return svr.(AccessServer).Forward(ctx, body)
	}
	return filters.Filter(ctx, req, handle)
}
